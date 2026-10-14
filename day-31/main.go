// Command day-31 is a developer assistant that understands this repository: /help
// answers questions about the project from its README and docs/ (RAG over local
// bge-m3 embeddings) and from live git facts served by an MCP server, written by
// DeepSeek over REST.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/docrag"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/gitmcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	embedModel = "bge-m3"
	keyEnv     = "DEEPSEEK_API_KEY_DAY31"
	prompt     = "day31> "
	usage      = "Команды: /help <вопрос о проекте> · /branch · /status · /files [папка] · /diff [путь] · /exit"
)

type config struct {
	repo, indexPath, ollama, server string
	k                               int
}

func main() {
	var cfg config
	build := flag.Bool("index", false, "построить индекс README + docs/ и выйти")
	question := flag.String("q", "", "один вопрос, как /help <вопрос>, без интерактивного режима")
	demo := flag.String("demo", "", "сценарий для записи экрана: команды печатаются сами, строки с # — подписи (темп: PACE)")
	flag.StringVar(&cfg.repo, "repo", "", "репозиторий проекта (по умолчанию корень текущего git-репозитория)")
	flag.StringVar(&cfg.indexPath, "index-file", filepath.Join("day-31", "index", "docs.json"), "файл индекса")
	flag.StringVar(&cfg.ollama, "ollama", "http://127.0.0.1:11434", "адрес Ollama для эмбеддингов")
	flag.StringVar(&cfg.server, "server", filepath.Join("bin", "day31-mcp"), "бинарник MCP-сервера git (go build -o bin/day31-mcp ./day-31/mcp-server)")
	flag.IntVar(&cfg.k, "k", 5, "сколько фрагментов документации передавать модели")
	flag.Parse()

	if cfg.repo == "" {
		top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			fail(errors.New("не нашёл git-репозиторий: запустите из него или задайте -repo"))
		}
		cfg.repo = strings.TrimSpace(string(top))
	}
	embedder := &rag.OllamaClient{BaseURL: cfg.ollama, Model: embedModel}
	ctx := context.Background()

	if *build {
		fail(runIndex(ctx, cfg, embedder))
		return
	}
	llm.LoadDotEnv(".env")
	assistant, err := open(ctx, cfg, embedder)
	if err != nil {
		fail(err)
	}
	defer assistant.close()
	if *question != "" {
		fail(assistant.help(ctx, os.Stdout, *question))
		return
	}
	var input io.Reader = os.Stdin
	if *demo != "" {
		script, err := newDemoInput(*demo, os.Stdout)
		if err != nil {
			fail(err)
		}
		input = script
	}
	repl(ctx, assistant, input, os.Stdout)
}

func runIndex(ctx context.Context, cfg config, embedder *rag.OllamaClient) error {
	fmt.Printf("Индексирую документацию %s (эмбеддинги %s через Ollama)\n", cfg.repo, embedModel)
	file, err := docrag.Build(ctx, cfg.repo, embedder, embedModel, func(source string, chunks int) {
		fmt.Printf("  %-34s %3d фрагм.\n", rag.TerminalSafe(source, 120), chunks)
	})
	if err != nil {
		return err
	}
	if err := docrag.Save(cfg.indexPath, file); err != nil {
		return err
	}
	fmt.Printf("Готово: файлов %d, фрагментов %d, размерность %d, токенов %d, %.1f с → %s\n",
		len(file.Files), file.Header.ChunkCount, file.Header.Dimension, file.Header.TotalTokens,
		float64(file.Header.EmbeddingDurationMS)/1000, cfg.indexPath)
	return nil
}

// staleWarning names the docs changed since indexing, or returns "" when the index is current.
func staleWarning(repo string, index docrag.File) (string, error) {
	stale, err := docrag.Stale(repo, index)
	if err != nil || len(stale) == 0 {
		return "", err
	}
	return "⚠ Документация изменилась после индексации (" + rag.TerminalSafe(strings.Join(stale, ", "), 600) +
		"): ответы могут опираться на старый текст. Перестройте: -index", nil
}

type assistant struct {
	cfg      config
	embedder rag.Embedder
	index    docrag.File
	model    toolagent.LLM
	session  *mcpclient.Session
}

