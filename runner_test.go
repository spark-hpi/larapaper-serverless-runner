package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

func TestResolveTimeout(t *testing.T) {
	orig := timeoutCap
	defer func() { timeoutCap = orig }()
	timeoutCap = 10

	tests := []struct {
		req  int
		want int
	}{
		{0, 10},
		{-1, 10},
		{11, 10},
		{10, 10},
		{9, 9},
		{1, 1},
	}
	for _, tt := range tests {
		got := resolveTimeout(tt.req)
		if got != tt.want {
			t.Errorf("resolveTimeout(%d) = %d, want %d", tt.req, got, tt.want)
		}
	}
}

func requireInterpreter(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH", name)
	}
}

func TestHandleHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handleHealth(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestRunPHP(t *testing.T) {
	requireInterpreter(t, "php")
	output := mustRun(t, RunRequest{
		Language: "php",
		Code:     `function run($input) { return ["doubled" => $input["value"] * 2]; }`,
		Input:    json.RawMessage(`{"value": 5}`),
		Timeout:  10,
	})
	if output["doubled"] != float64(10) {
		t.Fatalf("expected doubled=10, got %v", output)
	}
}

func TestRunPHPStripsTag(t *testing.T) {
	requireInterpreter(t, "php")
	output := mustRun(t, RunRequest{
		Language: "php",
		Code:     "<?php function run($input) { return [\"ok\" => true]; }",
		Input:    json.RawMessage(`{}`),
		Timeout:  10,
	})
	if output["ok"] != true {
		t.Fatalf("expected ok=true, got %v", output)
	}
}

func TestRunNode(t *testing.T) {
	requireInterpreter(t, "node")
	output := mustRun(t, RunRequest{
		Language: "node",
		Code:     `function run(input) { return { doubled: input.value * 2 }; }`,
		Input:    json.RawMessage(`{"value": 5}`),
		Timeout:  10,
	})
	if output["doubled"] != float64(10) {
		t.Fatalf("expected doubled=10, got %v", output)
	}
}

func TestRunPython(t *testing.T) {
	requireInterpreter(t, "python3")
	output := mustRun(t, RunRequest{
		Language: "python",
		Code:     "def run(input):\n    return {'doubled': input['value'] * 2}",
		Input:    json.RawMessage(`{"value": 5}`),
		Timeout:  10,
	})
	if output["doubled"] != float64(10) {
		t.Fatalf("expected doubled=10, got %v", output)
	}
}

func TestRunUnsupportedLanguage(t *testing.T) {
	w := doRequest(t, RunRequest{Language: "cobol", Code: "noop", Input: json.RawMessage(`{}`), Timeout: 5})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestRunNonZeroExit(t *testing.T) {
	requireInterpreter(t, "php")
	w := doRequest(t, RunRequest{
		Language: "php",
		Code:     `this is not valid php`,
		Input:    json.RawMessage(`{}`),
		Timeout:  5,
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}
}

func TestRunTimeout(t *testing.T) {
	requireInterpreter(t, "php")
	w := doRequest(t, RunRequest{
		Language: "php",
		Code:     `<?php function run($input) { sleep(10); return $input; }`,
		Input:    json.RawMessage(`{}`),
		Timeout:  1,
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp["error"] == "" {
		t.Fatal("expected error message in response body")
	}
}

func TestRunNonObjectOutput(t *testing.T) {
	requireInterpreter(t, "php")
	// run() returns null → harness writes "null" → not a JSON object → 422
	w := doRequest(t, RunRequest{
		Language: "php",
		Code:     `function run($input) { return null; }`,
		Input:    json.RawMessage(`{}`),
		Timeout:  5,
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}
}

func TestSubprocessRunsAsNobody(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root to test user isolation")
	}
	requireInterpreter(t, "python3")
	output := mustRun(t, RunRequest{
		Language: "python",
		Code:     "def run(input):\n    import os\n    return {'uid': os.getuid()}",
		Input:    json.RawMessage(`{}`),
		Timeout:  5,
	})
	uid := uint32(output["uid"].(float64))
	if uid == 0 {
		t.Fatal("expected non-root subprocess, got uid 0")
	}
	if uid != nobodyUID {
		t.Fatalf("expected uid %d (nobody), got %d", nobodyUID, uid)
	}
}

// mustRun calls handleRun and asserts 200, returns the decoded output map.
func mustRun(t *testing.T, r RunRequest) map[string]any {
	t.Helper()
	w := doRequest(t, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var output map[string]any
	if err := json.Unmarshal(resp["output"], &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	return output
}

// doRequest POSTs r to handleRun via httptest and returns the recorder.
func doRequest(t *testing.T, r RunRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(r)
	req := httptest.NewRequest(http.MethodPost, "/run", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleRun(w, req)
	return w
}
