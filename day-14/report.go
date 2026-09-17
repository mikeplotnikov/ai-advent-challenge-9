package main

// RESULTS.md is built from the journal and from nothing else. The rule is day 13's and
// it is enforced by a test: a number typed into the document by hand is a number that
// cannot be recomputed, and by the next day nobody remembers which ones were typed.
//
// -rescore re-derives every verdict from the stored answers without calling the model
// again. It exists because a detector gets fixed after a run more often than a run gets
// repeated, and re-running to correct a criterion would spend money to change a number
// that the stored text already determines.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

const generatedHeader = "<!-- Сгенерировано `go run ./day-14 -report day-14/cells.jsonl`. Руками не править. -->"

func readRows(path string) ([]cellRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []cellRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row cellRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s: ни одной строки", path)
	}
	return rows, nil
}

// rescoreRows re-derives the verdicts from the stored answers. It touches nothing the
// model produced — only what we concluded about it.
func rescoreRows(rows []cellRow, rules []agent.Invariant) []cellRow {
	out := make([]cellRow, 0, len(rows))
	for _, r := range rows {
		if r.Outcome == outcomeOK {
			scoreRow(&r, rules)
		}
		out = append(out, r)
	}
	return out
}

type cellStats struct {
	n         int
	empty     int
	transport int
	// The classes of the model's OWN first answer. They are counted apart because the
	// machine check cannot tell a proposal from a quotation of the rule, and folding
	// "refused, and named what it refused" into "violated" would report the refusal as
	// the failure.
	firstComplied int
	firstClean    int
	firstRefused  int
	firstMixed    int

	deliveredBad int // a violation reached the person in the model's own words
	deliveredMix int // delivered, declined, and still naming something banned
	refusedProg  int // the program refused and delivered its template instead
	warned       int // delivered with a warning because the answer itself declined
	retried      int
	fixedByRetry int
	judgeCalls   int
	calls        int
	cost         float64
	judgeCost    float64
	retryCost    float64
	rubric       map[string]int
	rubricN      int
}

// firstBad is the model breaking a rule where a banned name cannot be anything but a
// proposal. "mixed" is deliberately NOT added in: it is a suspicion the instrument
// cannot resolve, and it is reported in its own column.
func (c cellStats) firstBad() int { return c.firstComplied }

func (c *cellStats) add(r cellRow) {
	c.n++
	switch r.Outcome {
	case outcomeEmpty:
		c.empty++
		return
	case outcomeTransport:
		c.transport++
		return
	}
	c.calls += r.Calls
	c.cost += r.Cost
	c.judgeCost += r.JudgeCost
	c.retryCost += r.RetryCost
	c.judgeCalls += r.JudgeCalls
	switch r.FirstClass {
	case classComplied:
		c.firstComplied++
	case classClean:
		c.firstClean++
	case classRefused:
		c.firstRefused++
	case classMixed:
		c.firstMixed++
	}
	switch r.DeliveredClass {
	case classComplied:
		c.deliveredBad++
	case classMixed:
		c.deliveredMix++
	}
	if r.Refused {
		c.refusedProg++
	}
	if r.Warned {
		c.warned++
	}
	if r.Retried {
		c.retried++
		if !r.Refused {
			c.fixedByRetry++
		}
	}
	if len(r.Rubric) > 0 {
		c.rubricN++
		if c.rubric == nil {
			c.rubric = map[string]int{}
		}
		for k, v := range r.Rubric {
			if v {
				c.rubric[k]++
			}
		}
	}
}

// usable is the cell count the rates are computed over: empty answers and transport
// failures are reported, never silently folded into a denominator.
func (c cellStats) usable() int { return c.n - c.empty - c.transport }

func buildReport(rowsPath, outPath string, rescore bool, invPath string) error {
	rows, err := readRows(rowsPath)
	if err != nil {
		return err
	}
	rules, err := loadRules(invPath)
	if err != nil {
		return err
	}
	if rescore {
		rows = rescoreRows(rows, rules)
		if err := writeRows(rowsPath, rows); err != nil {
			return err
		}
	}

	var b strings.Builder
	writeReport(&b, rows, rules)
	if err := os.WriteFile(outPath, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "отчёт: %s\n", outPath)
	return nil
}

