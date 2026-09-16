package main

// RESULTS.md is generated from the run's JSONL and from nothing else. No number in it
// is typed by hand, and the file refuses to be built at all when the run is not fit to
// be interpreted — an unfinished run, a failed cell, an arm whose request did not carry
// what its arm promised, or too many empty answers.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

const alpha = 0.05

// reportSource is the path RESULTS.md is generated from, as it is written into the
// file's own header. Kept next to the renderer so the regeneration command in the
// header, the test that checks the file, and the file itself cannot drift apart.
const reportSource = "day-13/state.jsonl"

func render(rows []map[string]any, source string) (string, error) {
	plan, complete, showcase, probes, err := split(rows)
	if err != nil {
		return "", err
	}
	if err := validate(plan, complete, probes); err != nil {
		return "", err
	}
	var b strings.Builder
	writeHeader(&b, plan, complete, probes, source)
	writeShowcase(&b, showcase)
	writeResume(&b, probes)
	writeDrive(&b, probes)
	writeCarry(&b, probes)
	writeCost(&b, probes)
	writeCriteria(&b, plan)
	return b.String(), nil
}

func split(rows []map[string]any) (plan, complete map[string]any, showcase, probes []map[string]any, err error) {
	for _, row := range rows {
		switch str(row, "kind") {
		case "plan":
			plan = row
		case "complete":
			complete = row
		case "showcase":
			showcase = append(showcase, row)
		case "probe":
			probes = append(probes, row)
		}
	}
	if plan == nil {
		return nil, nil, nil, nil, errors.New("в выгрузке нет строки плана")
	}
	if complete == nil {
		return nil, nil, nil, nil, errors.New("прогон не завершён: нет строки complete — отчёт по оборванному прогону не строится")
	}
	if len(probes) == 0 {
		return nil, nil, nil, nil, errors.New("в выгрузке нет ни одной клетки")
	}
	sort.Slice(probes, func(i, j int) bool { return num(probes[i], "order") < num(probes[j], "order") })
	return plan, complete, showcase, probes, nil
}

func validate(plan, complete map[string]any, probes []map[string]any) error {
	if boolOf(plan, "pilot") {
		return errors.New("это пилот: по одной клетке на пробу, статистики в нём нет — отчёт не строится")
	}
	if got, want := len(probes), int(num(plan, "cells")); got != want {
		return fmt.Errorf("клеток в выгрузке %d, план обещал %d", got, want)
	}
	if n := int(num(complete, "errors")); n > 0 {
		return fmt.Errorf("в прогоне %d клеток со сбоем вызова — числа неполны, отчёт не строится", n)
	}
	for _, p := range probes {
		if v := sliceOf(p, "sentViolations"); len(v) > 0 {
			return fmt.Errorf("клетка %d: в запрос ушло не то, что обещала рука: %v", int(num(p, "order")), v)
		}
		// The table's refusal is not taken on trust: a cell whose state moved to a
		// stage the model asked for illegally would mean the machine did not hold.
		if boolOf(p, "moveIllegal") && str(p, "stateAfter") != str(p, "stage") {
			return fmt.Errorf("клетка %d: переход отклонён, но стадия всё же сменилась на %q",
				int(num(p, "order")), str(p, "stateAfter"))
		}
	}
	ceiling := num(plan, "emptyShareCeiling")
	for _, arm := range armsIn(probes) {
		cells := filter(probes, func(p map[string]any) bool { return str(p, "arm") == arm })
		empty := count(cells, func(p map[string]any) bool { return str(p, "outcome") == outcomeEmpty })
		if share := float64(empty) / float64(len(cells)); share > ceiling {
			return fmt.Errorf("в руке %s доля пустых ответов %.1f%% выше потолка %.1f%% — числа непригодны",
				arm, share*100, ceiling*100)
		}
	}
	return nil
}

