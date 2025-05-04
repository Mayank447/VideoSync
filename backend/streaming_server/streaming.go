package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
	"github.com/go-redis/redis/v8"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"context"
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

var (
	// [TODO] Get the below 4 param through command line
	mainServerURL = "http://localhost:8080"
	serverID      = os.Getenv("SERVER_ID")
	serverURL     = os.Getenv("SERVER_URL")
	serverPort    = os.Getenv("SERVER_PORT")
	capacity      = 100 // Default capacity

	clients  = make(map[string]*ClientConnection)
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true // Allow all origins for now
		},
	}

	rdb          *redis.Client
)

const (
	videoPath = "../sample.mp4"
	HEARTBEAT_INTERVAL = 30
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
		server := StreamingServer{
			ID:          serverID,
			URL:         serverURL,
			Capacity:    capacity,
			CurrentLoad: len(clients),
			Status:      "active",
			LastPing:    time.Now().Unix(),
		}

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

	clients[sessionID] = client
	defer cleanupClient(client)

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
			log.Println(string(msg.State))
			// Broadcast state to all clients in the session
			// log.Println(msg.State)
			
			type State struct {
				CurrentTime  float64 `json:"currentTime"`
				Paused       bool    `json:"paused"`
				PlaybackRate float64 `json:"playbackRate"`
				Timestamp time.Time `json:"timestamp"`
			}

			var ctx = context.Background()
			val, err := rdb.Get(ctx, "session:"+client.sessionID+":state").Result()
			
			if err == redis.Nil {
				log.Printf("Invalid Session key %s \n", client.sessionID)
			} else if err != nil {
				log.Println("Error Synchronizing")
			}

			
			
			// broadcastState(client.sessionID, msg.State)
		}
	case "heartbeat":
		// Send heartbeat acknowledgment
		client.conn.WriteJSON(map[string]string{"type": "heartbeatAck"})
	}
}

func broadcastState(sessionID string, state json.RawMessage) {
	for _, client := range clients {
		if client.sessionID == sessionID {
			client.conn.WriteJSON(map[string]interface{}{
				"type":  "stateUpdate",
				"state": state,
			})
		}
	}
}


func cleanupClient(client *ClientConnection) {
	client.conn.Close()
	delete(clients, client.sessionID)
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"id":          serverID,
		"url":         serverURL,
		"capacity":    capacity,
		"currentLoad": len(clients),
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