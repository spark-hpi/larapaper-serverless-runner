package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestMain applies env config (e.g. TRANSFORM_NODE_RUNTIME) so tests match the image they run in.
func TestMain(m *testing.M) {
	initConfig()
	os.Exit(m.Run())
}

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

// nodeBinary is the executable behind the configured JS runtime.
func nodeBinary() string {
	rt, _ := runtimeFor("node")
	return rt.interp[0]
}

// requireNodeJS skips tests for features only the Node.js runtime provides.
func requireNodeJS(t *testing.T) {
	t.Helper()
	if nodeRuntime != nodeRuntimeNode {
		t.Skip("requires the Node.js runtime")
	}
	requireInterpreter(t, "node")
}

// requireBwrap skips the test if bwrap is absent or the sandbox can't exec inside it.
func requireBwrap(t *testing.T) {
	t.Helper()
	requireInterpreter(t, "bwrap")
	// Probe with the runner's own sandbox flags so tests run wherever the service would.
	cmd := exec.Command("bwrap", append(buildBwrapArgs(t.TempDir(), ""), "/bin/sh", "-c", "true")...)
	if err := cmd.Run(); err != nil {
		t.Skipf("bwrap sandbox not functional: %v", err)
	}
}

func TestHandleHealth(t *testing.T) {
	mux := routes()
	for _, path := range []string{"/health"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, w.Code)
		}
		var body map[string]string
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("%s: decode body: %v", path, err)
		}
		if body["status"] != "UP" {
			t.Fatalf("%s: expected status UP, got %q", path, body["status"])
		}
	}
}

func TestHandleManifest(t *testing.T) {
	get := func(path string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		routes().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var body map[string]any
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		return body
	}

	body := get("/manifest")
	if root := get("/"); fmt.Sprint(root) != fmt.Sprint(body) {
		t.Fatalf("/ should alias /manifest, got %v", root)
	}
	want := map[string]string{
		"name":             manifestDisplayName(),
		"author":           "Benjamin Nussbaum, Spark HPI",
		"license":          "EUPL-1.2",
		"repository":       "https://github.com/bnussbau/byos-transform-runner",
		"contributor_info": "Based on the work of https://github.com/spark-hpi/larapaper-serverless-runner",
	}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("%s: expected %q, got %v", k, v, body[k])
		}
	}
	if v, _ := body["version"].(string); v == "" {
		t.Fatalf("expected a non-empty version, got %v", body["version"])
	}
	raw, ok := body["language_capabilities"].([]any)
	if !ok {
		t.Fatalf("expected language_capabilities list, got %v", body["language_capabilities"])
	}
	languages := make([]string, len(raw))
	for i, v := range raw {
		languages[i], _ = v.(string)
	}
	for _, lang := range languages {
		rt, ok := runtimeFor(lang)
		if !supportedLanguages[lang] || !ok {
			t.Fatalf("unexpected language %q", lang)
		}
		if _, err := exec.LookPath(rt.interp[0]); err != nil {
			t.Fatalf("%s reported without its interpreter %s", lang, rt.interp[0])
		}
	}
	for lang := range supportedLanguages {
		rt, _ := runtimeFor(lang)
		if _, err := exec.LookPath(rt.interp[0]); err == nil && !slices.Contains(languages, lang) {
			t.Fatalf("%s interpreter is installed but not reported", lang)
		}
	}

	prev := nodeRuntime
	nodeRuntime = nodeRuntimeTxiki
	defer func() { nodeRuntime = prev }()
	if name := get("/manifest")["name"]; name != "BYOS Transform Runner (slim)" {
		t.Fatalf("slim: expected name with (slim), got %v", name)
	}
}

