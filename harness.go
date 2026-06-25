package main

import (
	"fmt"
	"regexp"
	"strings"
)

var phpTagRe = regexp.MustCompile(`(?s)^\s*<\?php\s*`)

func buildHarness(language, code, outputPath string) string {
	switch language {
	case "python":
		return pythonHarness(code, outputPath)
	case "node":
		return nodeHarness(code, outputPath)
	case "php":
		return phpHarness(code, outputPath)
	default:
		return ""
	}
}

func pythonHarness(code, outputPath string) string {
	lines := []string{
		"import sys, json",
		"input = json.loads(sys.stdin.read())",
		"",
		code,
		"",
		"if callable(locals().get('run', None)):",
		"    output = run(input)",
		"elif 'result' in locals():",
		"    output = result",
		"else:",
		"    output = input",
		fmt.Sprintf("json.dump(output, open(%q, 'w'))", outputPath),
	}
	return strings.Join(lines, "\n")
}

func nodeHarness(code, outputPath string) string {
	return "const input = JSON.parse(require('fs').readFileSync(0, 'utf8'));\n\n" +
		code +
		"\n\nlet output;\n" +
		"if (typeof run === 'function') {\n  output = run(input);\n" +
		"} else if (typeof result !== 'undefined') {\n  output = result;\n" +
		"} else {\n  output = input;\n}\n" +
		fmt.Sprintf("require('fs').writeFileSync(%q, JSON.stringify(output));\n", outputPath)
}

func phpHarness(code, outputPath string) string {
	cleanCode := phpTagRe.ReplaceAllString(code, "")
	return "<?php\n" +
		"$input = json_decode(file_get_contents('php://stdin'), true);\n\n" +
		cleanCode +
		"\n\nif (function_exists('run')) {\n    $output = run($input);\n" +
		"} elseif (isset($result)) {\n    $output = $result;\n" +
		"} else {\n    $output = $input;\n}\n" +
		fmt.Sprintf("file_put_contents(%q, json_encode($output));\n", outputPath)
}
