package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIModesAreMutuallyExclusiveAndRequired(t *testing.T) {
	for _, args := range [][]string{{}, {"-index", "-compare"}, {"-index", "лишнее"}, {"-search", ""}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestCLIWithoutOllamaSuggestsServe(t *testing.T) {
	originalClient := httpClientNoFixedTimeout
	httpClientNoFixedTimeout = http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	defer func() { httpClientNoFixedTimeout = originalClient }()
	var stdout, stderr bytes.Buffer
	code := run([]string{"-index", "-ollama", "http://127.0.0.1:1", "-timeout", "2s"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "ollama serve") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestCompareAndSearchRejectStaleStrategyParameters(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("corpus", 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("# X\ntext")
	digest := sha256.Sum256(body)
	manifest := Manifest{SourceCommit: "test", Files: []ManifestFile{{Path: "x.md", Runes: len([]rune(string(body))), SHA256: hex.EncodeToString(digest[:])}}}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("corpus", "x.md"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("corpus", manifestName), manifestRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestRaw)
	index := sampleIndex()
	index.Header.ManifestSHA256 = hex.EncodeToString(manifestDigest[:])
	index.Header.Parameters.Size++
	if err := writeIndexAtomic(filepath.Join("index", "fixed.json"), index); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-search", "вопрос", "-strategy", "fixed"}, {"-compare"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "-index") {
			t.Errorf("%v: code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestCompareAndSearchAskForIndexWhenItIsMissing(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("corpus", 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("# X\ntext")
	digest := sha256.Sum256(body)
	manifestRaw, err := json.Marshal(Manifest{SourceCommit: "test", Files: []ManifestFile{{Path: "x.md", Runes: len([]rune(string(body))), SHA256: hex.EncodeToString(digest[:])}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("corpus", "x.md"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("corpus", manifestName), manifestRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-search", "вопрос", "-strategy", "fixed"}, {"-compare"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "нет: сначала выполните `go run ./day-21 -index`") {
			t.Errorf("%v: code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}
