package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

// comparison is one pair of rungs on one class. The family is deliberately small.
// All fifteen pairs of six rungs across four scored classes would be sixty tests,
// and at a flat 0.05 threshold roughly three of them would come back "confirmed"
// by chance alone. So only the pairs that answer a question get tested: the three
// tiers the task names, and each step of the local size curve.
type comparison struct {
	task    task
	a, b    rung
	label   string
	pValue  float64
	rateA   float64
	rateB   float64
	usableA int
	usableB int
	holm    bool
}

// share formats a hit rate, or says the cell was never measured. A percentage is a
// claim about observations; with no observations there is nothing to claim.
func share(c cell) string {
	if !c.measured() {
		return "не измерено"
	}
	rate, _, _ := c.rate()
	return fmt.Sprintf("%.0f%%", rate*100)
}

// reasonedCells lists every cell where the reasoning switch did not take.
func reasonedCells(run gridRun) []string {
	var out []string
	for _, t := range run.tasks {
		for _, r := range run.rungs {
			if _, _, _, reasoned, _ := run.cell(t, r).counts(); reasoned > 0 {
				out = append(out, fmt.Sprintf("%s на %s (%d из %d)", t.key, shortID(r.id), reasoned, run.repeat))
			}
		}
	}
	return out
}

func rungByID(id string) (rung, bool) {
	for _, r := range ladder {
		if r.id == id {
			return r, true
		}
	}
	return rung{}, false
}

// family builds the comparisons and corrects them together. Holm runs once across
// every test in the report, not per class: the family-wide error rate is a property
// of the whole report, and correcting each table on its own would leave the promise
// unkept exactly where a reader would trust it most.
func family(run gridRun) []comparison {
	pairs := [][2]string{
		{"qwen3:1.7b", "deepseek-v4-flash"},
		{"deepseek-v4-flash", "deepseek-v4-pro"},
		{"qwen3:1.7b", "deepseek-v4-pro"},
		{"qwen3:1.7b", "qwen3:4b"},
		{"qwen3:4b", "qwen3:8b"},
		{"qwen3:8b", "qwen3:14b"},
	}

	var out []comparison
	for _, t := range run.tasks {
		if t.kind == open {
			continue
		}
		for _, p := range pairs {
			a, okA := rungByID(p[0])
			b, okB := rungByID(p[1])
			if !okA || !okB {
				continue
			}
			ca, cb := run.cell(t, a), run.cell(t, b)
			rateA, correctA, usableA := ca.rate()
			rateB, correctB, usableB := cb.rate()
			if usableA == 0 || usableB == 0 {
				continue
			}
			out = append(out, comparison{
				task: t, a: a, b: b,
				label:   fmt.Sprintf("%s → %s", shortID(a.id), shortID(b.id)),
				pValue:  stats.FisherTwoSided(correctA, usableA-correctA, correctB, usableB-correctB),
				rateA:   rateA,
				rateB:   rateB,
				usableA: usableA,
				usableB: usableB,
			})
		}
	}

	ps := make([]float64, len(out))
	for i, c := range out {
		ps[i] = c.pValue
	}
	for i, ok := range stats.Holm(ps, 0.05) {
		out[i].holm = ok
	}
	return out
}

// plural picks the Russian form. "1 прогонов" in a report header is the kind of
// detail that makes a reader wonder what else was not looked at.
func plural(n int, one, few, many string) string {
	form := many
	switch {
	case n%100 >= 11 && n%100 <= 14:
	case n%10 == 1:
		form = one
	case n%10 >= 2 && n%10 <= 4:
		form = few
	}
	return fmt.Sprintf("%d %s", n, form)
}

func shortID(id string) string {
	return strings.NewReplacer("deepseek-v4-", "ds-", "qwen3:", "q3-").Replace(id)
}

