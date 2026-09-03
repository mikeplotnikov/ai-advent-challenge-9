package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// reportTask is where it is decided which numbers get printed about which class
// of task, and which rows are measurements versus the thinking demo. Every
// table below it was tested in isolation and the composition was not, so three
// mutations of exactly that decision passed the whole suite: an accuracy table
// for the task that has no truth, thinking-enabled rows folded into the
// measured tables, and judge calls billed on the exact tasks.

func reportOf(t *testing.T, task task, cells []cell, judgePasses int) string {
	t.Helper()
	client := fakeDeepSeek(t, &captured{replies: []string{"1: 3\n2: 3"}})
	var buf bytes.Buffer
	reportTask(context.Background(), &buf, client, task, cells, "deepseek-v4-flash", 1, judgePasses, false)
	return buf.String()
}

func cellsFor(t *testing.T, task task, withDemo bool) []cell {
	t.Helper()
	settings, err := buildSettings("0,1.2", true, withDemo)
	if err != nil {
		t.Fatal(err)
	}
	answer, correct := "депо кофе", false
	if task.kind == exact {
		answer, correct = task.truth, true
	}
	out := make([]cell, 0, len(settings))
	for _, s := range settings {
		out = append(out, cell{setting: s, runs: []run{
			{parsed: true, answer: answer, correct: correct, raw: answer, finish: "stop"},
			{parsed: true, answer: answer, correct: correct, raw: answer, finish: "stop"},
		}})
	}
	return out
}

func TestReportPrintsNoAccuracyForTheTaskWithoutATruth(t *testing.T) {
	// An accuracy column for the creative task would be invented out of
	// nothing: there is no answer to be right about.
	openTask := taskByKey(t, "T2")
	out := reportOf(t, openTask, cellsFor(t, openTask, false), 0)
	if strings.Contains(out, "ТОЧНОСТЬ") {
		t.Errorf("у творческой задачи напечатана таблица точности:\n%s", out)
	}
	if strings.Contains(out, "Эталон:") {
		t.Errorf("у творческой задачи напечатан эталон:\n%s", out)
	}
	if !strings.Contains(out, "РАЗНООБРАЗИЕ") {
		t.Errorf("у творческой задачи нет таблицы разнообразия:\n%s", out)
	}

	exactTask := taskByKey(t, "T1")
	out = reportOf(t, exactTask, cellsFor(t, exactTask, false), 0)
	if !strings.Contains(out, "ТОЧНОСТЬ") || !strings.Contains(out, "Эталон: "+exactTask.truth) {
		t.Errorf("у счётной задачи нет точности или эталона:\n%s", out)
	}
}

func TestReportKeepsTheThinkingDemoOutOfTheMeasuredTables(t *testing.T) {
	// The premise printed on every report is that the measured rows differ only
	// in temperature. A thinking-enabled row among them would break exactly
	// that, and its diversity would be read as a temperature effect.
	openTask := taskByKey(t, "T2")
	out := reportOf(t, openTask, cellsFor(t, openTask, true), 0)

	demoAt := strings.Index(out, "ДЕМОНСТРАЦИЯ")
	if demoAt < 0 {
		t.Fatalf("демонстрационная секция не напечатана:\n%s", out)
	}
	measured, demo := out[:demoAt], out[demoAt:]
	for _, label := range []string{"0 + thinking", "1.2 + thinking"} {
		if strings.Contains(measured, label) {
			t.Errorf("настройка %q попала в измеренные таблицы:\n%s", label, measured)
		}
		if !strings.Contains(demo, label) {
			t.Errorf("настройка %q отсутствует в демонстрации:\n%s", label, demo)
		}
	}
	// And the measured half must still carry the three plain rows.
	for _, label := range []string{"не отправлен", "0 ", "1.2 "} {
		if !strings.Contains(measured, label) {
			t.Errorf("измеренная строка %q пропала:\n%s", label, measured)
		}
	}
	if !strings.Contains(demo, "api-docs.deepseek.com/guides/thinking_mode") {
		t.Errorf("демонстрация не ссылается на документацию поставщика:\n%s", demo)
	}
}

