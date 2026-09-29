package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultCorpusDir = "day-21/corpus"
	manifestName     = "MANIFEST.json"
)

type Manifest struct {
	SourceCommit string         `json:"source_commit"`
	Files        []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	Runes  int    `json:"runes"`
	SHA256 string `json:"sha256"`
}

type Document struct {
	Source string
	Title  string
	Text   string
	Runes  []rune
}

func projectPath(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path
	}
	trimmed := strings.TrimPrefix(path, "day-21/")
	if trimmed != path {
		if _, err := os.Stat(trimmed); err == nil {
			return trimmed
		}
		if _, err := os.Stat(filepath.Join("corpus", manifestName)); err == nil {
			return trimmed
		}
	}
	return path
}

func readManifest(corpusDir string) (Manifest, []byte, error) {
	path := filepath.Join(corpusDir, manifestName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("прочитать манифест корпуса: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, nil, fmt.Errorf("разобрать манифест корпуса: %w", err)
	}
	return manifest, raw, nil
}

func loadCorpus(corpusDir string) ([]Document, Manifest, string, error) {
	manifest, rawManifest, err := readManifest(corpusDir)
	if err != nil {
		return nil, Manifest{}, "", err
	}
	documents := make([]Document, 0, len(manifest.Files))
	seen := make(map[string]bool, len(manifest.Files))
	for _, entry := range manifest.Files {
		if entry.Path == "" || filepath.IsAbs(entry.Path) || strings.Contains(filepath.ToSlash(entry.Path), "../") {
			return nil, Manifest{}, "", fmt.Errorf("небезопасный путь в манифесте: %q", entry.Path)
		}
		if seen[entry.Path] {
			return nil, Manifest{}, "", fmt.Errorf("повтор пути в манифесте: %s", entry.Path)
		}
		seen[entry.Path] = true
		body, err := os.ReadFile(filepath.Join(corpusDir, filepath.FromSlash(entry.Path)))
		if err != nil {
			return nil, Manifest{}, "", fmt.Errorf("прочитать %s: %w", entry.Path, err)
		}
		digest := sha256.Sum256(body)
		if got := hex.EncodeToString(digest[:]); got != entry.SHA256 {
			return nil, Manifest{}, "", fmt.Errorf("sha256 %s не совпадает с манифестом", entry.Path)
		}
		runes := []rune(string(body))
		if len(runes) != entry.Runes {
			return nil, Manifest{}, "", fmt.Errorf("число рун %s: %d, в манифесте %d", entry.Path, len(runes), entry.Runes)
		}
		documents = append(documents, Document{Source: entry.Path, Title: documentTitle(runes, entry.Path), Text: string(body), Runes: runes})
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].Source < documents[j].Source })
	digest := sha256.Sum256(rawManifest)
	return documents, manifest, hex.EncodeToString(digest[:]), nil
}

func documentTitle(text []rune, source string) string {
	for _, section := range parseSections(text) {
		if section.Level == 1 && section.Heading != "" {
			return section.Heading
		}
	}
	return filepath.Base(source)
}
