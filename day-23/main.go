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

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

var askModes = map[string][]string{
	"compare":         {modePlain, modeRewriteFilter},
	modePlain:         {modePlain},
	modeRewrite:       {modeRewrite},
	modeFilter:        {modeFilter},
	modeRewriteFilter: {modeRewriteFilter},
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("day-23", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ask := fs.String("ask", "", "задать вопрос")
	evalMode := fs.Bool("eval", false, "выполнить замер четырёх режимов")
	reportMode := fs.Bool("report", false, "пересобрать отчёт из run.json")
	mode := fs.String("mode", "compare", "compare, plain, rewrite, filter или rewrite_filter")
	cos := fs.Float64("cos", evalParams.Cos, "порог косинуса (0..1)")
	minScore := fs.Int("min-score", evalParams.MinScore, "минимальная оценка реранкера (0..3)")
	kBefore := fs.Int("k-before", evalParams.KBefore, "кандидатов до фильтра (1..20)")
	kAfter := fs.Int("k-after", evalParams.KAfter, "чанков после фильтра (1..k-before)")
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
	for _, on := range []bool{visited["ask"], *evalMode, *reportMode} {
		if on {
			modes++
		}
	}
	if modes != 1 {
		fmt.Fprintln(stderr, "выберите ровно один режим: -ask, -eval или -report")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "-timeout должен быть больше нуля")
		return 2
	}
	if !visited["ask"] {
		for _, name := range []string{"mode", "cos", "min-score", "k-before", "k-after"} {
			if visited[name] {
				fmt.Fprintf(stderr, "-%s применим только к -ask: замер всегда идёт с зафиксированными настройками\n", name)
				return 2
			}
		}
	}
	params := Params{KPlain: evalParams.KPlain, KBefore: *kBefore, Cos: *cos, MinScore: *minScore, KAfter: *kAfter}
	if visited["ask"] {
		if strings.TrimSpace(*ask) == "" {
			fmt.Fprintln(stderr, "-ask требует непустой вопрос")
			return 2
		}
		if _, ok := askModes[*mode]; !ok {
			fmt.Fprintln(stderr, "-mode: допустимы compare, plain, rewrite, filter или rewrite_filter")
			return 2
		}
		if msg := validateParams(params); msg != "" {
			fmt.Fprintln(stderr, msg)
			return 2
		}
	}
	if *reportMode {
		if err := writeReport(rag.ProjectPath(defaultRun), rag.ProjectPath(defaultDay21), rag.ProjectPath(defaultDay22),
			rag.ProjectPath(defaultResults), rag.ProjectPath(defaultShowcase)); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "отчёт пересобран: day-23/RESULTS.md и day-23/showcase.json")
		return 0
	}
	runPath := rag.ProjectPath(defaultRun)
	if *evalMode {
		if _, err := os.Stat(runPath); err == nil {
			fmt.Fprintln(stderr, "замер уже есть; чтобы повторить, удалите run.json вручную")
			return 1
		} else if !os.IsNotExist(err) {
			fmt.Fprintln(stderr, "проверить run.json:", err)
			return 1
		}
	}

	llm.LoadDotEnv(".env")
	client, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY23")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client.Model = modelName
	searcher, manifestSHA, err := openSearcherFn(*ollamaURL)
	if err != nil {
		fmt.Fprintln(stderr, rag.TerminalSafe(err.Error(), 2000))
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if *evalMode {
		runValue, err := runEvaluation(ctx, client, searcher, manifestSHA, stdout)
		if err != nil {
			fmt.Fprintln(stderr, rag.TerminalSafe(err.Error(), 2000))
			return 1
		}
		raw, err := encodeJSON(runValue)
		if err == nil {
			err = writeBytesAtomic(runPath, raw)
		}
		if err != nil {
			fmt.Fprintln(stderr, "записать run.json:", err)
			return 1
		}
		if err := writeReport(runPath, rag.ProjectPath(defaultDay21), rag.ProjectPath(defaultDay22),
			rag.ProjectPath(defaultResults), rag.ProjectPath(defaultShowcase)); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "замер и отчёт записаны: %d вызовов, %s\n", runValue.Meta.ModelCallsCount, costCell(runValue.Meta.TotalCostUSD, runValue.Meta.TotalCostKnown))
		return 0
	}
	return runAsk(ctx, client, searcher, *ask, askModes[*mode], params, stdout, stderr)
}

