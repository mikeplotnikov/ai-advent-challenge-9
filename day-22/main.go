package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

var loadSearchFn = loadSearch

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("day-22", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ask := fs.String("ask", "", "задать вопрос")
	evalMode := fs.Bool("eval", false, "выполнить замер 10×2×3")
	reportMode := fs.Bool("report", false, "пересобрать отчёт из run.json")
	mode := fs.String("mode", "both", "режим ответа: both, rag или norag")
	topK := fs.Int("k", defaultK, "число найденных чанков (1..10)")
	showPrompt := fs.Bool("show-prompt", false, "показать сообщения, отправленные модели")
	ollamaURL := fs.String("ollama", defaultOllama, "адрес Ollama")
	timeout := fs.Duration("timeout", 30*time.Minute, "общий таймаут")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	visited := map[string]bool{}
	fs.Visit(func(value *flag.Flag) { visited[value.Name] = true })
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "позиционные аргументы не поддерживаются")
		return 2
	}
	modes := 0
	if visited["ask"] {
		modes++
	}
	if *evalMode {
		modes++
	}
	if *reportMode {
		modes++
	}
	if modes != 1 {
		fmt.Fprintln(stderr, "выберите ровно один режим: -ask, -eval или -report")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "-timeout должен быть больше нуля")
		return 2
	}
	if *topK < 1 || *topK > 10 {
		fmt.Fprintln(stderr, "-k должен быть от 1 до 10")
		return 2
	}
	if *evalMode && visited["k"] {
		fmt.Fprintln(stderr, "-k нельзя сочетать с -eval: замер всегда использует k = 5")
		return 2
	}
	if (*evalMode || *reportMode) && (visited["mode"] || *showPrompt || visited["ask"]) {
		fmt.Fprintln(stderr, "-mode, -show-prompt и -ask применимы только к режиму -ask")
		return 2
	}
	if visited["ask"] {
		if strings.TrimSpace(*ask) == "" {
			fmt.Fprintln(stderr, "-ask требует непустой вопрос")
			return 2
		}
		if *mode != "both" && *mode != "rag" && *mode != "norag" {
			fmt.Fprintln(stderr, "-mode: допустимы both, rag или norag")
			return 2
		}
	}
	if *reportMode {
		if err := writeReport(rag.ProjectPath(defaultRun), rag.ProjectPath(defaultQuestions), rag.ProjectPath(defaultResults), rag.ProjectPath(defaultShowcase)); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "отчёт пересобран: day-22/RESULTS.md и day-22/showcase.json")
		return 0
	}
	if *evalMode {
		runPath := rag.ProjectPath(defaultRun)
		if _, err := os.Stat(runPath); err == nil {
			fmt.Fprintln(stderr, "замер уже есть; чтобы повторить, удалите run.json вручную")
			return 1
		} else if !os.IsNotExist(err) {
			fmt.Fprintln(stderr, "проверить run.json:", err)
			return 1
		}
		lock, err := acquireEvalLock(runPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer func() {
			_ = lock.Close()
			_ = os.Remove(lock.Name())
		}()
	}

	llm.LoadDotEnv(".env")
	client, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY22")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client.Model = modelName
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if *evalMode {
		runValue, err := runEvaluation(ctx, *ollamaURL, client, stdout)
		if err != nil {
			fmt.Fprintln(stderr, rag.TerminalSafe(err.Error(), 2000))
			return 1
		}
		runPath := rag.ProjectPath(defaultRun)
		if err := writeRun(runPath, runValue); err != nil {
			fmt.Fprintln(stderr, "записать run.json:", err)
			return 1
		}
		if err := writeReport(runPath, rag.ProjectPath(defaultQuestions), rag.ProjectPath(defaultResults), rag.ProjectPath(defaultShowcase)); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "замер и отчёт записаны")
		return 0
	}
	return runAsk(ctx, client, *ollamaURL, *ask, *mode, *topK, *showPrompt, stdout, stderr)
}

func acquireEvalLock(runPath string) (*os.File, error) {
	lockPath := runPath + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return lock, nil
	}
	if os.IsExist(err) {
		return nil, fmt.Errorf("замер уже выполняется; если предыдущий процесс аварийно завершился, удалите %s вручную", lockPath)
	}
	return nil, fmt.Errorf("создать блокировку замера %s: %w", lockPath, err)
}

func runAsk(ctx context.Context, client *llm.Client, ollamaURL, question, mode string, k int, showPrompt bool, stdout, stderr io.Writer) int {
	var chunks []FoundChunk
	if mode == "rag" || mode == "both" {
		var err error
		chunks, _, _, err = loadSearchFn(ctx, ollamaURL, question, k)
		if err != nil {
			fmt.Fprintln(stderr, rag.TerminalSafe(err.Error(), 2000))
			return 1
		}
		fmt.Fprintln(stdout, "[найденные чанки]")
		for _, chunk := range chunks {
			preview := rag.TerminalSafe(chunk.Text, 240)
			fmt.Fprintf(stdout, "%d. %.4f · %s · %s · %s\n%s\n", chunk.Rank, chunk.Similarity,
				rag.TerminalSafe(chunk.ChunkID, 300), rag.TerminalSafe(chunk.Source, 300), rag.TerminalSafe(chunk.Section, 300), preview)
		}
	}
	failed := false
	callOne := func(name string, messages []llm.Message) {
		if showPrompt {
			printPrompt(stdout, name, messages)
		}
		call, err := callModel(ctx, client, name, 1, messages)
		if err != nil {
			failed = true
		}
		printCall(stdout, call)
	}
	if mode == "norag" || mode == "both" {
		callOne("norag", noRAGMessages(question))
	}
	if mode == "rag" || mode == "both" {
		callOne("rag", ragMessages(question, chunks))
	}
	if failed {
		return 1
	}
	return 0
}