func open(ctx context.Context, cfg config, embedder rag.Embedder) (*assistant, error) {
	index, err := docrag.Load(cfg.indexPath)
	if err != nil {
		return nil, err
	}
	warning, err := staleWarning(cfg.repo, index)
	if err != nil {
		return nil, err
	}
	if warning != "" {
		fmt.Println(warning)
	}
	model, err := llm.NewWithKeyEnv(keyEnv)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(cfg.server); err != nil {
		return nil, fmt.Errorf("нет MCP-сервера %s: соберите его `go build -o %s ./day-31/mcp-server`", cfg.server, cfg.server)
	}
	command := exec.Command(cfg.server, "-repo", cfg.repo)
	transport := mcpclient.Transport{MCP: &mcp.CommandTransport{Command: command}, Description: "stdio", Command: cfg.server + " -repo " + cfg.repo}
	session, err := mcpclient.NewNamed("day31-assistant", "1.0.0").Open(ctx, transport)
	if err != nil {
		return nil, fmt.Errorf("MCP-сервер не запустился: %w", err)
	}
	fmt.Printf("Подключено: MCP %s %s (stdio) · индекс: фрагментов %d, файлов %d · модель %s\n",
		session.ServerName, session.ServerVersion, index.Header.ChunkCount, len(index.Files), model.Model)
	return &assistant{cfg: cfg, embedder: embedder, index: index, model: model, session: session}, nil
}

func (a *assistant) close() { _ = a.session.Close() }

func repl(ctx context.Context, a *assistant, in io.Reader, out io.Writer) {
	fmt.Fprintln(out, usage)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for {
		fmt.Fprint(out, "\n"+prompt)
		if !scanner.Scan() {
			fmt.Fprintln(out)
			return
		}
		command, arg := parse(scanner.Text())
		switch command {
		case "":
		case "/exit", "/quit":
			return
		case "/help":
			if arg == "" {
				fmt.Fprintln(out, "Задайте вопрос: /help как устроен проект?")
				continue
			}
			if err := a.help(ctx, out, arg); err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
			}
		case "/branch", "/status", "/files", "/diff":
			if err := a.direct(ctx, out, command, arg); err != nil {
				fmt.Fprintln(out, "Ошибка:", err)
			}
		default:
			fmt.Fprintln(out, "Неизвестная команда.", usage)
		}
	}
}

// parse splits a REPL line into a slash command and its argument; text without a
// leading slash is reported as the command so the REPL can reject it.
func parse(line string) (string, string) {
	line = strings.TrimSpace(line)
	command, arg, _ := strings.Cut(line, " ")
	return strings.ToLower(command), strings.TrimSpace(arg)
}

func (a *assistant) help(ctx context.Context, out io.Writer, question string) error {
	started := time.Now()
	branch, err := a.session.CallTool(ctx, "git_branch", map[string]any{})
	if err != nil {
		return fmt.Errorf("MCP git_branch: %w", err)
	}
	branchText := mcpclient.ToolText(branch)
	if branch.IsError {
		return fmt.Errorf("MCP git_branch вернул ошибку: %s", branchText)
	}
	fmt.Fprintf(out, "▸ MCP git_branch → %s\n", describeBranch(branch.StructuredContent, branchText))

	results, err := docrag.Search(ctx, a.embedder, a.index, question, a.cfg.k)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "▸ RAG: фрагментов документации %d\n", len(results))
	for _, r := range results {
		fmt.Fprintf(out, "   %d. %.3f  %s § %s\n", r.Rank, r.Similarity, rag.TerminalSafe(r.Chunk.Source, 80), rag.TerminalSafe(r.Chunk.Section, 70))
	}

	trace, err := toolagent.Run(ctx, a.model, a.session, toolagent.Input{
		SystemPrompt:   systemPrompt(branchText, results),
		Question:       question,
		ModelCallLimit: 4,
	})
	for _, call := range trace.ToolCalls {
		status := "ok"
		if call.IsError || call.Rejected != "" {
			status = "ошибка"
		}
		fmt.Fprintf(out, "▸ Модель вызвала MCP %s %s → %s\n", rag.TerminalSafe(call.Name, 60), rag.TerminalSafe(call.RawArguments, 200), status)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%s\n\n", strings.TrimSpace(rag.TerminalSafeMultiline(trace.FinalAnswer, 8000)))
	cost := "цена неизвестна"
	if trace.Totals.CostKnown {
		cost = fmt.Sprintf("$%.5f", trace.Totals.Cost)
	}
	fmt.Fprintf(out, "▸ обращений к модели: %d · вызовов MCP моделью: %d · токены: вход %d (из кэша %d), выход %d · %s · %.1f с\n",
		trace.Totals.ModelCalls, trace.Totals.ToolCalls, trace.Totals.PromptTokens, trace.Totals.CachedTokens,
		trace.Totals.OutputTokens, cost, time.Since(started).Seconds())
	return nil
}

