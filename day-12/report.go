package main

// RESULTS.md is generated from the run's JSONL and from nothing else. Every number in
// it is recomputed here from the recorded rows, and a test rebuilds the committed file
// and compares it byte for byte — so a figure in the document cannot drift away from
// the run that produced it.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

const alpha = 0.05

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
	writeAdherence(&b, plan, probes)
	writeConflict(&b, probes)
	writePipeline(&b, probes)
	writeCost(&b, probes)
	writeCriteria(&b, plan)
	return b.String(), nil
}

func split(rows []map[string]any) (plan, complete map[string]any, showcase, probes []map[string]any, err error) {
	for _, r := range rows {
		switch r["kind"] {
		case "plan":
			plan = r
		case "complete":
			complete = r
		case "showcase":
			showcase = append(showcase, r)
		case "probe":
			probes = append(probes, r)
		}
	}
	if plan == nil || complete == nil {
		return nil, nil, nil, nil, fmt.Errorf("в JSONL нет строки плана или завершения — прогон не закончен")
	}
	sort.Slice(probes, func(i, j int) bool { return num(probes[i], "order") < num(probes[j], "order") })
	return plan, complete, showcase, probes, nil
}

// validate refuses to render a run that cannot carry a conclusion. A report is the
// place a number becomes a fact, so a run with a failed call, a moved fixture, a
// mismatched profile in the wire or too many empty answers produces an error here
// rather than a plausible-looking document.
func validate(plan, complete map[string]any, probes []map[string]any) error {
	run := str(plan, "run")
	if boolOf(plan, "pilot") {
		return fmt.Errorf("это пилот, а не прогон: отчёт по нему не строится")
	}
	if str(complete, "run") != run {
		return fmt.Errorf("строка завершения принадлежит прогону %q, а не %q", str(complete, "run"), run)
	}
	if got, want := len(probes), int(num(plan, "cells")); got != want {
		return fmt.Errorf("строк проб %d, а план обещал %d клеток", got, want)
	}
	if got := int(num(complete, "probeRows")); got != len(probes) {
		return fmt.Errorf("завершение насчитало %d строк проб, в файле %d", got, len(probes))
	}
	if errCount := int(num(complete, "errors")); errCount > 0 {
		return fmt.Errorf("в прогоне %d ошибок вызова — отчёт по нему не строится", errCount)
	}
	if before, after := str(plan, "fixtureSha256"), str(complete, "fixtureSha256After"); before != after {
		return fmt.Errorf("фикстура изменилась за прогон: %s → %s", before, after)
	}
	for _, p := range probes {
		if str(p, "run") != run {
			return fmt.Errorf("строка пробы принадлежит прогону %q, а не %q", str(p, "run"), run)
		}
		if list, ok := p["sentViolations"].([]any); ok && len(list) > 0 {
			return fmt.Errorf("в руке %s профиль в запросе разошёлся с рукой: %v", str(p, "arm"), list)
		}
	}
	// The pre-registered usability gate. It is checked here, before a single number is
	// printed: an arm that mostly returned nothing cannot be reported as an arm that
	// mostly disobeyed.
	ceiling := num(plan, "emptyShareCeiling")
	var unusable []string
	for _, armName := range armsIn(probes) {
		cells := filter(probes, func(p map[string]any) bool { return str(p, "arm") == armName })
		empty := count(cells, func(p map[string]any) bool { return str(p, "outcome") == outcomeEmpty })
		if len(cells) > 0 && float64(empty)/float64(len(cells)) > ceiling {
			unusable = append(unusable, fmt.Sprintf("%s (%d из %d)", armName, empty, len(cells)))
		}
	}
	if len(unusable) > 0 {
		return fmt.Errorf("доля пустых ответов выше предрегистрированного потолка %.0f%% в руках: %s — прогон непригоден",
			100*ceiling, strings.Join(unusable, ", "))
	}
	return nil
}

