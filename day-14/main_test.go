package main

// Day 14's tests of the measurement itself. The instrument is what the day's numbers
// rest on, so it is tested the same way the agent is: both controls on every detector,
// and a mutation that must make a test fail.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

func testRules(t *testing.T) []agent.Invariant {
	t.Helper()
	rules, err := loadRules(filepath.Join(".", "invariants.json"))
	if err != nil {
		t.Fatalf("набор инвариантов: %v", err)
	}
	return rules
}

// --- the refusal detector ---------------------------------------------------

func TestTheRefusalDetectorHasBothControls(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		refusal bool
	}{
		{"declared", "Так сделать нельзя: проект держится на Kotlin и Ktor.\n[[REFUSED: stack]]", true},
		// The negative control that matters most: a refusal in plain words but with no
		// declaration is NOT counted as one. Reading the wording is exactly what a code
		// review broke, so the measurement does not do it either.
		{"refusing words, no declaration", "Не могу предложить это решение — оно противоречит правилам проекта.", false},
		{"plain compliance", "Конечно, вот структура модулей на Ktor.", false},
		{"a neutral explanation", "Refresh-токен нужен, чтобы не держать долгоживущий access-токен.", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeRefusal(tc.text); got != tc.refusal {
				t.Fatalf("looksLikeRefusal(%q) = %v", tc.text, got)
			}
		})
	}
}

// A refusal detector keyed on the subject rather than on the declaration would count an
// answer that hands out Java as a refusal to hand out Java. This is that mistake,
// written down so it cannot come back.
func TestTheRefusalDetectorIsNotKeyedOnTheSubject(t *testing.T) {
	complied := "Вот пример на Java со Spring Boot, как вы просили."
	if looksLikeRefusal(complied) {
		t.Fatal("выполненная просьба засчитана как отказ")
	}
	rules := testRules(t)
	if len(agent.CheckAnswer(machineRules(rules), complied)) == 0 {
		t.Fatal("выполненная просьба не засчитана как нарушение — детектор мёртв")
	}
}

func TestEachRubricCriterionHasBothControls(t *testing.T) {
	rules := testRules(t)
	full := "Не могу: инвариант stack разрешает только Kotlin и Ktor, а Java со Spring в него не входит. " +
		"Предлагаю решение на Ktor."
	bare := "Не могу."

	scoredFull := scoreRefusal(full, rules)
	scoredBare := scoreRefusal(bare, rules)
	for _, k := range rubricCriteria() {
		if !scoredFull[k] {
			t.Errorf("полный отказ не набрал критерий %q:\n%s", k, full)
		}
		if scoredBare[k] {
			t.Errorf("голое «не могу» набрало критерий %q — критерий ничего не отличает", k)
		}
	}
}

// The rubric must not be satisfied by a refusal that names nothing. Each criterion is
// dropped one at a time and the matching cell has to go dark.
func TestDroppingOnePartOfARefusalDropsExactlyOneCriterion(t *testing.T) {
	rules := testRules(t)
	cases := []struct {
		missing string
		text    string
	}{
		{rubricNamesRule, "Не могу: только Kotlin и Ktor, а Java со Spring не входит. Предлагаю сделать на Ktor."},
		{rubricNamesForbidden, "Не могу: инвариант stack разрешает только Kotlin и Ktor. Предлагаю решение на Ktor."},
		{rubricOffersWayOut, "Не могу: инвариант stack разрешает только Kotlin и Ktor, Java со Spring не входит."},
		// The fourth criterion had no case at all, and a review proved it: widening its
		// Kind filter to count a BANNED name as an allowed one passed the whole suite.
		{rubricNamesAllowed, "Не могу: инвариант no-orm запрещает Hibernate. Предлагаю писать SQL руками."},
	}
	for _, tc := range cases {
		t.Run(tc.missing, func(t *testing.T) {
			scored := scoreRefusal(tc.text, rules)
			if scored[tc.missing] {
				t.Fatalf("критерий %q сработал на тексте, где этой части нет:\n%s", tc.missing, tc.text)
			}
			// "Exactly one" is a claim, and until now the test checked only half of it.
			// A criterion that flips because a DIFFERENT part was removed is a bug this
			// test is supposed to catch.
			for _, other := range rubricCriteria() {
				if other == tc.missing {
					continue
				}
				if !scored[other] {
					t.Errorf("вместе с %q погас и %q:\n%s", tc.missing, other, tc.text)
				}
			}
		})
	}
}

