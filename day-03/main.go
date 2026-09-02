// Day 3, CLI — one task solved five ways, so "which method reasons better" is
// answered with a hit rate against a known truth instead of an impression.
// The task is a counting problem: its answer is one integer, and the integer is
// computed here, not trusted to the model.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

const defaultModel = "deepseek-v4-pro"

// prices are dollars per 1M tokens, {input, output}, from the provider's own
// documentation (checked 2026-09-02). Reasoning tokens are part of the output
// count, so they are already paid for in completion_tokens.
var prices = map[string][2]float64{
	"deepseek-v4-pro":   {1.74, 3.48},
	"deepseek-v4-flash": {0.14, 0.28},
}

type attempt struct {
	answer    int
	parsed    bool
	correct   bool
	calls     int
	usage     llm.Usage
	elapsed   time.Duration
	text      string
	reasoning string
	// generated is the prompt the model wrote for itself in the meta-prompt method.
	generated string
	finish    string
	err       error
}

type outcome struct {
	method   method
	attempts []attempt
}

func main() {
	llm.LoadDotEnv(".env")

	repeat := flag.Int("repeat", 1, "прогнать каждый способ N раз и посчитать долю верных")
	model := flag.String("model", defaultModel, "модель DeepSeek")
	only := flag.String("only", "", "прогнать только эти способы, например A,B,E")
	full := flag.Bool("full", false, "печатать ответы и рассуждения целиком")
	workers := flag.Int("workers", 5, "сколько запросов держать в воздухе одновременно")
	flag.Parse()

	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "лишний аргумент %q: задача у дня 3 одна и зашита в код\n", flag.Arg(0))
		flag.Usage()
		os.Exit(2)
	}
	if *repeat < 1 || *workers < 1 {
		fmt.Fprintln(os.Stderr, "-repeat и -workers должны быть не меньше 1")
		os.Exit(2)
	}

	selected, err := pick(methods(), *only)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	client, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY3")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client.Model = *model

	truth := groundTruth()
	fmt.Printf("Задача (одна и та же во всех способах):\n  %s\n\n", taskText)
	fmt.Printf("Эталон: %d — посчитан перебором в day-03/task.go, а не взят у модели\n", truth)
	fmt.Printf("Эталонное множество: %s\n", joinInts(groundTruthSet()))
	fmt.Printf("Модель: %s · повторов на способ: %d\n", client.Model, *repeat)

	outcomes := make([]outcome, 0, len(selected))
	for _, m := range selected {
		attempts := runMethod(context.Background(), client, m, truth, *repeat, *workers)
		outcomes = append(outcomes, outcome{method: m, attempts: attempts})
		report(m, attempts, client.Model, *repeat == 1 || *full, *full)
	}

	summary(outcomes, client.Model, truth, *repeat)
}

// pick keeps the declared order so the comparison always reads A, B, C, D, E.
func pick(all []method, only string) ([]method, error) {
	if strings.TrimSpace(only) == "" {
		return all, nil
	}
	wanted := map[string]bool{}
	for _, part := range strings.Split(only, ",") {
		part = strings.ToUpper(strings.TrimSpace(part))
		if part != "" {
			wanted[part] = true
		}
	}
	var out []method
	for _, m := range all {
		if wanted[m.key] {
			out = append(out, m)
			delete(wanted, m.key)
		}
	}
	if len(wanted) > 0 {
		unknown := make([]string, 0, len(wanted))
		for k := range wanted {
			unknown = append(unknown, k)
		}
		return nil, fmt.Errorf("неизвестный способ: %s", strings.Join(unknown, ", "))
	}
	return out, nil
}

// runMethod fires the repeats concurrently but keeps them in order: the model is
// non-deterministic, so a single run proves nothing about a method.
func runMethod(ctx context.Context, c *llm.Client, m method, truth, repeat, workers int) []attempt {
	attempts := make([]attempt, repeat)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			attempts[i] = execute(ctx, c, m, truth)
		}(i)
	}
	wg.Wait()
	return attempts
}

