package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

func reportTask(ctx context.Context, w io.Writer, c *llm.Client, t task, cells []cell,
	model string, seed int64, judgePasses int, full bool) {

	fmt.Fprintln(w)
	fmt.Fprintln(w, strings.Repeat("═", 78))
	fmt.Fprintf(w, "%s · %s\n", t.key, t.title)
	fmt.Fprintln(w, strings.Repeat("═", 78))
	fmt.Fprintf(w, "Запрос (одинаковый во всех прогонах): %s\n", t.prompt)
	fmt.Fprintf(w, "Класс: %s\n", t.note)
	if t.kind == exact {
		fmt.Fprintf(w, "Эталон: %s\n", t.truth)
	}

	var main, demo []tally
	for _, cl := range cells {
		got := summarise(cl, model)
		if cl.setting.demo() {
			demo = append(demo, got)
		} else {
			main = append(main, got)
		}
	}

	if t.kind == exact {
		accuracyTable(w, main)
	}
	diversityTable(w, main, seed)

	costTable(w, main)
	if t.kind == open && judgePasses > 0 {
		judgeSection(ctx, w, c, main, model, seed, judgePasses)
	}
	if full || t.kind == open {
		answerDump(w, main, full)
	}
	if len(demo) > 0 {
		demoSection(w, t, demo, seed)
	}
}

func accuracyTable(w io.Writer, tallies []tally) {
	base, ok := baseline(tallies)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "ТОЧНОСТЬ")
	fmt.Fprintf(w, "%s %s %s %s %s %s\n",
		pad("temperature", 16), pad("верных", 9), pad("доля", 7),
		pad("95% интервал", 16), pad("p к базе", 9), "вывод")
	for _, t := range tallies {
		if t.total == 0 {
			continue
		}
		lo, hi := stats.Wilson(t.correct, t.total)
		score := fmt.Sprintf("%d/%d", t.correct, t.total)
		if t.missing > 0 {
			score += "!"
		}
		rate := fmt.Sprintf("%.0f%%", 100*float64(t.correct)/float64(t.total))
		span := fmt.Sprintf("[%.0f%%, %.0f%%]", 100*lo, 100*hi)

		verdict, pCol := "база сравнения", "—"
		if !ok {
			verdict = "базы нет"
		} else if t.setting.label != base.setting.label {
			p := stats.FisherTwoSided(base.correct, base.total-base.correct,
				t.correct, t.total-t.correct)
			pCol = fmt.Sprintf("%.3f", p)
			verdict = "не отличить от базы"
			if p < 0.05 {
				verdict = "разница подтверждена"
			}
		}
		fmt.Fprintf(w, "%s %s %s %s %s %s\n",
			pad(t.setting.label, 16), pad(score, 9), pad(rate, 7),
			pad(span, 16), pad(pCol, 9), verdict)
	}
	if ok {
		fmt.Fprintf(w, "База сравнения — «%s».\n", base.setting.label)
	}
	fmt.Fprintln(w, "«Не отличить» — это не «одинаково»: это значит, что прогонов мало,")
	fmt.Fprintln(w, "чтобы разницу можно было утверждать. Знак «!» — часть прогонов без ответа.")
}

// baseline is temperature 0 when it was run: the day compares the three
// required values, and 0 is the one they are read against. The "parameter not
// sent" column is deliberately not the baseline — it is a fourth fact, not one
// of the three the task asks about.
func baseline(tallies []tally) (tally, bool) {
	for _, t := range tallies {
		if t.setting.temp != nil && *t.setting.temp == 0 {
			return t, true
		}
	}
	return tally{}, false
}

