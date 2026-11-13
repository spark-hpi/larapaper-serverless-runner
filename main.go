package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	timeoutCap      int    = 30
	memLimit        uint64 = 128 << 20
	nodeRuntime            = nodeRuntimeNode
	apiKey          string
	maxRequestBytes int64 = 5 << 20
	logPayloads     bool
	// runSlots bounds concurrent transforms; a full channel means busy.
	runSlots = make(chan struct{}, goruntime.NumCPU()*2)
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const (
	manifestName            = "BYOS Transform Runner"
	manifestAuthor          = "Benjamin Nussbaum, Spark HPI"
	manifestLicense         = "EUPL-1.2"
	manifestRepository      = "https://github.com/bnussbau/byos-transform-runner"
	manifestContributorInfo = "Based on the work of https://github.com/spark-hpi/larapaper-serverless-runner"
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
	if v := os.Getenv("TRANSFORM_NODE_RUNTIME"); v != "" {
		switch v = strings.ToLower(strings.TrimSpace(v)); v {
		case nodeRuntimeNode, nodeRuntimeTxiki:
			nodeRuntime = v
		default:
			log.Printf("invalid TRANSFORM_NODE_RUNTIME %q, using default %q", v, nodeRuntimeNode)
			nodeRuntime = nodeRuntimeNode
		}
	}
	apiKey = os.Getenv("TRANSFORM_API_KEY")
	if v := os.Getenv("TRANSFORM_MAX_REQUEST_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			log.Printf("invalid TRANSFORM_MAX_REQUEST_BYTES %q, using default %d", v, maxRequestBytes)
		} else {
			maxRequestBytes = n
		}
	}
	if v := os.Getenv("TRANSFORM_MAX_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			log.Printf("invalid TRANSFORM_MAX_CONCURRENCY %q, using default %d", v, cap(runSlots))
		} else {
			runSlots = make(chan struct{}, n)
		}
	}
	logPayloads = strings.EqualFold(strings.TrimSpace(os.Getenv("LOG_LEVEL")), "debug")
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

	freshProcAvailable()
	if apiKey == "" {
		log.Printf("WARNING: TRANSFORM_API_KEY is not set; anyone who can reach this port can run code")
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      time.Duration(timeoutCap+15) * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutCap+5)*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx) //nolint:errcheck
	}()

	log.Printf("listening on :%s (max concurrency %d)", port, cap(runSlots))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /run", requireAPIKey(handleRun))
	mux.HandleFunc("GET /manifest", handleManifest)
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("GET /{$}", handleManifest)
	return mux
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
}

type manifest struct {
	Name                 string   `json:"name"`
	Version              string   `json:"version"`
	Author               string   `json:"author"`
	License              string   `json:"license"`
	Repository           string   `json:"repository"`
	ContributorInfo      string   `json:"contributor_info"`
	LanguageCapabilities []string `json:"language_capabilities"`
}

// manifestDisplayName marks the slim variant, which runs JS on txiki.js.
func manifestDisplayName() string {
	if nodeRuntime == nodeRuntimeTxiki {
		return manifestName + " (slim)"
	}
	return manifestName
}

// handleManifest describes this runner instance: identity, version and capabilities.
func handleManifest(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, manifest{
		Name:                 manifestDisplayName(),
		Version:              version,
		Author:               manifestAuthor,
		License:              manifestLicense,
		Repository:           manifestRepository,
		ContributorInfo:      manifestContributorInfo,
		LanguageCapabilities: availableLanguages(),
	})
}

// availableLanguages lists the supported languages whose interpreter is on PATH, sorted.
func availableLanguages() []string {
	languages := []string{}
	for lang := range supportedLanguages {
		rt, ok := runtimeFor(lang)
		if !ok {
			continue
		}
		if _, err := exec.LookPath(rt.interp[0]); err == nil {
			languages = append(languages, lang)
		}
	}
	slices.Sort(languages)
	return languages
}

var supportedLanguages = map[string]bool{
	"python": true,
	"node":   true,
	"php":    true,
	"ruby":   true,
}

// languageAliases maps alternative spellings (e.g. from settings.yml
// `serverless_language`) to the canonical language names.
var languageAliases = map[string]string{
	"javascript": "node",
	"js":         "node",
	"nodejs":     "node",
	"py":         "python",
	"python3":    "python",
	"rb":         "ruby",
}

func normalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if canonical, ok := languageAliases[lang]; ok {
		return canonical
	}
	return lang
}

// requireAPIKey enforces `Authorization: Bearer <TRANSFORM_API_KEY>` when a key is configured.
func requireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if apiKey != "" {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next(w, r)
	}
}

func handleRun(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req RunRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.Input) == 0 || string(req.Input) == "null" {
		req.Input = json.RawMessage(`{}`)
	}

	req.Language = normalizeLanguage(req.Language)
	if !supportedLanguages[req.Language] {
		errMsg := "unsupported language: " + req.Language
		logRun(req, nil, start, errMsg)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
		return
	}

	select {
	case runSlots <- struct{}{}:
		defer func() { <-runSlots }()
	default:
		logRun(req, nil, start, "busy")
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "runner busy, retry later"})
		return
	}

	output, logs, err := execute(req)
	if err != nil {
		logRun(req, nil, start, err.Error())
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error(), "logs": logs})
		return
	}

	logRun(req, output, start, "")
	writeJSON(w, http.StatusOK, map[string]any{"output": output, "logs": logs})
}

// logRun writes one line per request. Payloads may hold plugin secrets, so
// they are only logged with LOG_LEVEL=debug.
func logRun(req RunRequest, output []byte, start time.Time, errMsg string) {
	line := "run language=" + req.Language +
		" duration_ms=" + strconv.FormatInt(time.Since(start).Milliseconds(), 10) +
		" input_bytes=" + strconv.Itoa(len(req.Input))
	if errMsg == "" {
		line += " status=ok output_bytes=" + strconv.Itoa(len(output))
	} else {
		line += " status=error"
	}
	if logPayloads {
		line += " input=" + payloadForLog(req.Input)
		if errMsg == "" {
			line += " output=" + payloadForLog(output)
		}
	}
	if errMsg != "" {
		line += " error=" + strconv.Quote(lastLines(errMsg, 3))
	}
	log.Print(line)
}

// payloadForLog keeps request logs bounded. The cap matches the stderr returned to callers.
func payloadForLog(b []byte) string {
	if len(b) <= maxLogBytes {
		return string(b)
	}
	return string(b[:maxLogBytes]) + "...(truncated)"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v) //nolint:errcheck
}