func validateParams(p Params) string {
	switch {
	case p.Cos < 0 || p.Cos > 1:
		return "-cos должен быть от 0 до 1"
	case p.MinScore < 0 || p.MinScore > 3:
		return "-min-score должен быть от 0 до 3"
	case p.KBefore < 1 || p.KBefore > 20:
		return "-k-before должен быть от 1 до 20"
	case p.KAfter < 1 || p.KAfter > p.KBefore:
		return "-k-after должен быть от 1 до -k-before"
	}
	return ""
}

// runAsk answers one question in the chosen modes and prints every stage. A failing
// mode prints its error in place and the others still run; the exit code is then 1.
func runAsk(ctx context.Context, client *llm.Client, searcher Searcher, question string, modes []string, p Params, stdout, stderr io.Writer) int {
	query := Question{ID: "ask", Kind: "ask", Question: question}
	rewritten := ""
	needRewrite := false
	for _, mode := range modes {
		needRewrite = needRewrite || usesRewrite(mode)
	}
	if needRewrite {
		value, call, err := rewriteQuery(ctx, client, question)
		if err != nil {
			fmt.Fprintln(stderr, rag.TerminalSafe(err.Error(), 2000))
			return 1
		}
		rewritten = value
		fmt.Fprintf(stdout, "[rewrite] %s\n%s\n", rag.TerminalSafe(rewritten, 1000), callCostLine(call))
	}
	pools := map[string][]Candidate{}
	failed := false
	for _, mode := range modes {
		searchQuery := question
		if usesRewrite(mode) {
			searchQuery = rewritten
		}
		pool, ok := pools[searchQuery]
		if !ok {
			var err error
			pool, err = searcher.Search(ctx, searchQuery, poolSize(p))
			if err != nil {
				fmt.Fprintln(stderr, rag.TerminalSafe(err.Error(), 2000))
				return 1
			}
			pools[searchQuery] = pool
		}
		fmt.Fprintf(stdout, "\n===== %s =====\n", mode)
		modeRun, err := selectContext(ctx, client, mode, question, searchQuery, pool, p)
		printCandidates(stdout, modeRun, p)
		if err != nil {
			fmt.Fprintf(stdout, "ошибка: %s\n", rag.TerminalSafe(err.Error(), 1000))
			failed = true
			continue
		}
		if err := answerMode(ctx, client, query, &modeRun); err != nil {
			failed = true
		}
		printAnswer(stdout, *modeRun.Answer)
	}
	if failed {
		return 1
	}
	return 0
}

func printCandidates(w io.Writer, run ModeRun, p Params) {
	if run.Mode == modeFilter || run.Mode == modeRewriteFilter {
		fmt.Fprintf(w, "top-%d → косинус ≥ %.2f → реранкер ≥ %d → top-%d\n", p.KBefore, p.Cos, p.MinScore, p.KAfter)
	} else {
		fmt.Fprintf(w, "top-%d без фильтра\n", p.KPlain)
	}
	for _, candidate := range run.Candidates {
		score := "—"
		if candidate.RerankScore != nil {
			score = fmt.Sprint(*candidate.RerankScore)
		}
		fate := fmt.Sprintf("✓ в контексте [%d]", candidate.Position)
		switch candidate.Cut {
		case cutCos:
			fate = "✗ ниже порога косинуса"
		case cutScore:
			fate = "✗ оценка реранкера ниже порога"
		case cutTopK:
			fate = "✗ за пределами top-K"
		}
		fmt.Fprintf(w, "%2d. cos %.4f · оценка %s · %s · %s — %s\n", candidate.Rank, candidate.Similarity, score,
			rag.TerminalSafe(candidate.Source, 200), rag.TerminalSafe(candidate.Section, 200), fate)
	}
	if run.Rerank != nil && run.Rerank.Error == "" {
		fmt.Fprintf(w, "реранкер: %s\n", callCostLine(*run.Rerank))
	}
}
