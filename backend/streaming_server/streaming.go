package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

type StreamingServer struct {
	ID          string
	URL         string
	Capacity    int
	CurrentLoad int
	Status      string
	LastPing    int64
}

type ClientConnection struct {
	conn      *websocket.Conn
	sessionID string
	isHost    bool
}

type RedisState struct {
	Paused       bool    `json:"paused"`
	CurrentTime  float64 `json:"currentTime"`
	PlaybackRate float64 `json:"playbackRate"`
	Timestamp    int64   `json:"timestamp"`
}

var (
	// [TODO] Get the below 4 param through command line
	mainServerURL = "http://localhost:8080"
	serverID      = os.Getenv("SERVER_ID")
	serverURL     = os.Getenv("SERVER_URL")
	serverPort    = os.Getenv("SERVER_PORT")
	capacity      = 100 // Default capacity

	clients         = make(map[string][]*ClientConnection)
	client_lock     = make(map[string]*sync.Mutex)
	numClients      = 0
	numClients_lock = &sync.Mutex{}

	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true // Allow all origins for now
		},
	}

	rdb    *redis.Client
	pubsub *redis.PubSub
)

const (
	videoPath          = "../sample.mp4"
	HEARTBEAT_INTERVAL = 30
	REDIS_MSG_EXPIRY   = 24 * time.Hour
)

var ctx = context.Background()

func main() {
	rdb = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})

	pong, err := rdb.Ping(ctx).Result()
	if err != nil {
		log.Fatalf("Could not connect to Redis: %v", err)
	}
	fmt.Println(pong, "Connected to Redis")

	if serverID == "" {
		serverID = fmt.Sprintf("ss-%d", time.Now().Unix())
	}
	if serverPort == "" {
		serverPort = "8081"
	}
	if serverURL == "" {
		serverURL = "http://localhost:8081"
	}

	// Register with main server
	registerWithMainServer()

	// Start heartbeat goroutine
	go sendHeartbeats()

	// Setup routes
	r := mux.NewRouter()
	r.HandleFunc("/ws", handleWebSocket) // [TODO]
	r.HandleFunc("/status", handleStatus)
	r.HandleFunc("/api/video", streamVideo).Methods("GET", "HEAD")

	// Start server
	log.Printf("Streaming server starting on port %s", serverPort)
	log.Fatal(http.ListenAndServe(":"+serverPort, r))
}

func registerWithMainServer() {
	server := StreamingServer{
		ID:          serverID,
		URL:         serverURL,
		Capacity:    capacity,
		CurrentLoad: 0,
		Status:      "active",
		LastPing:    time.Now().Unix(),
	}

	jsonData, err := json.Marshal(server)
	if err != nil {
		log.Fatal("Error marshaling server data:", err)
	}

	resp, err := http.Post(mainServerURL+"/api/streaming-servers/register", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Fatal("Error registering with main server:", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Fatal("Failed to register with main server")
	}

	log.Println("Successfully registered with main server")
}

func sendHeartbeats() {
	ticker := time.NewTicker(HEARTBEAT_INTERVAL * time.Second)
	for range ticker.C {
		numClients_lock.Lock()
		server := StreamingServer{
			ID:          serverID,
			URL:         serverURL,
			Capacity:    capacity,
			CurrentLoad: numClients,
			Status:      "active",
			LastPing:    time.Now().Unix(),
		}
		numClients_lock.Unlock()

		jsonData, err := json.Marshal(server)
		if err != nil {
			log.Println("Error marshaling heartbeat data:", err)
			continue
		}

		resp, err := http.Post(mainServerURL+"/api/streaming-servers/heartbeat", "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			log.Println("Error sending heartbeat:", err)
			continue
		}
		resp.Body.Close()
	}
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	log.Println(r.URL)
	sessionID := r.URL.Query().Get("sessionID")
	if sessionID == "" {
		http.Error(w, "Missing session ID", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Error upgrading connection:", err)
		return
	}

	isHost := r.URL.Query().Get("isHost") == "true"
	client := &ClientConnection{
		conn:      conn,
		sessionID: sessionID,
		isHost:    isHost,
	}

	// Initialize mutex for this session if it doesn't exist
	if _, exists := client_lock[sessionID]; !exists {
		client_lock[sessionID] = &sync.Mutex{}
	}

	client_lock[sessionID].Lock()
	clients[sessionID] = append(clients[sessionID], client)
	client_lock[sessionID].Unlock()

	numClients_lock.Lock()
	numClients += 1
	numClients_lock.Unlock()
	defer cleanupClient(client)

	subscribeToSessionUpdates(sessionID)

	// Handle messages
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("Error reading message:", err)
			break
		}

		handleClientMessage(client, message)
	}
}

