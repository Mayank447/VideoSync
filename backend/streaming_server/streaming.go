package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
)

type StreamingConfig struct {
	Port      string
	RedisAddr string
	VideoDir  string
}

var (
	streamCfg = StreamingConfig{
		Port:      "8081",
		RedisAddr: "localhost:6379",
		VideoDir:  "./media",
	}
	rdb *redis.Client
)

func main() {
	rdb = redis.NewClient(&redis.Options{Addr: streamCfg.RedisAddr})

	r := mux.NewRouter()
	r.Use(streamingMiddleware)

	r.HandleFunc("/video/{filename}", streamVideo).Methods("GET", "HEAD")
	r.HandleFunc("/health", healthCheck)

	log.Printf("Streaming server starting on :%s", streamCfg.Port)
	log.Fatal(http.ListenAndServe(":"+streamCfg.Port, handlers.CORS(
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowedMethods([]string{"GET", "HEAD"}),
	)(r)))
}

func streamingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Server-Type", "streaming-server")
		next.ServeHTTP(w, r)
	})
}

func streamVideo(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	filename := vars["filename"]
	videoPath := fmt.Sprintf("%s/%s", streamCfg.VideoDir, filename)

	if _, err := os.Stat(videoPath); os.IsNotExist(err) {
		http.Error(w, "Video not found", http.StatusNotFound)
		return
	}

	http.ServeFile(w, r, videoPath)
}

func healthCheck(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		http.Error(w, "Redis connection failed", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":   "ok",
		"server":   "streaming",
		"capacity": "available",
	})
}