// direct runs one MCP tool without the model: the user pokes the project the same way
// the model does, and sees exactly what the model would see.
func (a *assistant) direct(ctx context.Context, out io.Writer, command, arg string) error {
	tool, args := map[string]string{"/branch": "git_branch", "/status": "git_status", "/files": "list_files", "/diff": "git_diff"}[command], map[string]any{}
	switch command {
	case "/files":
		args["prefix"] = arg
	case "/diff":
		args["path"] = arg
	}
	result, err := a.session.CallTool(ctx, tool, args)
	if err != nil {
		return fmt.Errorf("MCP %s: %w", tool, err)
	}
	text := mcpclient.ToolText(result)
	if result.IsError {
		return fmt.Errorf("MCP %s: %s", tool, text)
	}
	fmt.Fprintf(out, "▸ MCP %s\n", tool)
	switch tool {
	case "git_branch":
		fmt.Fprintln(out, describeBranch(result.StructuredContent, text))
	case "git_status":
		var s gitmcp.StatusOutput
		if err := json.Unmarshal([]byte(text), &s); err != nil {
			return err
		}
		if s.Clean {
			fmt.Fprintf(out, "Ветка %s, изменений нет\n", s.Branch)
		} else {
			fmt.Fprintf(out, "Ветка %s, изменено файлов: %d\n", s.Branch, len(s.Changed))
			for _, line := range s.Changed {
				fmt.Fprintln(out, "  "+rag.TerminalSafe(line, 200))
			}
		}
	case "list_files":
		var f gitmcp.ListFilesOutput
		if err := json.Unmarshal([]byte(text), &f); err != nil {
			return err
		}
		const shown = 40
		for i, name := range f.Files {
			if i == shown {
				fmt.Fprintf(out, "  … ещё %d\n", f.Total-shown)
				break
			}
			fmt.Fprintln(out, "  "+rag.TerminalSafe(name, 200))
		}
		fmt.Fprintf(out, "Всего файлов: %d\n", f.Total)
	case "git_diff":
		var d gitmcp.DiffOutput
		if err := json.Unmarshal([]byte(text), &d); err != nil {
			return err
		}
		if d.Stat == "" {
			fmt.Fprintln(out, "Незакоммиченных изменений в отслеживаемых файлах нет")
			return nil
		}
		fmt.Fprintln(out, rag.TerminalSafeMultiline(d.Stat, 4000))
	}
	return nil
}

func describeBranch(structured any, fallback string) string {
	// The commit subject is repository content: an escape sequence in it must not reach the terminal.
	m, ok := structured.(map[string]any)
	if !ok {
		return rag.TerminalSafe(fallback, 300)
	}
	return rag.TerminalSafe(fmt.Sprintf("%v @ %v «%v»", m["branch"], m["commit"], m["subject"]), 300)
}

func systemPrompt(branch string, results []rag.SearchResult) string {
	var b strings.Builder
	b.WriteString(`Ты — ассистент разработчика проекта ai-advent-challenge-9. Отвечай по-русски, коротко и по делу.

Источники ответа:
1. Фрагменты документации проекта ниже (README.md и docs/), найденные поиском по смыслу вопроса.
2. Инструменты MCP (git_branch, git_status, list_files, git_diff) — факты о рабочей копии прямо сейчас.

Правила:
- Утверждай только то, что есть во фрагментах или в результатах инструментов. Если ответа там нет, так и скажи и назови, где его искать.
- После утверждения из документации ставь ссылку вида [README.md § Раздел].
- Факты из git помечай [git].
- Если вопрос о файлах, папках, изменениях или ветке — вызови подходящий инструмент, не угадывай.

Текущая ветка (результат git_branch):
`)
	b.WriteString(branch)
	b.WriteString("\n\nФрагменты документации:\n")
	for _, r := range results {
		fmt.Fprintf(&b, "\n[%d] %s § %s\n%s\n", r.Rank, r.Chunk.Source, r.Chunk.Section, r.Chunk.Text)
	}
	return b.String()
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
