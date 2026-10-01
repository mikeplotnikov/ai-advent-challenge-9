package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
	"io"
	"math"
	"os"
	"strings"
	"time"
)

func main() { os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr)) }
func validateParams(p Params) error {
	if math.IsNaN(p.Cos) || math.IsInf(p.Cos, 0) || p.Cos < 0 || p.Cos > 1 {
		return fmt.Errorf("-cos: finite 0..1 required")
	}
	if p.MinScore < 0 || p.MinScore > 3 || p.KBefore < 1 || p.KBefore > 20 || p.KAfter < 1 || p.KAfter > p.KBefore {
		return fmt.Errorf("invalid -min-score/-k-before/-k-after")
	}
	return nil
}
func runCLI(args []string, out, errout io.Writer) int {
	f := flag.NewFlagSet("day-24", flag.ContinueOnError)
	f.SetOutput(errout)
	ask := f.String("ask", "", "вопрос")
	eval := f.Bool("eval", false, "10 вопросов")
	report := f.Bool("report", false, "отчёт без сети")
	ollama := f.String("ollama", defaultOllama, "Ollama URL")
	timeout := f.Duration("timeout", 20*time.Minute, "timeout")
	cos := f.Float64("cos", evalParams.Cos, "cosine")
	min := f.Int("min-score", 2, "rerank")
	before := f.Int("k-before", 10, "pool")
	after := f.Int("k-after", 3, "context")
	if e := f.Parse(args); e != nil {
		return 2
	}
	visited := map[string]bool{}
	f.Visit(func(v *flag.Flag) { visited[v.Name] = true })
	n := 0
	if visited["ask"] {
		n++
	}
	if *eval {
		n++
	}
	if *report {
		n++
	}
	if n != 1 || f.NArg() != 0 {
		fmt.Fprintln(errout, "выберите ровно один режим: -ask, -eval, -report")
		return 2
	}
	p := Params{*before, *cos, *min, *after}
	if e := validateParams(p); e != nil {
		fmt.Fprintln(errout, e)
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(errout, "timeout must be positive")
		return 2
	}
	if !visited["ask"] {
		for _, k := range []string{"cos", "min-score", "k-before", "k-after"} {
			if visited[k] {
				fmt.Fprintln(errout, "пороги применимы только к -ask")
				return 2
			}
		}
	}
	dir := rag.ProjectPath("day-24")
	if *report {
		if e := writeReport(dir); e != nil {
			fmt.Fprintln(errout, e)
			return 1
		}
		fmt.Fprintln(out, "отчёт пересобран")
		return 0
	}
	if *eval {
		if _, e := os.Stat(dir + "/run.json"); e == nil || !os.IsNotExist(e) {
			fmt.Fprintln(errout, "run.json уже существует или недоступен; сохраните исходный прогон")
			return 1
		}
	}
	if visited["ask"] && strings.TrimSpace(*ask) == "" {
		fmt.Fprintln(errout, "пустой вопрос")
		return 2
	}
	llm.LoadDotEnv(".env")
	client, e := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY24")
	if e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	client.Model = modelName
	s, _, e := openSearcherFn(*ollama)
	if e != nil {
		fmt.Fprintln(errout, rag.TerminalSafe(e.Error(), 2000))
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if *eval {
		r, e := runEvaluation(ctx, client, s, out)
		if e == nil {
			e = saveRun(dir+"/run.json", r)
		}
		if e == nil {
			e = writeReport(dir)
		}
		if e != nil {
			fmt.Fprintln(errout, rag.TerminalSafe(e.Error(), 2000))
			printConsumedCosts(errout, r.Questions)
			return 1
		}
		fmt.Fprintf(out, "run.json сохранён: %d вызовов, $%.8f\n", r.Meta.ModelCalls, r.Meta.TotalCostUSD)
		return 0
	}
	q, e := measureQuestion(ctx, client, s, Question{ID: "ask", Kind: "ask", Question: *ask}, p)
	if e != nil {
		fmt.Fprintln(errout, rag.TerminalSafe(e.Error(), 2000))
		printConsumedCosts(errout, []QuestionRun{q})
		return 1
	}
	printQuestion(out, q, p)
	return 0
}
func printConsumedCosts(w io.Writer, questions []QuestionRun) {
	var usage llm.Usage
	attempts := 0
	cost := 0.0
	known := true
	for _, q := range questions {
		for _, call := range questionCalls(q) {
			attempts += len(call.Attempts)
			addUsage(&usage, call.Usage)
			cost += call.CostUSD
			known = known && call.CostKnown
		}
	}
	price := "неизвестна"
	if known {
		price = fmt.Sprintf("$%.8f", cost)
	}
	fmt.Fprintf(w, "Затраты до ошибки: попыток %d; вход %d / выход %d / всего %d токенов; цена %s\n", attempts, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, price)
}
func printQuestion(w io.Writer, q QuestionRun, p Params) {
	safe := func(s string) string { return rag.TerminalSafeMultiline(s, 10000) }
	fmt.Fprintf(w, "Вопрос: %s\nRewrite: %s\nTop-%d → cosine ≥ %.2f → rerank ≥ %d → top-%d\n", safe(q.Question.Question), safe(q.Rewritten), p.KBefore, p.Cos, p.MinScore, p.KAfter)
	for _, c := range q.Candidates {
		score := "—"
		if c.RerankScore != nil {
			score = fmt.Sprint(*c.RerankScore)
		}
		fate := c.Cut
		if c.Position > 0 {
			fate = fmt.Sprintf("контекст #%d", c.Position)
		}
		fmt.Fprintf(w, "%d. cos=%.4f score=%s %s · %s · %s · %s\n", c.Rank, c.Similarity, score, fate, safe(c.Source), safe(c.Section), safe(c.ChunkID))
	}
	fmt.Fprintf(w, "\nОтвет: %s\n", safe(q.Answer.Answer))
	if q.Answer.Unknown {
		fmt.Fprintf(w, "Уточнение: %s\nПричина отказа: %s\n", safe(q.Answer.Clarification), q.Checks.RefusalReason)
	}
	for _, s := range q.Answer.Sources {
		fmt.Fprintf(w, "Источник: %s · %s · %s\nЦитата: %s\n", safe(s.Source), safe(s.Section), safe(s.ChunkID), safe(s.Quote))
	}
	fmt.Fprintf(w, "Проверки: схема=%t источники=%s цитаты=%s; смысловая оценка не выполнена для ask.\n", q.Checks.Schema, q.Checks.Sources, q.Checks.Quotes)
	total := 0.0
	known := true
	for _, c := range questionCalls(q) {
		cost := "неизвестна"
		if c.CostKnown {
			cost = fmt.Sprintf("$%.8f", c.CostUSD)
		}
		fmt.Fprintf(w, "%s: вход %d / выход %d токенов, %d попыток, цена %s\n", c.Stage, c.Usage.PromptTokens, c.Usage.CompletionTokens, len(c.Attempts), cost)
		total += c.CostUSD
		known = known && c.CostKnown
	}
	cost := "неизвестна"
	if known {
		cost = fmt.Sprintf("$%.8f", total)
	}
	fmt.Fprintf(w, "Итого: %s\n", cost)
}
