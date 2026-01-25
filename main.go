package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

func uploadTaskOutput(w http.ResponseWriter, req *http.Request) {
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
	filePath := filepath.Join("caches", fmt.Sprintf("%s.cache", hash))

	_, err = os.Stat(filePath)
	if err == nil {
		http.Error(w, "Cannot override an existing record", http.StatusConflict)
		return
	}

	body := make([]byte, contentLength)
	_, err = io.ReadFull(io.LimitReader(req.Body, contentLength), body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}

	err = os.WriteFile(filePath, body, 0644)
	if err != nil {
		http.Error(w, "Failed to write to file", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Successfully uploaded the output")
}

func downloadTaskOutput(w http.ResponseWriter, req *http.Request) {
	hash := req.PathValue("hash")
	filePath := filepath.Join("caches", fmt.Sprintf("%s.cache", hash))

	body, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "The record was not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to read the file", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(body)), 10))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func handleTask(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "PUT":
		uploadTaskOutput(w, req)
	case "GET":
		downloadTaskOutput(w, req)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func main() {
	http.HandleFunc("/v1/cache/{hash}", handleTask)

	http.ListenAndServe(":8090", nil)
}
