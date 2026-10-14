// Package docrag indexes a project's own documentation — README plus docs/ — for
// retrieval. It reuses the day-21 query-time pieces (Ollama embeddings, cosine ranking)
// and adds what a developer assistant needs on top: which files to read, a heading-aware
// chunker, and a staleness check so an answer never silently comes from old docs.
package docrag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

const (
	Strategy = "headings"
	// MaxChunkRunes splits a long section at paragraph boundaries.
	MaxChunkRunes = 2400
)

// docExtensions are the files under docs/ that count as documentation: prose plus
// data schemas and API descriptions, which the task names alongside README.
var docExtensions = map[string]bool{".md": true, ".json": true, ".yaml": true, ".yml": true}

type File struct {
	Header rag.IndexHeader    `json:"header"`
	Files  []rag.ManifestFile `json:"files"`
	Chunks []rag.Chunk        `json:"chunks"`
}

// Sources lists README.md and every documentation file under docs/, repo-relative and sorted.
func Sources(repo string) ([]string, error) {
	var out []string
	// Symlinks are skipped: a link under docs/ pointing at ../.env would otherwise send a
	// gitignored secret into the index and the model's prompt.
	if info, err := os.Lstat(filepath.Join(repo, "README.md")); err == nil && info.Mode().IsRegular() {
		out = append(out, "README.md")
	}
	docs := filepath.Join(repo, "docs")
	if info, err := os.Stat(docs); err == nil && info.IsDir() {
		err := filepath.WalkDir(docs, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.Type().IsRegular() || !docExtensions[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			rel, err := filepath.Rel(repo, path)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("в %s нет ни README.md, ни docs/: индексировать нечего", repo)
	}
	sort.Strings(out)
	return out, nil
}

// Manifest hashes the current sources; the index stores it to detect stale docs later.
func Manifest(repo string) ([]rag.ManifestFile, map[string]string, error) {
	sources, err := Sources(repo)
	if err != nil {
		return nil, nil, err
	}
	files := make([]rag.ManifestFile, 0, len(sources))
	texts := map[string]string{}
	for _, source := range sources {
		raw, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(source)))
		if err != nil {
			return nil, nil, err
		}
		sum := sha256.Sum256(raw)
		files = append(files, rag.ManifestFile{Path: source, Runes: utf8.RuneCount(raw), SHA256: hex.EncodeToString(sum[:])})
		texts[source] = string(raw)
	}
	return files, texts, nil
}

// Build chunks and embeds every source. progress, if set, is told about each file.
func Build(ctx context.Context, repo string, embedder rag.Embedder, model string, progress func(source string, chunks int)) (File, error) {
	started := time.Now()
	files, texts, err := Manifest(repo)
	if err != nil {
		return File{}, err
	}
	out := File{Files: files}
	for _, file := range files {
		chunks := Chunk(file.Path, texts[file.Path])
		for i := range chunks {
			vector, tokens, normalized, err := embedder.Embed(ctx, chunks[i].ChunkID, EmbeddingInput(chunks[i]))
			if err != nil {
				return File{}, err
			}
			chunks[i].Embedding, chunks[i].Tokens = vector, tokens
			out.Header.TotalTokens += tokens
			if normalized {
				out.Header.NormalizedVectors++
			}
			if out.Header.Dimension == 0 {
				out.Header.Dimension = len(vector)
			}
		}
		out.Chunks = append(out.Chunks, chunks...)
		if progress != nil {
			progress(file.Path, len(chunks))
		}
	}
	out.Header.Strategy = Strategy
	out.Header.Model = model
	out.Header.BuiltAt = time.Now().UTC().Format(time.RFC3339)
	out.Header.ChunkCount = len(out.Chunks)
	out.Header.Parameters = rag.StrategyParameters{MaxRunes: MaxChunkRunes}
	out.Header.EmbeddingDurationMS = time.Since(started).Milliseconds()
	return out, nil
}

// EmbeddingInput puts the location in front of the text, so a question about "the
// structure" can match a section by its heading even when the body never says the word.
func EmbeddingInput(chunk rag.Chunk) string {
	return "Источник: " + chunk.Source + "\nРаздел: " + chunk.Section + "\n\n" + chunk.Text
}

func Save(path string, file File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(file)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func Load(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, fmt.Errorf("индекса %s нет: сначала постройте его флагом -index", path)
		}
		return File{}, err
	}
	var file File
	if err := json.Unmarshal(raw, &file); err != nil {
		return File{}, fmt.Errorf("индекс %s повреждён: %w", path, err)
	}
	if len(file.Chunks) == 0 || file.Header.Dimension == 0 {
		return File{}, fmt.Errorf("индекс %s пуст: перестройте его флагом -index", path)
	}
	return file, nil
}

