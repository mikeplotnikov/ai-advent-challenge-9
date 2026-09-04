package main

import (
	"context"
	"fmt"
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
