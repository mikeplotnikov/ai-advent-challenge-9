package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// The pilot answers one question and spends as little as possible doing it: which
// classes actually separate the rungs? Day 4 spent 360 paid calls to find that not
// one of its six comparisons reached p < 0.05, because the effect it was chasing
// was smaller than 30 runs can see. Choosing the classes first is the cheap fix.
//
// It runs the three rungs the task requires — weak, medium, strong — and not the
// bonus local sizes: whether a class separates 1.7B from pro is answered by those
// three, and the middle of the curve costs time without changing the answer.

// outcome is one call. Every field here is something a report is allowed to
// claim, and nothing is inferred later from something else.
type outcome struct {
	correct   bool
	parsed    bool // produced an answer in the required form at all
	truncated bool // the token cap ended it, which is not a wrong answer
	reasoned  bool // the reasoning switch did not take: this run is not comparable
	elapsed   time.Duration
	usage     llm.Usage
	at        time.Time
	raw       string
	err       error
}

// cell is one class measured on one rung.
type cell struct {
	rung     rung
	task     task
	outcomes []outcome
}

func (c cell) n() int { return len(c.outcomes) }

func (c cell) counts() (correct, unparsed, truncated, reasoned, failed int) {
	for _, o := range c.outcomes {
		if o.err != nil {
			failed++
			continue
		}
		if o.correct {
			correct++
		}
		if !o.parsed {
			unparsed++
		}
		if o.truncated {
			truncated++
		}
		if o.reasoned {
			reasoned++
		}
	}
	return
}

// rate is the share of correct answers among calls that came back at all. A
// transport failure is not a wrong answer, and counting it as one would let a
// flaky network look like a weak model.
func (c cell) rate() (float64, int, int) {
	correct, _, _, _, failed := c.counts()
	usable := c.n() - failed
	if usable == 0 {
		return 0, 0, 0
	}
	return float64(correct) / float64(usable), correct, usable
}

func (c cell) medianSeconds() float64 {
	var xs []float64
	for _, o := range c.outcomes {
		if o.err == nil {
			xs = append(xs, o.elapsed.Seconds())
		}
	}
	if len(xs) == 0 {
		return 0
	}
	sort.Float64s(xs)
	mid := len(xs) / 2
	if len(xs)%2 == 1 {
		return xs[mid]
	}
	return (xs[mid-1] + xs[mid]) / 2
}

