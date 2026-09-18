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
	// fixedByRetry is the retried cells the program did NOT end up refusing. It is not
	// "repaired": an external review found that in check-retry and judge every such
	// cell was a DECLARED REFUSAL on the second answer, not a changed decision. The two
	// are counted apart, because "починено 100%" read as "the model complied after all"
	// and that is the opposite of what happened.
	fixedByRetry  int
	retryDeclared int
	// replacedRefusal is a cell whose first answer was a refusal in prose that carried
	// no marker, so the program refused it and delivered its own template instead. A
	// correct refusal thrown away costs more than a violation caught.
	replacedRefusal int
	judgeCalls      int
	calls           int
	// completion is what the arm's answers WEIGH. Without it the price column is read
	// as the price of the check, which a function call cannot have.
	completion int
	cost       float64
	judgeCost  float64
	retryCost  float64
	rubric     map[string]int
	rubricN    int
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
	c.completion += r.Completion
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
			if r.Declared {
				c.retryDeclared++
			}
		}
	}
	// The first answer broke a rule and declared nothing, and the program replaced it.
	// Whether that first answer was a refusal in prose is a reading, not a measurement,
	// so the report prints the cells and says which of them were read.
	if r.FirstClass == classComplied && r.Refused {
		c.replacedRefusal++
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
	// The provenance needs its own sentence, because the obvious reading of the SHA is
	// wrong: a run happens before the commit that contains it.
	b.WriteString("Ревизия — это состояние рабочего дерева на момент прогона. Прогон делается **до** коммита, поэтому SHA называет родителя того, что исполнялось; пометка `+dirty` означает, что в дереве были незакоммиченные правки — то есть исполнялся не в точности этот SHA.\n\n")
	fmt.Fprintf(b, "Клеток %d, вызовов %d, пустых ответов %d, сбоев вызова %d. Потрачено **$%.6f**, из них на судью $%.6f и на повторы $%.6f.\n\n",
		total.n, total.calls, total.empty, total.transport, total.cost, total.judgeCost, total.retryCost)
	if total.usable() > 0 && float64(total.empty)/float64(total.usable()+total.empty) > emptyShareCeiling {
		fmt.Fprintf(b, "> **Доля пустых ответов выше заранее объявленного потолка %.0f%%.** Числа ниже не интерпретируются.\n\n", emptyShareCeiling*100)
	}

	writeControlSection(b, byCell)
	writeLeakSection(b, byArm, rows)
	writeRetrySection(b, byArm)
	writeCostSection(b, byArm)
	writeRefusalSection(b, byArm, byCell)
	writeCostOfARefusalWithoutAMarker(b, byArm)
	writeInstrumentSection(b, rules)
}