func execute(ctx context.Context, c *llm.Client, m method, truth int) attempt {
	start := time.Now()
	var a attempt
	system := m.system

	if m.twoStep {
		written, err := c.AskWith(ctx, []llm.Message{
			{Role: "system", Content: m.system},
			{Role: "user", Content: "Задача:\n" + taskText},
		}, m.opts)
		a.calls++
		if err != nil {
			a.err, a.elapsed = err, time.Since(start)
			return a
		}
		a.usage = addUsage(a.usage, written.Usage)
		a.generated = written.Content
		// The generated prompt may say anything about the answer's shape, so the
		// one shared output rule is appended — the same rule every other method
		// already carries, and the only thing kept equal across all five.
		system = written.Content + "\n\n" + outputRule
	}

	solved, err := c.AskWith(ctx, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: taskText},
	}, m.opts)
	a.calls++
	a.elapsed = time.Since(start)
	if err != nil {
		a.err = err
		return a
	}
	a.usage = addUsage(a.usage, solved.Usage)
	a.text = solved.Content
	a.reasoning = solved.Reasoning
	a.finish = solved.FinishReason
	a.answer, a.parsed = parseAnswer(solved.Content)
	a.correct = a.parsed && a.answer == truth
	return a
}

func addUsage(into, from llm.Usage) llm.Usage {
	into.PromptTokens += from.PromptTokens
	into.CompletionTokens += from.CompletionTokens
	into.TotalTokens += from.TotalTokens
	into.CompletionDetails.ReasoningTokens += from.CompletionDetails.ReasoningTokens
	return into
}

func report(m method, attempts []attempt, model string, verbose, full bool) {
	fmt.Println()
	fmt.Println(strings.Repeat("─", 72))
	fmt.Printf("%s · %s\n", m.key, m.title)
	fmt.Printf("Идея: %s\n", m.idea)
	fmt.Printf("Отправлено: %s\n", sentFields(m))
	fmt.Println(strings.Repeat("─", 72))

	if verbose {
		first := attempts[0]
		if first.err == nil {
			if first.generated != "" {
				fmt.Printf("\nПромпт, который модель написала себе сама:\n%s\n", indent(first.generated))
			}
			if first.reasoning != "" {
				fmt.Printf("\nРассуждение модели (поле reasoning_content, в ответ не попадает):\n%s\n",
					indent(clip(first.reasoning, 900, full)))
			}
			fmt.Printf("\nОтвет:\n%s\n", indent(first.text))
		}
	}

	fmt.Println()
	ok := 0
	shown := make([]string, 0, len(attempts))
	for _, a := range attempts {
		switch {
		case a.err != nil:
			shown = append(shown, "ошибка")
		case !a.parsed:
			shown = append(shown, "нет ответа")
		default:
			shown = append(shown, fmt.Sprint(a.answer))
		}
		if a.correct {
			ok++
		}
	}
	fmt.Printf("верных: %d из %d · ответы: %s\n", ok, len(attempts), strings.Join(shown, ", "))
	for i, a := range attempts {
		if a.err != nil {
			fmt.Printf("  прогон %d — ошибка: %v\n", i+1, a.err)
			continue
		}
		mark := "✗ мимо"
		if a.correct {
			mark = "✓ верно"
		}
		line := fmt.Sprintf("  прогон %d: %s · %d токенов ответа", i+1, mark, a.usage.CompletionTokens)
		if r := a.usage.CompletionDetails.ReasoningTokens; r > 0 {
			line += fmt.Sprintf(" (из них %d на рассуждение)", r)
		}
		line += fmt.Sprintf(" · %d вызов(а) · %.1f с", a.calls, a.elapsed.Seconds())
		if c, known := cost(model, a.usage); known {
			line += fmt.Sprintf(" · $%.4f", c)
		}
		if a.finish == "length" {
			line += " · упёрся в max_tokens"
		}
		fmt.Println(line)
	}
}

