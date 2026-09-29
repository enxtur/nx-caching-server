// Nx Caching Server
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	authTokensKey       = "AUTH_TOKENS"
)

// validHash allows only characters that cannot form path separators or
// traversal sequences, so the hash is always a single file name.
var validHash = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// cacheFilePath returns the storage path for hash within namespace, or false
// if hash is invalid. An empty namespace stores files directly in STORAGE_DIR.
func cacheFilePath(namespace, hash string) (string, bool) {
	if !validHash.MatchString(hash) {
		return "", false
	}
	storageDir := GetEnv(storageDirKey, os.TempDir())
	return filepath.Join(storageDir, namespace, fmt.Sprintf("%s.cache", hash)), true
}

func UploadTaskOutput(w http.ResponseWriter, req *http.Request) {
	filePath, ok := cacheFilePath(namespaceFrom(req), req.PathValue("hash"))
	if !ok {
		http.Error(w, "Invalid hash", http.StatusBadRequest)
		return
	}

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
	filePath, ok := cacheFilePath(namespaceFrom(req), req.PathValue("hash"))
	if !ok {
		http.Error(w, "Invalid hash", http.StatusBadRequest)
		return
	}

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
	filePath, ok := cacheFilePath(namespaceFrom(req), req.PathValue("hash"))
	if !ok {
		http.Error(w, "Invalid hash", http.StatusBadRequest)
		return
	}

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

// loadAuthTokens returns the SHA-256 hashes of the configured tokens mapped to
// their namespace. It returns nil when authentication is disabled. A token set
// through AUTH_TOKEN maps to the empty namespace (flat STORAGE_DIR).
func loadAuthTokens() (map[[32]byte]string, error) {
	authToken := os.Getenv(authTokenKey)
	authTokens := os.Getenv(authTokensKey)

	if authToken != "" && authTokens != "" {
		return nil, fmt.Errorf("%s and %s cannot be used together", authTokenKey, authTokensKey)
	}

	if authToken != "" {
		return map[[32]byte]string{sha256.Sum256([]byte(authToken)): ""}, nil
	}

	if authTokens == "" {
		return nil, nil
	}

	tokens := make(map[[32]byte]string)
	names := make(map[string]bool)
	for i, entry := range strings.Split(authTokens, ",") {
		name, token, found := strings.Cut(strings.TrimSpace(entry), ":")
		name, token = strings.TrimSpace(name), strings.TrimSpace(token)
		if !found || name == "" || token == "" {
			return nil, fmt.Errorf("%s entry %d: expected name:token", authTokensKey, i+1)
		}
		if !validHash.MatchString(name) {
			return nil, fmt.Errorf("%s entry %d: invalid name %q (allowed: letters, digits, '_' and '-')", authTokensKey, i+1, name)
		}
		if names[name] {
			return nil, fmt.Errorf("%s: duplicate name %q", authTokensKey, name)
		}
		hash := sha256.Sum256([]byte(token))
		if _, ok := tokens[hash]; ok {
			return nil, fmt.Errorf("%s: duplicate token for name %q", authTokensKey, name)
		}
		names[name] = true
		tokens[hash] = name
	}
	return tokens, nil
}

type namespaceCtxKey struct{}

// namespaceFrom returns the namespace assigned by CheckBearerTokenMiddleware,
// or "" when none was set.
func namespaceFrom(req *http.Request) string {
	ns, _ := req.Context().Value(namespaceCtxKey{}).(string)
	return ns
}

// CheckBearerTokenMiddleware authenticates the request against tokens and
// stores the matching namespace in the request context. A nil map disables
// authentication. Looking up the SHA-256 hash in a map is safe against timing
// attacks: any timing difference leaks information about the hash, not the token.
func CheckBearerTokenMiddleware(tokens map[[32]byte]string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if tokens == nil {
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

		ns, ok := tokens[sha256.Sum256([]byte(parts[1]))]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		next(w, req.WithContext(context.WithValue(req.Context(), namespaceCtxKey{}, ns)))
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

	if !strings.HasSuffix(storageDir, "/") {
		storageDir = storageDir + "/"
	}

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

		var stat unix.Stat_t
		if err := unix.Stat(path, &stat); err != nil {
			log.Printf("Skipping %s: unix.Stat failed: %v", path, err)
			return nil
		}

		atime := time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
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

	tokens, err := loadAuthTokens()
	if err != nil {
		log.Fatalf("Invalid auth configuration: %v", err)
	}

	storageDir := GetEnv(storageDirKey, os.TempDir())
	var namespaces []string
	for _, name := range tokens {
		if name == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Join(storageDir, name), 0755); err != nil {
			log.Fatalf("Failed to create namespace directory %q: %v", name, err)
		}
		namespaces = append(namespaces, name)
	}
	if len(namespaces) > 0 {
		sort.Strings(namespaces)
		log.Printf("Loaded namespaces: %s", strings.Join(namespaces, ", "))
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
	http.HandleFunc("/v1/cache/{hash}", CheckBearerTokenMiddleware(tokens, HandleTask))

	port := GetEnv(portKey, "8090")

	http.ListenAndServe(fmt.Sprintf(":%s", port), nil)
}