func (c cell) avgOutputTokens() int {
	sum, n := 0, 0
	for _, o := range c.outcomes {
		if o.err == nil {
			sum += o.usage.CompletionTokens
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

func (c cell) cost() (float64, bool) {
	total := 0.0
	known := false
	for _, o := range c.outcomes {
		if o.err != nil {
			continue
		}
		if v, ok := llm.CostAt(c.rung.id, o.usage, o.at); ok {
			total += v
			known = true
		}
	}
	return total, known
}

// runCell sends one class to one rung repeat times, strictly one call at a time.
// Sequential on purpose: local rungs share the same 11.8 GiB of GPU memory, so
// parallel calls would queue inside the server and every second measured would
// belong to the queue rather than to the model.
func runCell(ctx context.Context, client *llm.Client, r rung, t task, repeat int) cell {
	c := cell{rung: r, task: t}
	for i := 0; i < repeat; i++ {
		messages := []llm.Message{
			{Role: "system", Content: t.system},
			{Role: "user", Content: t.prompt},
		}
		started := time.Now()
		answer, err := client.AskWith(ctx, messages, llm.Options{MaxTokens: t.maxTokens})
		o := outcome{elapsed: time.Since(started), at: started, err: err}
		if err == nil {
			o.raw = answer.Content
			o.usage = answer.Usage
			o.truncated = answer.FinishReason == "length"
			o.reasoned = answer.Reasoned()
			if t.kind != open {
				o.correct, o.parsed = t.check(answer.Content)
			} else {
				o.parsed = strings.TrimSpace(answer.Content) != ""
			}
		}
		c.outcomes = append(c.outcomes, o)
	}
	return c
}

// warm loads a local model's weights before anything is timed. The smoke run
// measured qwen3:8b taking 4.14 s to return a 4-token answer, nearly all of it
// load: leaving that in would make the first class of the first rung look slow
// for a reason that has nothing to do with the model.
func warm(ctx context.Context, client *llm.Client, r rung) {
	if !r.local {
		return
	}
	_, _ = client.AskWith(ctx, []llm.Message{{Role: "user", Content: "привет"}},
		llm.Options{MaxTokens: 8})
}

// verdict decides whether a class earned a place in the paid grid. A class every
// rung solves and a class none solves are both useless for comparing rungs — but
// they are useless in different ways, and T0 is expected to be the first kind on
// purpose, so the verdict names which rather than just rejecting.
func verdict(cells []cell) string {
	if len(cells) == 0 {
		return "нет данных"
	}
	lo, hi := 2.0, -1.0
	for _, c := range cells {
		rate, _, usable := c.rate()
		if usable == 0 {
			return "нет пригодных вызовов — класс не оценён"
		}
		lo = min(lo, rate)
		hi = max(hi, rate)
	}
	spread := hi - lo
	switch {
	case lo >= 0.9:
		return fmt.Sprintf("НЕ РАЗДЕЛЯЕТ: справляются все (разброс %.0f п.п.) — это и есть вывод класса", spread*100)
	case hi <= 0.1:
		return fmt.Sprintf("НЕ РАЗДЕЛЯЕТ: не справляется никто (разброс %.0f п.п.) — класс бесполезен для сравнения", spread*100)
	case spread >= 0.4:
		return fmt.Sprintf("РАЗДЕЛЯЕТ ХОРОШО: разброс %.0f п.п. между крайними ступенями", spread*100)
	default:
		return fmt.Sprintf("разделяет слабо: разброс %.0f п.п. — на пилоте из %d прогонов это ещё шум", spread*100, cells[0].n())
	}
}

func runPilot(repeat int, onlyTasks string) error {
	all, err := pickTasks(tasks(), onlyTasks)
	if err != nil {
		return err
	}

	// The three rungs the assignment names. Bonus local sizes join the real grid,
	// not the selection run.
	var rungs []rung
	for _, r := range ladder {
		if r.tier == "слабая" || r.tier == "средняя" || r.tier == "сильная" {
			rungs = append(rungs, r)
		}
	}

	fmt.Printf("ПИЛОТ · %d прогонов на клетку · %d классов × %d ступеней = %d вызовов\n",
		repeat, len(all), len(rungs), repeat*len(all)*len(rungs))
	fmt.Printf("Вопрос пилота: какие классы вообще различают ступени. Отбор — до платной сетки.\n\n")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()

	clients := map[string]*llm.Client{}
	for _, r := range rungs {
		client, err := clientFor(r)
		if err != nil {
			return fmt.Errorf("%s: %w", r.id, err)
		}
		clients[r.id] = client
		warm(ctx, client, r)
	}

	started := time.Now()
	for _, t := range all {
		fmt.Printf("═══ %s · %s\n", t.key, t.title)
		fmt.Printf("    %s\n", t.note)
		if t.kind == exact {
			fmt.Printf("    эталон: %s\n", t.truth)
		}
		fmt.Printf("    запрос: %s\n\n", oneLine(t.prompt, 100))

		var cells []cell
		for _, r := range rungs {
			c := runCell(ctx, clients[r.id], r, t, repeat)
			cells = append(cells, c)
			printCell(c)
		}
		if t.kind != open {
			fmt.Printf("    ▸ %s\n", verdict(cells))
		} else {
			fmt.Printf("    ▸ не оценивается: эталона нет, в сетку идёт как сырые примеры\n")
		}
		fmt.Println()
	}
	fmt.Printf("Пилот занял %s\n", started.Format("15:04")+"–"+time.Now().Format("15:04"))
	return nil
}

func printCell(c cell) {
	correct, unparsed, truncated, reasoned, failed := c.counts()
	rate, _, usable := c.rate()

	line := fmt.Sprintf("    %-20s %-9s", c.rung.id, c.rung.tier)
	if c.task.kind == open {
		line += fmt.Sprintf(" ответов %d/%d", usable, c.n())
	} else {
		line += fmt.Sprintf(" верных %d/%d (%.0f%%)", correct, usable, rate*100)
	}
	line += fmt.Sprintf(" · медиана %.1f с · выход ~%d ток.", c.medianSeconds(), c.avgOutputTokens())
	if cost, known := c.cost(); known && !llm.IsFree(c.rung.id) {
		line += fmt.Sprintf(" · $%.5f", cost)
	} else if llm.IsFree(c.rung.id) {
		line += " · $0"
	}
	fmt.Println(line)

	var flags []string
	if unparsed > 0 {
		flags = append(flags, fmt.Sprintf("без ответа в нужной форме: %d", unparsed))
	}
	if truncated > 0 {
		flags = append(flags, fmt.Sprintf("обрезано лимитом токенов: %d", truncated))
	}
	if reasoned > 0 {
		flags = append(flags, fmt.Sprintf("⚠ РАССУЖДЕНИЕ НЕ ОТКЛЮЧИЛОСЬ: %d", reasoned))
	}
	if failed > 0 {
		flags = append(flags, fmt.Sprintf("вызов не прошёл: %d", failed))
	}
	if len(flags) > 0 {
		fmt.Printf("    %-30s %s\n", "", strings.Join(flags, " · "))
	}

	// One raw sample, so a parser bug cannot masquerade as a weak model. The
	// first wrong answer is more informative than the first right one.
	sample := ""
	for _, o := range c.outcomes {
		if o.err == nil && !o.correct {
			sample = o.raw
			break
		}
	}
	if sample == "" && len(c.outcomes) > 0 {
		sample = c.outcomes[0].raw
	}
	if sample != "" {
		fmt.Printf("    %-30s пример: %s\n", "", oneLine(sample, 110))
	}
	if first := firstError(c); first != "" {
		fmt.Printf("    %-30s ошибка: %s\n", "", oneLine(first, 110))
	}
}

func firstError(c cell) string {
	for _, o := range c.outcomes {
		if o.err != nil {
			return o.err.Error()
		}
	}
	return ""
}