func handleClientMessage(client *ClientConnection, message []byte) {
	var msg struct {
		Type  string          `json:"type"`
		State json.RawMessage `json:"state"`
	}

	if err := json.Unmarshal(message, &msg); err != nil {
		log.Println("Error unmarshaling message:", err)
		return
	}

	switch msg.Type {
	case "stateUpdate":
		if client.isHost {
			// log.Println(string(msg.State))
			var ctx = context.Background()
			val, err := rdb.Get(ctx, "session:"+client.sessionID+":state").Result()

			if err == redis.Nil {
				log.Printf("Invalid Session key %s \n", client.sessionID)
			} else if err != nil {
				log.Println("Error Synchronizing")
			}

			// Define a struct to hold the state with a timestamp
			stateFromRedis := RedisState{}
			stateFromMsg := RedisState{}

			// Unmarshal the state from Redis
			if err := json.Unmarshal([]byte(val), &stateFromRedis); err != nil {
				log.Println("Error unmarshaling state from Redis:", err)
				return
			}

			// Unmarshal the state from the message
			if err := json.Unmarshal(msg.State, &stateFromMsg); err != nil {
				log.Println("Error unmarshaling state from message:", err)
				return
			}

			// Compare the timestamps
			if stateFromMsg.Timestamp > stateFromRedis.Timestamp {
				stateJson, _ := json.Marshal(msg.State)
				err := rdb.SetEX(ctx, "session:"+client.sessionID+":state", stateJson, REDIS_MSG_EXPIRY).Err()
				if err != nil {
					log.Println("Error updating state in Redis:", err)
				}

				// Publish the state update to all clients in this session
				publishStateUpdate(client.sessionID, msg.State)

				// Also broadcast directly to connected clients on this server
				// broadcastState(client.sessionID, msg.State)
			}
		}
	case "heartbeat":
		// Send heartbeat acknowledgment
		client.conn.WriteJSON(map[string]string{"type": "heartbeatAck"})
	}
}

func broadcastState(sessionID string, state json.RawMessage) {
	if _, exists := client_lock[sessionID]; !exists {
		client_lock[sessionID] = &sync.Mutex{}
	}

	client_lock[sessionID].Lock()
	// Check if the session exists in the clients map
	sessionClients, exists := clients[sessionID]
	if !exists || len(sessionClients) == 0 {
		client_lock[sessionID].Unlock()
		return
	}

	clientsToSend := make([]*ClientConnection, len(sessionClients))
	copy(clientsToSend, sessionClients)
	client_lock[sessionID].Unlock()

	for _, client := range clientsToSend {
		if client == nil || client.conn == nil {
			continue
		}

		err := client.conn.WriteJSON(map[string]interface{}{
			"type":  "stateUpdate",
			"state": state,
		})
		if err != nil {
			log.Printf("Error broadcasting to client: %v", err)
			// Don't attempt to remove the client here - it will be handled by the connection handler
		}
	}
}

func cleanupClient(client *ClientConnection) {
	if client == nil || client.sessionID == "" {
		return
	}

	if _, exists := client_lock[client.sessionID]; !exists {
		client_lock[client.sessionID] = &sync.Mutex{}
	}

	client_lock[client.sessionID].Lock()
	sessionClients, exists := clients[client.sessionID]
	if !exists || len(sessionClients) == 0 {
		client_lock[client.sessionID].Unlock()
		return
	}

	for i, c := range sessionClients {
		if c == client {
			// Safely close the connection
			if client.conn != nil {
				client.conn.Close()
			}

			// Remove the client from the slice
			clients[client.sessionID] = append(sessionClients[:i], sessionClients[i+1:]...)
			break
		}
	}
	client_lock[client.sessionID].Unlock()

	numClients_lock.Lock()
	numClients -= 1
	numClients_lock.Unlock()
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"id":          serverID,
		"url":         serverURL,
		"capacity":    capacity,
		"currentLoad": numClients,
		"status":      "active",
		"lastPing":    time.Now().Unix(),
	}

	respondJSON(w, http.StatusOK, status)
}

// Video streaming endpoint
func streamVideo(w http.ResponseWriter, r *http.Request) {
	// Enhanced CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "bytes")

	// Handle OPTIONS preflight
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Handle range requests properly
	videoFile, err := os.Open(videoPath)
	if err != nil {
		http.Error(w, "Video not found", http.StatusNotFound)
		log.Printf("Video file missing at: %s", videoPath)
		return
	}
	defer videoFile.Close()

	stat, _ := videoFile.Stat()
	http.ServeContent(w, r, "video.mp4", stat.ModTime(), videoFile)
}

// ///////////////////////////////////// PUBSUB FUNCTIONS //////////////////////////////////////////////////////////////
func publishStateUpdate(sessionID string, state json.RawMessage) {
	ctx := context.Background()
	payload, err := json.Marshal(state)
	if err != nil {
		log.Println("Error marshaling state for publish:", err)
		return
	}

	err = rdb.Publish(ctx, "session-updates:"+sessionID, string(payload)).Err()
	if err != nil {
		log.Println("Error publishing state update:", err)
	}
}

func subscribeToSessionUpdates(sessionID string) {
	ctx := context.Background()
	pubsub = rdb.Subscribe(ctx, "session-updates:"+sessionID)

	go func() {
		for {
			msg, err := pubsub.ReceiveMessage(ctx)
			if err != nil {
				log.Println("Error receiving message:", err)
				continue
			}

			// Process received message
			handleSessionUpdate(msg.Channel, msg.Payload)
		}
	}()
}

// Handle session updates received from Redis pub/sub
func handleSessionUpdate(channel, payload string) {
	// Extract sessionID from channel
	sessionID := channel[len("session-updates:"):]
	log.Printf("Received update for session %s: %s", sessionID, payload)

	var state json.RawMessage
	err := json.Unmarshal([]byte(payload), &state)
	if err != nil {
		log.Println("Error unmarshaling state:", err)
		return
	}
	broadcastState(sessionID, state)
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
