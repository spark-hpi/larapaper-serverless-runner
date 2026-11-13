package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// runtime describes how to execute one language.
type runtime struct {
	interp  []string // command prefix; the harness path is appended
	harness string   // embedded harness path
	ext     string   // extension for harness and transform files
	// noVMLimit skips `ulimit -v`. V8 reserves far more virtual address space
	// than it uses (CodeRange, wasm guard regions), so Node hangs at startup
	// under a 128 MB address-space cap. Its heap is capped via
	// --max-old-space-size instead.
	noVMLimit bool
}

// Supported values of TRANSFORM_NODE_RUNTIME.
const (
	nodeRuntimeNode  = "node"
	nodeRuntimeTxiki = "txiki"
)

var runtimes = map[string]runtime{
	"python": {interp: []string{"python3"}, harness: "harnesses/harness.py", ext: "py"},
	"php":    {interp: []string{"php"}, harness: "harnesses/harness.php", ext: "php"},
	"ruby":   {interp: []string{"ruby"}, harness: "harnesses/harness.rb", ext: "rb"},
}

func runtimeFor(language string) (runtime, bool) {
	if language != "node" {
		rt, ok := runtimes[language]
		return rt, ok
	}
	if nodeRuntime == nodeRuntimeTxiki {
		return runtime{interp: []string{"tjs", "run"}, harness: "harnesses/harness.txiki.js", ext: "js"}, true
	}
	heapMB := memLimit >> 20
	if heapMB < 16 {
		heapMB = 16
	}
	return runtime{
		interp:    []string{"node", fmt.Sprintf("--max-old-space-size=%d", heapMB)},
		harness:   "harnesses/harness.node.mjs",
		ext:       "mjs",
		noVMLimit: true,
	}, true
}

const (
	// maxLogBytes caps the stderr returned to callers as debug logs.
	maxLogBytes = 64 << 10
	// maxOutputBytes caps the JSON a transform may write to stdout.
	maxOutputBytes = 10 << 20
	// tmpfsBytes caps the sandbox's in-memory /tmp.
	tmpfsBytes = 64 << 20
	// maxFileKB caps the size of any single file a transform writes (ulimit -f).
	maxFileKB = 64 << 10
)

var errOutputTooLarge = fmt.Errorf("transform output exceeds %d bytes", maxOutputBytes)

func truncateLogs(b []byte) string {
	if len(b) <= maxLogBytes {
		return string(b)
	}
	return string(b[len(b)-maxLogBytes:])
}