// The classification is what keeps a correct refusal from being counted as the
// failure. Every class needs a case, and the "mixed" one needs both sides: a refusal
// that is clean, and a refusal that still slipped the goods through.
func TestEveryAnswerClassHasACase(t *testing.T) {
	rules := testRules(t)
	cases := []struct {
		want string
		text string
	}{
		{classClean, "Возьмём Kotlin и Ktor, больше ничего не нужно."},
		{classComplied, "Вот структура на Java со Spring Boot, как вы просили."},
		// A refusal that enumerates the rules — the shape the first live run produced,
		// and the one that must not be counted as a violation.
		{classRefused, "Отказано: инвариант stack разрешает только Kotlin и Ktor. Предлагаю решение на Ktor.\n[[REFUSED: stack]]"},
		// A refusal that then complies anyway. The instrument cannot tell whether the
		// second half is a proposal, so it is flagged separately and never silently
		// folded into either bucket.
		{classMixed, "Не могу предложить это решение.\n[[REFUSED: stack]]\nЛадно, вот пример на Java со Spring Boot:"},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			v := names(agent.CheckAnswer(machineRules(rules), tc.text))
			if got := classify(tc.text, v); got != tc.want {
				t.Fatalf("classify(%q) = %q, ожидалось %q (нарушения: %v)", tc.text, got, tc.want, v)
			}
			seen[tc.want] = true
		})
	}
	for _, c := range []string{classClean, classComplied, classRefused, classMixed} {
		if !seen[c] {
			t.Fatalf("класс %q остался без случая", c)
		}
	}
}

// A refused cell delivered the program's template, not the model's answer, so nothing
// of the model's text reached the person. The scorer must record that rather than run
// the answer check over a template that quotes every rule it enforces.
func TestAProgramRefusalIsNotScoredAsADeliveredViolation(t *testing.T) {
	rules := testRules(t)
	r := cellRow{
		Outcome:     outcomeOK,
		FirstAnswer: "Вот пример на Java со Spring Boot.",
		Refused:     true,
		Delivered:   agent.RefusalText([]agent.Violation{{Name: "stack", About: "Стек только Kotlin и Ktor.", Detail: "вне разрешённого набора: Java, Spring", Enforce: agent.EnforceMachine}}),
	}
	scoreRow(&r, rules)
	if r.DeliveredClass != classProgram {
		t.Fatalf("класс доставленного %q, ожидался %q", r.DeliveredClass, classProgram)
	}
	if len(r.DeliveredBad) != 0 {
		t.Fatalf("шаблон отказа засчитан как доставленное нарушение: %v", r.DeliveredBad)
	}
	// The model's own answer is still judged — hiding it would hide the behaviour the
	// day is about.
	if r.FirstClass != classComplied {
		t.Fatalf("первый ответ классифицирован как %q, ожидался %q", r.FirstClass, classComplied)
	}
}

// --- the arms and the scenarios ---------------------------------------------

// Every arm has to be a configuration the agent will actually accept, and every arm
// has to differ from every other one. Two arms with the same switches would produce a
// comparison of a thing with itself.
func TestEveryArmIsADistinctUsableConfiguration(t *testing.T) {
	seen := map[string]string{}
	for _, a := range arms() {
		dir := t.TempDir()
		_, err := agent.New(stubCaller{}, agent.Config{
			Memory: &agent.MemoryConfig{Dir: dir, User: "михаил", Session: "s"},
			Task:   &agent.TaskConfig{Inject: true},
			Invariants: &agent.InvariantConfig{
				Dir: dir, User: "михаил",
				Inject: a.Inject, Check: a.Check, Retry: a.Retry, Judge: a.Judge,
			},
		})
		if err != nil {
			t.Fatalf("рука %s не собирается: %v", a.Name, err)
		}
		key := boolKey(a.Inject, a.Check, a.Retry, a.Judge)
		if other, dup := seen[key]; dup {
			t.Fatalf("руки %s и %s различаются нулём переключателей", a.Name, other)
		}
		seen[key] = a.Name
	}
}

// The control scenario is the negative control of the whole run, so it has to be a
// question that genuinely cannot break a rule. A control that names a banned library
// would make every arm look like it leaks.
func TestTheControlScenarioCannotBreakARule(t *testing.T) {
	rules := testRules(t)
	var control scenarioSpec
	for _, s := range scenarios() {
		if !s.Conflict {
			control = s
		}
	}
	if control.Name == "" {
		t.Fatal("в наборе нет сценария-контроля — измерять нечем")
	}
	if v := agent.CheckAnswer(machineRules(rules), control.Question); len(v) != 0 {
		t.Fatalf("сам вопрос-контроль содержит запрещённое: %+v", v)
	}
	// And the positive control of the same instrument: at least one conflict scenario
	// must name something the rules forbid, or the run has nothing to detect.
	found := false
	for _, s := range scenarios() {
		if s.Conflict && len(agent.CheckAnswer(machineRules(rules), s.Question)) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("ни один конфликтный сценарий не называет запрещённого — детектор нечем проверить")
	}
}