func diversityTable(w io.Writer, tallies []tally, seed int64) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "РАЗНООБРАЗИЕ")
	fmt.Fprintf(w, "%s %s %s %s %s %s\n",
		pad("temperature", 16), pad("различных", 11), pad("повтор", 8),
		pad("дистанция", 11), pad("95% интервал", 16), "слов уник.")
	for _, t := range tallies {
		if len(t.answered) == 0 {
			fmt.Fprintf(w, "%s ни один прогон не дошёл до ответа\n", pad(t.setting.label, 16))
			continue
		}
		distance, hasPairs := meanPairwiseDistance(t.answered)
		distCol, spanCol := "—", "пар нет"
		if hasPairs {
			distCol = fmt.Sprintf("%.3f", distance)
			// Resample the answers and recompute, because the pairwise
			// distances are not independent observations of anything.
			sample := t.answered
			lo, hi := stats.BootstrapCI(len(sample), bootstrapResamples, seed, func(idx []int) float64 {
				picked := make([]string, len(idx))
				for i, j := range idx {
					picked[i] = sample[j]
				}
				v, ok := meanPairwiseDistance(picked)
				if !ok {
					return math.NaN()
				}
				return v
			})
			spanCol = fmt.Sprintf("[%.3f, %.3f]", lo, hi)
		}
		vocab := "—"
		if ratio, ok := vocabularyRatio(t.answered); ok {
			vocab = fmt.Sprintf("%.2f", ratio)
		}
		fmt.Fprintf(w, "%s %s %s %s %s %s\n",
			pad(t.setting.label, 16),
			pad(fmt.Sprintf("%d из %d", distinct(t.answered), len(t.answered)), 11),
			pad(fmt.Sprint(largestRepeat(t.answered)), 8),
			pad(distCol, 11), pad(spanCol, 16), vocab)
	}
	fmt.Fprintln(w, "«различных» — сколько разных ответов среди дошедших до ответа прогонов.")
	fmt.Fprintln(w, "«повтор» — самая большая группа одинаковых ответов; при temperature 0 это")
	fmt.Fprintln(w, "и есть проверка детерминированности: параметра seed у API нет.")
	fmt.Fprintln(w, "«дистанция» — средняя нормированная дистанция редактирования по всем парам:")
	fmt.Fprintln(w, "0 — все ответы совпали, 1 — не совпадает ничего. Интервал — bootstrap.")
	fmt.Fprintln(w, "«слов уник.» — доля уникальных словоформ во всех ответах вместе.")
}

// costTable answers "what did the setting cost", which is the other half of
// every conclusion here: a temperature that buys accuracy is only interesting
// next to what it charged in tokens and seconds.
func costTable(w io.Writer, tallies []tally) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "ТОКЕНЫ, ВРЕМЯ И ЦЕНА · в среднем на прогон, включая прогоны без ответа")
	fmt.Fprintf(w, "%s %s %s %s\n",
		pad("temperature", 16), pad("токенов", 10), pad("секунд", 9), "цена")
	for _, t := range tallies {
		if t.total == 0 {
			continue
		}
		fmt.Fprintf(w, "%s %s %s %s\n",
			pad(t.setting.label, 16),
			pad(fmt.Sprint(t.tokens/t.total), 10),
			pad(fmt.Sprintf("%.1f", t.elapsed.Seconds()/float64(t.total)), 9),
			formatMoney(t.avgCost, t.priced))
	}
}

// formatMoney never lets a real charge print as $0.0000. Day 3 closed this hole
// for an unknown model; on flash the per-run cost is small enough that the
// rounding does it too, and "free" is the one thing it is not.
func formatMoney(v float64, known bool) string {
	switch {
	case !known:
		return "цена неизвестна: модель не в прайс-листе"
	case v == 0:
		return "$0"
	case v < 0.00005:
		return "меньше $0.0001"
	default:
		return fmt.Sprintf("$%.4f", v)
	}
}

