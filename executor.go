package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	nobodyUID uint32 = 65534
	nobodyGID uint32 = 65534
)

func init() {
	u, err := user.Lookup("nobody")
	if err != nil {
		log.Fatalf("nobody user not found: %v", err)
	}
	if uid, err := strconv.ParseUint(u.Uid, 10, 32); err == nil {
		nobodyUID = uint32(uid)
	}
	if gid, err := strconv.ParseUint(u.Gid, 10, 32); err == nil {
		nobodyGID = uint32(gid)
	}
}

var interpreters = map[string]string{
	"python": "python3",
	"node":   "node",
	"php":    "php",
}

var extensions = map[string]string{
	"python": "py",
	"node":   "js",
	"php":    "php",
}

func resolveTimeout(req int) int {
	if req <= 0 || req > timeoutCap {
		return timeoutCap
	}
	return req
}

func execute(req RunRequest) (json.RawMessage, error) {
	interp, ok := interpreters[req.Language]
	if !ok {
		return nil, fmt.Errorf("unsupported language: %s", req.Language)
	}

	tmpDir, err := os.MkdirTemp("", "trmnl-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.Chmod(tmpDir, 0711); err != nil {
		return nil, fmt.Errorf("chmod tmpdir: %w", err)
	}

	outputPath := filepath.Join(tmpDir, "output.json")
	scriptPath := filepath.Join(tmpDir, "transform."+extensions[req.Language])

	if err := os.WriteFile(scriptPath, []byte(buildHarness(req.Language, req.Code, outputPath)), 0644); err != nil {
		return nil, fmt.Errorf("write script: %w", err)
	}

	// Pre-create output file owned by runner with 622 so nobody can write to it.
	if err := os.WriteFile(outputPath, []byte{}, 0622); err != nil {
		return nil, fmt.Errorf("create output file: %w", err)
	}
	if err := os.Chmod(outputPath, 0622); err != nil {
		return nil, fmt.Errorf("chmod output file: %w", err)
	}

	timeout := resolveTimeout(req.Timeout)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, interp, scriptPath)
	cmd.Stdin = bytes.NewReader(req.Input)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: nobodyUID, Gid: nobodyGID},
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("transform timed out after %d seconds", timeout)
		}
		return nil, fmt.Errorf("transform exited non-zero: %s", strings.TrimSpace(stderr.String()))
	}

	raw, err := os.ReadFile(outputPath)
	if err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("transform produced no output")
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("transform output is not valid JSON: %w", err)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, fmt.Errorf("transform output must be a JSON object, got %T", v)
	}

	return json.RawMessage(raw), nil
}
