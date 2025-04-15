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
	"github.com/gorilla/handlers" // For CORS
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

var (
	rdb          *redis.Client
	upgrader     = websocket.Upgrader{}
	sessionMutex = &sync.Mutex{}
)

var connections = struct {
	sync.Mutex
	m map[string][]*websocket.Conn
}{m: make(map[string][]*websocket.Conn)}

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
	r.HandleFunc("/api/video", streamVideo).Methods("GET", "HEAD")
	r.HandleFunc("/ws", handleWebSocket)

	// Start server
	log.Println("Starting server on :8080")

	// With CORS-enabled server (Middle ware)
	headersOk := handlers.AllowedHeaders([]string{"Content-Type", "Authorization"})
	originsOk := handlers.AllowedOrigins([]string{"*"})
	methodsOk := handlers.AllowedMethods([]string{"GET", "POST", "OPTIONS"})
	exposedOk := handlers.ExposedHeaders([]string{"Content-Length"})

	log.Fatal(http.ListenAndServe(":8080",
		handlers.CORS(originsOk, headersOk, methodsOk, exposedOk)(r)))
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
	defer func() {
		conn.Close()
		cleanupConnection(conn)
	}()

	// Get session key from query params
	sessionKey := r.URL.Query().Get("sessionKey")
	if sessionKey == "" {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4001, "Missing session key"))
		return
	}

	// Validate session
	ctx := context.Background()
	exists, err := rdb.Exists(ctx, "session:"+sessionKey).Result()
	if err != nil || exists == 0 {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4001, "Invalid session key"))
		return
	}

	// Determine host status
	isHost := false
	hostKey := "session:" + sessionKey + ":host"
	if rdb.SetNX(ctx, hostKey, "connected", sessionExpiry).Val() {
		isHost = true
		log.Printf("New host connected to session: %s", sessionKey)
	}

	// Register connection
	connections.Lock()
	connections.m[sessionKey] = append(connections.m[sessionKey], conn)
	connections.Unlock()

	// Send initial state
	stateJson, err := rdb.Get(ctx, "session:"+sessionKey+":state").Bytes()
	if err != nil {
		log.Printf("Error getting initial state: %v", err)
		stateJson, _ = json.Marshal(SessionState{
			Paused:       true,
			CurrentTime:  0,
			PlaybackRate: 1.0,
		})
	}

	initialMessage := map[string]interface{}{
		"type":      "init",
		"isHost":    isHost,
		"state":     json.RawMessage(stateJson),
		"timestamp": time.Now().UnixMilli(),
	}

	if err := conn.WriteJSON(initialMessage); err != nil {
		log.Printf("Error sending initial message: %v", err)
		return
	}

	// Start heartbeat
	done := make(chan struct{})
	go heartbeatRoutine(conn, done)

	// Message handling loop
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		if isHost {
			var msg struct {
				Type      string       `json:"type"`
				State     SessionState `json:"state"`
				Timestamp int64        `json:"timestamp"`
			}

			if err := json.Unmarshal(message, &msg); err != nil {
				log.Printf("Error decoding message: %v", err)
				continue
			}

			if msg.Type == "stateUpdate" {
				// Update state in Redis with timestamp
				stateWithTs := struct {
					SessionState
					Timestamp int64 `json:"timestamp"`
				}{
					SessionState: msg.State,
					Timestamp:    time.Now().UnixMilli(),
				}

				stateJson, _ := json.Marshal(stateWithTs)

				// Store in Redis with extended expiration
				err := rdb.SetEX(ctx,
					"session:"+sessionKey+":state",
					stateJson,
					sessionExpiry,
				).Err()

				if err != nil {
					log.Printf("Error saving state: %v", err)
					continue
				}

				// Broadcast to all clients in session
				broadcastState(sessionKey, stateJson)
			}
		}
	}

	// Cleanup if host disconnects
	if isHost {
		if err := rdb.Del(ctx, hostKey).Err(); err != nil {
			log.Printf("Error deleting host key: %v", err)
		}
		log.Printf("Host disconnected from session: %s", sessionKey)
	}

	close(done)
}

func heartbeatRoutine(conn *websocket.Conn, done <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("Heartbeat failed: %v", err)
				return
			}
		case <-done:
			return
		}
	}
}

// Video streaming endpoint
func streamVideo(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD")

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
	connections.Lock()
	defer connections.Unlock()

	clients, exists := connections.m[sessionKey]
	if !exists {
		return
	}

	// Prepare message with server timestamp
	msg := struct {
		Type      string          `json:"type"`
		State     json.RawMessage `json:"state"`
		Timestamp int64           `json:"timestamp"`
	}{
		Type:      "stateUpdate",
		State:     state,
		Timestamp: time.Now().UnixMilli(),
	}

	msgBytes, _ := json.Marshal(msg)

	// Broadcast to all clients in session
	for _, client := range clients {
		if client != nil {
			go func(c *websocket.Conn) {
				c.SetWriteDeadline(time.Now().Add(2 * time.Second))
				if err := c.WriteMessage(websocket.TextMessage, msgBytes); err != nil {
					log.Printf("Broadcast error: %v", err)
				}
			}(client)
		}
	}
}

func cleanupConnection(conn *websocket.Conn) {
	connections.Lock()
	defer connections.Unlock()

	for sessionKey, clients := range connections.m {
		for i, client := range clients {
			if client == conn {
				// Remove connection from slice
				connections.m[sessionKey] = append(clients[:i], clients[i+1:]...)

				// Remove session if empty
				if len(connections.m[sessionKey]) == 0 {
					delete(connections.m, sessionKey)
				}
				return
			}
		}
	}
}