func writeReport(run gridRun, w io.Writer) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }

	p("# День 5 — версии моделей: что показали %s на клетку\n\n", plural(run.repeat, "прогон", "прогона", "прогонов"))
	p("Прогон `go run ./day-05 -grid -repeat %d`, %s–%s, %s.\n",
		run.repeat, run.startedAt.Format("2006-01-02 15:04"), run.finishedAt.Format("15:04"),
		run.finishedAt.Sub(run.startedAt).Round(time.Minute))
	if leaked := reasonedCells(run); len(leaked) == 0 {
		p("Размышление выключено на всех ступенях, и это проверено по ответу, а не по запросу.\n")
	} else {
		p("⚠ **Размышление отключилось не везде.** Выключение проверяется по ответу, и вот где\n")
		p("оно не сработало: %s. Эти клетки с остальными не сравнимы — сравнивались бы\n", strings.Join(leaked, ", "))
		p("одновременно модель и режим.\n")
	}
	p("Локальные ступени — блоками с прогревом, облачные — с чередованием вызовов.\n\n")

	if run.crossedPeakBoundary() {
		p("⚠ **Прогон пересёк границу тарифа поставщика** (peak — будни 01:00–04:00 и\n")
		p("06:00–10:00 UTC). Вызовы по разные стороны границы биллятся по разным ставкам;\n")
		p("каждый посчитан по своей, но сравнивать колонку стоимости между ступенями,\n")
		p("измеренными в разных полосах, нельзя.\n\n")
	}

	writeLadder(run, p)
	writeWindows(run, p)

	all := family(run)
	for _, t := range run.tasks {
		writeClass(run, t, all, p)
	}
	writeSummary(run, p)
}

func writeLadder(run gridRun, p func(string, ...any)) {
	p("## Лесенка\n\n")
	p("Ступень определяется по критериям ведущего: «количество параметров, квантизация,\n")
	p("размер контекстного окна и так далее» [чат #1832], плюс цена, которая «очень сильно\n")
	p("коррелирует» [чат #1834]. Где поставщик не публикует критерий, стоит «—»:\n")
	p("ярлыки flash и pro опираются на цену и контекст, а не на число параметров.\n\n")
	p("| Ступень | Ярлык | Параметры | Квантизация | Контекст | Где |\n")
	p("|---|---|---|---|---|---|\n")
	for _, r := range run.rungs {
		where := "облако, REST"
		if r.local {
			where = "локально, ollama"
		}
		p("| `%s` | %s | %s | %s | %s | %s |\n", r.id, r.tier, r.params, r.quant, r.context, where)
	}
	p("\n")
}

// writeWindows says when each rung was measured. The local rungs run in blocks and
// the cloud ones interleave, so the blocks are not simultaneous — and if the run
// crossed a rate change, this is where a reader sees which side each rung fell on.
func writeWindows(run gridRun, p func(string, ...any)) {
	p("## Когда что мерялось\n\n")
	p("| Ступень | Первый вызов | Последний | Полоса тарифа |\n")
	p("|---|---|---|---|\n")
	for _, r := range run.rungs {
		var first, last time.Time
		for _, t := range run.tasks {
			f, l := run.cell(t, r).window()
			if !f.IsZero() && (first.IsZero() || f.Before(first)) {
				first = f
			}
			if l.After(last) {
				last = l
			}
		}
		if first.IsZero() {
			p("| `%s` | — | — | не мерялась |\n", shortID(r.id))
			continue
		}
		band := "off-peak"
		if llm.IsPeak(first) || llm.IsPeak(last) {
			band = "затронут peak"
		}
		if llm.IsFree(r.id) {
			band = "неприменима — бесплатная"
		}
		p("| `%s` | %s | %s | %s |\n", shortID(r.id),
			first.UTC().Format("15:04:05")+" UTC", last.UTC().Format("15:04:05")+" UTC", band)
	}
	p("\n")
}