func writeHeader(b *strings.Builder, plan, complete map[string]any, probes []map[string]any, source string) {
	fmt.Fprintf(b, "# День 12 — персонализация: результаты прогона\n\n")
	fmt.Fprintf(b, "Сгенерировано командой `go run ./day-12 -report %s`. Числа ниже не вписаны руками: "+
		"`TestCommittedResultsAreRenderedFromTheCommittedRun` (`day-12/main_test.go`) пересобирает этот файл "+
		"из JSONL и сравнивает побайтно.\n\n", source)

	served := map[string]int{}
	peak := 0
	for _, p := range probes {
		served[str(p, "servedModel")]++
		if boolOf(p, "peak") {
			peak++
		}
	}
	fmt.Fprintf(b, "- Прогон `%s`, начат %s, коммит `%s`, seed `%d`.\n",
		str(plan, "run"), str(plan, "started"), str(plan, "commit"), int64(num(plan, "seed")))
	fmt.Fprintf(b, "- Запрошена модель `%s`; ответили: %s. Вызовов в часы пика: %d из %d.\n",
		str(plan, "model"), servedList(served), peak, len(probes))

	spend, _ := complete["spend"].(map[string]any)
	cached := 0.0
	if spend != nil && num(spend, "promptTokens") > 0 {
		cached = 100 * num(spend, "cachedTokens") / num(spend, "promptTokens")
	}
	fmt.Fprintf(b, "- Расход всего прогона (E0 + пробы + вызовы плана): вызовов %d, вход %d токенов (из кэша %.0f%%), выход %d, $%.6f.\n",
		int(num(spend, "calls")), int(num(spend, "promptTokens")), cached,
		int(num(spend, "completionTokens")), num(spend, "cost"))

	retried := count(probes, func(p map[string]any) bool { return num(p, "attempts") > 1 })
	fmt.Fprintf(b, "- Проверки целостности пройдены: клеток %d по плану, ошибок вызова 0, "+
		"фикстура до и после прогона одна (`%s`), ни в одном запросе профиль не разошёлся с рукой. "+
		"Отчёт по прогону, не прошедшему эти проверки, не строится вовсе.\n",
		int(num(plan, "cells")), str(plan, "fixtureSha256"))
	fmt.Fprintf(b, "- Пустых ответов: %d из %d (потолок пригодности %.0f%%, предрегистрирован до прогона). "+
		"Клеток, решённых со второй попытки: %d.\n",
		int(num(complete, "empty")), len(probes), 100*num(plan, "emptyShareCeiling"), retried)
	if retried > 0 {
		var reasons []string
		for _, p := range probes {
			for _, r := range sliceOf(p, "retryErrors") {
				reasons = append(reasons, fmt.Sprint(r))
			}
		}
		fmt.Fprintf(b, "- Причины первых неудачных попыток: %s.\n", strings.Join(reasons, "; "))
	}
	b.WriteString("\n")
}

func writeShowcase(b *strings.Builder, showcase []map[string]any) {
	if len(showcase) == 0 {
		return
	}
	b.WriteString("## E0. Один вопрос — разные профили\n\n")
	fmt.Fprintf(b, "Вопрос один и тот же: «%s». Меняется только профиль. Ответы приведены целиком.\n\n", questionExplain)
	for _, r := range showcase {
		fmt.Fprintf(b, "### Профиль `%s`\n\n", str(r, "profile"))
		if sent, ok := r["sent"].(map[string]any); ok {
			if block := fmt.Sprint(sent["wire"]); block != "" && block != "<nil>" {
				fmt.Fprintf(b, "Что ушло в запрос:\n\n```text\n%s\n```\n\n", block)
			} else {
				b.WriteString("В запрос не ушло ни байта профиля.\n\n")
			}
		}
		if str(r, "outcome") != outcomeOK {
			fmt.Fprintf(b, "Исход вызова: `%s`.\n\n", str(r, "outcome"))
			continue
		}
		fmt.Fprintf(b, "Ответ:\n\n```text\n%s\n```\n\n", str(r, "answer"))
		if scores, ok := r["scores"].(map[string]any); ok {
			var hit []string
			for _, name := range sortedKeys(scores) {
				if v, _ := scores[name].(bool); v {
					hit = append(hit, "`"+name+"`")
				}
			}
			if len(hit) == 0 {
				b.WriteString("Критерии: ни один не сработал.\n\n")
			} else {
				fmt.Fprintf(b, "Критерии, сработавшие на этом ответе: %s.\n\n", strings.Join(hit, ", "))
			}
		}
	}
}

// cellKey identifies one (arm, criterion) pair of the adherence table.
type cellKey struct{ arm, criterion string }

