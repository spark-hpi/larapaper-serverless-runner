package main

import (
	"embed"
	"fmt"
	"regexp"
	"strings"
)

//go:embed harnesses/*
var harnessFS embed.FS

var phpTagRe = regexp.MustCompile(`(?s)^\s*<\?php\s*`)

func harnessContent(language string) ([]byte, error) {
	rt, ok := runtimeFor(language)
	if !ok {
		return nil, fmt.Errorf("unsupported language: %s", language)
	}
	return harnessFS.ReadFile(rt.harness)
}

// nodeEntryShim exposes the user's entry point to the harness whether or not
// it was exported. TRMNL snippets declare a bare `function run(input)`;
// legacy exports use `function transform(input)`.
const nodeEntryShim = "\n;export const __trmnl_entry = typeof run === 'function' ? run : (typeof transform === 'function' ? transform : undefined);\n"

func prepareCode(language, code string) string {
	// A UTF-8 BOM (Windows editors, zip imports) breaks PHP's tag stripping.
	code = strings.TrimPrefix(code, "\uFEFF")
	switch language {
	case "php":
		return "<?php\n" + phpTagRe.ReplaceAllString(code, "")
	case "node":
		return code + nodeEntryShim
	}
	return code
}