func sentFields(m method) string {
	parts := []string{fmt.Sprintf("max_tokens=%d", m.opts.MaxTokens)}
	if m.opts.Thinking == "enabled" {
		parts = append(parts, "thinking=enabled", "reasoning_effort="+m.opts.ReasoningEffort)
	} else {
		parts = append(parts, "thinking=disabled")
	}
	calls := "1 вызов"
	if m.twoStep {
		calls = "2 вызова: сначала пишем промпт, потом решаем по нему"
	}
	return strings.Join(parts, " · ") + " · " + calls
}

func cost(model string, u llm.Usage) (float64, bool) {
	p, ok := prices[model]
	if !ok {
		return 0, false
	}
	return float64(u.PromptTokens)/1e6*p[0] + float64(u.CompletionTokens)/1e6*p[1], true
}

func summary(outcomes []outcome, model string, truth, repeat int) {
	fmt.Println()
	fmt.Println(strings.Repeat("═", 72))
	fmt.Printf("СРАВНЕНИЕ · эталон %d · модель %s · повторов %d\n", truth, model, repeat)
	fmt.Println(strings.Repeat("═", 72))
	// The price ratio is read against the plain method, not against whatever
	// happens to be first: with -only C,D,E the first row is C, and a column
	// headed "к A" would then compare everything to the wrong baseline.
	base := baseline(outcomes)
	fmt.Printf("%s %s %s %s %s\n",
		pad("Способ", 26), pad("верных", 9), pad("токенов", 10), pad("цена", 10), "к "+base)

	var baseCost float64
	for _, o := range outcomes {
		ok, tokens, spent, n := 0, 0, 0.0, 0
		for _, a := range o.attempts {
			if a.err != nil {
				continue
			}
			n++
			tokens += a.usage.CompletionTokens
			if c, known := cost(model, a.usage); known {
				spent += c
			}
			if a.correct {
				ok++
			}
		}
		if n == 0 {
			fmt.Printf("%s все прогоны с ошибкой\n", pad(o.method.key+" · "+o.method.title, 26))
			continue
		}
		avgCost := spent / float64(n)
		if o.method.key == base {
			baseCost = avgCost
		}
		ratio := "—"
		if baseCost > 0 {
			ratio = fmt.Sprintf("×%.1f", avgCost/baseCost)
		}
		fmt.Printf("%s %s %s %s %s\n",
			pad(o.method.key+" · "+o.method.title, 26),
			pad(fmt.Sprintf("%d/%d", ok, n), 9),
			pad(fmt.Sprint(tokens/n), 10),
			pad(fmt.Sprintf("$%.4f", avgCost), 10),
			ratio)
	}
	fmt.Println()
	fmt.Println("Точность здесь — не мнение о тексте, а доля совпадений с эталоном.")
	fmt.Println("Способ рассуждения — единственное, что менялось между прогонами.")
}

// baseline picks the plain method when it was run, and the first one otherwise.
func baseline(outcomes []outcome) string {
	for _, o := range outcomes {
		if o.method.key == "A" {
			return o.method.key
		}
	}
	if len(outcomes) > 0 {
		return outcomes[0].method.key
	}
	return "A"
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return strings.Join(parts, ", ")
}

func indent(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = "  │ " + l
	}
	return strings.Join(lines, "\n")
}

// clip keeps a long chain of thought readable on screen without hiding that it
// was cut: reasoning for this task runs to thousands of characters.
func clip(s string, limit int, keepAll bool) string {
	if keepAll || utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit]) + fmt.Sprintf("\n… ещё %d символов (полностью — с -full)", len(runes)-limit)
}

// pad aligns by runes: %-Ns counts bytes, and Cyrillic would break the columns.
func pad(s string, width int) string {
	if n := utf8.RuneCountInString(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}