func TestReportRunsTheJudgeOnlyWhereThereIsNothingToScoreAgainst(t *testing.T) {
	// A judge pass costs money and says nothing useful about a task that has a
	// computable answer — the truth already scores those.
	exactTask := taskByKey(t, "T1")
	c := &captured{replies: []string{"1: 3\n2: 3"}}
	client := fakeDeepSeek(t, c)
	var buf bytes.Buffer
	reportTask(context.Background(), &buf, client, exactTask,
		cellsFor(t, exactTask, false), "deepseek-v4-flash", 1, 3, false)
	if c.bodyCount() != 0 {
		t.Errorf("судья вызван %d раз для задачи с эталоном", c.bodyCount())
	}
	if strings.Contains(buf.String(), "ОЦЕНКА СУДЬИ") {
		t.Errorf("судейская секция напечатана для задачи с эталоном:\n%s", buf.String())
	}

	openTask := taskByKey(t, "T2")
	c2 := &captured{replies: []string{"1: 3\n2: 3"}}
	client2 := fakeDeepSeek(t, c2)
	buf.Reset()
	reportTask(context.Background(), &buf, client2, openTask,
		cellsFor(t, openTask, false), "deepseek-v4-flash", 1, 2, false)
	if c2.bodyCount() != 2 {
		t.Errorf("проходов судьи %d, ожидалось 2", c2.bodyCount())
	}
	if !strings.Contains(buf.String(), "ОЦЕНКА СУДЬИ") {
		t.Errorf("судейская секция не напечатана для задачи без эталона:\n%s", buf.String())
	}

	// -judge-runs 0 must mean no judge at all, on any class.
	c3 := &captured{replies: []string{"1: 3"}}
	client3 := fakeDeepSeek(t, c3)
	buf.Reset()
	reportTask(context.Background(), &buf, client3, openTask,
		cellsFor(t, openTask, false), "deepseek-v4-flash", 1, 0, false)
	if c3.bodyCount() != 0 {
		t.Errorf("при -judge-runs 0 судья всё равно вызван %d раз", c3.bodyCount())
	}
}

func TestReportAlwaysPrintsTheAnswersOfTheOpenTask(t *testing.T) {
	// The task asks for examples of answers, and for the open class the answers
	// are the whole result — they must not depend on -full.
	openTask := taskByKey(t, "T2")
	out := reportOf(t, openTask, cellsFor(t, openTask, false), 0)
	if !strings.Contains(out, "ОТВЕТЫ") || !strings.Contains(out, "депо кофе") {
		t.Errorf("ответы творческой задачи не напечатаны:\n%s", out)
	}
}

func TestDiversityTablePrintsTheTruncationCountItself(t *testing.T) {
	// The legend lines are unconditional, so asserting them proves nothing about
	// the column: printing a literal "0" there passed the earlier test.
	answers := []string{"депо кофе", "депо ко", "депо к"}
	got := summarise(cell{setting: setting{label: "1.2"}, runs: []run{
		{parsed: true, answer: answers[0], finish: "stop"},
		{parsed: true, answer: answers[1], finish: "length"},
		{parsed: true, answer: answers[2], finish: "length"},
	}}, "deepseek-v4-flash")
	got.answered = answers

	var buf bytes.Buffer
	diversityTable(&buf, []tally{got}, 1)
	row := firstDataRow(t, buf.String(), "1.2")
	if !strings.HasSuffix(strings.TrimSpace(row), "2") {
		t.Fatalf("в строке не напечатано число обрезанных прогонов (ожидалось 2):\n%s", row)
	}
}