// writeCostOfARefusalWithoutAMarker is the price of the design choice the day made: the
// declaration is the model's, and a refusal that forgets to declare is treated as an
// ordinary answer. An external review found what that costs in this run, and it is a
// finding, not a footnote — a correct refusal thrown away is worse than a violation
// caught, because the person loses an answer that was right.
func writeCostOfARefusalWithoutAMarker(b *strings.Builder, byArm map[string]*cellStats) {
	// Only the arms where the rules actually travelled. In the silent arms the model was
	// never told the rules, so it really did propose the forbidden thing and replacing
	// its answer is the correct outcome — counting those here would turn a working
	// defence into a cost.
	injecting := map[string]bool{}
	for _, a := range arms() {
		injecting[a.Name] = a.Inject
	}
	total := 0
	b.WriteString("## E. Чего стоит отказ без маркера\n\n")
	b.WriteString("Объявление — модели, и отказ, забывший его поставить, идёт по обычному пути: проверка находит имена, которые он называет, и программа заменяет его своим шаблоном. Человек теряет ответ, который был правильным.\n\n")
	b.WriteString("Считаются только руки, в которых правила уезжали в запрос. В немых руках модель запретов не знала и запрещённое действительно предлагала — там замена ответа и есть работающая защита, а не цена.\n\n")
	b.WriteString("| Рука | Клеток | Первый ответ заменён шаблоном при ненайденном объявлении |\n|---|---|---|\n")
	for _, name := range sortedArmNames(byArm, "") {
		if !injecting[name] {
			continue
		}
		c := byArm[name]
		total += c.replacedRefusal
		fmt.Fprintf(b, "| %s | %d | %d |\n", name, c.usable(), c.replacedRefusal)
	}
	fmt.Fprintf(b, "\nВсего таких клеток **%d** из 320. Обмен назван сознательно: альтернатива — угадывать речевой акт по словам, и она была сломана обычной связкой «вместо». Но цена у обмена есть, и она здесь.\n\n", total)
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

func writeLeakSection(b *strings.Builder, byArm map[string]*cellStats, rows []cellRow) {
	b.WriteString("## A. Где течёт текстовое правило\n\n")
	b.WriteString("Антипаттерн 03 в числах.\n\n")
	// The column says what it measures, which is narrower than what an earlier version
	// claimed. It counted "нарушение найдено И маркер отказа не поставлен" and was
	// published as «первый ответ модели без всякого отказа» — an external review read
	// all such cells and found refusals among them. The name is now the definition.
	b.WriteString("Столбец «нарушение без объявленного отказа» — это ровно то, что он считает: проверка нашла запрещённое имя, а маркера `[[REFUSED: …]]` в первом ответе не было. Это **не** «модель выполнила запрещённое»: маркер мог приехать со второго ответа, а отказ — быть написан прозой без маркера. Разбор каждой такой клетки — сноской под таблицей.\n\n")
	b.WriteString("| Рука | Клеток | Нарушение без объявленного отказа | Доля | 95% Уилсон | Отказ с примесью | Нарушение дошло до человека | Доля |\n|---|---|---|---|---|---|---|---|\n")
	for _, name := range sortedArmNames(byArm, "") {
		c := byArm[name]
		n := c.usable()
		loFirst, hiFirst := stats.Wilson(c.firstBad(), n)
		fmt.Fprintf(b, "| %s | %d | %d/%d | %s | [%s, %s] | %d | %d/%d | %s |\n",
			name, n, c.firstBad(), n, pct(c.firstBad(), n), pct1(loFirst), pct1(hiFirst),
			c.firstMixed, c.deliveredBad, n, pct(c.deliveredBad, n))
	}

	writeCompliedFootnote(b, rows)

	// The comparison is between the arms that differ in the thing being measured: the
	// rules travelling in the request or not. Comparing two arms that both deliver zero
	// would produce p = 1 and prove nothing — the first version of this report did
	// exactly that and printed it as a finding.
	if with, ok := byArm["prompt-only"]; ok {
		if without, ok2 := byArm["silent-check"]; ok2 {
			p := stats.FisherTwoSided(
				with.firstBad(), with.usable()-with.firstBad(),
				without.firstBad(), without.usable()-without.firstBad())
			fmt.Fprintf(b, "\nФишер по «нарушение без объявленного отказа», `prompt-only` против `silent-check` — то есть правила в запросе против их отсутствия, двусторонний: p = %.2g.\n", p)
		}
	}
	// The second comparison is reported WITH its own limitation, because the limitation
	// is structural: with checking on, a violation cannot reach the person by
	// construction — scoreRow marks such a cell `program-refusal`. A p-value against a
	// zero that the code guarantees measures the code, not the model.
	if with, ok := byArm["prompt-only"]; ok {
		if checked, ok2 := byArm["check"]; ok2 {
			p := stats.FisherTwoSided(
				with.deliveredBad, with.usable()-with.deliveredBad,
				checked.deliveredBad, checked.usable()-checked.deliveredBad)
			fmt.Fprintf(b, "\nФишер по «дошло до человека», `prompt-only` против `check`, двусторонний: p = %.2g. "+
				"**Это значение ничего не доказывает и приведено только для полноты:** в руке с проверкой ноль в этом столбце **конструктивный** — отклонённый ответ по построению не доставляется, и класс «нарушение доставлено» там недостижим. Сравнивать измеренное число с гарантированным нулём нельзя.\n", p)
		}
	}
	b.WriteString("\n")
}

// writeCompliedFootnote prints every cell the column counted, in the arms that carry the
// rules. It exists because the column was published as a rate and read as «модель
// выполнила запрещённое», and an external review showed the cells are not that. Six
// cells fit under a table; a rate does not let anyone check it.
func writeCompliedFootnote(b *strings.Builder, rows []cellRow) {
	type entry struct {
		arm, scenario string
		repeat        int
		viol, note    string
	}
	var found []entry
	for _, r := range rows {
		if r.Scenario == "control" || r.Outcome != outcomeOK || r.FirstClass != classComplied {
			continue
		}
		if r.Arm == "silent-check" || r.Arm == "silent-retry" {
			continue // there the rules never travelled; the column means what it says
		}
		note := "ответ доставлен как есть"
		switch {
		case r.Declared:
			note = "маркер приехал со второго ответа, первый его не поставил"
		case r.Refused:
			note = "программа отказала и заменила текст своим шаблоном"
		}
		found = append(found, entry{r.Arm, r.Scenario, r.Repeat, strings.Join(r.FirstViolations, ", "), note})
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].arm != found[j].arm {
			return found[i].arm < found[j].arm
		}
		if found[i].scenario != found[j].scenario {
			return found[i].scenario < found[j].scenario
		}
		return found[i].repeat < found[j].repeat
	})
	if len(found) == 0 {
		b.WriteString("\nВ руках, где правила уезжали в запрос, таких клеток нет ни одной.\n")
		return
	}
	fmt.Fprintf(b, "\nВсе %d клеток этого столбца в руках с правилами, поимённо:\n\n", len(found))
	b.WriteString("| Клетка | Что нашла проверка | Что с ответом сделала программа |\n|---|---|---|\n")
	for _, e := range found {
		fmt.Fprintf(b, "| `%s/%s/%d` | %s | %s |\n", e.arm, e.scenario, e.repeat, e.viol, e.note)
	}
	b.WriteString("\nЭти строки сгенерированы из журнала. **Прочитаны они руками, и это отдельное утверждение:** ни в одной из них модель не предлагает запрещённое как решение — это либо отказ прозой без маркера, либо перечисление вариантов, часть которых тут же отклоняется. То есть настоящих случаев «модель выполнила запрещённое при правилах в запросе» в прогоне **нет**. Чтение проверяемо: клетки названы, журнал в репозитории.\n")
}

