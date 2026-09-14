package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

type rows struct {
	plan      planRow
	lifecycle []lifecycleRow
	probes    []probeRow
	complete  completeRow
}

func readRows(path string) (rows, error) {
	var out rows
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Bytes()
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &kind); err != nil {
			return out, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		var target any
		switch kind.Kind {
		case "plan":
			target = &out.plan
		case "lifecycle":
			out.lifecycle = append(out.lifecycle, lifecycleRow{})
			target = &out.lifecycle[len(out.lifecycle)-1]
		case "probe":
			out.probes = append(out.probes, probeRow{})
			target = &out.probes[len(out.probes)-1]
		case "complete":
			target = &out.complete
		default:
			return out, fmt.Errorf("%s:%d: неизвестный вид строки %q", path, line, kind.Kind)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return out, fmt.Errorf("%s:%d: %w", path, line, err)
		}
	}
	return out, scanner.Err()
}

// validateRun refuses a file that is not one complete, comparable run. A report built
// from a partial or contaminated run would read exactly like a valid one.
func validateRun(r rows) error {
	switch {
	case r.plan.Kind != "plan":
		return errors.New("нет строки plan")
	case r.plan.Pilot:
		return errors.New("это пилотный прогон — в отчёт не идёт")
	case r.complete.Kind != "complete":
		return errors.New("нет строки complete — прогон не завершён")
	case r.complete.Run != r.plan.Run:
		return errors.New("plan и complete от разных прогонов")
	case len(r.probes) != r.plan.Cells || r.complete.ProbeRows != r.plan.Cells:
		return fmt.Errorf("строк проб %d, по плану %d", len(r.probes), r.plan.Cells)
	case r.complete.Errors > 0:
		return fmt.Errorf("в прогоне %d клеток с ошибкой — N не совпадает с заявленным до прогона", r.complete.Errors)
	case r.complete.FixtureSHA256After != r.plan.FixtureSHA256:
		return errors.New("фикстура изменилась за время прогона — руки видели разные данные")
	case len(r.lifecycle) != 5:
		return fmt.Errorf("контрольных точек E0 %d, ожидалось 5", len(r.lifecycle))
	}
	for _, p := range r.probes {
		if p.Run != r.plan.Run {
			return fmt.Errorf("строка %d от другого прогона", p.Order)
		}
		if len(p.SentViolations) > 0 {
			return fmt.Errorf("клетка %s/%s/%d: в запросе не совпали маркеры %v — слои ушли не так, как задано рукой",
				p.Arm, p.Probe, p.Repeat, p.SentViolations)
		}
	}
	return nil
}