func writeClass(run gridRun, t task, all []comparison, p func(string, ...any)) {
	p("## %s · %s\n\n", t.key, t.title)
	p("%s\n\n", t.note)
	if t.kind == exact {
		p("Эталон — `%s`, считает код, не человек.\n\n", t.truth)
	}
	if t.kind == schema {
		p("Проверяется форма ответа, не значение: эталона у значения нет, и выдавать\n")
		p("соблюдение схемы за факт о мире нельзя.\n\n")
	}
	p("Запрос: %s\n\n", oneLine(t.prompt, 200))

	if t.kind == open {
		p("Класс не оценивается — эталона нет. Сырые ответы:\n\n")
		for _, r := range run.rungs {
			c := run.cell(t, r)
			p("**`%s`** (медиана %.1f с, ~%d ток.):\n", r.id, c.medianSeconds(), c.avgOutputTokens())
			seen := map[string]bool{}
			for _, o := range c.usable() {
				line := oneLine(o.raw, 80)
				if seen[line] {
					continue
				}
				seen[line] = true
				p("- %s\n", line)
			}
			p("\n")
		}
		return
	}

	p("| Ступень | Верных | Доля | 95%% Уилсон | Медиана с | 95%% интервал медианы | Самый долгий | Выход ток. | Символов | Ток/с | $/вызов | $/верный |\n")
	p("|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range run.rungs {
		c := run.cell(t, r)
		if !c.measured() {
			// Nothing came back. Printing 0% here would read identically to a rung
			// that answered wrong thirty times, and those are different statements.
			_, _, _, _, failed := c.counts()
			p("| `%s` | — | **не измерено** | — | — | — | — | — | — | — | %s |\n",
				shortID(r.id), fmt.Sprintf("провалов: %d", failed))
			continue
		}
		rate, correct, usable := c.rate()
		lo, hi := stats.Wilson(correct, usable)
		tps, tpsN := c.tokensPerSecond(20)
		tpsCell := "—"
		if tpsN > 0 {
			tpsCell = fmt.Sprintf("%.0f", tps)
			if tpsN < usable {
				tpsCell += fmt.Sprintf(" (по %d)", tpsN)
			}
		}
		secLo, secHi := c.medianSecondsCI()
		secCI := "—"
		if secHi > 0 {
			secCI = fmt.Sprintf("[%.1f, %.1f]", secLo, secHi)
		}
		p("| `%s` | %d/%d | %.0f%% | [%.0f%%, %.0f%%] | %.1f | %s | %.1f | %d | %d | %s | %s | %s |\n",
			shortID(r.id), correct, usable, rate*100, lo*100, hi*100,
			c.medianSeconds(), secCI, c.slowest(), c.avgOutputTokens(), c.avgChars(), tpsCell,
			money(r.id, c.costPerCall), money(r.id, c.costPerCorrect))
	}
	p("\n")

	writeFlags(run, t, p)
	writeComparisons(t, all, p)
}

// money never lets "free by construction", "billed nothing" and "we do not know"
// print the same way. A local rung genuinely costs nothing; a paid rung that came
// back with empty usage was billed something we failed to record, and printing
// plain "$0" for it would read as the former.
func money(model string, f func() (float64, bool)) string {
	v, ok := f()
	switch {
	case llm.IsFree(model):
		return "$0 · локальная"
	case !ok:
		return "неизвестна"
	case v == 0:
		return "$0 · токены не пришли"
	case v < 0.000005:
		return "<$0.00001"
	}
	return fmt.Sprintf("$%.5f", v)
}