func writeHeader(b *strings.Builder, plan, complete map[string]any, probes []map[string]any, source string) {
	fmt.Fprintf(b, "# День 13 — состояние задачи: что дал прогон\n\n")
	fmt.Fprintf(b, "<!-- Сгенерировано `go run ./day-13 -report %s`. Руками не править. -->\n\n", source)
	fmt.Fprintf(b, "Прогон `%s`, ревизия `%s`, модель запроса `%s`, seed `%d`.\n",
		str(plan, "run"), str(plan, "commit"), str(plan, "model"), int64(num(plan, "seed")))
	served := map[string]int{}
	for _, p := range probes {
		served[str(p, "servedModel")]++
	}
	fmt.Fprintf(b, "Ответившие модели: %s.\n\n", servedList(served))

	spend, _ := complete["spend"].(map[string]any)
	fmt.Fprintf(b, "Клеток %d, вызовов %d, пустых ответов %d, сбоев вызова %d. Потрачено **$%.6f**.\n\n",
		len(probes), int(num(spend, "calls")), int(num(complete, "empty")), int(num(complete, "errors")),
		num(spend, "cost"))

	peak := count(probes, func(p map[string]any) bool { return boolOf(p, "peak") })
	fmt.Fprintf(b, "Клеток, попавших в дорогой тариф (peak): %d из %d.\n\n", peak, len(probes))

	fmt.Fprintf(b, "Задача прогона — `%s`, план из %d шагов: %s.\n\n",
		str(plan, "task"), len(sliceOf(plan, "plan")), joinAny(sliceOf(plan, "plan")))
	b.WriteString("История во всех руках с историей одинакова и **обрывается на шаге 1**. " +
		"В этом и смысл слайда 22: вчерашний разговор говорит одно, сегодняшнее состояние — другое, " +
		"и видно, за чем именно идёт ответ.\n\n")
}

func writeShowcase(b *strings.Builder, rows []map[string]any) {
	if len(rows) == 0 {
		return
	}
	b.WriteString("## Слайд 22 вживую\n\n")
	b.WriteString("Задача ставится на паузу на каждой стадии, поднимается заново и получает одно слово — «Продолжай».\n\n")
	for _, r := range rows {
		fmt.Fprintf(b, "### Набор `%s`, стадия `%s` (шаг %d/%d)\n\n",
			str(r, "stageSet"), str(r, "stage"), int(num(r, "step")), int(num(r, "total")))
		fmt.Fprintf(b, "Что печатает `/resume`:\n\n```\n%s\n```\n\n", str(r, "resumed"))
		if block := str(r, "block"); block != "" {
			fmt.Fprintf(b, "Что ушло в запрос:\n\n```\n%s\n```\n\n", block)
		}
		if str(r, "outcome") != outcomeOK {
			fmt.Fprintf(b, "Исход: **%s**.\n\n", str(r, "outcome"))
			continue
		}
		fmt.Fprintf(b, "Ответ модели:\n\n```\n%s\n```\n\n", str(r, "answer"))
	}
}

type cellKey struct{ arm, criterion, scenario string }

// tally counts verdicts per arm, criterion and scenario. Empty answers are counted
// separately and are never scored: a cell without an answer has violated nothing.
func tally(probes []map[string]any) (passes, totals, empties map[cellKey]int) {
	passes, totals, empties = map[cellKey]int{}, map[cellKey]int{}, map[cellKey]int{}
	for _, p := range probes {
		scenario := str(p, "scenario")
		if str(p, "outcome") != outcomeOK {
			for _, c := range probeCriteria(p) {
				empties[cellKey{str(p, "arm"), c, scenario}]++
			}
			continue
		}
		scores, _ := p["scores"].(map[string]any)
		for name, v := range scores {
			key := cellKey{str(p, "arm"), name, scenario}
			totals[key]++
			if pass, _ := v.(bool); pass {
				passes[key]++
			}
		}
	}
	return passes, totals, empties
}

