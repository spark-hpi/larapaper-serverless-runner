package main

import (
	"embed"
	"fmt"
	"regexp"
)

//go:embed harnesses/*
var harnessFS embed.FS

var phpTagRe = regexp.MustCompile(`(?s)^\s*<\?php\s*`)

var harnessFilePaths = map[string]string{
	"python": "harnesses/harness.py",
	"node":   "harnesses/harness.js",
	"php":    "harnesses/harness.php",
}

func harnessContent(language string) ([]byte, error) {
	path, ok := harnessFilePaths[language]
	if !ok {
		return nil, fmt.Errorf("unsupported language: %s", language)
	}
	return harnessFS.ReadFile(path)
}

func prepareCode(language, code string) string {
	if language == "php" {
		return "<?php\n" + phpTagRe.ReplaceAllString(code, "")
	}
	return code
}