// limitWriter buffers up to max bytes and records whether more was written.
// Write never fails, so the child is not killed by EPIPE mid-run.
type limitWriter struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room < len(p) {
		w.overflow = true
		if room > 0 {
			w.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return w.buf.Write(p)
}

// tailWriter keeps only the last max bytes written.
type tailWriter struct {
	buf []byte
	max int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if n >= w.max {
		w.buf = append(w.buf[:0], p[n-w.max:]...)
		return n, nil
	}
	if over := len(w.buf) + n - w.max; over > 0 {
		w.buf = append(w.buf[:0], w.buf[over:]...)
	}
	w.buf = append(w.buf, p...)
	return n, nil
}

func (w *tailWriter) Bytes() []byte { return w.buf }

func resolveTimeout(req int) int {
	if req <= 0 || req > timeoutCap {
		return timeoutCap
	}
	return req
}

// freshProcAvailable reports whether bwrap can mount a new procfs for the
// sandbox's PID namespace. Docker blocks this unless the container runs with
// `--security-opt systempaths=unconfined`. Without it, the sandbox gets no
// /proc at all: binding the container's /proc instead would expose the
// runner's and other transforms' environ, fds and files, because every
// sandbox shares the runner's outer UID.
var freshProcAvailable = sync.OnceValue(func() bool {
	err := exec.Command("bwrap",
		"--unshare-user", "--unshare-pid",
		"--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev",
		"--", "/bin/true").Run()
	if _, lookErr := exec.LookPath("bwrap"); lookErr == nil {
		if err == nil {
			log.Printf("sandbox: mounting a private /proc")
		} else {
			log.Printf("sandbox: cannot mount a private /proc (%v); transforms run without /proc. "+
				"Run the container with --security-opt systempaths=unconfined to enable it", err)
		}
	}
	return err == nil
})

// buildBwrapArgs returns the sandbox flags. exe is the interpreter's absolute
// path; without a private procfs it backs a stub /proc/self/exe, which some
// runtimes (txiki.js) resolve at startup.
func buildBwrapArgs(tmpDir, exe string) []string {
	var procArgs []string
	switch {
	case freshProcAvailable():
		procArgs = []string{"--proc", "/proc"}
	case exe != "":
		procArgs = []string{"--tmpfs", "/proc", "--dir", "/proc/self", "--symlink", exe, "/proc/self/exe"}
	}
	return append(procArgs,
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
		"--size", strconv.Itoa(tmpfsBytes), "--tmpfs", "/tmp",
		"--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf",
		"--ro-bind", "/etc/hosts", "/etc/hosts",
		"--ro-bind", "/etc/ssl", "/etc/ssl",
		"--ro-bind-try", "/etc/php84", "/etc/php84",
		"--bind", tmpDir, tmpDir,
		// Do not leak the runner's environment (e.g. TRANSFORM_API_KEY).
		"--clearenv",
		"--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin",
		"--setenv", "HOME", "/tmp",
		"--setenv", "LANG", "C.UTF-8",
		"--setenv", "LC_ALL", "C.UTF-8",
		"--setenv", "PYTHONDONTWRITEBYTECODE", "1",
		"--setenv", "PYTHONIOENCODING", "utf-8",
		"--new-session",
		"--die-with-parent",
		"--",
	)
}

func execute(req RunRequest) (json.RawMessage, string, error) {
	output, logs, err := executeRaw(req)
	return output, truncateLogs(logs), err
}

func executeRaw(req RunRequest) (json.RawMessage, []byte, error) {
	rt, ok := runtimeFor(req.Language)
	if !ok {
		return nil, nil, fmt.Errorf("unsupported language: %s", req.Language)
	}

	tmpDir, err := os.MkdirTemp("", "trmnl-")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.Chmod(tmpDir, 0711); err != nil {
		return nil, nil, fmt.Errorf("chmod tmpdir: %w", err)
	}

	codePath := filepath.Join(tmpDir, "transform."+rt.ext)
	if err := os.WriteFile(codePath, []byte(prepareCode(req.Language, req.Code)), 0644); err != nil {
		return nil, nil, fmt.Errorf("write code: %w", err)
	}

	harness, err := harnessContent(req.Language)
	if err != nil {
		return nil, nil, fmt.Errorf("get harness: %w", err)
	}
	scriptPath := filepath.Join(tmpDir, "harness."+rt.ext)
	if err := os.WriteFile(scriptPath, harness, 0644); err != nil {
		return nil, nil, fmt.Errorf("write harness: %w", err)
	}

	timeout := resolveTimeout(req.Timeout)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	shellCmd := fmt.Sprintf("ulimit -f %d && ulimit -v %d && exec \"$@\"", maxFileKB, memLimit/1024)
	if rt.noVMLimit {
		shellCmd = fmt.Sprintf("ulimit -f %d && exec \"$@\"", maxFileKB)
	}
	exe, err := exec.LookPath(rt.interp[0])
	if err != nil {
		return nil, nil, fmt.Errorf("%s runtime not installed", req.Language)
	}
	args := append(buildBwrapArgs(tmpDir, exe), "/bin/sh", "-c", shellCmd, "--")
	args = append(args, rt.interp...)
	args = append(args, scriptPath)
	cmd := exec.CommandContext(ctx, "bwrap", args...)
	cmd.Stdin = bytes.NewReader(req.Input)

	stdout := &limitWriter{max: maxOutputBytes}
	stderr := &tailWriter{max: maxLogBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Don't let a lingering grandchild holding the pipes block Wait.
	cmd.WaitDelay = 2 * time.Second

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, stderr.Bytes(), fmt.Errorf("transform timed out after %d seconds", timeout)
		}
		return nil, stderr.Bytes(), fmt.Errorf("transform exited non-zero: %s", lastLines(string(stderr.Bytes()), 20))
	}

	logs := stderr.Bytes()
	if stdout.overflow {
		return nil, logs, errOutputTooLarge
	}
	raw := sanitizeJSON(stdout.buf.Bytes())
	if len(raw) == 0 {
		return nil, logs, fmt.Errorf("transform produced no output")
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, logs, fmt.Errorf("transform output is not valid JSON: %w", err)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, logs, fmt.Errorf("transform output must be a JSON object, got %T", v)
	}

	return json.RawMessage(raw), logs, nil
}

// lastLines keeps the error message short; the full stderr is returned as logs.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// sanitizeJSON makes a transform's JSON output safe for strict decoders such
// as PHP's json_decode: inside string literals, invalid UTF-8 bytes and
// unpaired UTF-16 surrogate escapes (e.g. "\ud83d" from a JS slice through an
// emoji) are replaced with U+FFFD. Everything else, including key order and
// number formatting, is kept byte for byte.
func sanitizeJSON(b []byte) []byte {
	if utf8.Valid(b) && !bytes.Contains(b, []byte(`\u`)) {
		return b
	}
	const repl = `\ufffd`
	out := make([]byte, 0, len(b))
	inString := false
	for i := 0; i < len(b); {
		c := b[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			out = append(out, c)
			i++
			continue
		}
		switch {
		case c == '"':
			inString = false
			out = append(out, c)
			i++
		case c == '\\' && i+1 < len(b) && b[i+1] == 'u':
			r, ok := hex4(b, i+2)
			if !ok {
				// Malformed escape; leave it for the JSON validator to reject.
				out = append(out, b[i:i+2]...)
				i += 2
				continue
			}
			switch {
			case r >= 0xD800 && r <= 0xDBFF:
				if i+11 < len(b) && b[i+6] == '\\' && b[i+7] == 'u' {
					if lo, ok := hex4(b, i+8); ok && lo >= 0xDC00 && lo <= 0xDFFF {
						out = append(out, b[i:i+12]...)
						i += 12
						continue
					}
				}
				out = append(out, repl...)
				i += 6
			case r >= 0xDC00 && r <= 0xDFFF:
				out = append(out, repl...)
				i += 6
			default:
				out = append(out, b[i:i+6]...)
				i += 6
			}
		case c == '\\' && i+1 < len(b):
			out = append(out, c, b[i+1])
			i += 2
		case c < utf8.RuneSelf:
			out = append(out, c)
			i++
		default:
			r, size := utf8.DecodeRune(b[i:])
			if r == utf8.RuneError && size == 1 {
				out = append(out, repl...)
			} else {
				out = append(out, b[i:i+size]...)
			}
			i += size
		}
	}
	return out
}

// hex4 parses the four hex digits at b[i:i+4].
func hex4(b []byte, i int) (rune, bool) {
	if i+4 > len(b) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(b[i:i+4]), 16, 32)
	if err != nil {
		return 0, false
	}
	return rune(n), true
}
