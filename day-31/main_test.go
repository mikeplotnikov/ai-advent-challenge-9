package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/docrag"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/gitmcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHelpUsesBranchFromMCPAndDocsFromRAG(t *testing.T) {
	a, model := newAssistant(t)
	var out bytes.Buffer
	repl(context.Background(), a, strings.NewReader("/help где описаны MCP-инструменты?\n/exit\n"), &out)

	if len(model.systems) != 1 {
		t.Fatalf("model calls: %d", len(model.systems))
	}
	system := model.systems[0]
	for _, want := range []string{`"branch":"feature/help"`, "docs/mcp.md § MCP › Инструменты", "git_branch возвращает ветку"} {
		if !strings.Contains(system, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	text := out.String()
	for _, want := range []string{"▸ MCP git_branch → feature/help @", "docs/mcp.md § MCP › Инструменты", "Ответ модели [docs/mcp.md § MCP › Инструменты]"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
}

func TestDirectCommandsAndUnknownInput(t *testing.T) {
	a, model := newAssistant(t)
	var out bytes.Buffer
	in := "/branch\n/status\n/files docs\n/diff\n/help\nпривет\n/exit\n/branch\n"
	repl(context.Background(), a, strings.NewReader(in), &out)
	text := out.String()
	for _, want := range []string{
		"▸ MCP git_branch\nfeature/help @",
		"Ветка feature/help, изменений нет",
		"  docs/mcp.md\nВсего файлов: 1",
		"Незакоммиченных изменений в отслеживаемых файлах нет",
		"Задайте вопрос: /help",
		"Неизвестная команда.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "▸ MCP git_branch\n") != 1 {
		t.Errorf("command after /exit was executed:\n%s", text)
	}
	if len(model.systems) != 0 {
		t.Errorf("direct commands called the model %d times", len(model.systems))
	}
}

// TestHostileRepositoryContentNeverReachesTheTerminalRaw drives every output sink with
// escape sequences planted in a file name, a heading, a commit subject and the model's
// answer, on a dirty tree so /status and /diff print their full output.
func TestHostileRepositoryContentNeverReachesTheTerminalRaw(t *testing.T) {
	a, model := newAssistant(t)
	model.answer = "ответ\x1b[2J\x07 модели"
	repo := a.cfg.repo
	evil := "docs/evil\x1b[2J.md"
	if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(evil)), []byte("# Bad\x1b]0;title\x07 heading\n\ntext\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Demo\n\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "subject\x1b]0;pwn\x07 end"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Demo\n\nchanged again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	index, err := docrag.Build(context.Background(), repo, fakeEmbedder{}, "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	a.index, a.cfg.k = index, 2
	warning, err := staleWarning(repo, index)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	out.WriteString(warning)
	if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(evil)), []byte("# changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if warning, err = staleWarning(repo, index); err != nil || warning == "" {
		t.Fatalf("stale warning %q (%v)", warning, err)
	}
	out.WriteString(warning)
	repl(context.Background(), a, strings.NewReader("/branch\n/status\n/files docs\n/diff\n/help вопрос\n"), &out)
	text := out.String()
	if strings.ContainsAny(text, "\x1b\x07") {
		t.Fatalf("raw control bytes reached the terminal:\n%q", text)
	}
	for _, want := range []string{"изменено файлов: 2", " M README.md", "2 files changed", "фрагментов документации 2", "ответ"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
}

func TestBranchLineStripsTerminalEscapes(t *testing.T) {
	got := describeBranch(map[string]any{"branch": "main", "commit": "abc1234", "subject": "hi\x1b]0;pwn\x07 there"}, "")
	if strings.ContainsAny(got, "\x1b\x07") || !strings.Contains(got, "main @ abc1234") {
		t.Fatalf("branch line %q", got)
	}
}

func TestParse(t *testing.T) {
	cases := map[string][2]string{
		"  /HELP  как дела ":  {"/help", "как дела"},
		"/files internal/rag": {"/files", "internal/rag"},
		"/exit":               {"/exit", ""},
		"":                    {"", ""},
	}
	for line, want := range cases {
		if c, a := parse(line); c != want[0] || a != want[1] {
			t.Errorf("parse(%q) = %q, %q", line, c, a)
		}
	}
}

// fakeModel answers at once, or — when callTool is set — first asks for list_files
// and then answers with what the tool returned, like the real tool-use loop.
type fakeModel struct {
	systems  []string
	callTool bool
	toolText string
	answer   string
}

func (f *fakeModel) Converse(_ context.Context, messages []llm.ToolMessage, opts llm.Options) (llm.Answer, error) {
	last := messages[len(messages)-1]
	if f.callTool && last.Role == "user" {
		f.systems = append(f.systems, messages[0].Content)
		if len(opts.Tools) == 0 {
			return llm.Answer{Content: "инструменты не переданы", Model: llm.DefaultModel}, nil
		}
		return llm.Answer{Model: llm.DefaultModel, ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function",
			Function: llm.ToolCallFunction{Name: "list_files", Arguments: `{"prefix":"docs"}`}}}}, nil
	}
	if last.Role == "tool" {
		f.toolText = last.Content
		return llm.Answer{Content: "В docs: " + last.Content, Model: llm.DefaultModel}, nil
	}
	f.systems = append(f.systems, messages[0].Content)
	if f.answer != "" {
		return llm.Answer{Content: f.answer, Model: llm.DefaultModel}, nil
	}
	return llm.Answer{Content: "Ответ модели [docs/mcp.md § MCP › Инструменты]", Model: llm.DefaultModel}, nil
}

