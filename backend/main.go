package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/gorilla/handlers" // For CORS
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

type Config struct {
	RedisAddr        string
	StreamingServers map[string]*StreamingServer
	mu               sync.RWMutex
}

type ServerMetrics struct {
	ActiveConnections int
	ActiveSessions    int
	LastHealthCheck   time.Time
	Status            string
}

var (
	cfg = Config{
		RedisAddr:        "localhost:6379",
		StreamingServers: make(map[string]*StreamingServer),
	}
	rdb          *redis.Client
	sessionMutex = &sync.Mutex{}
	metrics      = ServerMetrics{
		Status: "starting",
	}
	ctx      = context.Background() // Add global context
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

var (
	streamingServers = make(map[string]*StreamingServer)
	serverMutex      sync.RWMutex
)

const (
	sessionExpiry = time.Hour * 24
)

type SessionState struct {
	Paused       bool    `json:"paused"`
	CurrentTime  float64 `json:"currentTime"`
	PlaybackRate float64 `json:"playbackRate"`
}

type StreamingServer struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Capacity    int       `json:"capacity"`
	CurrentLoad int       `json:"currentLoad"`
	Status      string    `json:"status"`
	LastPing    int64     `json:"lastPing"`
	Registered  time.Time `json:"registered"`
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

	// Health Check endpoint

	// Add error recovery middleware
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					log.Printf("Panic recovered: %v", err)
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	})

	// Add logging middleware
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Printf("Incoming connection from %s to %s %s", r.RemoteAddr, r.Method, r.URL.Path)
			next.ServeHTTP(w, r)
		})
	})

	// Health check endpoint
	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Health check request from %s", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
			"time":   time.Now().String(),
		})
	}).Methods("GET")

	// Root endpoint
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Root request from %s", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"message": "VideoSync API Server",
			"status":  "running",
		})
	}).Methods("GET")

	// API routes
	r.HandleFunc("/ws", handleWebSocket)
	r.HandleFunc("/api/sessions", createSession).Methods("POST")
	r.HandleFunc("/api/sessions/{key}/validate", validateSession).Methods("GET")
	r.HandleFunc("/api/streaming-servers/register", registerStreamingServer).Methods("POST")
	r.HandleFunc("/api/streaming-servers/heartbeat", handleHeartbeat).Methods("POST")

	// Start server
	log.Println("Starting server on :8080")

	// With CORS-enabled server (Middle ware)
	headersOk := handlers.AllowedHeaders([]string{
		"Content-Type",
		"Origin",
		"Sec-WebSocket-Extensions",
		"Sec-WebSocket-Key",
		"Sec-WebSocket-Version",
	})
	originsOk := handlers.AllowedOrigins([]string{"*"})
	methodsOk := handlers.AllowedMethods([]string{"GET", "POST", "OPTIONS"})
	exposedOk := handlers.ExposedHeaders([]string{"Content-Length"})

	// Start background tasks
	go cleanupInactiveServers()

	log.Fatal(http.ListenAndServe("0.0.0.0:8080",
		handlers.CORS(originsOk, headersOk, methodsOk, exposedOk)(r)))
}