func writeRows(path string, rows []cellRow) error {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func writeReport(b *strings.Builder, rows []cellRow, rules []agent.Invariant) {
	fmt.Fprintf(b, "# День 14 — инварианты: что дал прогон\n\n%s\n\n", generatedHeader)

	byArm := map[string]*cellStats{}
	byCell := map[string]*cellStats{}
	total := &cellStats{}
	models := map[string]int{}
	runID, revision := "", ""
	for _, r := range rows {
		if runID == "" {
			runID, revision = r.Run, r.Revision
		}
		if r.Model != "" {
			models[r.Model]++
		}
		total.add(r)
		key := r.Arm + "|" + r.Scenario
		if byCell[key] == nil {
			byCell[key] = &cellStats{}
		}
		byCell[key].add(r)
		if r.Scenario == "control" {
			continue // the control belongs to the detector, not to the arm's rate
		}
		if byArm[r.Arm] == nil {
			byArm[r.Arm] = &cellStats{}
		}
		byArm[r.Arm].add(r)
	}

	fmt.Fprintf(b, "Прогон `%s`, ревизия `%s`. Ответившие модели: %s.\n\n", runID, revision, modelList(models))
	fmt.Fprintf(b, "Клеток %d, вызовов %d, пустых ответов %d, сбоев вызова %d. Потрачено **$%.6f**, из них на судью $%.6f и на повторы $%.6f.\n\n",
		total.n, total.calls, total.empty, total.transport, total.cost, total.judgeCost, total.retryCost)
	if total.usable() > 0 && float64(total.empty)/float64(total.usable()+total.empty) > emptyShareCeiling {
		fmt.Fprintf(b, "> **Доля пустых ответов выше заранее объявленного потолка %.0f%%.** Числа ниже не интерпретируются.\n\n", emptyShareCeiling*100)
	}

	writeControlSection(b, byCell)
	writeLeakSection(b, byArm)
	writeRetrySection(b, byArm)
	writeCostSection(b, byArm)
	writeRefusalSection(b, byArm, byCell)
	writeInstrumentSection(b, rules)
}

// writeControlSection comes first, because every number after it is worthless if the
// control failed. A detector that fires on a question with nothing to break is broken,
// and a run that printed rates anyway would be reporting its own defect as a finding.
func writeControlSection(b *strings.Builder, byCell map[string]*cellStats) {
	b.WriteString("## Контроль детектора\n\nСценарий `control` сломать нечего: он не называет ни библиотек, ни архитектур. Нарушения здесь — свойство прибора, а не модели.\n\n")
	b.WriteString("| Рука | Клеток | Выполнил запрещённое | Отказ с примесью | Отказов программы |\n|---|---|---|---|---|\n")
	arms := sortedArmNames(byCell, "control")
	bad := 0
	for _, arm := range arms {
		c := byCell[arm+"|control"]
		fmt.Fprintf(b, "| %s | %d | %d | %d | %d |\n", arm, c.usable(), c.firstBad(), c.firstMixed, c.refusedProg)
		bad += c.firstBad() + c.firstMixed
	}
	if bad > 0 {
		b.WriteString("\n> **Отрицательный контроль не прошёл**: детектор сработал там, где ломать нечего. Остальные доли ниже завышены на эту величину и не могут считаться измерением.\n")
	} else {
		b.WriteString("\n Ни одного срабатывания на пустом месте — доли ниже можно читать.\n")
	}
	b.WriteString("\n")
}

func writeLeakSection(b *strings.Builder, byArm map[string]*cellStats) {
	b.WriteString("## A. Где течёт текстовое правило\n\nАнтипаттерн 03 в числах. «Выполнила запрещённое» — первый ответ модели без всякого отказа; «дошло до человека» — то же самое в тексте, который агент в итоге выдал. Отказы, называющие запрещённое, сюда не входят: см. столбец «отказ с примесью» и раздел о приборах.\n\n")
	b.WriteString("| Рука | Клеток | Выполнила запрещённое | Доля | 95% Уилсон | Отказ с примесью | Нарушение дошло до человека | Доля |\n|---|---|---|---|---|---|---|---|\n")
	for _, name := range sortedArmNames(byArm, "") {
		c := byArm[name]
		n := c.usable()
		loFirst, hiFirst := stats.Wilson(c.firstBad(), n)
		fmt.Fprintf(b, "| %s | %d | %d/%d | %s | [%s, %s] | %d | %d/%d | %s |\n",
			name, n, c.firstBad(), n, pct(c.firstBad(), n), pct1(loFirst), pct1(hiFirst),
			c.firstMixed, c.deliveredBad, n, pct(c.deliveredBad, n))
	}
	if a, ok := byArm["prompt-only"]; ok {
		if c, ok2 := byArm["check"]; ok2 {
			p := stats.FisherTwoSided(a.deliveredBad, a.usable()-a.deliveredBad, c.deliveredBad, c.usable()-c.deliveredBad)
			fmt.Fprintf(b, "\nФишер по «дошло до человека», `prompt-only` против `check`, двусторонний: p = %.2g.\n", p)
		}
	}
	b.WriteString("\n")
}

func writeRetrySection(b *strings.Builder, byArm map[string]*cellStats) {
	b.WriteString("## B. Что покупает повтор\n\nИз ответов, провалившихся на первом вызове, — сколько прошли после одного повтора со списком нарушений.\n\n")
	b.WriteString("| Рука | Повторов | Починено повтором | Доля | Отказов после повтора |\n|---|---|---|---|---|\n")
	for _, name := range sortedArmNames(byArm, "") {
		c := byArm[name]
		if c.retried == 0 {
			continue
		}
		fmt.Fprintf(b, "| %s | %d | %d | %s | %d |\n", name, c.retried, c.fixedByRetry, pct(c.fixedByRetry, c.retried), c.retried-c.fixedByRetry)
	}
	b.WriteString("\n")
}

// writeCostSection is the host's claim put to a number: "может быть адски дорого и
// нивелировать весь эффект от ИИ" (chat #3156). It is the only place this run can say
// "подтвердилось" or "не подтвердилось" about his words, so the arms are priced per
// cell rather than in totals — a total depends on how many cells were run.
func writeCostSection(b *strings.Builder, byArm map[string]*cellStats) {
	b.WriteString("## C. Цена контроля\n\nУтверждение ведущего [чат #3156]: программная проверка «может быть адски дорого и нивелировать весь эффект от ИИ». Здесь она измерена.\n\n")
	b.WriteString("| Рука | Клеток | Вызовов на клетку | Цена клетки | Из них судья | Из них повтор | К базовой руке |\n|---|---|---|---|---|---|---|\n")
	base := 0.0
	if c, ok := byArm["prompt-only"]; ok && c.usable() > 0 {
		base = c.cost / float64(c.usable())
	}
	for _, name := range sortedArmNames(byArm, "") {
		c := byArm[name]
		n := c.usable()
		if n == 0 {
			continue
		}
		per := c.cost / float64(n)
		ratio := "—"
		if base > 0 {
			ratio = fmt.Sprintf("×%.2f", per/base)
		}
		fmt.Fprintf(b, "| %s | %d | %.2f | $%.6f | $%.6f | $%.6f | %s |\n",
			name, n, float64(c.calls)/float64(n), per, c.judgeCost/float64(n), c.retryCost/float64(n), ratio)
	}
	b.WriteString("\n")
}

func writeRefusalSection(b *strings.Builder, byArm map[string]*cellStats, byCell map[string]*cellStats) {
	b.WriteString("## D. Как ассистент объясняет отказ\n\nСчитается **собственный отказ модели**, не шаблон программы: шаблон содержит все четыре части по построению, и мерить его — мерить константу.\n\n")
	b.WriteString("| Рука | Своих отказов | назвал инвариант | назвал запрещённое | назвал разрешённое | предложил замену |\n|---|---|---|---|---|---|\n")
	for _, name := range sortedArmNames(byArm, "") {
		c := byArm[name]
		if c.rubricN == 0 {
			fmt.Fprintf(b, "| %s | 0 | — | — | — | — |\n", name)
			continue
		}
		row := []string{}
		for _, k := range rubricCriteria() {
			row = append(row, fmt.Sprintf("%d/%d", c.rubric[k], c.rubricN))
		}
		fmt.Fprintf(b, "| %s | %d | %s |\n", name, c.rubricN, strings.Join(row, " | "))
	}
	b.WriteString("\nПо сценариям — что именно делал первый ответ модели. `отказ с примесью` — отказ, в котором запрещённое всё же названо вне отклоняющего предложения: прибор этот случай не разрешает и называет его подозрением, а не нарушением.\n\n")
	b.WriteString("| Сценарий | Рука | Клеток | выполнил запрещённое | чистый ответ | отказ | отказ с примесью |\n|---|---|---|---|---|---|---|\n")
	keys := make([]string, 0, len(byCell))
	for k := range byCell {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts := strings.SplitN(k, "|", 2)
		c := byCell[k]
		fmt.Fprintf(b, "| %s | %s | %d | %d | %d | %d | %d |\n",
			parts[1], parts[0], c.usable(), c.firstComplied, c.firstClean, c.firstRefused, c.firstMixed)
	}
	b.WriteString("\n")
}

// writeInstrumentSection is not optional. Every check here is narrower than the rule it
// enforces, and a report that did not say where it is blind would let a reader take
// "нарушений нет" for "нарушить нельзя".
func writeInstrumentSection(b *strings.Builder, rules []agent.Invariant) {
	b.WriteString("## Приборы: чем именно измеряли и чего они не видят\n\n")
	b.WriteString("| Инвариант | Область | Как проверяется | Гарантия |\n|---|---|---|---|\n")
	for _, i := range rules {
		how := "функция на Go"
		guarantee := "детерминированная в пределах словаря"
		if i.Enforce() == agent.EnforceJudge {
			how, guarantee = "отдельный вызов модели-судьи", "**нет** — ведущий сказал прямо [чат #3162]"
		}
		if i.Kind == agent.KindTransitionBan {
			how, guarantee = "таблица переходов дня 13", "детерминированная"
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n", i.Name, i.Scope, how, guarantee)
	}
	b.WriteString("\nЧего проверки НЕ видят:\n\n")
	b.WriteString("- **Словарь конечен.** Технология, которой нет в `stackVocabulary`, машинной проверке невидима. Это и есть «все программно не проверишь» [чат #3155], записанное явно.\n")
	b.WriteString("- **Голого `go` в словаре нет** намеренно: это обычное слово, и проверка ловила бы «go to the next step». Дыра названа, а не замаскирована.\n")
	b.WriteString("- **Предложение отделяется от отказа по предложениям текста.** «Java нельзя, берём Kotlin» нарушением не считается, «Java нельзя. Вот пример на Java:» — считается. Разделение приблизительное и режет в сторону недосчёта.\n")
	b.WriteString("- **Судейский инвариант в журнале не пересчитывается**: его вердикт стоил вызова и хранится как есть, `-rescore` его не трогает.\n")
	b.WriteString("- **Отказ и цитату проверка не различает.** Модель, которой сообщили правила, отказывает, ПЕРЕЧИСЛЯЯ их, и называет запрещённое, чтобы его запретить. Поэтому нарушения считаются только там, где отказа нет вовсе; смешанный случай вынесен в отдельный столбец и нарушением не назван.\n")
	b.WriteString("- **Отказ программы проверке не подвергается** и нарушением дойти до человека не может: его текст — шаблон, а ответ модели в этом случае не доставлен вообще.\n")
}

func sortedArmNames(m map[string]*cellStats, suffix string) []string {
	order := map[string]int{}
	for i, a := range arms() {
		order[a.Name] = i
	}
	var out []string
	for k := range m {
		name := k
		if suffix != "" {
			cut, ok := strings.CutSuffix(k, "|"+suffix)
			if !ok {
				continue
			}
			name = cut
		}
		out = append(out, name)
	}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i]] < order[out[j]] })
	return out
}

func modelList(models map[string]int) string {
	if len(models) == 0 {
		return "нет"
	}
	names := make([]string, 0, len(models))
	for m := range models {
		names = append(names, m)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, m := range names {
		parts = append(parts, fmt.Sprintf("%s — %d", m, models[m]))
	}
	return strings.Join(parts, ", ")
}

func pct(k, n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(k)/float64(n))
}

func pct1(v float64) string { return fmt.Sprintf("%.0f%%", 100*v) }