func render(r rows, source string) (string, error) {
	if err := validateRun(r); err != nil {
		return "", err
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	served := map[string]int{}
	peak, retried := 0, 0
	var retryReasons []string
	for _, p := range r.probes {
		served[p.ServedModel]++
		if p.Peak {
			peak++
		}
		if p.Attempts > 1 {
			retried++
			reason := "причина не записана"
			if len(p.RetryErrors) > 0 {
				reason = p.RetryErrors[0]
			}
			retryReasons = append(retryReasons, fmt.Sprintf("%s/%s/%d: %s", p.Arm, p.Probe, p.Repeat, reason))
		}
	}
	var servedList []string
	for _, m := range sortedKeys(served) {
		servedList = append(servedList, fmt.Sprintf("`%s` (%d)", m, served[m]))
	}
	spend := r.complete.Spend
	cache := "нет данных"
	if share, ok := spend.CacheShare(); ok {
		cache = fmt.Sprintf("%.0f%%", share*100)
	}

	w("# День 11 — слои памяти: результаты прогона\n\n")
	w("Сгенерировано командой `go run ./day-11 -report %s`. Числа ниже не вписаны руками: `TestCommittedResultsAreRenderedFromTheCommittedRun` (`day-11/main_test.go`) пересобирает этот файл из JSONL и сравнивает побайтно.\n\n", source)
	w("- Прогон `%s`, начат %s, коммит `%s`, seed `%d`.\n", r.plan.Run, r.plan.Started, short(r.plan.Commit), r.plan.Seed)
	w("- Запрошена модель `%s`; ответили: %s. Вызовов в часы пика: %d из %d.\n", r.plan.Model, strings.Join(servedList, ", "), peak, len(r.probes))
	w("- Расход всего прогона (E0 + пробы): вызовов %d, вход %d токенов (из кэша %s), выход %d, $%.6f.\n",
		spend.Calls, spend.PromptTokens, cache, spend.CompletionTokens, spend.Cost)
	w("- Проверки целостности пройдены: клеток %d по плану, ошибок в итоге 0, фикстура до и после прогона одна (`%s`), ни в одном запросе маркеры слоёв не разошлись с рукой.\n", len(r.probes), short(r.plan.FixtureSHA256))
	if retried == 0 {
		w("- Повторных вызовов не было: каждая клетка решена за один вызов. Пустой ответ модели засчитывается как исход (вердикт `empty`) и не повторяется.\n\n")
	} else {
		w("- Клеток, получивших ответ со второй попытки: %d. Расход первых попыток входит в итог. Причины: %s.\n\n", retried, strings.Join(retryReasons, "; "))
	}

	renderLifecycle(&b, r.lifecycle)
	renderRecall(&b, r)
	renderBehaviour(&b, r)
	renderTokens(&b, r)
	return b.String(), nil
}

func renderLifecycle(b *strings.Builder, life []lifecycleRow) {
	markers := []string{markerShort, markerTask, markerOldTask, markerDecision, markerKnowledge}
	fmt.Fprintf(b, "## E0. Что попадает в каждый слой\n\n")
	fmt.Fprintf(b, "Живой сценарий: два обычных хода (в первом тикет %s), затем явные записи — решение %s, знание %s, задача %s с %s закрыта, задача %s с %s. После каждого шага файлы трёх слоёв сканируются на маркеры.\n\n",
		markerShort, markerDecision, markerKnowledge, taskOther, markerOldTask, taskActive, markerTask)
	fmt.Fprintf(b, "| Контрольная точка | %s | совпало с ожиданием |\n|---|%s---|\n", strings.Join(markers, " | "), strings.Repeat("---|", len(markers)))
	for _, row := range life {
		var cells []string
		for _, m := range markers {
			found := strings.Join(row.Found[m], ", ")
			if found == "" {
				found = "—"
			}
			if strings.Join(row.Found[m], ",") != strings.Join(row.Expected[m], ",") {
				found += " (ожидалось: " + orDash(strings.Join(row.Expected[m], ", ")) + ")"
			}
			cells = append(cells, found)
		}
		fmt.Fprintf(b, "| %s | %s | %s |\n", row.Checkpoint, strings.Join(cells, " | "), yesNo(row.OK))
	}
	longSame, taskSame := true, true
	for i := 1; i < len(life); i++ {
		longSame = longSame && life[i].Files["long"] == life[0].Files["long"]
		if i < len(life)-1 {
			taskSame = taskSame && life[i].Files["working"] == life[0].Files["working"]
		}
	}
	fmt.Fprintf(b, "\nХэш файла долговременного слоя одинаков во всех пяти точках: %s. Хэш рабочего слоя одинаков от записи до новой сессии включительно: %s.\n\n", yesNo(longSame), yesNo(taskSame))
}

func renderRecall(b *strings.Builder, r rows) {
	fmt.Fprintf(b, "## A. Вспоминание: что доходит до ответа\n\n")
	fmt.Fprintf(b, "Фикстура одинакова во всех руках; меняется только, какие слои уходят в запрос. Ответ — JSON `{\"value\": строка или null}`. N = %d на клетку.\n\n", r.plan.RecallRepeats)
	fmt.Fprintf(b, "| Рука | Проба | Ожидалось | hit | correct_null | lost | wrong | fabricated | leak | parse_error | empty | Прошло |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, a := range r.plan.RecallArms {
		for _, p := range r.plan.RecallProbes {
			counts := map[string]int{}
			pass, n := 0, 0
			want := ""
			for _, row := range r.probes {
				if row.Family != familyRecall || row.Arm != a.Name || row.Probe != p.Name {
					continue
				}
				n++
				counts[row.Verdict]++
				if row.Pass {
					pass++
				}
				want = row.Want
			}
			fmt.Fprintf(b, "| %s | %s | %s | %d | %d | %d | %d | %d | %d | %d | %d | %d/%d |\n", a.Name, p.Name, orNull(want),
				counts[verdictHit], counts[verdictCorrectNull], counts[verdictLost], counts[verdictWrong],
				counts[verdictFabricated], counts[verdictLeak], counts[verdictParseError], counts[verdictEmpty], pass, n)
		}
	}
	renderMisses(b, r)
	fmt.Fprintf(b, "\nПробы на вспоминание проваливаются без слоя почти по построению: без него у модели нет кода. Они проверяют проводку и изоляцию (`fabricated`, `leak`), а не «влияние» — его меряет раздел B.\n\n")
}

// renderMisses lists what the model actually returned wherever a recall answer did
// not pass: a count of verdicts says that something went wrong, the values say what.
func renderMisses(b *strings.Builder, r rows) {
	type key struct{ arm, probe, verdict, value string }
	counts := map[key]int{}
	var order []key
	for _, row := range r.probes {
		if row.Family != familyRecall || row.Pass {
			continue
		}
		value := "null"
		if row.Value != nil {
			value = *row.Value
		}
		if row.Verdict == verdictParseError {
			value = row.Answer
		}
		if row.Verdict == verdictEmpty {
			value = "(пустой ответ)"
		}
		k := key{row.Arm, row.Probe, row.Verdict, value}
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
	}
	if len(order) == 0 {
		fmt.Fprintf(b, "\nНепрошедших ответов нет.\n")
		return
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].arm != order[j].arm {
			return order[i].arm < order[j].arm
		}
		if order[i].probe != order[j].probe {
			return order[i].probe < order[j].probe
		}
		return order[i].value < order[j].value
	})
	fmt.Fprintf(b, "\nЧто модель вернула в непрошедших ответах:\n\n| Рука | Проба | Вердикт | Значение | Раз |\n|---|---|---|---|---|\n")
	for _, k := range order {
		fmt.Fprintf(b, "| %s | %s | %s | `%s` | %d |\n", k.arm, k.probe, k.verdict, strings.ReplaceAll(k.value, "|", "\\|"), counts[k])
	}
}

