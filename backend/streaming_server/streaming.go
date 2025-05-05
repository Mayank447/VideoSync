package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/handlers"
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
	send      chan []byte
}

type RedisState struct {
	Paused       bool    `json:"paused"`
	CurrentTime  float64 `json:"currentTime"`
	PlaybackRate float64 `json:"playbackRate"`
	Timestamp    int64   `json:"timestamp"`
}

type VideoManifest struct {
	ChunkDuration int     `json:"chunkDuration"` // Duration in seconds
	ChunkCount    int     `json:"chunkCount"`
	VideoDuration float64 `json:"videoDuration"` // Duration in seconds
	VideoFileType string  `json:"videoFileType"`
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

// HLS directory structure
const (
	HLS_BASE_DIR       = "../hls"
	HLS_PLAYLIST_NAME  = "playlist.m3u8"
	HLS_SEGMENT_DIR    = "segments"
	HLS_MASTER_NAME    = "master.m3u8"
	HEARTBEAT_INTERVAL = 30
	REDIS_MSG_EXPIRY   = 24 * time.Hour
	CHUNK_DURATION     = 5
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
	log.Println(pong, "Connected to Redis")

	// Initialize HLS system
	initHLS()

	portFlag := flag.String("port", "", "Port to run the server on")
	flag.Parse()

	if serverID == "" {
		serverID = fmt.Sprintf("ss-%d", time.Now().Unix())
	}
	if *portFlag != "" {
		serverPort = *portFlag
	} else if serverPort == "" {
		serverPort = "8081"
	}

	if serverURL == "" {
		serverURL = "http://localhost:" + serverPort
	}

	// Register with main server
	registerWithMainServer()

	// Start heartbeat goroutine
	go sendHeartbeats()

	// Setup routes
	r := mux.NewRouter()
	r.HandleFunc("/ws", handleWebSocket)
	r.HandleFunc("/status", handleStatus)

	// HLS routes
	r.HandleFunc("/hls/{sessionID}/master.m3u8", serveHLSMasterPlaylist).Methods("GET", "OPTIONS")
	r.HandleFunc("/hls/{sessionID}/playlist.m3u8", serveHLSMediaPlaylist).Methods("GET", "OPTIONS")
	r.HandleFunc("/hls/{sessionID}/{quality}/playlist.m3u8", serveHLSQualityPlaylist).Methods("GET", "OPTIONS")
	r.HandleFunc("/hls/{sessionID}/{segmentName}", serveHLSSegment).Methods("GET", "OPTIONS")
	r.HandleFunc("/hls/{sessionID}/{quality}/{segmentName}", serveHLSQualitySegment).Methods("GET", "OPTIONS")

	// Wrap the router with Gorilla's CORS handler:
	corsHandler := handlers.CORS(
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowedMethods([]string{"GET", "OPTIONS"}),
		handlers.AllowedHeaders([]string{"Content-Type", "Range"}),
		handlers.ExposedHeaders([]string{"Content-Length", "Content-Range", "Accept-Ranges"}),
	)(r)

	log.Printf("Streaming server starting on port %s", serverPort)
	log.Fatal(http.ListenAndServe(":"+serverPort, corsHandler))
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

	client.send = make(chan []byte, 256)
	go client.writePump()

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

	// Send the initial state to the client
	if !client.isHost {
		if client.conn != nil {
			ctx := context.Background()
			val, err := rdb.Get(ctx, "session:"+client.sessionID+":state").Result()

			if err == redis.Nil {
				log.Printf("Invalid Session key %s \n", client.sessionID)
			} else if err != nil {
				log.Println("Error Synchronizing")
			}

			var state json.RawMessage
			err = json.Unmarshal([]byte(val), &state)
			if err != nil {
				log.Println("Error unmarshaling state:", err)
				return
			}

			payload, err := json.Marshal(map[string]interface{}{
				"type":       "stateUpdate",
				"state":      state,
				"servertime": time.Now().UnixMilli(),
			})
			if err != nil {
				log.Println("Error marshaling state:", err)
				return
			}

			select {
			case client.send <- payload:
			default:
				log.Printf("Dropping message to client in session %s (send buffer full)", sessionID)
			}
		}
	}
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

func (c *ClientConnection) writePump() {
	for {
		msg, ok := <-c.send
		if !ok {
			// Channel closed, close the WebSocket
			c.conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		}

		err := c.conn.WriteMessage(websocket.TextMessage, msg)
		if err != nil {
			log.Println("writePump error:", err)
			return
		}
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
			}
		}

	case "videoMetadata":
		videoMetadata := VideoManifest{
			ChunkDuration: 5,
			ChunkCount:    10,
			VideoDuration: 117,
			VideoFileType: "mp4",
		}

		payload, err := json.Marshal(map[string]interface{}{
			"type":  "videoMetadata",
			"state": videoMetadata,
		})

		if err != nil {
			log.Println("Error marshaling video metadata:", err)
			return
		}
		client.send <- payload

	case "heartbeat":
		// Send heartbeat acknowledgment
		client.conn.WriteJSON(map[string]string{"type": "heartbeatAck"})
	}
}