func writeAdherence(b *strings.Builder, plan map[string]any, probes []map[string]any) {
	b.WriteString("## A. Соблюдение предпочтений\n\n")
	b.WriteString("Один и тот же вопрос во всех руках; меняется только профиль. " +
		"Каждый ответ проверяется независимыми двоичными критериями. Пустые ответы в долю не входят и показаны отдельно.\n\n")

	behaviour := filter(probes, func(p map[string]any) bool { return str(p, "family") == familyBehaviour })
	arms := armsIn(behaviour)
	names := criteriaIn(behaviour)

	passes, totals, empties, cut := tally(behaviour)

	b.WriteString("| Критерий | Рука | Прошло | Доля | 95% интервал Уилсона | Пустых | Оборвано по потолку |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, name := range names {
		for _, armName := range arms {
			k := cellKey{armName, name}
			if totals[k] == 0 {
				continue
			}
			lo, hi := stats.Wilson(passes[k], totals[k])
			fmt.Fprintf(b, "| %s | %s | %d/%d | %.0f%% | [%.0f%%, %.0f%%] | %d | %d |\n",
				name, armName, passes[k], totals[k], 100*float64(passes[k])/float64(totals[k]),
				100*lo, 100*hi, empties[k], cut[k])
		}
	}
	b.WriteString("\n")

	// Every arm with a profile is compared against the no-profile baseline on every
	// criterion they share; Holm corrects the whole family at once.
	type comparison struct {
		criterion, arm string
		p              float64
	}
	var comparisons []comparison
	for _, name := range names {
		base := cellKey{profileNone, name}
		if totals[base] == 0 {
			continue
		}
		for _, armName := range arms {
			if armName == profileNone {
				continue
			}
			k := cellKey{armName, name}
			if totals[k] == 0 {
				continue
			}
			comparisons = append(comparisons, comparison{name, armName, stats.FisherTwoSided(
				passes[k], totals[k]-passes[k], passes[base], totals[base]-passes[base])})
		}
	}
	if len(comparisons) == 0 {
		return
	}
	ps := make([]float64, len(comparisons))
	for i, c := range comparisons {
		ps[i] = c.p
	}
	survives := stats.Holm(ps, alpha)
	fmt.Fprintf(b, "Сравнения каждой руки с рукой `%s`: точный тест Фишера, двусторонний; "+
		"поправка Холма на семейство из %d сравнений, α = %.2f.\n\n", profileNone, len(comparisons), alpha)
	b.WriteString("| Критерий | Сравнение | p | После поправки Холма |\n")
	b.WriteString("|---|---|---|---|\n")
	for i, c := range comparisons {
		verdict := "разница не показана"
		if survives[i] {
			verdict = "разница показана"
		}
		fmt.Fprintf(b, "| %s | %s против %s | %s | %s |\n", c.criterion, c.arm, profileNone, formatP(c.p), verdict)
	}
	b.WriteString("\n«Разница не показана» не означает «разницы нет»: это значит, что при этом N " +
		"её не удалось отличить от случайности.\n\n")
	b.WriteString("**Оборванный ответ читается с оглядкой.** Потолок генерации режет хвост, поэтому " +
		"критерий, который ищет в ответе присутствие чего-то (`context`, `code`, `kotlin`), в оборванной " +
		"клетке мог не увидеть то, что модель написала бы дальше. Критерии на отсутствие и на начало " +
		"ответа (`format`, `english`, `java`) от обрыва не страдают, а `brevity` обрыв может только " +
		"помочь пройти — и там, где он стоит 0%, ответ остался длинным даже обрезанным.\n\n")
	b.WriteString("Проба `control` — отрицательный контроль: арифметика, на которую ни один профиль " +
		"не влияет. Если по ней разница показана, инструмент видит то, чего нет, и остальные строки " +
		"этой таблицы читать нельзя. Проба `context` — маркерная: она видит упоминание обстоятельства, " +
		"а не то, что решение принято из-за него.\n\n")
}