func TestRunPHP(t *testing.T) {
	requireBwrap(t)
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
	requireBwrap(t)
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
	requireBwrap(t)
	requireInterpreter(t, nodeBinary())
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
	requireBwrap(t)
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
	requireBwrap(t)
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
	requireBwrap(t)
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
	requireBwrap(t)
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
	requireBwrap(t)
	requireInterpreter(t, "python3")
	output := mustRun(t, RunRequest{
		Language: "python",
		Code:     "def run(input):\n    import os\n    return {'uid': os.getuid()}",
		Input:    json.RawMessage(`{}`),
		Timeout:  5,
	})
	uid := uint32(output["uid"].(float64))
	if uid != 65534 {
		t.Fatalf("expected uid 65534 (nobody) inside sandbox, got %d", uid)
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

func TestRunRuby(t *testing.T) {
	requireBwrap(t)
	requireInterpreter(t, "ruby")
	output := mustRun(t, RunRequest{
		Language: "ruby",
		Code:     "def helper(input)\n  input['value'] * 2\nend\n\ndef run(input)\n  doubled = helper(input)\n  puts 'debug'\n  { doubled:, input: }\nend\n",
		Input:    json.RawMessage(`{"value": 5}`),
		Timeout:  10,
	})
	if output["doubled"] != float64(10) {
		t.Fatalf("expected doubled=10, got %v", output)
	}
}

// TRMNL legacy exports name the entry point `transform` instead of `run`.
func TestRunTransformFallback(t *testing.T) {
	requireBwrap(t)
	cases := []struct{ lang, interp, code string }{
		{"python", "python3", "def transform(input):\n    return {'ok': True}"},
		{"node", nodeBinary(), "function transform(input) { return { ok: true }; }"},
		{"php", "php", "function transform(array $input): array { return ['ok' => true]; }"},
		{"ruby", "ruby", "def transform(input)\n  { ok: true }\nend"},
	}
	for _, c := range cases {
		t.Run(c.lang, func(t *testing.T) {
			requireInterpreter(t, c.interp)
			output := mustRun(t, RunRequest{Language: c.lang, Code: c.code, Input: json.RawMessage(`{}`), Timeout: 10})
			if output["ok"] != true {
				t.Fatalf("expected ok=true, got %v", output)
			}
		})
	}
}

func TestRunNodeAsyncAndExported(t *testing.T) {
	requireBwrap(t)
	requireInterpreter(t, nodeBinary())
	for _, code := range []string{
		`async function run(input) { return { v: await Promise.resolve(input.value) }; }`,
		`export function run(input) { return { v: input.value }; }`,
	} {
		output := mustRun(t, RunRequest{Language: "node", Code: code, Input: json.RawMessage(`{"value": 7}`), Timeout: 10})
		if output["v"] != float64(7) {
			t.Fatalf("expected v=7, got %v for %q", output, code)
		}
	}
}

func TestRunPHPEchoGoesToLogs(t *testing.T) {
	requireBwrap(t)
	requireInterpreter(t, "php")
	w := doRequest(t, RunRequest{
		Language: "php",
		Code:     `function run($input) { echo "hello log"; return []; }`,
		Input:    json.RawMessage(`{}`),
		Timeout:  10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Output map[string]any `json:"output"`
		Logs   string         `json:"logs"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Output == nil || len(resp.Output) != 0 {
		t.Fatalf("expected empty object output, got %v", resp.Output)
	}
	if resp.Logs != "hello log" {
		t.Fatalf("expected logs to contain echo output, got %q", resp.Logs)
	}
}

func TestPrepareCodeNodeShim(t *testing.T) {
	got := prepareCode("node", "function run(i) { return i; }")
	if !bytes.Contains([]byte(got), []byte("export const __trmnl_entry")) {
		t.Fatalf("expected node entry shim, got %q", got)
	}
}

func TestTruncateLogs(t *testing.T) {
	big := bytes.Repeat([]byte("a"), maxLogBytes+10)
	if got := truncateLogs(big); len(got) != maxLogBytes {
		t.Fatalf("expected %d bytes, got %d", maxLogBytes, len(got))
	}
}

func TestNormalizeLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"Node": "node", "javascript": "node", " JS ": "node",
		"py": "python", "Ruby": "ruby", "rb": "ruby", "php": "php", "cobol": "cobol",
	} {
		if got := normalizeLanguage(in); got != want {
			t.Errorf("normalizeLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNodeRuntimeConfig(t *testing.T) {
	orig := nodeRuntime
	defer func() { nodeRuntime = orig }()
	cases := map[string]string{"": orig, "node": "node", " TXIKI ": "txiki", "deno": "node"}
	for in, want := range cases {
		nodeRuntime = orig
		t.Setenv("TRANSFORM_NODE_RUNTIME", in)
		initConfig()
		if nodeRuntime != want {
			t.Errorf("TRANSFORM_NODE_RUNTIME=%q: got %q, want %q", in, nodeRuntime, want)
		}
	}
}

func TestRunNodeRequireAndIntl(t *testing.T) {
	requireBwrap(t)
	requireNodeJS(t)
	code := `
const crypto = require('node:crypto');
function run(input) {
    console.log('hello', { a: 1 });
    const date = new Intl.DateTimeFormat('de-DE', { month: 'long', timeZone: 'UTC' }).format(new Date(Date.UTC(2026, 2, 1)));
    return { hash: crypto.createHash('sha256').update('x').digest('hex').slice(0, 8), date, buf: Buffer.from('hi').toString('base64') };
}`
	w := doRequest(t, RunRequest{Language: "node", Code: code, Input: json.RawMessage(`{}`), Timeout: 10})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Output map[string]any `json:"output"`
		Logs   string         `json:"logs"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Output["hash"] != "2d711642" || resp.Output["date"] != "März" || resp.Output["buf"] != "aGk=" {
		t.Fatalf("unexpected output %v", resp.Output)
	}
	if resp.Logs != "hello { a: 1 }\n" {
		t.Fatalf("unexpected logs %q", resp.Logs)
	}
}

func TestRunNodeMemoryLimit(t *testing.T) {
	requireBwrap(t)
	requireNodeJS(t)
	w := doRequest(t, RunRequest{
		Language: "node",
		Code:     `function run() { const a = []; for (;;) a.push(new Array(1e6).fill(1)); }`,
		Input:    json.RawMessage(`{}`),
		Timeout:  20,
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
}

// CommonJS-style snippets: `require.main === module` guards and module.exports entry points.
func TestRunNodeCommonJSGlobals(t *testing.T) {
	requireBwrap(t)
	requireNodeJS(t)
	for _, code := range []string{
		"async function transform(input) { return { v: input.value }; }\nif (require.main === module) { throw new Error('self-test block must not run'); }",
		"module.exports = { run: (input) => ({ v: input.value }) };",
		"exports.transform = async (input) => ({ v: input.value });",
		"module.exports = function (input) { return { v: input.value, d: typeof __dirname, f: typeof __filename }; };",
	} {
		output := mustRun(t, RunRequest{Language: "node", Code: code, Input: json.RawMessage(`{"value": 3}`), Timeout: 10})
		if output["v"] != float64(3) {
			t.Fatalf("expected v=3, got %v for %q", output, code)
		}
	}
}

func TestSanitizeJSON(t *testing.T) {
	tests := []struct{ in, want string }{
		{`{"a":"plain"}`, `{"a":"plain"}`},
		{`{"a":"Zürich 🎉"}`, `{"a":"Zürich 🎉"}`},
		{`{"a":"\ud83c\udf89"}`, `{"a":"\ud83c\udf89"}`},
		{`{"a":"x\ud83c"}`, `{"a":"x\ufffd"}`},
		{`{"a":"\udf89y"}`, `{"a":"\ufffdy"}`},
		{`{"a":"\ud83c\u0041"}`, `{"a":"\ufffd\u0041"}`},
		{`{"a":"\\ud83c"}`, `{"a":"\\ud83c"}`},
		{"{\"a\":\"bad\xffbyte\"}", `{"a":"bad\ufffdbyte"}`},
		{`{"\ud800":1, "n": 12345678901234567890}`, `{"\ufffd":1, "n": 12345678901234567890}`},
	}
	for _, tt := range tests {
		got := string(sanitizeJSON([]byte(tt.in)))
		if got != tt.want {
			t.Errorf("sanitizeJSON(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPrepareCodeStripsBOM(t *testing.T) {
	got := prepareCode("php", "\uFEFF<?php\nfunction run($i) { return []; }")
	if got != "<?php\nfunction run($i) { return []; }" {
		t.Fatalf("unexpected %q", got)
	}
}

func TestLimitAndTailWriters(t *testing.T) {
	lw := &limitWriter{max: 5}
	lw.Write([]byte("abc"))
	lw.Write([]byte("defg"))
	if lw.buf.String() != "abcde" || !lw.overflow {
		t.Fatalf("limitWriter: %q overflow=%v", lw.buf.String(), lw.overflow)
	}
	tw := &tailWriter{max: 5}
	tw.Write([]byte("abc"))
	tw.Write([]byte("defg"))
	if string(tw.Bytes()) != "cdefg" {
		t.Fatalf("tailWriter: %q", tw.Bytes())
	}
	tw.Write([]byte("0123456789"))
	if string(tw.Bytes()) != "56789" {
		t.Fatalf("tailWriter: %q", tw.Bytes())
	}
}

func TestRunRequiresAPIKey(t *testing.T) {
	prev := apiKey
	apiKey = "s3cret"
	defer func() { apiKey = prev }()
	for _, tc := range []struct {
		auth string
		want int
	}{
		{"", http.StatusUnauthorized},
		{"Bearer wrong", http.StatusUnauthorized},
		{"s3cret", http.StatusUnauthorized},
		// Right key passes auth; the unsupported language proves we reached the handler.
		{"Bearer s3cret", http.StatusBadRequest},
	} {
		req := httptest.NewRequest(http.MethodPost, "/run", bytes.NewReader([]byte(`{"language":"cobol"}`)))
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		w := httptest.NewRecorder()
		routes().ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("auth %q: expected %d, got %d", tc.auth, tc.want, w.Code)
		}
	}
	// Health and manifest stay public.
	for _, path := range []string{"/health", "/manifest"} {
		w := httptest.NewRecorder()
		routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, w.Code)
		}
	}
}

func TestRunRejectsGET(t *testing.T) {
	w := httptest.NewRecorder()
	routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/run", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestRunBodyTooLarge(t *testing.T) {
	prev := maxRequestBytes
	maxRequestBytes = 100
	defer func() { maxRequestBytes = prev }()
	body := `{"language":"python","code":"` + strings.Repeat("x", 200) + `"}`
	w := httptest.NewRecorder()
	handleRun(w, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body)))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", w.Code)
	}
}

func TestRunBusy(t *testing.T) {
	prev := runSlots
	runSlots = make(chan struct{}, 1)
	runSlots <- struct{}{}
	defer func() { runSlots = prev }()
	w := doRequest(t, RunRequest{Language: "python", Code: "def run(i): return {}"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

// unicodeSample covers Latin-1, symbols, CJK and a 4-byte emoji.
const unicodeSample = "Zürich ✓ 日本 🎉"

func TestRunUnicodeRoundTrip(t *testing.T) {
	requireBwrap(t)
	codes := map[string]string{
		"python": "def run(input):\n    print(input['s'])\n    return {'s': input['s'], 'n': len(input['s'])}",
		"node":   "function run(input) { console.log(input.s); return { s: input.s, n: [...input.s].length }; }",
		"php":    "function run($input) { echo $input['s'], \"\\n\"; return ['s' => $input['s'], 'n' => mb_strlen($input['s'])]; }",
		"ruby":   "def run(input)\n  puts input['s']\n  { s: input['s'], n: input['s'].length }\nend",
	}
	interps := map[string]string{"python": "python3", "node": nodeBinary(), "php": "php", "ruby": "ruby"}
	for lang, code := range codes {
		t.Run(lang, func(t *testing.T) {
			requireInterpreter(t, interps[lang])
			in, _ := json.Marshal(map[string]string{"s": unicodeSample})
			w := doRequest(t, RunRequest{Language: lang, Code: code, Input: in, Timeout: 10})
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
			var resp struct {
				Output map[string]any `json:"output"`
				Logs   string         `json:"logs"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Output["s"] != unicodeSample {
				t.Fatalf("output s = %q", resp.Output["s"])
			}
			if resp.Output["n"] != float64(13) {
				t.Fatalf("expected 13 characters, got %v", resp.Output["n"])
			}
			if resp.Logs != unicodeSample+"\n" {
				t.Fatalf("logs = %q", resp.Logs)
			}
		})
	}
}

func TestRunNodeLoneSurrogate(t *testing.T) {
	requireBwrap(t)
	requireInterpreter(t, nodeBinary())
	w := doRequest(t, RunRequest{
		Language: "node",
		Code:     `function run() { return { s: "ab🎉".slice(0, 3) }; }`,
		Timeout:  10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(strings.ToLower(w.Body.String()), `\ud83c`) {
		t.Fatalf("lone surrogate leaked: %s", w.Body.String())
	}
	output := mustRun(t, RunRequest{Language: "node", Code: `function run() { return { s: "ab🎉".slice(0, 3) }; }`, Timeout: 10})
	if output["s"] != "ab\uFFFD" {
		t.Fatalf("expected replacement char, got %q", output["s"])
	}
}

func TestRunPHPWithBOM(t *testing.T) {
	requireBwrap(t)
	requireInterpreter(t, "php")
	output := mustRun(t, RunRequest{
		Language: "php",
		Code:     "\uFEFF<?php\nfunction run($input) { return ['ok' => true]; }",
		Timeout:  10,
	})
	if output["ok"] != true {
		t.Fatalf("unexpected %v", output)
	}
}

func TestRunMissingInputIsEmptyObject(t *testing.T) {
	requireBwrap(t)
	requireInterpreter(t, "python3")
	output := mustRun(t, RunRequest{
		Language: "python",
		Code:     "def run(input):\n    return {'type': type(input).__name__, 'len': len(input)}",
		Timeout:  5,
	})
	if output["type"] != "dict" || output["len"] != float64(0) {
		t.Fatalf("unexpected %v", output)
	}
}

func TestRunOutputTooLarge(t *testing.T) {
	requireBwrap(t)
	requireInterpreter(t, "python3")
	w := doRequest(t, RunRequest{
		Language: "python",
		Code:     fmt.Sprintf("def run(input):\n    return {'s': 'x' * %d}", maxOutputBytes+1),
		Timeout:  10,
	})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "exceeds") {
		t.Fatalf("expected 422 output too large, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRunPythonNaN(t *testing.T) {
	requireBwrap(t)
	requireInterpreter(t, "python3")
	w := doRequest(t, RunRequest{Language: "python", Code: "def run(input):\n    return {'x': float('nan')}", Timeout: 5})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "JSON compliant") {
		t.Fatalf("expected clear NaN error, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRunNodeStdoutWriteGoesToLogs(t *testing.T) {
	requireBwrap(t)
	requireNodeJS(t)
	w := doRequest(t, RunRequest{
		Language: "node",
		Code:     "function run() { process.stdout.write('noise\\n'); setInterval(() => {}, 1000); return { ok: 1 }; }",
		Timeout:  10,
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"logs":"noise\n"`) {
		t.Fatalf("expected 200 with noise in logs, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRunNodeNonObjectReturn(t *testing.T) {
	requireBwrap(t)
	requireInterpreter(t, nodeBinary())
	w := doRequest(t, RunRequest{Language: "node", Code: "function run() {}", Timeout: 10})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "must return an object") {
		t.Fatalf("expected clear error, got %d: %s", w.Code, w.Body.String())
	}
}

// The sandbox must not see the runner's environment or other processes.
func TestSandboxIsolation(t *testing.T) {
	requireBwrap(t)
	requireInterpreter(t, "python3")
	t.Setenv("TRANSFORM_API_KEY", "leak-me")
	output := mustRun(t, RunRequest{
		Language: "python",
		Code: "import os\ndef run(input):\n" +
			"    pids = [p for p in os.listdir('/proc') if p.isdigit()] if os.path.isdir('/proc') else []\n" +
			"    return {'env': dict(os.environ), 'pids': len(pids)}",
		Timeout: 5,
	})
	env, _ := output["env"].(map[string]any)
	if _, ok := env["TRANSFORM_API_KEY"]; ok {
		t.Fatalf("runner env leaked into sandbox: %v", env)
	}
	if env["LANG"] != "C.UTF-8" {
		t.Fatalf("expected LANG=C.UTF-8, got %v", env["LANG"])
	}
	// sh exec's into python; bwrap's own init may also be listed.
	if n := output["pids"].(float64); n > 3 {
		t.Fatalf("sandbox sees %v processes; expected only its own", n)
	}
}
