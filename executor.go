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

var interpreters = map[string][]string{
	"python": {"python3"},
	"node":   {"tjs", "run"},
	"php":    {"php"},
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
		"--ro-bind", "/proc", "/proc",
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

	codePath := filepath.Join(tmpDir, "transform."+extensions[req.Language])
	if err := os.WriteFile(codePath, []byte(prepareCode(req.Language, req.Code)), 0644); err != nil {
		return nil, fmt.Errorf("write code: %w", err)
	}

	harness, err := harnessContent(req.Language)
	if err != nil {
		return nil, fmt.Errorf("get harness: %w", err)
	}
	scriptPath := filepath.Join(tmpDir, "harness."+extensions[req.Language])
	if err := os.WriteFile(scriptPath, harness, 0644); err != nil {
		return nil, fmt.Errorf("write harness: %w", err)
	}

	timeout := resolveTimeout(req.Timeout)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	shellCmd := fmt.Sprintf("ulimit -v %d && exec \"$@\"", memLimit/1024)
	args := append(buildBwrapArgs(tmpDir), "/bin/sh", "-c", shellCmd, "--")
	args = append(args, interp...)
	args = append(args, scriptPath)
	cmd := exec.CommandContext(ctx, "bwrap", args...)
	cmd.Stdin = bytes.NewReader(req.Input)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("transform timed out after %d seconds", timeout)
		}
		return nil, fmt.Errorf("transform exited non-zero: %s", strings.TrimSpace(stderr.String()))
	}

	raw := stdout.Bytes()
	if len(raw) == 0 {
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
