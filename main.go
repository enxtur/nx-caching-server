// Nx Caching Server
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func GetEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

const (
	storageDirKey       = "STORAGE_DIR"
	cleanupThresholdKey = "CLEANUP_THRESHOLD"
	portKey             = "PORT"
	authTokenKey        = "AUTH_TOKEN"
)

func UploadTaskOutput(w http.ResponseWriter, req *http.Request) {
	contentLengthString := req.Header.Get("Content-Length")
	if contentLengthString == "" {
		http.Error(w, "Content-Length header is required", http.StatusBadRequest)
		return
	}

	contentLength, err := strconv.ParseInt(contentLengthString, 10, 64)
	if err != nil {
		http.Error(w, "Invalid Content-Length header", http.StatusBadRequest)
		return
	}

	hash := req.PathValue("hash")
	storageDir := GetEnv(storageDirKey, os.TempDir())
	filePath := filepath.Join(storageDir, fmt.Sprintf("%s.cache", hash))

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if os.IsExist(err) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return
	}

	_, err = io.CopyN(file, req.Body, contentLength)
	if err != nil {
		file.Close()
		os.Remove(filePath) // Clean up incomplete file
		http.Error(w, "Failed to write file", http.StatusInternalServerError)
		return
	}
	file.Close()

	w.WriteHeader(http.StatusOK)
}

func CheckTaskOutput(w http.ResponseWriter, req *http.Request) {
	hash := req.PathValue("hash")
	storageDir := GetEnv(storageDirKey, os.TempDir())
	filePath := filepath.Join(storageDir, fmt.Sprintf("%s.cache", hash))

	_, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to check the file", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func DownloadTaskOutput(w http.ResponseWriter, req *http.Request) {
	hash := req.PathValue("hash")
	storageDir := GetEnv(storageDirKey, os.TempDir())
	filePath := filepath.Join(storageDir, fmt.Sprintf("%s.cache", hash))

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to read the file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		http.Error(w, "Failed to read the file", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
	w.Header().Set("Content-Type", "application/octet-stream")

	io.Copy(w, file)
}

func CheckBearerTokenMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		authToken := os.Getenv(authTokenKey)
		if authToken == "" {
			next(w, req)
			return
		}

		authHeader := req.Header.Get("Authorization")
		if authHeader == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if parts[1] != authToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		next(w, req)
	}
}

func HandleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "OK")
}

func HandleTask(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "PUT":
		UploadTaskOutput(w, req)
	case "HEAD":
		CheckTaskOutput(w, req)
	case "GET":
		DownloadTaskOutput(w, req)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func cleanupOldRecords(cleanupThreshold time.Duration) {
	storageDir := GetEnv(storageDirKey, os.TempDir())
	err := filepath.Walk(storageDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		if !strings.HasSuffix(path, ".cache") {
			return nil
		}

		stat, ok := info.Sys().(*unix.Stat_t)
		if !ok {
			log.Printf("Skipping %s: no syscall.Stat_t", path)
			return nil
		}

		atime := time.Unix(int64(stat.Atim.Sec), int64(stat.Atim.Nsec))
		if time.Since(atime) > cleanupThreshold {
			log.Printf("Removing %s: last accessed %s ago", path, time.Since(atime))
			return os.Remove(path)
		}
		return nil
	})

	if err != nil {
		log.Printf("Error walking the storage directory: %v", err)
	}
}

func main() {

	cleanupThreshold, err := time.ParseDuration(GetEnv(cleanupThresholdKey, "1h"))

	if err != nil {
		log.Fatalf("Invalid cleanup threshold: %v", err)
	}

	go func() {
		ticker := time.NewTicker(cleanupThreshold)
		defer ticker.Stop()
		for {
			cleanupOldRecords(cleanupThreshold)
			<-ticker.C
		}
	}()

	http.HandleFunc("/health", HandleHealth)
	http.HandleFunc("/v1/cache/{hash}", CheckBearerTokenMiddleware(HandleTask))

	port := GetEnv(portKey, "8090")

	http.ListenAndServe(fmt.Sprintf(":%s", port), nil)
}