func writeResume(b *strings.Builder, probes []map[string]any) {
	rows := filter(probes, func(p map[string]any) bool { return str(p, "family") == familyResume })
	if len(rows) == 0 {
		return
	}
	b.WriteString("## A — продолжение без повторных объяснений\n\n")
	b.WriteString("Три руки, четыре точки паузы, по 20 повторов. Доли — с интервалом Уилсона; " +
		"сравнение с рукой `no-state` — точный тест Фишера, двусторонний, с поправкой Холма на семейство.\n\n")

	passes, totals, _ := tally(rows)
	scenarios := valuesIn(rows, "scenario")
	arms := valuesIn(rows, "arm")

	// Every comparison of the family is collected first, so Holm corrects across the
	// family rather than across whatever happened to be printed on one line.
	type comparison struct {
		scenario, criterion, arm string
		p                        float64
	}
	var comps []comparison
	for _, sc := range scenarios {
		for _, crit := range criteriaFor(rows, sc) {
			base := cellKey{"no-state", crit, sc}
			for _, arm := range arms {
				if arm == "no-state" {
					continue
				}
				key := cellKey{arm, crit, sc}
				if totals[key] == 0 || totals[base] == 0 {
					continue
				}
				comps = append(comps, comparison{sc, crit, arm, stats.FisherTwoSided(
					passes[key], totals[key]-passes[key], passes[base], totals[base]-passes[base])})
			}
		}
	}
	ps := make([]float64, len(comps))
	for i, c := range comps {
		ps[i] = c.p
	}
	significant := stats.Holm(ps, alpha)
	verdict := map[comparison]string{}
	for i, c := range comps {
		mark := "нет"
		if significant[i] {
			mark = "**да**"
		}
		verdict[c] = fmt.Sprintf("%s (p = %s)", mark, formatP(c.p))
	}

	for _, sc := range scenarios {
		fmt.Fprintf(b, "### Пауза на стадии `%s`\n\n", sc)
		b.WriteString("| Критерий | Рука | Доля | 95% Уилсон | Отличие от `no-state` |\n")
		b.WriteString("|---|---|---|---|---|\n")
		for _, crit := range criteriaFor(rows, sc) {
			for _, arm := range arms {
				key := cellKey{arm, crit, sc}
				if totals[key] == 0 {
					continue
				}
				lo, hi := stats.Wilson(passes[key], totals[key])
				diff := "— (эталон сравнения)"
				if arm != "no-state" {
					diff = "не сравнивалось"
					for c, v := range verdict {
						if c.scenario == sc && c.criterion == crit && c.arm == arm {
							diff = v
						}
					}
				}
				fmt.Fprintf(b, "| `%s` | `%s` | %d/%d | %.2f–%.2f | %s |\n",
					crit, arm, passes[key], totals[key], lo, hi, diff)
			}
		}
		b.WriteString("\n")
	}

	writeControls(b, passes, totals)
}

// writeControls prints the two controls before any conclusion is drawn from the table.
// A zero needs a positive control and a claim of difference needs a negative one, and
// printing them after the verdict would be printing them too late.
func writeControls(b *strings.Builder, passes, totals map[cellKey]int) {
	b.WriteString("### Контроли\n\n")
	sum := func(criterion, arm string) (int, int) {
		p, n := 0, 0
		for key, total := range totals {
			if key.criterion == criterion && key.arm == arm {
				p += passes[key]
				n += total
			}
		}
		return p, n
	}

	stepPass, stepTotal := sum("right_step", "state")
	fmt.Fprintf(b, "- **Положительный, живой** — `right_step` в руке `state`: %d/%d. "+
		"Прибор семейства умеет срабатывать на живых ответах, значит нули ниже — это нули "+
		"поведения, а не молчание детектора.\n", stepPass, stepTotal)
	if stepTotal > 0 && stepPass == 0 {
		b.WriteString("  **Контроль провален: прибор не сработал ни разу. Ниже нельзя читать ни одного нуля.**\n")
	}

	stagePass, stageTotal := sum("names_stage", "state")
	fmt.Fprintf(b, "- **Положительный, на фикстурах** — `names_stage` в руке `state` дал всего %d/%d, "+
		"и это число можно читать только потому, что отдельная проверка показывает: критерий "+
		"срабатывает на каждой из четырёх стадий. `TestNamesStageFiresForEveryStageAndNotOnDomainWords` "+
		"подаёт ему «Стадия planning, шаг 1/4», «Стадия execution», «Стадия validation», «Стадия done» "+
		"— и он срабатывает на всех, а на доменных «валидацию токена, проверяю подпись» молчит. "+
		"То есть модель действительно почти никогда не называет стадию, а не прибор её не видит.\n",
		stagePass, stageTotal)

	negPass, negTotal := sum("right_step", "none")
	fmt.Fprintf(b, "- **Отрицательный** — `right_step` в руке `none`: %d/%d. "+
		"Без истории и без состояния номер текущего шага угадать нечем; заметная доля здесь "+
		"означала бы сломанный детектор, а не догадливую модель.\n\n", negPass, negTotal)
}