func judgeSection(ctx context.Context, w io.Writer, c *llm.Client, tallies []tally,
	model string, seed int64, passes int) {

	perSetting := make([][]string, len(tallies))
	for i, t := range tallies {
		perSetting[i] = t.answered
	}
	board := buildSlate(perSetting, seed)
	if len(board.answers) == 0 {
		return
	}
	runs := runJudge(ctx, c, board, passes)

	fmt.Fprintln(w)
	fmt.Fprintln(w, "ОЦЕНКА СУДЬИ — это мнение модели, а не измерение")
	fmt.Fprintf(w, "%s %s %s %s\n",
		pad("temperature", 16), pad("средняя оценка", 16), pad("охват", 12), "по проходам")

	spent, priced := 0.0, true
	failed := 0
	for _, r := range runs {
		if price, known := llm.Cost(model, r.usage); known {
			spent += price
		} else {
			priced = false
		}
		if r.err != nil {
			failed++
		}
	}
	for i, t := range tallies {
		var means []string
		sum, ok := 0.0, 0
		scored, presented := 0, len(t.answered)
		for _, r := range runs {
			if r.err != nil {
				continue
			}
			mean, covered := meanScore(perSetting[i], board, r.scores)
			if covered == 0 {
				continue
			}
			means = append(means, fmt.Sprintf("%.2f", mean))
			sum += mean
			ok++
			if covered > scored {
				scored = covered
			}
		}
		if ok == 0 {
			fmt.Fprintf(w, "%s судья не дал оценок\n", pad(t.setting.label, 16))
			continue
		}
		fmt.Fprintf(w, "%s %s %s %s\n",
			pad(t.setting.label, 16),
			pad(fmt.Sprintf("%.2f из 5", sum/float64(ok)), 16),
			pad(fmt.Sprintf("%d из %d", scored, presented), 12),
			strings.Join(means, " · "))
	}
	fmt.Fprintf(w, "Судья видел %d различных ответов перемешанными и без ярлыков температуры;\n",
		len(board.answers))
	fmt.Fprintln(w, "одинаковые ответы получают одну оценку, поэтому шум судьи не может")
	fmt.Fprintln(w, "превратиться в разницу между температурами. Судья работал на temperature 0.")
	fmt.Fprintf(w, "Проходов: %d", len(runs))
	if failed > 0 {
		fmt.Fprintf(w, ", из них с ошибкой: %d", failed)
	}
	fmt.Fprintf(w, " · стоимость судьи: %s", formatMoney(spent, priced))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "«Разброс по проходам» — собственный шум судьи. Разница между температурами,")
	fmt.Fprintln(w, "которая меньше этого разброса, ничего не значит.")
}

func answerDump(w io.Writer, tallies []tally, full bool) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "ОТВЕТЫ")
	for _, t := range tallies {
		fmt.Fprintf(w, "  temperature %s:\n", t.setting.label)
		for i, r := range t.runs {
			switch {
			case r.err != nil:
				fmt.Fprintf(w, "    %2d. ошибка: %v\n", i+1, r.err)
			case !r.parsed:
				fmt.Fprintf(w, "    %2d. ответа нет (finish_reason=%s)\n", i+1, r.finish)
			default:
				fmt.Fprintf(w, "    %2d. %s\n", i+1, clip(strings.Join(strings.Fields(r.raw), " "), 110, full))
			}
		}
	}
}

func demoSection(w io.Writer, t task, demo []tally, seed int64) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, strings.Repeat("─", 78))
	fmt.Fprintln(w, "ДЕМОНСТРАЦИЯ: с thinking=enabled температура игнорируется")
	fmt.Fprintln(w, strings.Repeat("─", 78))
	fmt.Fprintln(w, "Документация поставщика: «Thinking mode does not support parameters like")
	fmt.Fprintln(w, "temperature, top_p... Setting these parameters will not cause an error, but")
	fmt.Fprintln(w, "they will have no effect on the model's output.»")
	fmt.Fprintln(w, "— https://api-docs.deepseek.com/guides/thinking_mode (проверено 03.09.2026)")
	fmt.Fprintln(w, "Ошибки в ответ действительно нет — и разницы между 0 и 1.2 тоже быть не должно.")
	fmt.Fprintf(w, "Лимит токенов в демонстрации поднят до %d: рассуждение не влезло бы в лимит\n", tokenCap)
	fmt.Fprintln(w, "творческой задачи, и вместо игнорирования параметра мы увидели бы обрезку.")
	diversityTable(w, demo, seed)
	if t.kind == exact {
		accuracyTable(w, demo)
	}
}

// clip keeps a long answer readable without hiding that it was cut.
func clip(s string, limit int, keepAll bool) string {
	if keepAll || utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit]) + fmt.Sprintf("… ещё %d символов (полностью — с -full)", len(runes)-limit)
}

// pad aligns by runes: %-Ns counts bytes, and Cyrillic would break the columns.
func pad(s string, width int) string {
	if n := utf8.RuneCountInString(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}