func TestDiversityIntervalDoesNotWobbleWithTheSeed(t *testing.T) {
	// Wave 2 noted that hard-coding the seed at the call site survives the
	// suite. It does — and that is the property to assert, not a hole to plug:
	// at bootstrapResamples the printed interval must be the same whatever seed
	// produced it, or two runs of the same report would disagree in print and
	// nobody could check a number from the video afterwards. The seed still
	// matters to BootstrapCI itself, and internal/stats pins that.
	answers := []string{"депо кофе", "рельсы и кофе", "верхнее кольцо", "трамвайный аромат", "колея"}
	runs := make([]run, len(answers))
	for i, a := range answers {
		runs[i] = run{parsed: true, answer: a, raw: a, finish: "stop"}
	}
	render := func(seed int64) string {
		var buf bytes.Buffer
		diversityTable(&buf, []tally{{
			setting: setting{label: "1.2"}, runs: runs, answered: answers, total: len(runs),
		}}, seed)
		return firstDataRow(t, buf.String(), "1.2")
	}
	first := render(1)
	for _, seed := range []int64{1, 7, 99, 12345} {
		if got := render(seed); got != first {
			t.Fatalf("при %d пересэмплированиях интервал поплыл от seed %d:\n%s\n%s",
				bootstrapResamples, seed, first, got)
		}
	}
	// And the interval must be a real one, not a collapsed point: the check
	// above would also pass if the bootstrap returned zeros every time.
	if strings.Contains(first, "[0.000, 0.000]") {
		t.Fatalf("интервал выродился:\n%s", first)
	}
}

func TestAccuracyTableMarksRunsThatNeverAnswered(t *testing.T) {
	zero := 0.0
	var buf bytes.Buffer
	accuracyTable(&buf, []tally{
		{setting: setting{label: "0", temp: &zero}, correct: 18, total: 30, missing: 4},
	})
	row := firstDataRow(t, buf.String(), "0")
	if !strings.Contains(row, "18/30!") {
		t.Fatalf("прогоны без ответа не отмечены знаком «!»:\n%s", row)
	}
}

func TestJudgeSectionPrintsEachPassSeparately(t *testing.T) {
	// The per-pass column is the quantity the day's creativity non-conclusion is
	// weighed against, so it has to be the real per-pass means and not a
	// constant: every earlier fixture returned identical scores on every pass.
	c := &captured{replies: []string{"1: 1\n2: 1", "1: 5\n2: 5"}}
	client := fakeDeepSeek(t, c)
	var buf bytes.Buffer
	judgeSection(context.Background(), &buf, client, []tally{
		{setting: setting{label: "0"}, answered: []string{"депо кофе", "рельсы и кофе"}, total: 2},
	}, "deepseek-v4-flash", 1, 2)
	out := buf.String()
	// Two passes scoring 1 and 5 average to 3, and both must be visible.
	if !strings.Contains(out, "3.00 из 5") {
		t.Errorf("средняя по проходам посчитана неверно:\n%s", out)
	}
	if !strings.Contains(out, "1.00 · 5.00") {
		t.Errorf("проходы не напечатаны по отдельности:\n%s", out)
	}
}

func TestJudgeSectionPrintsHowMuchOfTheSettingWasScored(t *testing.T) {
	// A judge that scores half the slate must show it, and the coverage cell has
	// to be the maximum over passes rather than their sum.
	c := &captured{replies: []string{"1: 4", "1: 4"}}
	client := fakeDeepSeek(t, c)
	var buf bytes.Buffer
	judgeSection(context.Background(), &buf, client, []tally{
		{setting: setting{label: "0"}, answered: []string{"депо кофе", "рельсы и кофе"}, total: 2},
	}, "deepseek-v4-flash", 1, 2)
	out := buf.String()
	if !strings.Contains(out, "1 из 2") {
		t.Fatalf("охват напечатан неверно (ожидалось «1 из 2»):\n%s", out)
	}
}

// firstDataRow returns the table row whose first column is label, so a test can
// assert a single cell instead of matching anywhere in the whole report.
func firstDataRow(t *testing.T, out, label string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, label+" ") {
			return line
		}
	}
	t.Fatalf("в отчёте нет строки для настройки %q:\n%s", label, out)
	return ""
}