func writeRetrySection(b *strings.Builder, byArm map[string]*cellStats) {
	b.WriteString("## B. Что покупает повтор\n\n")
	b.WriteString("Из ответов, провалившихся на первом вызове, — что стало после одного повтора со списком нарушений.\n\n")
	// "Починено" was one column and meant "the program did not refuse". An external
	// review showed that in the rule-carrying arms every such cell was a DECLARED
	// REFUSAL on the second answer: the model did not change its solution, it stated
	// that it refuses. Two different outcomes under one heading read as the wrong one.
	b.WriteString("«Прошло» значит только, что программа не отказала. Из чего оно состоит — в следующих двух столбцах: модель либо **объявила отказ** вторым ответом, либо действительно **переделала решение**.\n\n")
	b.WriteString("| Рука | Повторов | Прошло | Доля | из них объявленный отказ | из них решение переделано | Отказов после повтора |\n|---|---|---|---|---|---|---|\n")
	for _, name := range sortedArmNames(byArm, "") {
		c := byArm[name]
		if c.retried == 0 {
			continue
		}
		fmt.Fprintf(b, "| %s | %d | %d | %s | %d | %d | %d |\n",
			name, c.retried, c.fixedByRetry, pct(c.fixedByRetry, c.retried),
			c.retryDeclared, c.fixedByRetry-c.retryDeclared, c.retried-c.fixedByRetry)
	}
	b.WriteString("\n")
}