func TestHelpLetsTheModelCallMCPTools(t *testing.T) {
	a, model := newAssistant(t)
	model.callTool = true
	var out bytes.Buffer
	if err := a.help(context.Background(), &out, "какие файлы в docs?"); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, `▸ Модель вызвала MCP list_files {"prefix":"docs"} → ok`) || !strings.Contains(model.toolText, "docs/mcp.md") {
		t.Fatalf("tool call not executed over MCP:\n%s\ntool result: %s", text, model.toolText)
	}
	if !strings.Contains(text, "вызовов MCP моделью: 1") {
		t.Fatalf("totals:\n%s", text)
	}
}

func TestStaleWarningNamesChangedDocs(t *testing.T) {
	a, _ := newAssistant(t)
	if warning, err := staleWarning(a.cfg.repo, a.index); err != nil || warning != "" {
		t.Fatalf("fresh index warned %q (%v)", warning, err)
	}
	if err := os.WriteFile(filepath.Join(a.cfg.repo, "docs", "mcp.md"), []byte("# MCP\n\nновый текст\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	warning, err := staleWarning(a.cfg.repo, a.index)
	if err != nil || !strings.Contains(warning, "docs/mcp.md (изменён)") || !strings.Contains(warning, "-index") {
		t.Fatalf("warning %q (%v)", warning, err)
	}
}

func TestFilesCommandShowsRemainderBeyondScreenLimit(t *testing.T) {
	a, _ := newAssistant(t)
	for i := 0; i < 45; i++ {
		if err := os.WriteFile(filepath.Join(a.cfg.repo, "docs", fmt.Sprintf("n%02d.md", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	repl(context.Background(), a, strings.NewReader("/files docs\n"), &out)
	if text := out.String(); !strings.Contains(text, "  … ещё 6\nВсего файлов: 46") {
		t.Fatalf("output:\n%s", text)
	}
}

func TestDemoScriptTypesCommandsAndCaptions(t *testing.T) {
	a, _ := newAssistant(t)
	script := filepath.Join(t.TempDir(), "demo.txt")
	if err := os.WriteFile(script, []byte("# Прямая команда MCP\n/branch\n\n/exit\n/status\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PACE", "0.001")
	var out bytes.Buffer
	input, err := newDemoInput(script, &out)
	if err != nil {
		t.Fatal(err)
	}
	repl(context.Background(), a, input, &out)
	text := out.String()
	if !strings.Contains(text, "# Прямая команда MCP\x1b[0m\nday31> /branch\n▸ MCP git_branch\nfeature/help @") {
		t.Fatalf("demo output:\n%s", text)
	}
	if strings.Contains(text, "▸ MCP git_status") {
		t.Fatalf("demo ran past /exit:\n%s", text)
	}
}

// fakeEmbedder puts every text on one axis, so ranking is by input order only — the
// test checks wiring, not retrieval quality, which internal/docrag tests on its own.
type fakeEmbedder struct{}

func (fakeEmbedder) Embed(context.Context, string, string) ([]float64, int, bool, error) {
	return []float64{1}, 1, false, nil
}

func newAssistant(t *testing.T) (*assistant, *fakeModel) {
	t.Helper()
	repo := t.TempDir()
	for name, text := range map[string]string{"README.md": "# Demo\n\ntext\n", "docs/mcp.md": "# MCP\n\n## Инструменты\n\ngit_branch возвращает ветку\n"} {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "feature/help"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}, {"add", "."}, {"commit", "-q", "-m", "docs"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	index, err := docrag.Build(context.Background(), repo, fakeEmbedder{}, "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Keep only the MCP section so the fake ranking has one obvious winner.
	index.Chunks = index.Chunks[len(index.Chunks)-1:]

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := gitmcp.NewServer(gitmcp.Options{Repo: repo}).Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcpclient.NewNamed("day31-test", "1").Open(context.Background(), mcpclient.Transport{MCP: clientTransport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(); _ = serverSession.Close() })
	model := &fakeModel{}
	return &assistant{cfg: config{repo: repo, k: 3}, embedder: fakeEmbedder{}, index: index, model: model, session: session}, model
}
