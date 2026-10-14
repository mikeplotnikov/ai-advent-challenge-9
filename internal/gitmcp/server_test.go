package gitmcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolsOverMCPAgainstARealRepository(t *testing.T) {
	repo := newRepo(t)
	session, closeFn := open(t, repo)
	defer closeFn()

	listed, err := session.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	if got := strings.Join(names, ","); got != "git_branch,git_diff,git_status,list_files" {
		t.Fatalf("tools: %s", got)
	}

	branch := call(t, session, "git_branch", map[string]any{})
	if text := mcpclient.ToolText(branch); !strings.Contains(text, `"branch":"feature/help"`) || !strings.Contains(text, `"subject":"initial docs"`) {
		t.Fatalf("git_branch: %s", text)
	}

	status := call(t, session, "git_status", map[string]any{})
	if text := mcpclient.ToolText(status); !strings.Contains(text, `"clean":true`) {
		t.Fatalf("clean repo reported dirty: %s", text)
	}
	write(t, repo, "README.md", "# Demo\n\nchanged line\n")
	write(t, repo, "docs/new.md", "# New\n")
	write(t, repo, "day-32/main.go", "package main\n")
	write(t, repo, ".gitignore", "*.log\n")
	write(t, repo, "debug.log", "ignored\n")
	status = call(t, session, "git_status", map[string]any{})
	text := mcpclient.ToolText(status)
	if !strings.Contains(text, " M README.md") || !strings.Contains(text, "?? docs/new.md") || !strings.Contains(text, "?? day-32/main.go") || !strings.Contains(text, `"clean":false`) {
		t.Fatalf("git_status after edits: %s", text)
	}

	diff := call(t, session, "git_diff", map[string]any{"path": "README.md"})
	if text := mcpclient.ToolText(diff); !strings.Contains(text, "+changed line") || !strings.Contains(text, "README.md | ") {
		t.Fatalf("git_diff: %s", text)
	}

	files := call(t, session, "list_files", map[string]any{"prefix": "docs/"})
	if text := mcpclient.ToolText(files); !strings.Contains(text, "docs/guide.md") || !strings.Contains(text, "docs/new.md") || strings.Contains(text, "README.md") || !strings.Contains(text, `"total":2`) {
		t.Fatalf("list_files docs/: %s", text)
	}
	all := call(t, session, "list_files", map[string]any{})
	if text := mcpclient.ToolText(all); !strings.Contains(text, `"total":5`) || strings.Contains(text, "debug.log") {
		t.Fatalf("list_files all: %s", text)
	}
}

func TestModelSuppliedPathsCannotEscapeOrInjectOptions(t *testing.T) {
	repo := newRepo(t)
	session, closeFn := open(t, repo)
	defer closeFn()
	for _, bad := range []string{"../", "../../etc", "--output=/tmp/x", ":(top)", "/etc/passwd", "./:(exclude)README.md", "./--stat", "/"} {
		for _, tool := range []struct{ name, arg string }{{"list_files", "prefix"}, {"git_diff", "path"}} {
			result := call(t, session, tool.name, map[string]any{tool.arg: bad})
			if !result.IsError {
				t.Errorf("%s %q accepted: %s", tool.name, bad, mcpclient.ToolText(result))
			}
		}
	}
	for _, good := range []string{"", ".", "docs", "docs/", "./docs", "docs/.."} {
		if _, err := cleanPrefix(good); err != nil {
			t.Errorf("cleanPrefix(%q) rejected: %v", good, err)
		}
	}
}

func TestDiffFiltersByPathAndIncludesStagedChanges(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "README.md", "# Demo\n\nreadme edit\n")
	write(t, repo, "docs/guide.md", "# Guide\n\nguide edit\n")
	run(t, repo, "add", "docs/guide.md")
	filtered, err := Diff(context.Background(), repo, "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filtered.Diff, "+readme edit") || strings.Contains(filtered.Diff, "guide") ||
		!strings.Contains(filtered.Stat, "README.md") || strings.Contains(filtered.Stat, "guide") {
		t.Fatalf("path filter ignored: %s", filtered.Diff)
	}
	all, err := Diff(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all.Diff, "+guide edit") || !strings.Contains(all.Stat, "2 files changed") {
		t.Fatalf("staged change missing from diff against HEAD: %s", all.Stat)
	}
}

func TestListFilesCapsOutputAndHidesDeletedFiles(t *testing.T) {
	repo := newRepo(t)
	for i := 0; i < MaxFiles+5; i++ {
		write(t, repo, fmt.Sprintf("many/f%03d.txt", i), "x\n")
	}
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "many files")
	if err := os.Remove(filepath.Join(repo, "README.md")); err != nil {
		t.Fatal(err)
	}
	out, err := ListFiles(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != MaxFiles+6 || len(out.Files) != MaxFiles || !out.Truncated {
		t.Fatalf("total %d, listed %d, truncated %v", out.Total, len(out.Files), out.Truncated)
	}
	for _, name := range out.Files {
		if name == "README.md" {
			t.Fatal("file deleted from disk is still listed")
		}
	}
}

func TestDetachedHeadIsReported(t *testing.T) {
	repo := newRepo(t)
	run(t, repo, "checkout", "--detach", "HEAD")
	out, err := Branch(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Detached || out.Branch != "(detached HEAD)" || out.Commit == "" {
		t.Fatalf("detached: %+v", out)
	}
}

func TestLongDiffIsTruncated(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "README.md", strings.Repeat("строка изменения\n", 2000))
	out, err := Diff(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || !strings.HasSuffix(out.Diff, "[обрезано]") {
		t.Fatalf("diff of %d runes not truncated", len([]rune(out.Diff)))
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run(t, repo, "init", "-q", "-b", "feature/help")
	run(t, repo, "config", "user.email", "test@example.com")
	run(t, repo, "config", "user.name", "test")
	write(t, repo, "README.md", "# Demo\n")
	write(t, repo, "docs/guide.md", "# Guide\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "initial docs")
	return repo
}

func run(t *testing.T, repo string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func write(t *testing.T, repo, name, text string) {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, repo string) (*mcpclient.Session, func()) {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := NewServer(Options{Repo: repo}).Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcpclient.NewNamed("git-test", "1").Open(context.Background(), mcpclient.Transport{MCP: clientTransport})
	if err != nil {
		t.Fatal(err)
	}
	return session, func() { _ = session.Close(); _ = serverSession.Close() }
}

func call(t *testing.T, session *mcpclient.Session, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), name, args)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