func cleanupClient(client *ClientConnection) {
	if client == nil || client.sessionID == "" {
		return
	}
	if client.send != nil {
		close(client.send)
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

// ////////////////////////////////////// PUBSUB FUNCTIONS //////////////////////////////////////////////////////////////
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

		payload, err := json.Marshal(map[string]interface{}{
			"type":       "stateUpdate",
			"state":      state,
			"servertime": time.Now().UnixMilli(),
		})
		if err != nil {
			log.Printf("Error marshaling broadcast state: %v", err)
			return
		}

		for _, client := range clientsToSend {
			if client == nil || client.conn == nil {
				continue
			}
			select {
			case client.send <- payload:
			default:
				log.Printf("Dropping message to client in session %s (send buffer full)", sessionID)
			}
		}

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

// ///////////////////////////////////// HLS FUNCTIONS //////////////////////////////////////////////////////////////
func initHLS() {
	// Create base HLS directory if it doesn't exist
	if _, err := os.Stat(HLS_BASE_DIR); err != nil {
		if os.IsNotExist(err) {
			log.Fatalf("HLS base directory doesn't exist: %v", err)
		}
	}

	log.Println("HLS system initialized")
}

// Handle CORS preflight requests
func handleCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Origin, Accept")
}

// Serve HLS master playlist (contains multiple quality variants)
func serveHLSMasterPlaylist(w http.ResponseWriter, r *http.Request) {
	handleCORS(w)
	if r.Method == "OPTIONS" {
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]

	// Check if the master playlist file exists
	masterPath := filepath.Join(HLS_BASE_DIR, HLS_MASTER_NAME)
	if _, err := os.Stat(masterPath); err == nil {
		// Master playlist exists, serve it
		http.ServeFile(w, r, masterPath)
		log.Printf("Served existing master playlist for session %s", sessionID)
		return
	}

	// Serve the newly generated master playlist
	http.ServeFile(w, r, masterPath)
}

// Serve HLS media playlist (contains segment info)
func serveHLSMediaPlaylist(w http.ResponseWriter, r *http.Request) {
	handleCORS(w)
	if r.Method == "OPTIONS" {
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]

	playlistPath := filepath.Join(HLS_BASE_DIR, HLS_PLAYLIST_NAME)
	// Check if playlist exists
	if _, err := os.Stat(playlistPath); os.IsNotExist(err) {
		http.Error(w, "Playlist not found", http.StatusNotFound)
		return
	}

	// Read the playlist content
	playlistContent, err := os.ReadFile(playlistPath)
	if err != nil {
		http.Error(w, "Error reading playlist", http.StatusInternalServerError)
		log.Printf("Error reading playlist for session %s: %v", sessionID, err)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(playlistContent)

	log.Printf("Served media playlist for session %s", sessionID)
}

// Add a quality-specific playlist handler
func serveHLSQualityPlaylist(w http.ResponseWriter, r *http.Request) {
	handleCORS(w)
	if r.Method == "OPTIONS" {
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]
	quality := vars["quality"]

	playlistPath := filepath.Join(HLS_BASE_DIR, quality, HLS_PLAYLIST_NAME)

	// Check if playlist exists
	if _, err := os.Stat(playlistPath); os.IsNotExist(err) {
		http.Error(w, "Quality playlist not found", http.StatusNotFound)
		return
	}

	// Read the playlist content
	http.ServeFile(w, r, playlistPath)
	log.Printf("Served %s playlist for session %s", quality, sessionID)
}

// Serve HLS segment
func serveHLSSegment(w http.ResponseWriter, r *http.Request) {
	handleCORS(w)
	log.Println("Serving segment")
	if r.Method == "OPTIONS" {
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]
	segmentName := vars["segmentName"]

	// Validate segment name to prevent directory traversal
	if strings.Contains(segmentName, "..") || strings.Contains(segmentName, "/") {
		http.Error(w, "Invalid segment name", http.StatusBadRequest)
		return
	}

	segmentPath := filepath.Join(HLS_BASE_DIR, HLS_SEGMENT_DIR, segmentName)
	log.Printf("Serving segment %s for session %s", segmentPath, sessionID)
	// Check if segment exists
	if _, err := os.Stat(segmentPath); os.IsNotExist(err) {
		http.Error(w, "Segment not found", http.StatusNotFound)
		return
	}

	// Serve the segment file
	http.ServeFile(w, r, segmentPath)
	log.Printf("Served segment %s for session %s", segmentName, sessionID)
}

// Add a quality-specific segment handler
func serveHLSQualitySegment(w http.ResponseWriter, r *http.Request) {
	handleCORS(w)
	log.Println("Serving quality segment")
	if r.Method == "OPTIONS" {
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]
	quality := vars["quality"]
	segmentName := vars["segmentName"]

	// Validate segment name to prevent directory traversal
	if strings.Contains(segmentName, "..") || strings.Contains(segmentName, "/") {
		http.Error(w, "Invalid segment name", http.StatusBadRequest)
		return
	}

	segmentPath := filepath.Join(HLS_BASE_DIR, quality, segmentName)
	log.Printf("Serving segment %s for session %s", segmentPath, sessionID)
	// Check if segment exists
	if _, err := os.Stat(segmentPath); os.IsNotExist(err) {
		http.Error(w, "Quality segment not found", http.StatusNotFound)
		return
	}

	// Serve the segment file
	http.ServeFile(w, r, segmentPath)
	log.Printf("Served %s segment %s for session %s", quality, segmentName, sessionID)
}