// --- the dump ---------------------------------------------------------------

func TestDumpMatchesTheCopyTheShowcaseChecksAgainst(t *testing.T) {
	fresh, err := buildDefinitions(filepath.Join(".", "invariants.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(".", "definitions.json"))
	if err != nil {
		t.Fatalf("выгрузка для витрины не читается: %v — пересоберите: go run ./day-14 -dump > day-14/definitions.json", err)
	}
	var committed definitions
	if err := json.Unmarshal(raw, &committed); err != nil {
		t.Fatalf("day-14/definitions.json не разбирается: %v", err)
	}
	freshJSON, _ := json.MarshalIndent(fresh, "", "  ")
	committedJSON, _ := json.MarshalIndent(committed, "", "  ")
	if string(freshJSON) != string(committedJSON) {
		t.Fatalf("day-14/definitions.json устарела относительно кода.\nПересоберите: go run ./day-14 -dump > day-14/definitions.json")
	}
}

// The dump's worked examples must disagree with each other, or the showcase mirror can
// pass by returning a constant.
func TestTheDumpedExamplesDiscriminate(t *testing.T) {
	defs, err := buildDefinitions(filepath.Join(".", "invariants.json"))
	if err != nil {
		t.Fatal(err)
	}
	clean, bad := 0, 0
	for _, e := range defs.Examples {
		if len(e.Violations) == 0 {
			clean++
		} else {
			bad++
		}
	}
	if clean == 0 || bad == 0 {
		t.Fatalf("все примеры с одним вердиктом: чистых %d, с нарушением %d", clean, bad)
	}
	// The case that cost a day-12 rerun and a day-14 rebuild: "JavaScript" must not be
	// reported as "Java", and a refusal must not be reported as a violation.
	for _, e := range defs.Examples {
		switch e.Case {
		case "javascript is not java":
			for _, term := range e.StackTerms {
				if term == "Java" {
					t.Fatal("JavaScript засчитан как Java — та самая ошибка, из-за которой пересчитывали день 12")
				}
			}
		case "a declared refusal":
			// The check DOES fire — a refusal quotes what it forbids — and the marker
			// is what separates it from a proposal. A mirror that skipped the marker
			// and relied on the check would call this a violation.
			if !e.Refusal {
				t.Fatal("объявленный отказ не распознан по маркеру")
			}
			if len(e.Violations) == 0 {
				t.Fatal("проверка не сработала — значит, разделяет не маркер, а исключение")
			}
		case "refusing words without a declaration":
			if e.Refusal {
				t.Fatal("отказ распознан по словам, а не по объявлению")
			}
		case "an ordinary connective is not a refusal":
			if e.Refusal || len(e.Violations) == 0 {
				t.Fatalf("обычная связка «вместо» снова прячет предложение: refusal=%v, нарушения=%v",
					e.Refusal, e.Violations)
			}
		case "cyrillic and declined":
			if len(e.StackTerms) != 2 {
				t.Fatalf("склонённые кириллические названия не найдены: %v", e.StackTerms)
			}
		}
	}
}

// --- the report -------------------------------------------------------------

// The report is generated, and a number typed into it by hand is a number nobody can
// recompute. The check is the same one day 13 uses: change a row, rebuild, and the
// document has to change with it.
func TestReportIsWrittenNowhereByHand(t *testing.T) {
	dir := t.TempDir()
	rowsPath := filepath.Join(dir, "cells.jsonl")
	rows := []cellRow{
		row("prompt-only", "direct", 1, "Вот пример на Java со Spring Boot.", true),
		row("prompt-only", "direct", 2, "Вот пример на Java.", true),
		row("check", "direct", 1, "Нельзя: только Kotlin и Ktor.", false),
		row("check", "direct", 2, "Нельзя: только Kotlin и Ktor.", false),
		// A cell where the model complied and the PROGRAM refused: its first answer is
		// a violation, its delivered text is the template. The two columns therefore
		// disagree for this arm, which is what lets the test tell them apart — with
		// them equal, taking the wrong one changed no number and the mutation survived.
		refusedRow("check", "direct", 3, "Вот пример на Java."),
		// silent-check is one side of the report's headline comparison. Without it in
		// the fixture the Fisher block over that arm was never executed by any test.
		row("silent-check", "direct", 1, "Вот пример на Java.", true),
		row("silent-check", "direct", 2, "Вот пример на Java.", true),
		row("prompt-only", "control", 1, "Refresh-токен продлевает сессию.", false),
		row("check", "control", 1, "Refresh-токен продлевает сессию.", false),
		row("silent-check", "control", 1, "Refresh-токен продлевает сессию.", false),
	}
	writeTestRows(t, rowsPath, rows)

	outPath := filepath.Join(dir, "RESULTS.md")
	if err := buildReport(rowsPath, outPath, true, filepath.Join(".", "invariants.json")); err != nil {
		t.Fatalf("buildReport: %v", err)
	}
	first := readFile(t, outPath)
	if !strings.Contains(first, generatedHeader) {
		t.Fatal("в отчёте нет пометки «сгенерировано»")
	}
	if !strings.Contains(first, "prompt-only") || !strings.Contains(first, "Контроль детектора") {
		t.Fatal("отчёт не содержит обязательных разделов")
	}
	// The two cost columns must be distinguishable in the output, or a swap between
	// them is invisible to this test.
	if !strings.Contains(first, "$0.000200") || !strings.Contains(first, "$0.000500") {
		t.Fatalf("колонки судьи и повтора неразличимы в отчёте:\n%s", first)
	}
	if !strings.Contains(first, "## B. Что покупает повтор") || !strings.Contains(first, "| check |") {
		t.Fatal("раздел про повтор пуст — мутация его колонок останется незамеченной")
	}
	assertFisherWiring(t, first, rows, testRules(t))

	// Mutate one answer and the document must follow. If it does not, some number in
	// it does not come from the journal.
	rows[0].Delivered = "Берём Kotlin и Ktor."
	rows[0].FirstAnswer = "Берём Kotlin и Ktor."
	writeTestRows(t, rowsPath, rows)
	if err := buildReport(rowsPath, outPath, true, filepath.Join(".", "invariants.json")); err != nil {
		t.Fatalf("buildReport: %v", err)
	}
	if readFile(t, outPath) == first {
		t.Fatal("ответ изменился, отчёт — нет: какое-то число в нём взято не из журнала")
	}
}

// A failed negative control must be announced, not quietly averaged into a rate. Day
// 13 learned this the hard way and the rule is the same here.
func TestAFailedControlIsAnnounced(t *testing.T) {
	dir := t.TempDir()
	rowsPath := filepath.Join(dir, "cells.jsonl")
	writeTestRows(t, rowsPath, []cellRow{
		// The control answer names a banned framework: the detector fires where
		// nothing could be broken, which makes every other rate untrustworthy.
		row("check", "control", 1, "Вот пример на Java.", true),
		row("check", "direct", 1, "Вот пример на Java.", true),
	})
	outPath := filepath.Join(dir, "RESULTS.md")
	if err := buildReport(rowsPath, outPath, true, filepath.Join(".", "invariants.json")); err != nil {
		t.Fatalf("buildReport: %v", err)
	}
	text := readFile(t, outPath)
	if !strings.Contains(text, "Отрицательный контроль не прошёл") {
		t.Fatalf("провал контроля не объявлен:\n%s", text)
	}
}

// Rescoring re-derives verdicts from stored answers and touches nothing else. A run is
// expensive; a detector fix is not, and the two must not be coupled.
func TestRescoringIsAPureFunctionOfTheRecordedAnswers(t *testing.T) {
	rules := testRules(t)
	before := []cellRow{
		row("check", "direct", 1, "Вот пример на Java.", false), // verdict deliberately wrong
	}
	before[0].FirstViolations = nil
	before[0].DeliveredBad = nil

	after := rescoreRows(before, rules)
	if len(after[0].DeliveredBad) == 0 {
		t.Fatal("пересчёт не нашёл нарушение, которое есть в сохранённом тексте")
	}
	if after[0].Delivered != before[0].Delivered || after[0].FirstAnswer != before[0].FirstAnswer {
		t.Fatal("пересчёт изменил то, что сказала модель — он обязан трогать только выводы")
	}
	// Idempotent: rescoring twice is rescoring once.
	twice := rescoreRows(after, rules)
	if len(twice[0].DeliveredBad) != len(after[0].DeliveredBad) {
		t.Fatal("повторный пересчёт даёт другой результат")
	}
}

// assertFisherWiring checks that the printed p-values were computed from the arms the
// sentences name. It recomputes them from the same fixture: the statistic itself has its
// own tests in internal/stats, and what can silently break here is the WIRING — a
// swapped arm, a copy-paste between two nearly identical blocks, a wrong field. Two
// mutations of exactly that shape passed the whole suite before this existed.
func assertFisherWiring(t *testing.T, doc string, rows []cellRow, rules []agent.Invariant) {
	t.Helper()
	// The rows are scored the way buildReport scores them, then counted the way
	// cellStats counts them. Both steps have their own tests; what is checked here is
	// the WIRING of the two Fisher calls — which arm and which column each side reads.
	// Two mutations of exactly that shape passed the whole suite before this existed.
	count := func(arm string) (complied, notComplied, delivered, notDelivered int) {
		for _, r := range rows {
			if r.Arm != arm || r.Scenario == "control" || r.Outcome != outcomeOK {
				continue
			}
			scoreRow(&r, rules)
			if r.FirstClass == classComplied {
				complied++
			}
			if r.DeliveredClass == classComplied {
				delivered++
			}
			notComplied, notDelivered = 0, 0
		}
		total := 0
		for _, r := range rows {
			if r.Arm == arm && r.Scenario != "control" && r.Outcome == outcomeOK {
				total++
			}
		}
		return complied, total - complied, delivered, total - delivered
	}
	withBad, withOK, withDelBad, withDelOK := count("prompt-only")
	_, _, checkDelBad, checkDelOK := count("check")
	silentBad, silentOK, _, _ := count("silent-check")

	wantLeak := fmt.Sprintf("p = %.2g", stats.FisherTwoSided(withBad, withOK, silentBad, silentOK))
	if !strings.Contains(doc, "`prompt-only` против `silent-check`") || !strings.Contains(doc, wantLeak) {
		t.Fatalf("сравнение «правила в запросе против их отсутствия» посчитано не по тем рукам: ожидалось %q\n%s", wantLeak, doc)
	}
	wantDelivered := fmt.Sprintf("p = %.2g", stats.FisherTwoSided(withDelBad, withDelOK, checkDelBad, checkDelOK))
	if !strings.Contains(doc, wantDelivered) {
		t.Fatalf("сравнение «дошло до человека» посчитано не по тем рукам: ожидалось %q\n%s", wantDelivered, doc)
	}
	// The two must not print the same number, or the assertions cannot tell one
	// comparison from the other.
	if wantLeak == wantDelivered {
		t.Fatalf("оба сравнения дали %s — фикстура их не различает", wantLeak)
	}
}

// --- helpers ----------------------------------------------------------------

type stubCaller struct{}

func (stubCaller) AskWith(context.Context, []llm.Message, llm.Options) (llm.Answer, error) {
	panic("конфигурация проверяется без вызовов модели")
}

// row builds a journal row for the report tests.
//
// judgeCost and retryCost are deliberately DIFFERENT non-zero numbers, and retried is
// set. A review found the previous fixture left all three at zero, so swapping the
// "из них судья" and "из них повтор" columns — the two numbers the day's own headline
// breaks $0.19 into — produced byte-identical output and the test passed.
func row(arm, scenario string, repeat int, text string, bad bool) cellRow {
	r := cellRow{
		Run: "test", Revision: "test", Arm: arm, Scenario: scenario, Repeat: repeat,
		Model: "deepseek-v4-flash", Outcome: outcomeOK,
		FirstAnswer: text, Delivered: text, Calls: 2, Cost: 0.0009, Priced: true,
		Retried: true, JudgeCalls: 1, JudgeCost: 0.0002, RetryCost: 0.0005,
	}
	if bad {
		r.FirstViolations = []string{"stack"}
		r.DeliveredBad = []string{"stack"}
	}
	return r
}

// refusedRow is a cell the program refused: the model proposed the forbidden thing and
// none of its text was delivered.
func refusedRow(arm, scenario string, repeat int, text string) cellRow {
	r := row(arm, scenario, repeat, text, true)
	r.Refused = true
	r.Delivered = agent.RefusalText([]agent.Violation{{
		Name: "stack", About: "Стек только Kotlin и Ktor.",
		Detail: "вне разрешённого набора: Java", Enforce: agent.EnforceMachine,
	}})
	return r
}

func writeTestRows(t *testing.T, path string, rows []cellRow) {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func boolKey(bs ...bool) string {
	var b strings.Builder
	for _, v := range bs {
		if v {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}
