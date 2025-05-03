package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

type Config struct {
	RedisAddr        string
	StreamingServers []string
	CurrentServerIdx int
}

var (
	cfg = Config{
		RedisAddr:        "localhost:6379",
		StreamingServers: []string{"http://localhost:8081", "http://localhost:8082"},
	}
	rdb      *redis.Client
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}
	connections = struct {
		sync.Mutex
		m map[string][]*websocket.Conn
	}{m: make(map[string][]*websocket.Conn)}
)

type SessionState struct {
	Paused       bool    `json:"paused"`
	CurrentTime  float64 `json:"currentTime"`
	PlaybackRate float64 `json:"playbackRate"`
	StreamingURL string  `json:"streamingUrl"`
	HostToken    string  `json:"hostToken"`
	LastUpdated  int64   `json:"lastUpdated"`
}

func main() {
	rdb = redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: "",
		DB:       0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	r := mux.NewRouter()
	r.Use(panicRecovery)
	r.Use(loggingMiddleware)

	r.HandleFunc("/api/sessions", createSession).Methods("POST")
	r.HandleFunc("/api/sessions/{sessionKey}/validate", validateSession).Methods("GET")
	r.HandleFunc("/ws", handleWebSocket)
	r.HandleFunc("/health", healthCheck).Methods("GET")
	r.HandleFunc("/api/sessions/{sessionKey}", getSession).Methods("GET")

	cors := handlers.CORS(
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowedMethods([]string{"GET", "POST", "OPTIONS"}),
		handlers.AllowedHeaders([]string{"Content-Type", "Authorization"}),
	)

	log.Println("Main server starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", cors(r)))
}

func panicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("Panic recovered: %v", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s %v", r.Method, r.RequestURI, r.RemoteAddr, time.Since(start))
	})
}

func createSession(w http.ResponseWriter, r *http.Request) {
	cfg.CurrentServerIdx = (cfg.CurrentServerIdx + 1) % len(cfg.StreamingServers)
	streamingURL := fmt.Sprintf("%s/video/sample.mp4", cfg.StreamingServers[cfg.CurrentServerIdx])
	sessionKey := uuid.New().String()
	hostToken := uuid.New().String()

	initialState := SessionState{
		Paused:       true,
		CurrentTime:  0,
		PlaybackRate: 1.0,
		StreamingURL: streamingURL,
		HostToken:    hostToken,
		LastUpdated:  time.Now().UnixMilli(),
	}

	stateJson, _ := json.Marshal(initialState)
	ctx := context.Background()

	err := rdb.SetEX(ctx, "session:"+sessionKey, stateJson, 24*time.Hour).Err()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "session_creation_failed")
		return
	}

	err = rdb.SetEX(ctx, "session:"+sessionKey+":host", hostToken, 24*time.Hour).Err()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "host_registration_failed")
		return
	}

	respondJSON(w, http.StatusCreated, map[string]string{
		"sessionKey":    sessionKey,
		"streaming_url": streamingURL,
		"hostToken":     hostToken,
	})
}

func validateSession(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionKey := vars["sessionKey"]
	ctx := context.Background()

	exists, err := rdb.Exists(ctx, "session:"+sessionKey).Result()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "validation_error")
		return
	}

	respondJSON(w, http.StatusOK, map[string]bool{"valid": exists > 0})
}

func getSession(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sessionKey := vars["sessionKey"]
	ctx := context.Background()

	stateJson, err := rdb.Get(ctx, "session:"+sessionKey).Bytes()
	if err != nil {
		respondError(w, http.StatusNotFound, "session_not_found")
		return
	}

	respondJSON(w, http.StatusOK, json.RawMessage(stateJson))
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	sessionKey := r.URL.Query().Get("sessionKey")
	hostToken := r.URL.Query().Get("hostToken")
	ctx := context.Background()

	exists, err := rdb.Exists(ctx, "session:"+sessionKey).Result()
	if err != nil || exists == 0 {
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "invalid_session"))
		return
	}

	isHost := false
	if hostToken != "" {
		storedToken, err := rdb.Get(ctx, "session:"+sessionKey+":host").Result()
		if err != nil {
			log.Printf("Redis error: %v", err)
		} else {
			// Trim whitespace and compare
			if strings.TrimSpace(storedToken) == strings.TrimSpace(hostToken) {
				isHost = true
				log.Printf("Host validated for session: %s", sessionKey)
			}
		}
	}

	stateJson, err := rdb.Get(ctx, "session:"+sessionKey).Bytes()
	if err != nil {
		log.Printf("Error retrieving session state: %v", err)
		return
	}

	initialMessage := map[string]interface{}{
		"type":      "init",
		"isHost":    isHost,
		"state":     json.RawMessage(stateJson),
		"timestamp": time.Now().UnixMilli(),
	}

	if err := conn.WriteJSON(initialMessage); err != nil {
		log.Printf("Failed to send initial state: %v", err)
		return
	}

	connections.Lock()
	connections.m[sessionKey] = append(connections.m[sessionKey], conn)
	connections.Unlock()

	defer cleanupConnection(conn)

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var stateUpdate struct {
			Type  string       `json:"type"`
			State SessionState `json:"state"`
		}
		if err := json.Unmarshal(msg, &stateUpdate); err != nil {
			continue
		}

		if stateUpdate.Type == "state_update" && isHost {
			stateUpdate.State.LastUpdated = time.Now().UnixMilli()
			stateJson, _ := json.Marshal(stateUpdate.State)

			if err := rdb.SetEX(ctx, "session:"+sessionKey, stateJson, 24*time.Hour).Err(); err == nil {
				broadcastState(sessionKey, stateJson)
			}
		}
	}
}

func broadcastState(sessionKey string, state []byte) {
	connections.Lock()
	defer connections.Unlock()

	clients, exists := connections.m[sessionKey]
	if !exists {
		return
	}

	msg := map[string]interface{}{
		"type":      "state_update",
		"state":     json.RawMessage(state),
		"timestamp": time.Now().UnixMilli(),
	}
	msgBytes, _ := json.Marshal(msg)

	for _, client := range clients {
		go func(c *websocket.Conn) {
			c.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if err := c.WriteMessage(websocket.TextMessage, msgBytes); err != nil {
				log.Printf("Broadcast error: %v", err)
			}
		}(client)
	}
}

func cleanupConnection(conn *websocket.Conn) {
	connections.Lock()
	defer connections.Unlock()

	for sessionKey, clients := range connections.m {
		for i, client := range clients {
			if client == conn {
				connections.m[sessionKey] = append(clients[:i], clients[i+1:]...)
				if len(connections.m[sessionKey]) == 0 {
					delete(connections.m, sessionKey)
				}
				return
			}
		}
	}
}

func healthCheck(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "healthy",
		"servers":  len(cfg.StreamingServers),
		"sessions": len(connections.m),
	})
}

func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, code int, message string) {
	respondJSON(w, code, map[string]string{"error": message})
}