// Session creation endpoint
func createSession(w http.ResponseWriter, r *http.Request) {
	sessionKey := uuid.New().String()
	hostToken := uuid.New().String()
	ctx := context.Background()

	log.Printf("Creating new session - Key: %s, Host Token: %s", sessionKey, hostToken)

	// Store session
	err := rdb.SetEX(ctx, "session:"+sessionKey, "active", sessionExpiry).Err()
	if err != nil {
		log.Printf("Redis error creating session: %v", err)
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	// Store host token
	err = rdb.SetEX(ctx, "session:"+sessionKey+":host", hostToken, sessionExpiry).Err()
	if err != nil {
		log.Printf("Redis error storing host token: %v", err)
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	// Store initial state
	initialState := SessionState{
		Paused:       true,
		CurrentTime:  0,
		PlaybackRate: 1.0,
	}
	stateJson, _ := json.Marshal(initialState)
	err = rdb.SetEX(ctx, "session:"+sessionKey+":state", stateJson, sessionExpiry).Err()
	if err != nil {
		log.Printf("Redis error storing initial state: %v", err)
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	log.Printf("Session created successfully - Key: %s", sessionKey)

	// Return both session key and host token
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"sessionKey": sessionKey,
		"hostToken":  hostToken,
	})
}

// Session validation endpoint
func validateSession(w http.ResponseWriter, r *http.Request) {
	// print the request
	log.Printf(r.URL.Path)
	sessionKey := r.URL.Path[len("/api/sessions/"):]
	sessionKey = strings.Split(sessionKey, "?")[0]
	sessionKey = sessionKey[:len(sessionKey)-len("/validate")]
	hostToken := r.URL.Query().Get("hostToken")

	log.Printf("Validating session - Key: %s, Host Token provided: %v", sessionKey, hostToken != "")

	// Check if session exists
	exists, err := rdb.Exists(ctx, "session:"+sessionKey).Result()
	if err != nil {
		log.Printf("Redis error checking session: %v", err)
		respondError(w, http.StatusInternalServerError, "internal_server_error")
		return
	}

	if exists == 0 {
		log.Printf("Session not found: %s", sessionKey)
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"valid": false,
			"error": "session_not_found",
		})
		return
	}

	// Validate host token if provided
	isHost := false
	if hostToken != "" {
		storedToken, err := rdb.Get(ctx, "session:"+sessionKey+":host").Result()
		if err != nil {
			log.Printf("Redis error getting host token: %v", err)
		} else if storedToken == hostToken {
			isHost = true
			log.Printf("Host token validated for session: %s", sessionKey)
		} else {
			log.Printf("Invalid host token provided for session: %s", sessionKey)
		}
	}

	// Get streaming server for the session
	server := getLeastLoadedServer()
	if server == nil {
		log.Printf("No streaming servers available for session: %s", sessionKey)
		respondError(w, http.StatusServiceUnavailable, "no_streaming_servers_available")
		return
	}

	serverURL := server.URL
	if !strings.HasPrefix(serverURL, "http") {
		serverURL = "http://" + serverURL
	}
	serverURL = strings.TrimSuffix(serverURL, "/")

	log.Printf("Session validated - Key: %s, Is Host: %v, Server: %s", sessionKey, isHost, server.ID)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"valid":         true,
		"isHost":        isHost,
		"streaming_url": serverURL, // Send direct video URL
	})
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

func registerStreamingServer(w http.ResponseWriter, r *http.Request) {
	var server StreamingServer
	if err := json.NewDecoder(r.Body).Decode(&server); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	server.Registered = time.Now()
	server.LastPing = time.Now().Unix()

	serverMutex.Lock()
	streamingServers[server.ID] = &server
	serverMutex.Unlock()

	log.Printf("Registered streaming server: %s", server.ID)
	w.WriteHeader(http.StatusOK)
}

func handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var server StreamingServer
	if err := json.NewDecoder(r.Body).Decode(&server); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	serverMutex.Lock()
	if existingServer, exists := streamingServers[server.ID]; exists {
		existingServer.CurrentLoad = server.CurrentLoad
		existingServer.LastPing = time.Now().Unix()
		existingServer.Status = "active"
	}
	serverMutex.Unlock()

	w.WriteHeader(http.StatusOK)
}

func getLeastLoadedServer() *StreamingServer {
	serverMutex.RLock()
	defer serverMutex.RUnlock()

	var bestServer *StreamingServer
	lowestLoad := float64(1.0)

	for _, server := range streamingServers {
		if server.Status != "active" {
			continue
		}
		load := float64(server.CurrentLoad) / float64(server.Capacity)
		if load < lowestLoad {
			lowestLoad = load
			bestServer = server
		}
	}

	return bestServer
}

func cleanupInactiveServers() {
	ticker := time.NewTicker(1 * time.Minute)
	for range ticker.C {
		serverMutex.Lock()
		now := time.Now().Unix()
		for id, server := range streamingServers {
			if now-server.LastPing > 60 { // Remove servers inactive for more than 1 minute
				delete(streamingServers, id)
				log.Printf("Removed inactive streaming server: %s", id)
			}
		}
		serverMutex.Unlock()
	}
}

func respondJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, statusCode int, errorMessage string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{"error": errorMessage})
}