func writeConflict(b *strings.Builder, probes []map[string]any) {
	conflict := filter(probes, func(p map[string]any) bool { return str(p, "family") == familyConflict })
	if len(conflict) == 0 {
		return
	}
	b.WriteString("## C. Конфликт: просьба против ограничения\n\n")
	fmt.Fprintf(b, "Вопрос прямо просит то, что профиль `%s` запрещает: «%s». "+
		"Критерий `java` считает, что ответ просьбу выполнил.\n\n", profileSenior, questionConflict)

	passes, totals, empties, cut := tally(conflict)
	b.WriteString("| Рука | Дал Java или Spring | Доля | 95% интервал Уилсона | Пустых | Оборвано по потолку |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, armName := range armsIn(conflict) {
		k := cellKey{armName, "java"}
		if totals[k] == 0 {
			continue
		}
		lo, hi := stats.Wilson(passes[k], totals[k])
		fmt.Fprintf(b, "| %s | %d/%d | %.0f%% | [%.0f%%, %.0f%%] | %d | %d |\n",
			armName, passes[k], totals[k], 100*float64(passes[k])/float64(totals[k]), 100*lo, 100*hi, empties[k], cut[k])
	}
	withProfile := cellKey{profileSenior, "java"}
	without := cellKey{profileNone, "java"}
	if totals[withProfile] > 0 && totals[without] > 0 {
		p := stats.FisherTwoSided(passes[withProfile], totals[withProfile]-passes[withProfile],
			passes[without], totals[without]-passes[without])
		fmt.Fprintf(b, "\nФишер, двусторонний: p = %s. Рука `%s` здесь — положительный контроль детектора: "+
			"без запрета модель обязана дать Java, и если она этого не делает, детектор сломан, а не модель послушна.\n\n",
			formatP(p), profileNone)
	}
}

func writePipeline(b *strings.Builder, probes []map[string]any) {
	pipeline := filter(probes, func(p map[string]any) bool { return str(p, "family") == familyPipeline })
	if len(pipeline) == 0 {
		return
	}
	b.WriteString("## D. Конвейер профиля: один вызов против плана и ответа\n\n")
	b.WriteString("Две руки различаются одним полем профиля — `pipeline`. Предпочтения в них одинаковые.\n\n")
	b.WriteString("| Рука | Конвейер | Клеток | Вызовов на клетку | Медиана входа | Медиана выхода | Цена руки |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, armName := range armsIn(pipeline) {
		cells := filter(pipeline, func(p map[string]any) bool { return str(p, "arm") == armName })
		if len(cells) == 0 {
			continue
		}
		var prompts, completions []float64
		var cost float64
		calls := 0
		for _, c := range cells {
			u, _ := c["usage"].(map[string]any)
			pu, _ := c["planUsage"].(map[string]any)
			prompts = append(prompts, num(u, "prompt")+num(pu, "prompt"))
			completions = append(completions, num(u, "completion")+num(pu, "completion"))
			cost += num(u, "cost") + num(pu, "cost")
			calls += int(num(c, "attempts") + num(c, "planCalls"))
		}
		fmt.Fprintf(b, "| %s | %s | %d | %.1f | %.0f | %.0f | $%.6f |\n",
			armName, str(cells[0], "pipeline"), len(cells), float64(calls)/float64(len(cells)),
			median(prompts), median(completions), cost)
	}
	b.WriteString("\n")

	passes, totals, _, _ := tally(pipeline)
	b.WriteString("| Критерий | Рука | Прошло | Доля |\n|---|---|---|---|\n")
	for _, name := range criteriaIn(pipeline) {
		for _, armName := range armsIn(pipeline) {
			k := cellKey{armName, name}
			if totals[k] == 0 {
				continue
			}
			fmt.Fprintf(b, "| %s | %s | %d/%d | %.0f%% |\n", name, armName, passes[k], totals[k],
				100*float64(passes[k])/float64(totals[k]))
		}
	}
	b.WriteString("\n")
}

func writeCost(b *strings.Builder, probes []map[string]any) {
	b.WriteString("## Цена персонализации\n\n")
	b.WriteString("Вес профиля — локальная оценка агента до отправки (она завышает, см. день 8). " +
		"Входные и выходные токены — из `usage` поставщика.\n\n")
	b.WriteString("| Семейство | Рука | Вызовов | Медиана веса профиля | Медиана входа | Медиана выхода | Доля кэша | Цена |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, family := range []string{familyBehaviour, familyConflict, familyPipeline} {
		cells := filter(probes, func(p map[string]any) bool { return str(p, "family") == family })
		for _, armName := range armsIn(cells) {
			arm := filter(cells, func(p map[string]any) bool { return str(p, "arm") == armName })
			if len(arm) == 0 {
				continue
			}
			var profileTokens, prompts, completions []float64
			var cost, cached, prompt float64
			calls := 0
			for _, c := range arm {
				u, _ := c["usage"].(map[string]any)
				pu, _ := c["planUsage"].(map[string]any)
				profileTokens = append(profileTokens, num(c, "profileTokens"))
				prompts = append(prompts, num(u, "prompt"))
				completions = append(completions, num(u, "completion"))
				cost += num(u, "cost") + num(pu, "cost")
				cached += num(u, "cached")
				prompt += num(u, "prompt")
				calls += int(num(c, "attempts") + num(c, "planCalls"))
			}
			share := 0.0
			if prompt > 0 {
				share = 100 * cached / prompt
			}
			fmt.Fprintf(b, "| %s | %s | %d | %.0f | %.0f | %.0f | %.0f%% | $%.6f |\n",
				family, armName, calls, median(profileTokens), median(prompts), median(completions), share, cost)
		}
	}
	b.WriteString("\n")
}

