package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestCorpusMatchesManifest(t *testing.T) {
	if got := projectPath(defaultComparePath); got != "compare.json" {
		t.Fatalf("путь вывода из каталога пакета = %q", got)
	}
	documents, manifest, _, err := loadCorpus("corpus")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SourceCommit != "99298a3" {
		t.Fatalf("source_commit = %q", manifest.SourceCommit)
	}
	if len(manifest.Files) != 29 || len(documents) != 29 {
		t.Fatalf("manifest=%d documents=%d, ожидалось 29", len(manifest.Files), len(documents))
	}
	totalRunes := 0
	manifestPaths := make([]string, 0, len(manifest.Files))
	for _, entry := range manifest.Files {
		raw, err := os.ReadFile(filepath.Join("corpus", filepath.FromSlash(entry.Path)))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		if got := hex.EncodeToString(digest[:]); got != entry.SHA256 {
			t.Errorf("%s: sha256 %s, ожидался %s", entry.Path, got, entry.SHA256)
		}
		if got := len([]rune(string(raw))); got != entry.Runes {
			t.Errorf("%s: %d рун, ожидалось %d", entry.Path, got, entry.Runes)
		}
		totalRunes += entry.Runes
		manifestPaths = append(manifestPaths, entry.Path)
	}
	if totalRunes != 257426 {
		t.Fatalf("в корпусе %d рун, ожидалось 257426", totalRunes)
	}
	var diskPaths []string
	err = filepath.Walk("corpus", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".md" {
			relative, err := filepath.Rel("corpus", path)
			if err != nil {
				return err
			}
			diskPaths = append(diskPaths, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(manifestPaths)
	sort.Strings(diskPaths)
	if !reflect.DeepEqual(diskPaths, manifestPaths) {
		t.Fatalf("файлы на диске не совпадают с манифестом\nmanifest=%v\ndisk=%v", manifestPaths, diskPaths)
	}
}

func TestCorpusIsExactGitSnapshot(t *testing.T) {
	manifest, _, err := readManifest("corpus")
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("git", "-C", "..", "ls-tree", "-r", "--name-only", manifest.SourceCommit).Output()
	if err != nil {
		t.Fatal(err)
	}
	var gitPaths []string
	for _, line := range strings.Fields(string(output)) {
		if filepath.Ext(line) == ".md" {
			gitPaths = append(gitPaths, line)
		}
	}
	manifestPaths := make([]string, len(manifest.Files))
	for i, entry := range manifest.Files {
		manifestPaths[i] = entry.Path
		expected, err := exec.Command("git", "-C", "..", "show", manifest.SourceCommit+":"+entry.Path).Output()
		if err != nil {
			t.Fatalf("git show %s: %v", entry.Path, err)
		}
		actual, err := os.ReadFile(filepath.Join("corpus", filepath.FromSlash(entry.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual, expected) {
			t.Errorf("%s отличается от git show %s", entry.Path, manifest.SourceCommit)
		}
	}
	sort.Strings(gitPaths)
	sort.Strings(manifestPaths)
	if !reflect.DeepEqual(gitPaths, manifestPaths) {
		t.Fatalf("список Markdown в Git не совпадает с манифестом\ngit=%v\nmanifest=%v", gitPaths, manifestPaths)
	}
}

func TestRealCorpusChunksAreDeterministicAndTraceable(t *testing.T) {
	documents, _, _, err := loadCorpus("corpus")
	if err != nil {
		t.Fatal(err)
	}
	docBySource := map[string]Document{}
	for _, document := range documents {
		docBySource[document.Source] = document
	}
	for name, build := range map[string]func([]Document) []Chunk{"fixed": fixedChunks, "structure": structureChunks} {
		t.Run(name, func(t *testing.T) {
			first := build(documents)
			second := build(documents)
			if !reflect.DeepEqual(first, second) {
				t.Fatal("два прогона чанкера дали разные результаты")
			}
			ids := map[string]bool{}
			for _, chunk := range first {
				if chunk.ChunkID == "" || chunk.Source == "" || chunk.Title == "" || chunk.Strategy != name {
					t.Fatalf("неполные метаданные: %+v", chunk)
				}
				if ids[chunk.ChunkID] {
					t.Fatalf("повтор chunk_id %s", chunk.ChunkID)
				}
				ids[chunk.ChunkID] = true
				document := docBySource[chunk.Source]
				if chunk.Start < 0 || chunk.End > len(document.Runes) || chunk.Start >= chunk.End {
					t.Fatalf("неверный диапазон %+v", chunk)
				}
				if chunk.Text != string(document.Runes[chunk.Start:chunk.End]) || chunk.Runes != chunk.End-chunk.Start {
					t.Fatalf("%s: text не соответствует диапазону", chunk.ChunkID)
				}
				if chunk.Spans < 1 {
					t.Fatalf("%s: spans=%d", chunk.ChunkID, chunk.Spans)
				}
			}
		})
	}
}

func TestRealCorpusStructuralFacts(t *testing.T) {
	documents, _, _, err := loadCorpus("corpus")
	if err != nil {
		t.Fatal(err)
	}
	headings := 0
	oversized := 0
	foundDay15Preamble := false
	for _, document := range documents {
		for _, section := range parseSections(document.Runes) {
			if section.Level > 0 {
				headings++
			}
			if section.End-section.Start > structureMaxSize {
				oversized++
			}
		}
		if document.Source == "day-15/RESULTS.md" {
			chunks := structureChunks([]Document{document})
			if len(chunks) > 0 && chunks[0].Section == "" && chunks[0].Runes == 89 {
				foundDay15Preamble = true
			}
		}
	}
	if headings != 208 {
		t.Fatalf("ATX-заголовков вне fences: %d, ожидалось 208", headings)
	}
	if oversized != 4 {
		t.Fatalf("разделов длиннее 3600 рун: %d, ожидалось 4", oversized)
	}
	if !foundDay15Preamble {
		t.Fatal("89-рунный preamble day-15/RESULTS.md не стал отдельным structure-чанком")
	}
}
