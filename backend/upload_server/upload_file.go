package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
)

const (
	// allow uploads up to 100 MB
	MAX_UPLOAD_SIZE = 100 << 20
)

// handleCORS sets CORS headers for the upload endpoint
func handleCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Origin, Accept")
}

// SetupUploadRoutes registers the upload endpoint
func SetupUploadRoutes(r *mux.Router) {
	r.HandleFunc("/api/video/{sessionID}", handleVideoUpload).
		Methods(http.MethodPost, http.MethodOptions)
}

// handleVideoUpload accepts a multipart form with field "video"
func handleVideoUpload(w http.ResponseWriter, r *http.Request) {
	handleCORS(w)
	if r.Method == http.MethodOptions {
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]
	if sessionID == "" {
		http.Error(w, "Missing sessionID", http.StatusBadRequest)
		return
	}

	// cap the upload size
	r.Body = http.MaxBytesReader(w, r.Body, MAX_UPLOAD_SIZE)
	if err := r.ParseMultipartForm(MAX_UPLOAD_SIZE); err != nil {
		http.Error(w, "File too large", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		http.Error(w, "Missing 'video' form field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// ensure directory exists
	uploadDir := filepath.Join("uploads", sessionID)
	if err := os.MkdirAll(uploadDir, os.ModePerm); err != nil {
		http.Error(w, "Could not create upload directory", http.StatusInternalServerError)
		return
	}

	// save the file
	dstPath := filepath.Join(uploadDir, header.Filename)
	dst, err := os.Create(dstPath)
	if err != nil {
		http.Error(w, "Could not save file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		http.Error(w, "Failed copying file", http.StatusInternalServerError)
		return
	}

	// return JSON with sessionID & filename
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"sessionID": sessionID,
		"fileName":  header.Filename,
		"path":      dstPath,
	})
}

func main() {
	// allow a custom port, default 8082
	var port string
	flag.StringVar(&port, "port", "8082", "port for upload server")
	flag.Parse()

	// build router & register your upload route
	r := mux.NewRouter()
	SetupUploadRoutes(r)

	// wrap with CORS
	corsHandler := handlers.CORS(
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowedMethods([]string{"POST", "OPTIONS"}),
		handlers.AllowedHeaders([]string{"Content-Type", "Origin", "Accept"}),
	)(r)

	log.Printf("Upload server starting on port %s", port)
	if err := http.ListenAndServe(":"+port, corsHandler); err != nil {
		log.Fatalf("Upload server failed: %v", err)
	}
}
