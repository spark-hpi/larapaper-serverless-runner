package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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

func buildBwrapArgs(tmpDir string) []string {
	return []string{
		"--unshare-user",
		"--uid", "65534",
		"--gid", "65534",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/bin", "/bin",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf",
		"--ro-bind", "/etc/hosts", "/etc/hosts",
		"--ro-bind", "/etc/ssl/certs", "/etc/ssl/certs",
		"--ro-bind-try", "/etc/php83", "/etc/php83",
		"--bind", tmpDir, tmpDir,
		"--new-session",
		"--",
	}
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

	if err := os.WriteFile(outputPath, []byte{}, 0622); err != nil {
		return nil, fmt.Errorf("create output file: %w", err)
	}
	if err := os.Chmod(outputPath, 0622); err != nil {
		return nil, fmt.Errorf("chmod output file: %w", err)
	}

	timeout := resolveTimeout(req.Timeout)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	shellCmd := fmt.Sprintf("ulimit -v %d && exec \"$@\"", memLimit/1024)
	args := append(buildBwrapArgs(tmpDir), "/bin/sh", "-c", shellCmd, "--", interp, scriptPath)
	cmd := exec.CommandContext(ctx, "bwrap", args...)
	cmd.Stdin = bytes.NewReader(req.Input)

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