func writeCriteria(b *strings.Builder, plan map[string]any) {
	list, ok := plan["criteria"].([]any)
	if !ok || len(list) == 0 {
		return
	}
	b.WriteString("## Приборы: чем именно измеряли\n\n")
	b.WriteString("Каждый критерий — код, а не модель, и у каждого есть фикстуры, на которых он " +
		"обязан сработать и обязан промолчать. `TestCriteriaAreCalibrated` прогоняет их до первого " +
		"живого вызова: порог, который не видит свой объект, выдаёт правдоподобное неверное число.\n\n")
	b.WriteString("| Критерий | Что проверяет | Обязан сработать | Обязан промолчать |\n|---|---|---|---|\n")
	for _, item := range list {
		c, _ := item.(map[string]any)
		fmt.Fprintf(b, "| `%s` | %s | %d фикстур | %d фикстур |\n",
			str(c, "name"), str(c, "what"), len(sliceOf(c, "accept")), len(sliceOf(c, "reject")))
	}
	b.WriteString("\n")
}

// tally counts passes, scored cells and empty answers per (arm, criterion). An empty
// answer is not a failed criterion — it is a cell with no answer to score, and folding
// it into the denominator would quietly turn a provider outage into disobedience.
func tally(probes []map[string]any) (passes, totals, empties, cut map[cellKey]int) {
	passes, totals, empties, cut = map[cellKey]int{}, map[cellKey]int{}, map[cellKey]int{}, map[cellKey]int{}
	for _, p := range probes {
		armName := str(p, "arm")
		if str(p, "outcome") != outcomeOK {
			// Attribute the empty to every criterion this probe would have scored.
			for _, name := range probeCriteria(p) {
				empties[cellKey{armName, name}]++
			}
			continue
		}
		scores, _ := p["scores"].(map[string]any)
		for _, name := range sortedKeys(scores) {
			k := cellKey{armName, name}
			totals[k]++
			if v, _ := scores[name].(bool); v {
				passes[k]++
			}
			if boolOf(p, "truncated") {
				cut[k]++
			}
		}
	}
	return passes, totals, empties, cut
}

// probeCriteria recovers which criteria a cell would have been scored by. A cell with
// no answer has no scores map, so the names come from the probe's own definition.
func probeCriteria(p map[string]any) []string {
	for _, list := range [][]probe{behaviourProbes, conflictProbes, pipelineProbes} {
		for _, def := range list {
			if def.Name == str(p, "probe") && def.Family == str(p, "family") {
				return def.Criteria
			}
		}
	}
	return nil
}

func armsIn(probes []map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range probes {
		if name := str(p, "arm"); !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func criteriaIn(probes []map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range probes {
		for _, name := range probeCriteria(p) {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

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
	case p >= 0.001:
		return fmt.Sprintf("%.3f", p)
	case p == 0 || math.IsNaN(p):
		return "0"
	}
	return fmt.Sprintf("%.1e", p)
}

func servedList(served map[string]int) string {
	var parts []string
	for _, name := range sortedKeys(served) {
		if name == "" {
			name = "(модель не названа)"
		}
		parts = append(parts, fmt.Sprintf("`%s` (%d)", name, served[name]))
	}
	return strings.Join(parts, ", ")
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
	v, _ := m[key].(bool)
	return v
}

func sliceOf(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	s, _ := m[key].([]any)
	return s
}

// definitions is what the showcase page has to agree with: the profiles, the criteria
// and the assembled request. As on days 5-11 the page is checked against this dump
// rather than against a copy of the numbers typed into JavaScript.
type definitions struct {
	System   string           `json:"system"`
	Profiles []profileFixture `json:"profiles"`
	Criteria []criterionRow   `json:"criteria"`
	Examples []dumpedExample  `json:"examples"`
	Rules    []string         `json:"rules"`
}

type dumpedExample struct {
	Profile string          `json:"profile"`
	Inject  []string        `json:"inject"`
	Sent    []dumpedMessage `json:"sent"`
}

type dumpedMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func writeDefinitions(w io.Writer) error {
	defs, err := buildDefinitions()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(defs)
}