func renderBehaviour(b *strings.Builder, r rows) {
	fmt.Fprintf(b, "## B. Поведение: как долговременный профиль меняет ответ\n\n")
	fmt.Fprintf(b, "В профиле пользователя две записи: «%s» и «%s». Short и working в этой фикстуре пусты, поэтому руки `full` и `no-long` различаются только долговременным слоем. N = %d на клетку.\n\n",
		styleProfile, stackProfile, r.plan.BehaviourRepeats)
	fmt.Fprintf(b, "| Проба | Рука | Прошло | Доля | 95%% интервал Уилсона | Оборвано по потолку | Пустых ответов |\n|---|---|---|---|---|---|---|\n")
	type key struct{ probe, arm string }
	passes, totals := map[key]int{}, map[key]int{}
	for _, p := range r.plan.BehaviourProbes {
		for _, a := range r.plan.BehaviourArms {
			k := key{p.Name, a.Name}
			truncated, empty := 0, 0
			for _, row := range r.probes {
				if row.Family != familyBehaviour || row.Arm != a.Name || row.Probe != p.Name {
					continue
				}
				totals[k]++
				if row.Pass {
					passes[k]++
				}
				if row.Truncated {
					truncated++
				}
				if row.Verdict == verdictEmpty {
					empty++
				}
			}
			lo, hi := stats.Wilson(passes[k], totals[k])
			fmt.Fprintf(b, "| %s | %s | %d/%d | %.0f%% | [%.0f%%, %.0f%%] | %d | %d |\n", p.Name, a.Name, passes[k], totals[k],
				100*float64(passes[k])/float64(totals[k]), 100*lo, 100*hi, truncated, empty)
		}
	}
	type comparison struct {
		probe, against string
		p              float64
	}
	var comps []comparison
	for _, p := range r.plan.BehaviourProbes {
		for _, against := range []string{"no-long", "none"} {
			f, o := key{p.Name, "full"}, key{p.Name, against}
			comps = append(comps, comparison{p.Name, against,
				stats.FisherTwoSided(passes[f], totals[f]-passes[f], passes[o], totals[o]-passes[o])})
		}
	}
	ps := make([]float64, len(comps))
	for i, c := range comps {
		ps[i] = c.p
	}
	survives := stats.Holm(ps, 0.05)
	fmt.Fprintf(b, "\nСравнения `full` против руки без профиля: точный тест Фишера, двусторонний; поправка Холма на семейство из %d сравнений, α = 0.05.\n\n", len(comps))
	fmt.Fprintf(b, "| Проба | Сравнение | p | После поправки Холма |\n|---|---|---|---|\n")
	for i, c := range comps {
		verdict := "разница не показана"
		if survives[i] {
			verdict = "разница показана"
		}
		fmt.Fprintf(b, "| %s | full против %s | %s | %s |\n", c.probe, c.against, formatP(c.p), verdict)
	}
	fmt.Fprintf(b, "\n«Разница не показана» не означает «разницы нет»: это значит, что при N = %d её не удалось отличить от случайности.\n\n", r.plan.BehaviourRepeats)
}

