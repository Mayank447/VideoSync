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

// STruct and Global Variables for Redis
type SessionState struct {
	Paused       bool    `json:"paused"`
	CurrentTime  float64 `json:"currentTime"`
	PlaybackRate float64 `json:"playbackRate"`
	Timestamp	 time.Time    `json:"timestamp"`
}

type Config struct {
	RedisAddr        string
	StreamingServers map[string]*StreamingServer
	mu               sync.RWMutex
}

var (
	cfg = Config{
		RedisAddr:        "localhost:6379",
		StreamingServers: make(map[string]*StreamingServer),
	}
	rdb          *redis.Client
)

// STructs and Global Variable for Streaming Server
type StreamingServer struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Capacity    int       `json:"capacity"`
	CurrentLoad int       `json:"currentLoad"`
	Status      string    `json:"status"`
	LastPing    int64     `json:"lastPing"`
	Registered  time.Time `json:"registered"`
}

type ServerMetrics struct {
	ActiveConnections int
	ActiveSessions    int
	LastHealthCheck   time.Time
	Status            string
}

var (
	streamingServers = make(map[string]*StreamingServer)
	serverMutex      sync.RWMutex
)

var (
	sessionMutex = &sync.Mutex{}
	metrics      = ServerMetrics{
		Status: "starting",
	}
)

// Global Variables for Websocket
var (
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

// Gloabl Consts
const (
	sessionExpiry = time.Hour * 24
)

func main() {
	// Initialize Redis
	rdb = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})

	// Create router
	r := mux.NewRouter()

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
	r.HandleFunc("/api/sessions", createSession).Methods("POST")
	r.HandleFunc("/api/sessions/{key}/validate", validateSession).Methods("GET")
	r.HandleFunc("/api/streaming-servers/register", registerStreamingServer).Methods("POST")
	r.HandleFunc("/api/streaming-servers/heartbeat", handleHeartbeat).Methods("POST")
	// TODO - Reassign clients

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

	// [TODO] Check whether the sessionKey, hostToken already exists or not

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
		Timestamp: time.Now().Unix(),
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


/////////////////////////////////////// HELPER FUNCTIONS //////////////////////////////////////////////////////////////

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