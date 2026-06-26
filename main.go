package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
)

var (
	timeoutCap int    = 30
	memLimit   uint64 = 128 << 20
)

func initConfig() {
	if v := os.Getenv("TRANSFORM_TIMEOUT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			log.Printf("invalid TRANSFORM_TIMEOUT %q, using default %d", v, timeoutCap)
		} else {
			timeoutCap = n
		}
	}
	if v := os.Getenv("TRANSFORM_MEMORY_LIMIT"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil || n == 0 {
			log.Printf("invalid TRANSFORM_MEMORY_LIMIT %q, using default %d", v, memLimit)
		} else {
			memLimit = n
		}
	}
}

type RunRequest struct {
	Language string          `json:"language"`
	Code     string          `json:"code"`
	Input    json.RawMessage `json:"input"`
	Timeout  int             `json:"timeout"`
}

func main() {
	initConfig()
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	http.HandleFunc("/run", handleRun)
	http.HandleFunc("/health", handleHealth)

	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

var supportedLanguages = map[string]bool{
	"python": true,
	"node":   true,
	"php":    true,
}

func handleRun(w http.ResponseWriter, r *http.Request) {
	var req RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if !supportedLanguages[req.Language] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported language: " + req.Language})
		return
	}

	output, err := execute(req)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]json.RawMessage{"output": output})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
