package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
	"io"
	"os"
	"strings"
	"time"
)

func main() { os.Exit(runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
func printTurn(w io.Writer, t Turn) {
	safe := func(s string) string { return rag.TerminalSafeMultiline(s, 10000) }
	fmt.Fprintf(w, "\nШаг %d\nВы: %s\nЦель: %s\nОграничения: %s\nПоиск: %s\nНайдено: %d; контекст: %d\nАссистент: %s\nИсточники:\n", t.Number, safe(t.User), safe(t.After.Goal), safe(strings.Join(t.After.Constraints, "; ")), safe(t.Query), len(t.RAG.Candidates), len(t.RAG.Context()), safe(t.RAG.Answer.Answer))
	if t.RAG.Answer.Unknown {
		fmt.Fprintf(w, "Источников нет. %s\n", safe(t.RAG.Answer.Clarification))
	}
	for _, s := range t.RAG.Answer.Sources {
		fmt.Fprintf(w, "%s · %s · %s\nЦитата: %s\n", safe(s.Source), safe(s.Section), safe(s.ChunkID), safe(s.Quote))
	}
	fmt.Fprintf(w, "Проверка цитат: %s\n", t.RAG.Checks.Quotes)
	printCosts(w, t)
}
func printCosts(w io.Writer, t Turn) {
	cost := 0.0
	known := true
	attempts := 0
	for _, c := range turnCalls(t) {
		cost += c.CostUSD
		known = known && c.CostKnown
		attempts += len(c.Attempts)
	}
	price := "неизвестна"
	if known {
		price = fmt.Sprintf("$%.8f", cost)
	}
	fmt.Fprintf(w, "Попыток LLM: %d; цена: %s\n", attempts, price)
}
func runCLI(args []string, in io.Reader, out, errout io.Writer) int {
	f := flag.NewFlagSet("day-25", flag.ContinueOnError)
	f.SetOutput(errout)
	ask := f.String("ask", "", "one message")
	chat := f.Bool("chat", false, "interactive chat, /exit to quit")
	eval := f.Bool("eval", false, "two frozen scenarios")
	report := f.Bool("report", false, "offline report")
	session := f.String("session", "", "session JSON path (required for chat/ask)")
	ollama := f.String("ollama", defaultOllama, "Ollama URL")
	timeout := f.Duration("timeout", 10*time.Minute, "per-turn timeout")
	if e := f.Parse(args); e != nil {
		return 2
	}
	visited := map[string]bool{}
	f.Visit(func(v *flag.Flag) { visited[v.Name] = true })
	n := 0
	for _, b := range []bool{visited["ask"], *chat, *eval, *report} {
		if b {
			n++
		}
	}
	if n != 1 || f.NArg() != 0 || *timeout <= 0 || visited["ask"] && strings.TrimSpace(*ask) == "" {
		fmt.Fprintln(errout, "выберите один режим: -chat, -ask TEXT, -eval, -report")
		return 2
	}
	dir := rag.ProjectPath("day-25")
	if *report {
		if e := writeReport(dir); e != nil {
			fmt.Fprintln(errout, rag.TerminalSafe(e.Error(), 2000))
			return 1
		}
		fmt.Fprintln(out, "RESULTS.md и showcase.json пересобраны")
		return 0
	}
	if !*eval && strings.TrimSpace(*session) == "" {
		fmt.Fprintln(errout, "требуется -session PATH")
		return 2
	}
	if *eval {
		if e := checkCaptureClear(dir); e != nil {
			fmt.Fprintln(errout, rag.TerminalSafe(e.Error(), 2000))
			return 1
		}
	}
	s := emptySession()
	var e error
	if !*eval {
		s, e = loadSession(*session)
		if e != nil {
			fmt.Fprintln(errout, rag.TerminalSafe(e.Error(), 2000))
			return 1
		}
	}
	llm.LoadDotEnv(".env")
	c, e := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY25")
	if e != nil {
		fmt.Fprintln(errout, rag.TerminalSafe(e.Error(), 2000))
		return 1
	}
	c.Model = modelName
	search, _, e := openSearcherFn(*ollama)
	if e != nil {
		fmt.Fprintln(errout, rag.TerminalSafe(e.Error(), 2000))
		return 1
	}
	if *eval {
		if e = runEvaluation(c, search, dir, *timeout, out); e != nil {
			fmt.Fprintln(errout, rag.TerminalSafe(e.Error(), 2000))
			return 1
		}
		return 0
	}
	apply := func(user string) error {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		t, e := nextTurn(ctx, c, search, s, user)
		if e != nil {
			printCosts(errout, t)
			return e
		}
		next := Session{Version: 1, Turns: append(append([]Turn{}, s.Turns...), t)}
		if e = saveSession(*session, next); e != nil {
			printCosts(errout, t)
			return e
		}
		s = next
		printTurn(out, t)
		return nil
	}
	if visited["ask"] {
		if e = apply(*ask); e != nil {
			fmt.Fprintln(errout, rag.TerminalSafe(e.Error(), 2000))
			return 1
		}
		return 0
	}
	fmt.Fprintf(out, "Продолжаем: сохранённых ходов %d. /exit — выход; каждый ответ сохраняется.\n", len(s.Turns))
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 16000)
	for {
		fmt.Fprint(out, "\nВы> ")
		if !scanner.Scan() {
			break
		}
		user := strings.TrimSpace(scanner.Text())
		if user == "/exit" {
			break
		}
		if user == "" {
			continue
		}
		if e = apply(user); e != nil {
			fmt.Fprintln(errout, rag.TerminalSafe(e.Error(), 2000))
			return 1
		}
	}
	if e = scanner.Err(); e != nil {
		fmt.Fprintln(errout, rag.TerminalSafe(e.Error(), 2000))
		return 1
	}
	return 0
}