func renderTokens(b *strings.Builder, r rows) {
	fmt.Fprintf(b, "## Токены и цена по рукам\n\n")
	fmt.Fprintf(b, "Входные и выходные токены — из `usage` поставщика. Вес слоёв — локальная оценка агента до отправки (она завышает, см. день 8).\n\n")
	fmt.Fprintf(b, "| Семейство | Рука | Вызовов | Медиана входа | Медиана выхода | Медиана оценки: долговременный | рабочий | Доля кэша | Цена |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, family := range []struct {
		name string
		arms []arm
	}{{familyRecall, r.plan.RecallArms}, {familyBehaviour, r.plan.BehaviourArms}} {
		for _, a := range family.arms {
			var prompt, completion, long, working []int
			cached, missed := 0, 0
			cost := 0.0
			for _, row := range r.probes {
				if row.Family != family.name || row.Arm != a.Name {
					continue
				}
				prompt = append(prompt, row.Usage.Prompt)
				completion = append(completion, row.Usage.Completion)
				long = append(long, row.Memory.LongTermTokens)
				working = append(working, row.Memory.WorkingTokens)
				cached += row.Usage.Cached
				missed += row.Usage.Missed
				cost += row.Usage.Cost
			}
			share := "—"
			if cached+missed > 0 {
				share = fmt.Sprintf("%.0f%%", 100*float64(cached)/float64(cached+missed))
			}
			fmt.Fprintf(b, "| %s | %s | %d | %d | %d | %d | %d | %s | $%.6f |\n", family.name, a.Name, len(prompt),
				median(prompt), median(completion), median(long), median(working), share, cost)
		}
	}
	b.WriteString("\n")
}

func median(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func formatP(p float64) string {
	if p < 0.001 {
		return fmt.Sprintf("%.1e", p)
	}
	return fmt.Sprintf("%.3f", p)
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func yesNo(ok bool) string {
	if ok {
		return "да"
	}
	return "нет"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func orNull(s string) string {
	if s == "" {
		return "null"
	}
	return s
}