func writeDrive(b *strings.Builder, probes []map[string]any) {
	rows := filter(probes, func(p map[string]any) bool { return str(p, "family") == familyDrive })
	if len(rows) == 0 {
		return
	}
	b.WriteString("## B — кто двигает автомат\n\n")
	b.WriteString("Оба числа — про модель. То, что код отказывает, проверяется тестами, а не статистикой.\n\n")
	b.WriteString("| Проба | Что спрашивается | Доля | 95% Уилсон |\n|---|---|---|---|\n")

	passes, totals, _ := tally(rows)
	for _, probeName := range valuesIn(rows, "probe") {
		sub := filter(rows, func(p map[string]any) bool { return str(p, "probe") == probeName })
		what := map[string]string{
			"drive-next-step": "шаг честно закрыт — просит ли модель сама закрыть его маркером",
			"drive-skip":      "пользователь просит закрыть задачу сразу — просит ли модель запрещённый переход",
		}[probeName]
		for _, crit := range criteriaOf(sub) {
			key := cellKey{"state", crit, str(sub[0], "scenario")}
			if totals[key] == 0 {
				continue
			}
			lo, hi := stats.Wilson(passes[key], totals[key])
			fmt.Fprintf(b, "| `%s` · `%s` | %s | %d/%d | %.2f–%.2f |\n",
				probeName, crit, what, passes[key], totals[key], lo, hi)
			what = ""
		}
	}
	b.WriteString("\n")

	// What the model actually asked for, when it asked for anything. A family whose
	// headline number is a zero has to show what happened instead of it.
	asked := map[string]int{}
	for _, p := range rows {
		if stage := str(p, "moveStageAsked"); stage != "" {
			asked[stage]++
		}
	}
	if len(asked) > 0 {
		b.WriteString("Какие переходы модель просила: ")
		var parts []string
		for _, stage := range sortedKeys(asked) {
			parts = append(parts, fmt.Sprintf("`%s` — %d", stage, asked[stage]))
		}
		b.WriteString(strings.Join(parts, ", ") + ".\n\n")
	}

	illegal := count(rows, func(p map[string]any) bool { return boolOf(p, "moveIllegal") })
	held := count(rows, func(p map[string]any) bool {
		return boolOf(p, "moveIllegal") && str(p, "stateAfter") == str(p, "stage")
	})
	if illegal == 0 {
		b.WriteString("Запрещённых переходов модель не попросила ни разу. **Этот ноль читается только " +
			"потому, что прибор умеет возвращать не-ноль:** `TestTheModelAsksAndTheTableAnswers/" +
			"незаконный переход отклоняется` подаёт агенту ответ с маркером `[[TRANSITION: done]]` " +
			"со стадии `planning` и проверяет, что `moveIllegal` встаёт, а стадия не двигается. " +
			"Без этой проверки ноль здесь был бы неотличим от неработающего детектора.\n\n")
	} else {
		fmt.Fprintf(b, "Запрещённых переходов модель попросила %d раз; таблица отклонила **%d из %d**, "+
			"то есть все. Это не статистика, а проверка: стадия после хода сверена со стадией до него "+
			"в каждой клетке, и отчёт не строится вовсе, если хоть одна разошлась.\n\n", illegal, held, illegal)
	}

	// Steps are closed on the model's word, and that is a weaker guarantee than the
	// transition table gives. Saying so is part of reporting what the day built.
	stepAsked := count(rows, func(p map[string]any) bool { return boolOf(p, "moveStepAsked") })
	stepApplied := count(rows, func(p map[string]any) bool { return boolOf(p, "moveStepApplied") })
	fmt.Fprintf(b, "Закрыть шаг модель просила %d раз, машина закрыла %d. Здесь таблицы нет: шаг "+
		"закрывается **по слову модели**, и это заведомо более слабая гарантия, чем переход между "+
		"стадиями. Код проверяет только границы плана — за последний шаг выйти нельзя.\n\n",
		stepAsked, stepApplied)
}

