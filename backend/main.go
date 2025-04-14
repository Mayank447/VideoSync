package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

var (
	rdb          *redis.Client
	upgrader     = websocket.Upgrader{}
	sessionMutex = &sync.Mutex{}
)

const (
	videoPath     = "./sample.mp4"
	sessionExpiry = time.Hour * 24
)

type SessionState struct {
	Paused       bool    `json:"paused"`
	CurrentTime  float64 `json:"currentTime"`
	PlaybackRate float64 `json:"playbackRate"`
}

func main() {
	// Initialize Redis
	rdb = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})

	// Create router
	r := mux.NewRouter()

	// API routes
	r.HandleFunc("/api/sessions", createSession).Methods("POST")
	r.HandleFunc("/api/sessions/{key}/validate", validateSession).Methods("GET")
	r.HandleFunc("/api/video", streamVideo).Methods("GET")
	r.HandleFunc("/ws", handleWebSocket)

	// Start server
	log.Println("Starting server on :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}

// Session creation endpoint
func createSession(w http.ResponseWriter, r *http.Request) {
	sessionKey := uuid.New().String()
	ctx := context.Background()

	initialState := SessionState{
		Paused:       true,
		CurrentTime:  0,
		PlaybackRate: 1.0,
	}

	stateJson, _ := json.Marshal(initialState)

	err := rdb.SetEX(ctx, "session:"+sessionKey, "active", sessionExpiry).Err()
	if err != nil {
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	err = rdb.SetEX(ctx, "session:"+sessionKey+":state", stateJson, sessionExpiry).Err()
	if err != nil {
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"sessionKey": sessionKey})
}

// Session validation endpoint
func validateSession(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionKey := vars["key"]

	ctx := context.Background()
	exists, err := rdb.Exists(ctx, "session:"+sessionKey).Result()
	if err != nil {
		http.Error(w, "Error validating session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"valid": exists > 0})
}

// WebSocket handler
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}
	defer conn.Close()

	sessionKey := r.URL.Query().Get("sessionKey")
	ctx := context.Background()

	// Validate session
	exists, err := rdb.Exists(ctx, "session:"+sessionKey).Result()
	if err != nil || exists == 0 {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4001, "Invalid session key"))
		return
	}

	// Check if host exists
	isHost := false
	hostKey := "session:" + sessionKey + ":host"
	if rdb.SetNX(ctx, hostKey, "connected", sessionExpiry).Val() {
		isHost = true
	}

	// Send initial state
	stateJson, _ := rdb.Get(ctx, "session:"+sessionKey+":state").Bytes()
	initialMessage := map[string]interface{}{
		"type":   "init",
		"isHost": isHost,
		"state":  json.RawMessage(stateJson),
	}

	conn.WriteJSON(initialMessage)

	// Heartbeat
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	// Message handling
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}

		if isHost {
			var msg map[string]interface{}
			if err := json.Unmarshal(message, &msg); err != nil {
				continue
			}

			if msg["type"] == "stateUpdate" {
				state := msg["state"].(map[string]interface{})
				stateJson, _ := json.Marshal(state)

				rdb.SetEX(ctx, "session:"+sessionKey+":state", stateJson, sessionExpiry)
				broadcastState(sessionKey, stateJson)
			}
		}
	}

	// Cleanup if host disconnects
	if isHost {
		rdb.Del(ctx, hostKey)
	}
}

// Video streaming endpoint
func streamVideo(w http.ResponseWriter, r *http.Request) {
	videoFile, err := os.Open(videoPath)
	if err != nil {
		http.Error(w, "Video not found", http.StatusNotFound)
		return
	}
	defer videoFile.Close()

	stat, _ := videoFile.Stat()
	http.ServeContent(w, r, "video.mp4", stat.ModTime(), videoFile)
}

func broadcastState(sessionKey string, state []byte) {
	// In production, implement proper client tracking
	// This is a simplified version
	// You would need to maintain a list of connected clients per session
	// and iterate through them to send updates
}