// Stale names every source that changed, appeared or disappeared since the index was built.
func Stale(repo string, file File) ([]string, error) {
	current, _, err := Manifest(repo)
	if err != nil {
		return nil, err
	}
	indexed := map[string]string{}
	for _, f := range file.Files {
		indexed[f.Path] = f.SHA256
	}
	var out []string
	for _, f := range current {
		sum, ok := indexed[f.Path]
		switch {
		case !ok:
			out = append(out, f.Path+" (новый)")
		case sum != f.SHA256:
			out = append(out, f.Path+" (изменён)")
		}
		delete(indexed, f.Path)
	}
	for path := range indexed {
		out = append(out, path+" (удалён)")
	}
	sort.Strings(out)
	return out, nil
}

// Search embeds the question and returns the k closest chunks.
func Search(ctx context.Context, embedder rag.Embedder, file File, question string, k int) ([]rag.SearchResult, error) {
	vector, _, _, err := embedder.Embed(ctx, "query", question)
	if err != nil {
		return nil, err
	}
	return rag.RankChunks(rag.Index{Header: file.Header, Chunks: file.Chunks}, vector, k)
}

// Chunk splits Markdown at headings (outside fenced code), keeping the heading path as
// the section name; a section longer than MaxChunkRunes is split at blank lines.
// Non-Markdown files (schemas) become one section named after the file.
func Chunk(source, text string) []rag.Chunk {
	type section struct {
		title string
		start int
		body  strings.Builder
	}
	var sections []*section
	if !strings.EqualFold(filepath.Ext(source), ".md") {
		s := &section{title: filepath.Base(source)}
		s.body.WriteString(text)
		sections = append(sections, s)
	} else {
		var path []string
		current := &section{title: filepath.Base(source)}
		sections = append(sections, current)
		fenced := false
		offset := 0
		for _, line := range strings.SplitAfter(text, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") {
				fenced = !fenced
			}
			if level, title := heading(trimmed); !fenced && level > 0 {
				if level > len(path) {
					for len(path) < level-1 {
						path = append(path, "")
					}
					path = append(path, title)
				} else {
					path = append(path[:level-1], title)
				}
				current = &section{title: joinPath(path), start: utf8.RuneCountInString(text[:offset])}
				sections = append(sections, current)
			}
			current.body.WriteString(line)
			offset += len(line)
		}
	}
	var out []rag.Chunk
	for _, s := range sections {
		body := strings.TrimSpace(s.body.String())
		if body == "" || isOnlyHeading(body) {
			continue
		}
		parts := splitParagraphs(body, MaxChunkRunes)
		for i, part := range parts {
			out = append(out, rag.Chunk{
				ChunkID:  fmt.Sprintf("%s#%d", source, len(out)+1),
				Strategy: Strategy,
				Source:   source,
				Title:    filepath.Base(source),
				Section:  s.title,
				Spans:    len(parts),
				Part:     i + 1,
				Start:    s.start,
				Runes:    utf8.RuneCountInString(part),
				Text:     part,
			})
		}
	}
	return out
}

func heading(line string) (int, string) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level == len(line) || line[level] != ' ' {
		return 0, ""
	}
	return level, strings.TrimSpace(line[level:])
}

func isOnlyHeading(body string) bool {
	level, _ := heading(body)
	return level > 0 && !strings.Contains(body, "\n")
}

func joinPath(path []string) string {
	var parts []string
	for _, p := range path {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " › ")
}

func splitParagraphs(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	// A long Markdown table is one paragraph; its rows are the next boundary down.
	var pieces []string
	for _, paragraph := range strings.Split(text, "\n\n") {
		if utf8.RuneCountInString(paragraph) <= limit {
			pieces = append(pieces, paragraph)
			continue
		}
		pieces = append(pieces, splitLines(paragraph, limit)...)
	}
	var out []string
	var current strings.Builder
	for _, paragraph := range pieces {
		if current.Len() > 0 && utf8.RuneCountInString(current.String())+utf8.RuneCountInString(paragraph)+2 > limit {
			out = append(out, strings.TrimSpace(current.String()))
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(paragraph)
	}
	if strings.TrimSpace(current.String()) != "" {
		out = append(out, strings.TrimSpace(current.String()))
	}
	return out
}

// splitLines cuts one oversized paragraph into line groups of at most limit runes
// (a single longer line stays whole). Separated by "\n" so a table keeps its rows.
func splitLines(paragraph string, limit int) []string {
	var out []string
	var current strings.Builder
	for _, line := range strings.Split(paragraph, "\n") {
		if current.Len() > 0 && utf8.RuneCountInString(current.String())+utf8.RuneCountInString(line)+1 > limit {
			out = append(out, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString("\n")
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}
