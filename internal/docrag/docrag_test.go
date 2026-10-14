package docrag

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkKeepsHeadingPathAndIgnoresHeadingsInCode(t *testing.T) {
	text := "# Title\n\nintro\n\n## Setup\n\nrun it\n\n```bash\n# not a heading\ngo run .\n```\n\n### Keys\n\nuse .env\n\n## Tests\n\ngo test\n"
	chunks := Chunk("README.md", text)
	var sections []string
	for _, c := range chunks {
		sections = append(sections, c.Section)
	}
	want := []string{"Title", "Title › Setup", "Title › Setup › Keys", "Title › Tests"}
	if !reflect.DeepEqual(sections, want) {
		t.Fatalf("sections %q, want %q", sections, want)
	}
	if !strings.Contains(chunks[1].Text, "# not a heading") {
		t.Fatalf("fenced code lost from its section: %q", chunks[1].Text)
	}
	if chunks[0].ChunkID != "README.md#1" || chunks[3].ChunkID != "README.md#4" {
		t.Fatalf("ids: %s %s", chunks[0].ChunkID, chunks[3].ChunkID)
	}
}

func TestChunkSkippedHeadingLevelAndEmbeddingInput(t *testing.T) {
	chunks := Chunk("docs/a.md", "# A\n\nx\n\n### C\n\ny\n\n## B\n\nz\n")
	var sections []string
	for _, c := range chunks {
		sections = append(sections, c.Section)
	}
	if want := []string{"A", "A › C", "A › B"}; !reflect.DeepEqual(sections, want) {
		t.Fatalf("sections %q, want %q", sections, want)
	}
	input := EmbeddingInput(chunks[1])
	if !strings.HasPrefix(input, "Источник: docs/a.md\nРаздел: A › C\n\n") || !strings.HasSuffix(input, "y") {
		t.Fatalf("embedding input %q", input)
	}
}

func TestChunkSplitsALongTableByRows(t *testing.T) {
	var b strings.Builder
	b.WriteString("## Структура\n\n| Папка | Что |\n|---|---|\n")
	for i := 0; i < 200; i++ {
		b.WriteString("| `day-xx/` | строка таблицы с описанием дня |\n")
	}
	chunks := Chunk("README.md", b.String())
	if len(chunks) < 3 {
		t.Fatalf("long table stayed in %d chunk(s)", len(chunks))
	}
	total := 0
	for _, c := range chunks {
		if c.Runes > MaxChunkRunes {
			t.Errorf("%s has %d runes > %d", c.ChunkID, c.Runes, MaxChunkRunes)
		}
		if c.Section != "Структура" || c.Spans != len(chunks) {
			t.Errorf("%s section %q spans %d", c.ChunkID, c.Section, c.Spans)
		}
		total += strings.Count(c.Text, "строка таблицы")
	}
	if total != 200 {
		t.Fatalf("rows after split: %d, want 200", total)
	}
}

func TestSourcesTakeReadmeAndDocsOnly(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "README.md", "# R\n\ntext\n")
	write(t, repo, "docs/a.md", "# A\n\ntext\n")
	write(t, repo, "docs/api/schema.json", `{"type":"object"}`)
	write(t, repo, "docs/img.png", "png")
	write(t, repo, "other.md", "# not docs\n")
	got, err := Sources(repo)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"README.md", "docs/a.md", "docs/api/schema.json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sources %q, want %q", got, want)
	}
	outside := filepath.Join(t.TempDir(), "secret.env")
	write(t, filepath.Dir(outside), "secret.env", "KEY=secret\n")
	if err := os.Symlink(outside, filepath.Join(repo, "docs", "leak.md")); err != nil {
		t.Fatal(err)
	}
	if got, _ := Sources(repo); len(got) != 3 {
		t.Fatalf("symlink under docs/ was indexed: %q", got)
	}
	linked := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(linked, "README.md")); err != nil {
		t.Fatal(err)
	}
	if got, err := Sources(linked); err == nil {
		t.Fatalf("symlinked README.md was indexed: %q", got)
	}
	if _, err := Sources(t.TempDir()); err == nil {
		t.Fatal("empty repository indexed without error")
	}
}

func TestBuildSearchAndStaleness(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "README.md", "# Проект\n\n## Запуск\n\ngo run ./day-31\n")
	write(t, repo, "docs/mcp.md", "# MCP\n\n## Инструменты\n\ngit_branch возвращает ветку\n")
	file, err := Build(context.Background(), repo, keywordEmbedder{}, "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	if file.Header.ChunkCount != 2 || file.Header.Dimension != 3 || len(file.Files) != 2 {
		t.Fatalf("header %+v files %d", file.Header, len(file.Files))
	}
	results, err := Search(context.Background(), keywordEmbedder{}, file, "какие инструменты git_branch", 1)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Chunk.Source != "docs/mcp.md" || results[0].Chunk.Section != "MCP › Инструменты" {
		t.Fatalf("top result %s § %s", results[0].Chunk.Source, results[0].Chunk.Section)
	}
	if both, _ := Search(context.Background(), keywordEmbedder{}, file, "запуск", 5); len(both) != 2 || both[0].Chunk.Source != "README.md" {
		t.Fatalf("k=5 over 2 chunks: %d results", len(both))
	}

	path := filepath.Join(t.TempDir(), "index", "docs.json")
	if err := Save(path, file); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if stale, _ := Stale(repo, loaded); len(stale) != 0 {
		t.Fatalf("fresh index reported stale: %q", stale)
	}
	write(t, repo, "README.md", "# Проект\n\nдругой текст\n")
	write(t, repo, "docs/new.md", "# New\n")
	if err := os.Remove(filepath.Join(repo, "docs/mcp.md")); err != nil {
		t.Fatal(err)
	}
	stale, err := Stale(repo, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"README.md (изменён)", "docs/mcp.md (удалён)", "docs/new.md (новый)"}; !reflect.DeepEqual(stale, want) {
		t.Fatalf("stale %q, want %q", stale, want)
	}
}

func TestLoadRejectsMissingAndEmptyIndex(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "none.json")); err == nil || !strings.Contains(err.Error(), "-index") {
		t.Fatalf("missing index: %v", err)
	}
	empty := filepath.Join(dir, "empty.json")
	write(t, dir, "empty.json", `{"header":{},"chunks":[]}`)
	if _, err := Load(empty); err == nil {
		t.Fatal("empty index accepted")
	}
}

// keywordEmbedder maps text onto three topic axes, enough to make ranking checkable.
type keywordEmbedder struct{}

func (keywordEmbedder) Embed(_ context.Context, _ string, input string) ([]float64, int, bool, error) {
	lower := strings.ToLower(input)
	v := []float64{0.01, 0.01, 0.01}
	if strings.Contains(lower, "запуск") || strings.Contains(lower, "go run") {
		v[0] = 1
	}
	if strings.Contains(lower, "git_branch") || strings.Contains(lower, "инструмент") {
		v[1] = 1
	}
	norm := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
	for i := range v {
		v[i] /= norm
	}
	return v, utf8.RuneCountInString(input) / 4, false, nil
}

func write(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
