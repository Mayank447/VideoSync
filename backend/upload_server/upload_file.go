package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
)

const MAX_UPLOAD_SIZE = 100 << 20 // 100 MB

var (
	bucket   string
	uploader *manager.Uploader
)

// init() will run before main()
func init() {
	// must export S3_BUCKET + AWS_REGION + AWS credentials in env
	bucket = "videosync-mayank447"
	region := "eu-north-1"

	// load AWS SDK config
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
	)
	if err != nil {
		log.Fatalf("unable to load AWS SDK config: %v", err)
	}

	// create a high-level uploader
	uploader = manager.NewUploader(s3.NewFromConfig(cfg))
}

// handleCORS sets permissive CORS headers
func handleCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Origin, Accept")
}

// handleVideoUpload streams the uploaded file to S3 under `<sessionID>/<filename>`
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

	// enforce max upload size
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

	// 1) Save the incoming file to disk
	tmpDir := filepath.Join("tmp", sessionID)
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		http.Error(w, "could not make temp dir", http.StatusInternalServerError)
		return
	}
	srcPath := filepath.Join(tmpDir, header.Filename)
	dst, err := os.Create(srcPath)
	if err != nil {
		http.Error(w, "could not save upload", http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(dst, file); err != nil {
		dst.Close()
		http.Error(w, "copy error", http.StatusInternalServerError)
		return
	}
	dst.Close()

	// 2) Generate per-quality HLS outputs
	hlsDir := filepath.Join(tmpDir, "hls")
	if err := os.MkdirAll(hlsDir, 0755); err != nil {
		http.Error(w, "could not make hls dir", http.StatusInternalServerError)
		return
	}

	// Define qualities in descending order
	variants := []struct {
		Name       string
		Resolution string
		Bandwidth  int
	}{
		{"720p", "1280x720", 2800000},
		{"480p", "854x480", 1400000},
		{"360p", "640x360", 800000},
	}

	for _, v := range variants {
		qualityDir := filepath.Join(hlsDir, v.Name)
		if err := os.MkdirAll(qualityDir, 0755); err != nil {
			http.Error(w, "could not make quality dir", http.StatusInternalServerError)
			return
		}
		playlist := filepath.Join(qualityDir, "playlist.m3u8")
		segmentPattern := filepath.Join(qualityDir, "segment_%03d.ts")

		// run ffmpeg for this quality
		cmd := exec.Command("ffmpeg",
			"-i", srcPath,
			"-c:v", "libx264", "-b:v", fmt.Sprintf("%dk", v.Bandwidth/1000),
			"-s", v.Resolution,
			"-c:a", "aac",
			"-hls_time", "5",
			"-hls_list_size", "0",
			"-hls_segment_filename", segmentPattern,
			playlist,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			http.Error(w, "ffmpeg failed for "+v.Name+": "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// 3) Write a master playlist referencing each quality
	masterFile := filepath.Join(hlsDir, "master.m3u8")
	mf, err := os.Create(masterFile)
	if err != nil {
		http.Error(w, "could not create master playlist", http.StatusInternalServerError)
		return
	}
	defer mf.Close()

	mf.WriteString("#EXTM3U\n")
	for _, v := range variants {
		mf.WriteString(fmt.Sprintf(
			"#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%s\n%s/playlist.m3u8\n",
			v.Bandwidth, v.Resolution, v.Name,
		))
	}

	// 4) Upload EVERY .m3u8 + .ts under hlsDir/*
	masterPath := filepath.Join(hlsDir, "master.m3u8")
	masterKey := filepath.Join(sessionID, "master.m3u8")

	f, err := os.Open(masterPath)
	if err != nil {
		log.Printf("could not open master playlist: %v", err)
	} else {
		_, err = uploader.Upload(context.Background(), &s3.PutObjectInput{
			Bucket:      &bucket,
			Key:         &masterKey,
			Body:        f,
			ContentType: aws.String("application/vnd.apple.mpegurl"),
		})
		f.Close()
		if err != nil {
			log.Printf("failed uploading master playlist: %v", err)
		}
	}

	files, _ := filepath.Glob(filepath.Join(hlsDir, "*", "*"))
	for _, path := range files {
		key := filepath.Join(sessionID, filepath.Base(filepath.Dir(path)), filepath.Base(path))
		f, err := os.Open(path)
		if err != nil {
			log.Printf("open %s: %v", path, err)
			continue
		}
		_, err = uploader.Upload(context.Background(), &s3.PutObjectInput{
			Bucket:      &bucket,
			Key:         &key,
			Body:        f,
			ContentType: aws.String(http.DetectContentType(readHeader(f))),
		})
		f.Close()
		if err != nil {
			log.Printf("upload %s: %v", key, err)
		}
	}

	// 5) clean up & return the master playlist URL
	os.RemoveAll(tmpDir)
	region := os.Getenv("AWS_REGION")
	playlistURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s/master.m3u8",
		bucket, region, sessionID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"sessionID":   sessionID,
		"playlistURL": playlistURL,
	})
}

// Helper to sniff content-type from the first 512 bytes
func readHeader(f *os.File) []byte {
	buf := make([]byte, 512)
	f.Seek(0, 0)
	n, _ := f.Read(buf)
	f.Seek(0, 0)
	return buf[:n]
}

func main() {
	port := flag.String("port", "8082", "port for upload server")
	flag.Parse()

	r := mux.NewRouter()

	// upload endpoint
	r.HandleFunc("/api/video/{sessionID}", handleVideoUpload).
		Methods(http.MethodPost, http.MethodOptions)

	// serve your upload_video.html + JS
	r.PathPrefix("/").Handler(http.FileServer(http.Dir("../frontend/pages")))

	// wrap in CORS
	cors := handlers.CORS(
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowedMethods([]string{"GET", "POST", "OPTIONS"}),
		handlers.AllowedHeaders([]string{"Content-Type", "Origin", "Accept"}),
	)(r)

	log.Printf("Upload server running on port %s", *port)
	log.Fatal(http.ListenAndServe(":"+*port, cors))
}