func writeFlags(run gridRun, t task, p func(string, ...any)) {
	var lines []string
	for _, r := range run.rungs {
		c := run.cell(t, r)
		_, unparsed, truncated, reasoned, failed := c.counts()
		var parts []string
		if unparsed > 0 {
			parts = append(parts, fmt.Sprintf("не дала ответ в требуемой форме: %d", unparsed))
		}
		if truncated > 0 {
			parts = append(parts, fmt.Sprintf("уперлась в лимит %d токенов: %d", t.maxTokens, truncated))
		}
		if reasoned > 0 {
			parts = append(parts, fmt.Sprintf("**размышление не отключилось: %d — клетка несравнима**", reasoned))
		}
		if failed > 0 {
			parts = append(parts, fmt.Sprintf("вызов не прошёл: %d", failed))
		}
		if len(parts) > 0 {
			lines = append(lines, fmt.Sprintf("- `%s`: %s", shortID(r.id), strings.Join(parts, "; ")))
		}
	}
	if len(lines) == 0 {
		return
	}
	p("Дефекты, которые не являются неверным ответом и потому считаются отдельно:\n\n")
	p("%s\n\n", strings.Join(lines, "\n"))
}

func writeComparisons(t task, all []comparison, p func(string, ...any)) {
	var mine []comparison
	for _, c := range all {
		if c.task.key == t.key {
			mine = append(mine, c)
		}
	}
	if len(mine) == 0 {
		return
	}
	p("| Сравнение | Доля | Доля | p (точный тест Фишера) | Проходит Holm |\n")
	p("|---|---|---|---|---|\n")
	for _, c := range mine {
		verdict := "нет"
		if c.holm {
			verdict = "**да**"
		}
		p("| %s | %.0f%% | %.0f%% | %.3f | %s |\n",
			c.label, c.rateA*100, c.rateB*100, c.pValue, verdict)
	}
	p("\nПорог поправлен на число сравнений процедурой Holm по всему отчёту сразу: тестов\n")
	p("здесь два десятка, и при плоском 0.05 примерно одно «подтверждено» пришло бы\n")
	p("случайно. Не прошедшее сравнение означает «разница не показана», а не «разницы нет».\n\n")
}

func writeSummary(run gridRun, p func(string, ...any)) {
	p("## Итог: что покупается за более сильную модель\n\n")
	p("| Класс | Слабая (q3-1.7b) | Средняя (ds-flash) | Сильная (ds-pro) | $/верный: flash → pro |\n")
	p("|---|---|---|---|---|\n")
	weak, _ := rungByID("qwen3:1.7b")
	mid, _ := rungByID("deepseek-v4-flash")
	strong, _ := rungByID("deepseek-v4-pro")
	for _, t := range run.tasks {
		if t.kind == open {
			continue
		}
		rw := share(run.cell(t, weak))
		rm := share(run.cell(t, mid))
		rs := share(run.cell(t, strong))
		ratio := "—"
		cm, okM := run.cell(t, mid).costPerCorrect()
		cs, okS := run.cell(t, strong).costPerCorrect()
		if okM && okS && cm > 0 {
			ratio = fmt.Sprintf("×%.1f", cs/cm)
		}
		p("| %s · %s | %s | %s | %s | %s |\n", t.key, t.title, rw, rm, rs, ratio)
	}
	p("\n")

	// The slowest and fastest rungs, measured rather than assumed.
	type speed struct {
		id  string
		sec float64
	}
	var speeds []speed
	for _, r := range run.rungs {
		total, n := 0.0, 0
		for _, t := range run.tasks {
			if s := run.cell(t, r).medianSeconds(); s > 0 {
				total += s
				n++
			}
		}
		if n > 0 {
			speeds = append(speeds, speed{shortID(r.id), total / float64(n)})
		}
	}
	sort.Slice(speeds, func(i, j int) bool { return speeds[i].sec < speeds[j].sec })
	p("Средняя медиана по классам, от быстрой ступени к медленной:\n\n")
	for _, s := range speeds {
		p("- `%s` — %.1f с\n", s.id, s.sec)
	}
	p("\n")

	total := 0.0
	for _, t := range run.tasks {
		for _, r := range run.rungs {
			if v, ok := run.cell(t, r).cost(); ok {
				total += v
			}
		}
	}
	p("Весь прогон стоил **$%.4f** — локальные ступени бесплатны по построению, платили\n", total)
	p("только за две облачные.\n\n")
}