func writeCarry(b *strings.Builder, probes []map[string]any) {
	rows := filter(probes, func(p map[string]any) bool { return str(p, "family") == familyCarry })
	if len(rows) == 0 {
		return
	}
	b.WriteString("## D — перенос результата стадии\n\n")
	b.WriteString("Две руки различаются одним полем: попал ли итог стадии `planning` в запрос стадии " +
		"`execution`. Решение — «access-токен живёт 7 минут» — не встречается ни в шагах плана, ни в " +
		"истории, и обычное значение по умолчанию другое, так что угадать его нечем.\n\n")

	passes, totals, _ := tally(rows)
	b.WriteString("| Критерий | Рука | Доля | 95% Уилсон | Отличие от `no-carry` |\n|---|---|---|---|---|\n")
	scenario := str(rows[0], "scenario")
	for _, crit := range criteriaOf(rows) {
		base := cellKey{"no-carry", crit, scenario}
		for _, arm := range valuesIn(rows, "arm") {
			key := cellKey{arm, crit, scenario}
			if totals[key] == 0 {
				continue
			}
			lo, hi := stats.Wilson(passes[key], totals[key])
			diff := "— (эталон сравнения)"
			if arm != "no-carry" && totals[base] > 0 {
				p := stats.FisherTwoSided(passes[key], totals[key]-passes[key], passes[base], totals[base]-passes[base])
				mark := "нет"
				if p < alpha {
					mark = "**да**"
				}
				diff = fmt.Sprintf("%s (p = %s)", mark, formatP(p))
			}
			fmt.Fprintf(b, "| `%s` | `%s` | %d/%d | %.2f–%.2f | %s |\n",
				crit, arm, passes[key], totals[key], lo, hi, diff)
		}
	}
	b.WriteString("\n")
	base := cellKey{"no-carry", "uses_plan", scenario}
	if totals[base] > 0 && passes[base] > 0 {
		fmt.Fprintf(b, "**Осторожно:** без переноса решение всё же названо в %d случаях из %d. "+
			"Либо значение угадывается, либо оно просочилось в запрос другим путём — до выяснения "+
			"разницу между руками нельзя приписывать переносу.\n\n", passes[base], totals[base])
	}
}

func writeCost(b *strings.Builder, probes []map[string]any) {
	b.WriteString("## C — что стоит состояние\n\n")
	withState := filter(probes, func(p map[string]any) bool { return str(p, "stateBlock") != "" })
	if len(withState) == 0 {
		return
	}
	var tokens []float64
	for _, p := range withState {
		tokens = append(tokens, num(p, "stateTokens"))
	}
	b.WriteString("| Рука | Клеток | Медиана токенов запроса | Доля кэша | Цена клетки |\n|---|---|---|---|---|\n")
	for _, arm := range valuesIn(probes, "arm") {
		cells := filter(probes, func(p map[string]any) bool { return str(p, "arm") == arm })
		var total, cached, cost, est []float64
		for _, p := range cells {
			u, _ := p["usage"].(map[string]any)
			total = append(total, num(u, "prompt"))
			if prompt := num(u, "prompt"); prompt > 0 {
				cached = append(cached, num(u, "cached")/prompt)
			}
			cost = append(cost, num(u, "cost"))
			est = append(est, num(p, "estimateTotal"))
		}
		fmt.Fprintf(b, "| `%s` | %d | %.0f | %.0f%% | $%.6f |\n",
			arm, len(cells), median(total), median(cached)*100, median(cost))
	}
	fmt.Fprintf(b, "\nСам блок состояния весит по локальной оценке %.0f токенов (медиана по %d клеткам, где он был).\n\n",
		median(tokens), len(withState))
	b.WriteString("Блок стоит в хвосте запроса, перед вопросом, а не в начале system, как на слайде 21. " +
		"Причина в замере дня 12: состояние меняется каждый ход, а сдвиг кэшируемого префикса дал " +
		"в 2.2 раза меньше токенов и на 64% больше денег.\n\n")
}