// writeCostSection is the host's claim put to a number: "может быть адски дорого и
// нивелировать весь эффект от ИИ" (chat #3156). It is the only place this run can say
// "подтвердилось" or "не подтвердилось" about his words, so the arms are priced per
// cell rather than in totals — a total depends on how many cells were run.
func writeCostSection(b *strings.Builder, byArm map[string]*cellStats) {
	b.WriteString("## C. Цена контроля\n\n")
	b.WriteString("Утверждение ведущего [чат #3156]: программная проверка «может быть адски дорого и нивелировать весь эффект от ИИ». Здесь она измерена.\n\n")
	// Without this the column is read as "the check costs 6%", which it does not and
	// cannot: the machine check is a function call. An external review pointed at the
	// arm that proves it — silent-check carries NO rules at all and still costs more.
	b.WriteString("**Столбец «к базовой руке» — это цена РУКИ, а не цена проверки.** Машинная проверка не делает ни одного вызова модели: `prompt-only` и `check` обе стоят 1.00 вызова на клетку, и разница между ними — это длина ответов, а не работа проверки. Прямое доказательство — рука `silent-check`: правил в запросе там нет вообще, проверять модели нечего, а клетка всё равно дороже базовой, потому что без правил модель пишет длиннее. Столбец «выход, токенов на клетку» стоит рядом именно для этого: цены рук сравнимы только вместе с ним.\n\n")
	b.WriteString("Настоящая цена контроля — там, где появляются лишние **вызовы**: судья и повтор. Им отведены отдельные колонки, и только они меряют то, о чём говорил ведущий.\n\n")
	b.WriteString("| Рука | Клеток | Вызовов на клетку | Выход, токенов на клетку | Цена клетки | Из них судья | Из них повтор | К базовой руке |\n|---|---|---|---|---|---|---|---|\n")
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
		fmt.Fprintf(b, "| %s | %d | %.2f | %.0f | $%.6f | $%.6f | $%.6f | %s |\n",
			name, n, float64(c.calls)/float64(n), float64(c.completion)/float64(n),
			per, c.judgeCost/float64(n), c.retryCost/float64(n), ratio)
	}
	b.WriteString("\n")
}

func writeRefusalSection(b *strings.Builder, byArm map[string]*cellStats, byCell map[string]*cellStats) {
	b.WriteString("## D. Как ассистент объясняет отказ\n\n")
	b.WriteString("Считается **собственный отказ модели**, не шаблон программы: шаблон содержит все четыре части по построению, и мерить его — мерить константу.\n\n")
	// The second criterion is the same computation as the "отказ с примесью" column of
	// section A: both are CheckAnswer(...) > 0 on the same text. Publishing one number
	// twice — once as a suspicion, once as a virtue — was found by an external review,
	// and the honest fix is to say so where it is printed rather than to drop a column.
	b.WriteString("**«Назвал запрещённое» и «отказ с примесью» из раздела A — одно и то же число,** посчитанное одной и той же проверкой на одном и том же тексте. Разница только в прочтении: там это подозрение, что отказ всё же что-то предложил, здесь — достоинство, что отказ назвал, от чего отказывается. Прибор различить эти два случая не умеет, и поэтому печатает одно число дважды, а не два разных.\n\n")
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
	b.WriteString("| Сценарий | Рука | Клеток | нарушение без объявления | чистый ответ | отказ | отказ с примесью |\n|---|---|---|---|---|---|---|\n")
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
	b.WriteString("- **Потолок зависимостей считает только СТОРОННИЕ.** Термины, которые предписывает другое действующее правило (стек `stack`, архитектура `arch`), из счёта исключены. До правки они считались, и потолок «не больше трёх» на деле означал «три минус обязательные»: проверка срабатывала в 414 первых ответах из 600, и в 298 из них назывался требуемый Kotlin или Ktor. Правило наказывало за соблюдение другого правила.\n")
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
