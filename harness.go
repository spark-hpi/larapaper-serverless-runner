package main

import (
	"fmt"
	"regexp"
	"strings"
)

var phpTagRe = regexp.MustCompile(`(?s)^\s*<\?php\s*`)

func buildHarness(language, code string) string {
	switch language {
	case "python":
		return pythonHarness(code)
	case "node":
		return nodeHarness(code)
	case "php":
		return phpHarness(code)
	default:
		return ""
	}
}

func pythonHarness(code string) string {
	lines := []string{
		"import sys, json, os",
		"sys.stdout = sys.stderr",
		"input = json.loads(sys.stdin.buffer.read())",
		"",
		code,
		"",
		"if callable(locals().get('run', None)):",
		"    output = run(input)",
		"elif 'result' in locals():",
		"    output = result",
		"else:",
		"    output = input",
		"os.write(1, json.dumps(output).encode())",
	}
	return strings.Join(lines, "\n")
}

func nodeHarness(code string) string {
	return `const _enc = new TextEncoder();
const _dec = new TextDecoder();
const _elog = (...a) => { const w = tjs.stderr.getWriter(); w.write(_enc.encode(a.map(String).join(' ') + '\n')); w.releaseLock(); };
console.log = _elog; console.warn = _elog; console.info = _elog; console.debug = _elog;
const _r = tjs.stdin.getReader();
const _cs = [];
for (;;) { const { done, value } = await _r.read(); if (done) break; _cs.push(value); }
const input = JSON.parse(_dec.decode(_cs.reduce((a, b) => { const r = new Uint8Array(a.length + b.length); r.set(a); r.set(b, a.length); return r; }, new Uint8Array(0))));

` + code + `

let _out;
if (typeof run === 'function') { _out = await run(input); }
else if (typeof result !== 'undefined') { _out = result; }
else { _out = input; }
const _w = tjs.stdout.getWriter();
await _w.write(_enc.encode(JSON.stringify(_out)));
_w.releaseLock();
`
}

func phpHarness(code string) string {
	cleanCode := phpTagRe.ReplaceAllString(code, "")
	return "<?php\n" +
		"$input = json_decode(file_get_contents('php://stdin'), true);\n" +
		"ob_start();\n\n" +
		cleanCode +
		"\n\nif (function_exists('run')) {\n    $output = run($input);\n" +
		"} elseif (isset($result)) {\n    $output = $result;\n" +
		"} else {\n    $output = $input;\n}\n" +
		fmt.Sprintf("ob_end_clean();\nfwrite(STDOUT, json_encode($output));\n")
}