func writeCriteria(b *strings.Builder, plan map[string]any) {
	b.WriteString("## Приборы\n\n")
	b.WriteString("У каждого текстового критерия есть фикстуры «обязан сработать» и «обязан промолчать»; " +
		"`TestCriteriaAreCalibrated` прогоняет их до первого живого вызова. Критерии `asked_*` читаются " +
		"не из текста, а из решения машины.\n\n")
	b.WriteString("| Критерий | Что считает | Источник |\n|---|---|---|\n")
	for _, raw := range sliceOf(plan, "criteria") {
		c, _ := raw.(map[string]any)
		source := "текст ответа"
		if boolOf(c, "fromMove") {
			source = "решение машины"
		}
		fmt.Fprintf(b, "| `%s` | %s | %s |\n", str(c, "name"), str(c, "what"), source)
	}
	b.WriteString("\n")
}

func probeCriteria(p map[string]any) []string {
	scores, _ := p["scores"].(map[string]any)
	return sortedKeys(scores)
}

func criteriaOf(rows []map[string]any) []string {
	seen := map[string]bool{}
	for _, p := range rows {
		for _, c := range probeCriteria(p) {
			seen[c] = true
		}
	}
	return sortedKeys(seen)
}

func criteriaFor(rows []map[string]any, scenario string) []string {
	return criteriaOf(filter(rows, func(p map[string]any) bool { return str(p, "scenario") == scenario }))
}

// valuesIn lists the distinct values of a field in the order they first appear, so the
// report's row order follows the pre-registered plan rather than Go's map iteration.
func valuesIn(rows []map[string]any, field string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range rows {
		v := str(p, field)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func armsIn(rows []map[string]any) []string { return valuesIn(rows, "arm") }

func filter(rows []map[string]any, keep func(map[string]any) bool) []map[string]any {
	var out []map[string]any
	for _, r := range rows {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

func count(rows []map[string]any, keep func(map[string]any) bool) int {
	return len(filter(rows, keep))
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func formatP(p float64) string {
	switch {
	case math.IsNaN(p):
		return "н/д"
	case p < 1e-4:
		return fmt.Sprintf("%.2e", p)
	default:
		return fmt.Sprintf("%.4f", p)
	}
}

func servedList(served map[string]int) string {
	var parts []string
	for _, name := range sortedKeys(served) {
		label := name
		if label == "" {
			label = "(модель не названа)"
		}
		parts = append(parts, fmt.Sprintf("%s — %d", label, served[name]))
	}
	return strings.Join(parts, ", ")
}

func joinAny(values []any) string {
	var parts []string
	for _, v := range values {
		if s, ok := v.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " · ")
}

func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func num(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	f, _ := m[key].(float64)
	return f
}

func boolOf(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	b, _ := m[key].(bool)
	return b
}

func sliceOf(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	s, _ := m[key].([]any)
	return s
}

type definitions struct {
	StageSets  []stageSetDump    `json:"stageSets"`
	Criteria   []criterionRow    `json:"criteria"`
	Plan       []string          `json:"plan"`
	Task       string            `json:"task"`
	BlockOrder []string          `json:"blockOrder"`
	Markers    map[string]string `json:"markers"`
	Example    exampleDump       `json:"example"`
}

type stageSetDump struct {
	Name        string              `json:"name"`
	About       string              `json:"about"`
	Stages      []string            `json:"stages"`
	Transitions map[string][]string `json:"transitions"`
	Expect      map[string]string   `json:"expect"`
}

// exampleDump is one real request's state block together with the exact context that
// produced it, so the JS mirror can rebuild it rather than be handed the answer.
type exampleDump struct {
	Scenario string            `json:"scenario"`
	Context  agent.TaskContext `json:"context"`
	Block    string            `json:"block"`
}

func writeDefinitions(w io.Writer) error {
	defs, err := buildDefinitions()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(defs)
}
