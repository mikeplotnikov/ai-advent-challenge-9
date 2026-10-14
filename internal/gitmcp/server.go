// Package gitmcp exposes read-only git facts about one repository through MCP.
//
// Every tool runs a fixed `git` subcommand with its own argument list: the model
// supplies at most a path prefix, never a command line, so nothing it writes can
// change the repository. Lesson 7 (slide 37) puts status, diff, log and code search
// among the operations that run without asking; push, delete and config edits need
// a human, and none of them exists here.
package gitmcp

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	ServerName    = "day31-git"
	ServerVersion = "1.0.0"
	// MaxOutputRunes bounds one tool result so a large diff cannot flood the context.
	MaxOutputRunes = 12000
	// MaxFiles bounds list_files for the same reason.
	MaxFiles = 400
	timeout  = 10 * time.Second
)

type Options struct {
	// Repo is the working tree every git command runs in.
	Repo string
}

type BranchOutput struct {
	Branch   string `json:"branch"`
	Commit   string `json:"commit"`
	Subject  string `json:"subject"`
	Detached bool   `json:"detached"`
}

type StatusOutput struct {
	Branch  string   `json:"branch"`
	Changed []string `json:"changed"`
	Clean   bool     `json:"clean"`
}

type ListFilesInput struct {
	Prefix string `json:"prefix,omitempty" jsonschema:"Необязательный префикс пути внутри репозитория, например internal/ или day-21/. Пусто — весь репозиторий."`
}

type ListFilesOutput struct {
	Prefix    string   `json:"prefix"`
	Files     []string `json:"files"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated"`
}

type DiffInput struct {
	Path string `json:"path,omitempty" jsonschema:"Необязательный путь или префикс пути: показать изменения только в нём."`
}

type DiffOutput struct {
	Stat      string `json:"stat"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

func NewServer(options Options) *mcp.Server {
	repo := options.Repo
	if repo == "" {
		repo = "."
	}
	server := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: ServerVersion}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "git_branch",
		Description: "Текущая git-ветка проекта, хеш и заголовок последнего коммита.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, BranchOutput, error) {
		out, err := Branch(ctx, repo)
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "git_status",
		Description: "Незакоммиченные изменения в рабочей копии: список изменённых, новых и удалённых файлов.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, StatusOutput, error) {
		out, err := Status(ctx, repo)
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_files",
		Description: "Список файлов проекта на диске (под контролем git и новые, не игнорируемые), по желанию только под заданным префиксом. Для вопросов «где лежит», «какие файлы в папке».",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListFilesInput) (*mcp.CallToolResult, ListFilesOutput, error) {
		out, err := ListFiles(ctx, repo, in.Prefix)
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "git_diff",
		Description: "Незакоммиченный diff рабочей копии относительно HEAD (сводка и текст), по желанию только по одному пути.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DiffInput) (*mcp.CallToolResult, DiffOutput, error) {
		out, err := Diff(ctx, repo, in.Path)
		return nil, out, err
	})
	return server
}

func Branch(ctx context.Context, repo string) (BranchOutput, error) {
	branch, err := git(ctx, repo, "branch", "--show-current")
	if err != nil {
		return BranchOutput{}, err
	}
	head, err := git(ctx, repo, "log", "-1", "--format=%h%x00%s")
	if err != nil {
		return BranchOutput{}, err
	}
	commit, subject, _ := strings.Cut(strings.TrimSpace(head), "\x00")
	out := BranchOutput{Branch: strings.TrimSpace(branch), Commit: commit, Subject: subject}
	if out.Branch == "" {
		out.Detached = true
		out.Branch = "(detached HEAD)"
	}
	return out, nil
}

func Status(ctx context.Context, repo string) (StatusOutput, error) {
	branch, err := Branch(ctx, repo)
	if err != nil {
		return StatusOutput{}, err
	}
	raw, err := git(ctx, repo, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return StatusOutput{}, err
	}
	out := StatusOutput{Branch: branch.Branch, Changed: lines(raw)}
	out.Clean = len(out.Changed) == 0
	return out, nil
}

func ListFiles(ctx context.Context, repo, prefix string) (ListFilesOutput, error) {
	clean, err := cleanPrefix(prefix)
	if err != nil {
		return ListFilesOutput{}, err
	}
	// Untracked but not ignored files count too: a developer asks about what is on
	// disk, and a file not yet added is the one being worked on.
	tail := []string{}
	if clean != "" {
		tail = []string{"--", clean}
	}
	raw, err := git(ctx, repo, append([]string{"ls-files", "--cached", "--others", "--exclude-standard"}, tail...)...)
	if err != nil {
		return ListFilesOutput{}, err
	}
	// --cached still lists files deleted from disk but not yet committed; drop them.
	deletedRaw, err := git(ctx, repo, append([]string{"ls-files", "--deleted"}, tail...)...)
	if err != nil {
		return ListFilesOutput{}, err
	}
	deleted := map[string]bool{}
	for _, name := range lines(deletedRaw) {
		deleted[name] = true
	}
	files := []string{}
	for _, name := range lines(raw) {
		if !deleted[name] {
			files = append(files, name)
		}
	}
	out := ListFilesOutput{Prefix: clean, Total: len(files), Files: files}
	if len(files) > MaxFiles {
		out.Files, out.Truncated = files[:MaxFiles], true
	}
	return out, nil
}

func Diff(ctx context.Context, repo, filter string) (DiffOutput, error) {
	clean, err := cleanPrefix(filter)
	if err != nil {
		return DiffOutput{}, err
	}
	tail := []string{}
	if clean != "" {
		tail = []string{"--", clean}
	}
	stat, err := git(ctx, repo, append([]string{"diff", "HEAD", "--stat"}, tail...)...)
	if err != nil {
		return DiffOutput{}, err
	}
	body, err := git(ctx, repo, append([]string{"diff", "HEAD"}, tail...)...)
	if err != nil {
		return DiffOutput{}, err
	}
	out := DiffOutput{Stat: strings.TrimSpace(stat)}
	out.Diff, out.Truncated = truncate(body, MaxOutputRunes)
	return out, nil
}

// cleanPrefix keeps a model-supplied path inside the repository and away from git's
// own option and pathspec-magic syntax.
func cleanPrefix(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "." {
		return "", nil
	}
	if strings.HasPrefix(raw, "-") || strings.HasPrefix(raw, ":") || strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("путь %q должен быть относительным путём внутри репозитория", raw)
	}
	clean := path.Clean(raw)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("путь %q выходит за пределы репозитория", raw)
	}
	// Clean strips "./", so "./:(exclude)x" must be checked again after it.
	if clean == "." {
		return "", nil
	}
	if strings.HasPrefix(clean, "-") || strings.HasPrefix(clean, ":") {
		return "", fmt.Errorf("путь %q должен быть относительным путём внутри репозитория", raw)
	}
	return clean, nil
}

func git(ctx context.Context, repo string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo, "--no-pager"}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func lines(raw string) []string {
	out := []string{}
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, strings.TrimRight(line, "\r"))
		}
	}
	return out
}

func truncate(text string, limit int) (string, bool) {
	if utf8.RuneCountInString(text) <= limit {
		return text, false
	}
	runes := []rune(text)
	return string(runes[:limit]) + "\n… [обрезано]", true
}
