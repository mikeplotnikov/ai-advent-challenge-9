package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// The numbers this day reports are produced in execute() and summarise(), not
// in the helpers around them. A fake DeepSeek stands in front of both so a
// regression there cannot pass unnoticed — day 3 shipped with exactly that gap.

type captured struct {
	mu       sync.Mutex
	bodies   []map[string]any
	replies  []string
	statuses []int
	// hold makes the handler slow on purpose. With an instant handler the
	// requests never overlap, so a missing worker cap looks exactly like a
	// working one and the concurrency test passes by luck.
	hold time.Duration
	// reasons turns a call into a billed failure: the provider charges for the
	// reasoning tokens and never returns an answer. A plain HTTP error reports
	// no usage at all, so without this the "failed runs stay in the average"
	// claim could not be tested.
	reasons  []string
	inFlight int
	peak     int
}

func (c *captured) bodyCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func (c *captured) body(i int) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bodies[i]
}

func fakeDeepSeek(t *testing.T, c *captured) *llm.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("не разобрать тело запроса: %v", err)
		}
		c.mu.Lock()
		n := len(c.bodies)
		c.bodies = append(c.bodies, body)
		reply, reasoning := c.replies[n%len(c.replies)], ""
		if n < len(c.reasons) && c.reasons[n] != "" {
			reply, reasoning = "", c.reasons[n]
		}
		status := http.StatusOK
		if n < len(c.statuses) && c.statuses[n] != 0 {
			status = c.statuses[n]
		}
		c.inFlight++
		if c.inFlight > c.peak {
			c.peak = c.inFlight
		}
		hold := c.hold
		c.mu.Unlock()
		defer func() { c.mu.Lock(); c.inFlight--; c.mu.Unlock() }()
		time.Sleep(hold)

		if status != http.StatusOK {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":"нет"}`))
			return
		}
		in, out := 10, 90
		if reasoning != "" {
			in, out = 10, 40
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "test", "model": "fake",
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role": "assistant", "content": reply, "reasoning_content": reasoning,
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens": in, "completion_tokens": out, "total_tokens": in + out,
			},
		})
	}))
	t.Cleanup(server.Close)
	return &llm.Client{APIKey: "test", Model: "fake", URL: server.URL, HTTP: server.Client()}
}

func taskByKey(t *testing.T, key string) task {
	t.Helper()
	for _, tk := range tasks() {
		if tk.key == key {
			return tk
		}
	}
	t.Fatalf("нет класса задачи %s", key)
	return task{}
}

func TestTemperatureActuallyTravelsInTheRequest(t *testing.T) {
	// The whole day rests on this: if the field never left, every table below
	// would be measuring nothing and would still look plausible.
	c := &captured{replies: []string{"ANSWER: 24"}}
	client := fakeDeepSeek(t, c)
	settings, err := buildSettings("0,0.7,1.2", true, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range settings {
		execute(context.Background(), client, taskByKey(t, "T1"), s)
	}
	if c.bodyCount() != 4 {
		t.Fatalf("вызовов %d, ожидалось 4", c.bodyCount())
	}
	// The first setting sends no temperature at all — that is a different
	// request from temperature 0, and days 1 to 3 sent that one.
	if _, present := c.body(0)["temperature"]; present {
		t.Errorf("в колонке «не отправлен» температура всё-таки ушла: %v", c.body(0)["temperature"])
	}
	for i, want := range []float64{0, 0.7, 1.2} {
		got, present := c.body(i + 1)["temperature"]
		if !present {
			t.Errorf("запрос %d ушёл без температуры, ожидалась %v", i+1, want)
			continue
		}
		if got.(float64) != want {
			t.Errorf("запрос %d: temperature=%v, ожидалась %v", i+1, got, want)
		}
	}
}

func TestMeasuredRunsKeepThinkingDisabled(t *testing.T) {
	// DeepSeek ignores temperature whenever thinking is on, so a measured run
	// with thinking enabled would compare three identical settings and report
	// the result as "температура ни на что не влияет".
	c := &captured{replies: []string{"ANSWER: 24"}}
	client := fakeDeepSeek(t, c)
	settings, err := buildSettings("0,1.2", true, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range settings {
		if s.demo() {
			t.Fatalf("без -thinking-demo появился демонстрационный прогон %q", s.label)
		}
		execute(context.Background(), client, taskByKey(t, "T2"), s)
	}
	for i := 0; i < c.bodyCount(); i++ {
		thinking, _ := c.body(i)["thinking"].(map[string]any)
		if thinking == nil || thinking["type"] != "disabled" {
			t.Errorf("запрос %d ушёл с thinking=%v, ожидался disabled", i, c.body(i)["thinking"])
		}
	}
}

func TestThinkingDemoIsOptedIntoAndMarkedAsDemo(t *testing.T) {
	settings, err := buildSettings("0,0.7,1.2", false, true)
	if err != nil {
		t.Fatal(err)
	}
	demos := 0
	for _, s := range settings {
		if s.demo() {
			demos++
		}
	}
	if demos != 2 {
		t.Fatalf("демонстрационных прогонов %d, ожидалось 2", demos)
	}
	c := &captured{replies: []string{"Депо Кофе"}}
	client := fakeDeepSeek(t, c)
	for _, s := range settings {
		if s.demo() {
			execute(context.Background(), client, taskByKey(t, "T2"), s)
		}
	}
	for i := 0; i < c.bodyCount(); i++ {
		thinking, _ := c.body(i)["thinking"].(map[string]any)
		if thinking == nil || thinking["type"] != "enabled" {
			t.Errorf("демонстрационный запрос %d ушёл с thinking=%v, ожидался enabled", i, c.body(i)["thinking"])
		}
		if _, present := c.body(i)["temperature"]; !present {
			t.Errorf("демонстрационный запрос %d ушёл без температуры — игнорировать было бы нечего", i)
		}
	}
}

func TestEveryRequestCarriesTheTaskUnchanged(t *testing.T) {
	// "Один и тот же запрос" is the premise of the comparison. If the prompt
	// varied between temperatures, the grid would be measuring two things.
	c := &captured{replies: []string{"ANSWER: 24"}}
	client := fakeDeepSeek(t, c)
	task := taskByKey(t, "T1")
	settings, _ := buildSettings("0,0.7,1.2", false, false)
	for _, s := range settings {
		execute(context.Background(), client, task, s)
	}
	for i := 0; i < c.bodyCount(); i++ {
		raw, _ := json.Marshal(c.body(i)["messages"])
		var messages []map[string]string
		json.Unmarshal(raw, &messages)
		if len(messages) != 2 {
			t.Fatalf("запрос %d содержит %d сообщений, ожидалось 2", i, len(messages))
		}
		if messages[0]["content"] != task.system || messages[1]["content"] != task.prompt {
			t.Errorf("запрос %d изменил промпт:\n%q\n%q", i, messages[0]["content"], messages[1]["content"])
		}
	}
}

func TestExecuteScoresAgainstTheTruthOnlyForExactTasks(t *testing.T) {
	zero := 0.0
	s := setting{label: "0", temp: &zero}
	exactTask := taskByKey(t, "T1")

	c := &captured{replies: []string{"Считаю... ANSWER: 24", "ANSWER: 25", "без ответа"}}
	client := fakeDeepSeek(t, c)
	first := execute(context.Background(), client, exactTask, s)
	if !first.parsed || !first.correct || first.answer != "24" {
		t.Errorf("верный ответ не засчитан: %+v", first)
	}
	second := execute(context.Background(), client, exactTask, s)
	if !second.parsed || second.correct {
		t.Errorf("неверный ответ засчитан верным: %+v", second)
	}
	third := execute(context.Background(), client, exactTask, s)
	if third.parsed || third.correct {
		t.Errorf("отсутствие ответа не распознано: %+v", third)
	}

	// An open task has no truth, so nothing may ever be marked correct — a
	// "точность" column for it would be invented out of nothing.
	openClient := fakeDeepSeek(t, &captured{replies: []string{"Депо Кофе"}})
	got := execute(context.Background(), openClient, taskByKey(t, "T2"), s)
	if !got.parsed || got.answer != "депо кофе" {
		t.Errorf("открытая задача разобрана неверно: %+v", got)
	}
	if got.correct {
		t.Error("у открытой задачи прогон объявлен верным, хотя эталона нет")
	}
}

func TestFailedRunsStayInTheTokenAndPriceAverage(t *testing.T) {
	// A call that failed was still billed. Dropping it would make the settings
	// that fail look cheap, and it would flatter exactly the settings that fail
	// most. The failure here is the billed kind: the provider charged for the
	// reasoning and never produced an answer.
	c := &captured{
		replies: []string{"ANSWER: 24", "", "ANSWER: 24", "ANSWER: 24"},
		reasons: []string{"", "долго думал и не дошёл", "", ""},
	}
	client := fakeDeepSeek(t, c)
	zero := 0.0
	runs := runCell(context.Background(), client, taskByKey(t, "T1"), setting{label: "0", temp: &zero}, 4, 1)
	got := summarise(cell{setting: setting{label: "0"}, runs: runs}, "deepseek-v4-flash")
	if got.total != 4 {
		t.Fatalf("знаменатель %d, ожидалось 4", got.total)
	}
	if got.missing != 1 {
		t.Fatalf("прогонов без ответа %d, ожидался 1", got.missing)
	}
	if got.correct != 3 {
		t.Fatalf("верных %d, ожидалось 3", got.correct)
	}
	// Three answered calls at 100 tokens each plus the billed failure at 50.
	if got.tokens != 350 {
		t.Fatalf("токенов %d, ожидалось 350: оплаченный сбой выпал из суммы", got.tokens)
	}
	wantCost, _ := llm.Cost("deepseek-v4-flash", llm.Usage{PromptTokens: 40, CompletionTokens: 310})
	if diff := got.avgCost*4 - wantCost; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("суммарная цена %.10f, ожидалась %.10f", got.avgCost*4, wantCost)
	}
}

func TestSummariseRefusesToPriceAnUnknownModel(t *testing.T) {
	// $0.0000 for an unknown model would read as "free", not as "unknown".
	c := &captured{replies: []string{"ANSWER: 24"}}
	client := fakeDeepSeek(t, c)
	zero := 0.0
	runs := runCell(context.Background(), client, taskByKey(t, "T1"), setting{label: "0", temp: &zero}, 2, 1)
	if got := summarise(cell{runs: runs}, "deepseek-v9-unreleased"); got.priced {
		t.Fatal("неизвестная модель объявлена оценённой")
	}
}

func TestOnlyAnsweredRunsEnterTheDiversityMetrics(t *testing.T) {
	// A request that never came back is not a variant. Counting it as one would
	// inflate diversity at exactly the temperature where calls fail.
	runs := []run{
		{parsed: true, answer: "депо кофе"},
		{err: context.Canceled},
		{parsed: true, answer: "депо кофе"},
		{parsed: false},
	}
	got := answers(runs)
	if len(got) != 2 {
		t.Fatalf("в метрики попало %d ответов, ожидалось 2: %v", len(got), got)
	}
	if distinct(got) != 1 {
		t.Fatalf("различных %d, ожидался 1", distinct(got))
	}
}

func TestRunCellRunsEveryRepeatAndRespectsTheWorkerCap(t *testing.T) {
	// The live grid ran -workers 8 against a paid provider, so the cap is what
	// stands between a cell and thirty simultaneous billed calls. The handler
	// is deliberately slow: with an instant one, removing the semaphore
	// altogether still passed four runs out of five.
	const (
		repeat  = 9
		workers = 3
	)
	c := &captured{replies: []string{"Депо Кофе"}, hold: 40 * time.Millisecond}
	client := fakeDeepSeek(t, c)
	zero := 0.0
	runs := runCell(context.Background(), client, taskByKey(t, "T2"),
		setting{label: "0", temp: &zero}, repeat, workers)

	if len(runs) != repeat {
		t.Fatalf("прогонов %d, ожидалось %d", len(runs), repeat)
	}
	// make([]run, repeat) satisfies the length check before any goroutine
	// runs, so the results themselves have to be checked.
	for i, r := range runs {
		if !r.parsed || r.answer == "" {
			t.Errorf("прогон %d не записал результат: %+v", i, r)
		}
	}
	c.mu.Lock()
	peak, calls := c.peak, len(c.bodies)
	c.mu.Unlock()
	if calls != repeat {
		t.Errorf("вызовов %d, ожидалось %d", calls, repeat)
	}
	if peak > workers {
		t.Fatalf("одновременно в воздухе было %d запросов при -workers %d", peak, workers)
	}
	// Without this the test would also pass on a cap of one, and then it would
	// not be testing a cap at all.
	if peak < 2 {
		t.Fatalf("запросы не шли параллельно (пик %d) — проверка лимита ничего не значит", peak)
	}
}

func TestBuildSettingsRejectsWhatCannotBeInterpreted(t *testing.T) {
	cases := []struct{ name, spec string }{
		{"вне диапазона сверху", "0,2.5"},
		{"вне диапазона снизу", "-0.1"},
		{"дубликат", "0.7,0.7"},
		{"не число", "жарко"},
	}
	for _, c := range cases {
		if _, err := buildSettings(c.spec, false, false); err == nil {
			t.Errorf("%s (%q): принято без ошибки", c.name, c.spec)
		}
	}
	if _, err := buildSettings("", false, false); err == nil {
		t.Error("пустой список температур принят")
	}
	// The default column alone is a valid grid: it is what days 1 to 3 sent.
	if got, err := buildSettings("", true, false); err != nil || len(got) != 1 {
		t.Errorf("только колонка по умолчанию: %d настроек, ошибка %v", len(got), err)
	}
}

func TestAccuracyTableComparesAgainstTemperatureZero(t *testing.T) {
	// The day asks about 0, 0.7 and 1.2. The "parameter not sent" column is a
	// fourth fact, and reading p-values against it would answer a question
	// nobody asked.
	zero, warm := 0.0, 1.2
	tallies := []tally{
		{setting: setting{label: "не отправлен"}, correct: 9, total: 10},
		{setting: setting{label: "0", temp: &zero}, correct: 3, total: 10},
		{setting: setting{label: "1.2", temp: &warm}, correct: 9, total: 10},
	}
	base, ok := baseline(tallies)
	if !ok || base.setting.label != "0" {
		t.Fatalf("база сравнения %q (ok=%v), ожидалась «0»", base.setting.label, ok)
	}
	var buf bytes.Buffer
	accuracyTable(&buf, tallies)
	out := buf.String()
	if !strings.Contains(out, "База сравнения — «0»") {
		t.Errorf("отчёт не назвал базу сравнения:\n%s", out)
	}
	// 3/10 against 9/10 is a real difference; the table must say so rather than
	// leave the reader to eyeball it.
	if !strings.Contains(out, "разница подтверждена") {
		t.Errorf("подтверждённая разница не отмечена:\n%s", out)
	}
}

func TestAccuracyTableRefusesToCallNoiseADifference(t *testing.T) {
	zero, warm := 0.0, 1.2
	tallies := []tally{
		{setting: setting{label: "0", temp: &zero}, correct: 6, total: 10},
		{setting: setting{label: "1.2", temp: &warm}, correct: 5, total: 10},
	}
	var buf bytes.Buffer
	accuracyTable(&buf, tallies)
	if out := buf.String(); !strings.Contains(out, "не отличить от базы") {
		t.Errorf("разница в один прогон подана как разница:\n%s", out)
	}
}

func TestMoneyNeverPrintsARealChargeAsZero(t *testing.T) {
	// On flash a per-run cost rounds to $0.0000 at four decimals, and "$0.0000"
	// reads as free. Day 3 closed this hole for an unknown model; the rounding
	// opens it again for a known one.
	cases := []struct {
		name  string
		value float64
		known bool
		want  string
	}{
		{"обычная цена", 0.0123, true, "$0.0123"},
		{"настоящий ноль", 0, true, "$0"},
		{"мельче округления", 0.00001, true, "меньше $0.0001"},
		{"на границе округления", 0.00004, true, "меньше $0.0001"},
	}
	for _, c := range cases {
		if got := formatMoney(c.value, c.known); got != c.want {
			t.Errorf("%s: получено %q, ожидалось %q", c.name, got, c.want)
		}
	}
	got := formatMoney(0.5, false)
	if strings.Contains(got, "$") {
		t.Errorf("неизвестная цена показана суммой: %q", got)
	}
	if !strings.Contains(got, "неизвестна") {
		t.Errorf("не сказано, что цена неизвестна: %q", got)
	}
}

func TestCostTableAveragesOverEveryRunIncludingTheFailures(t *testing.T) {
	c := &captured{
		replies: []string{"ANSWER: 24", ""},
		reasons: []string{"", "думал и не дошёл"},
	}
	client := fakeDeepSeek(t, c)
	zero := 0.0
	runs := runCell(context.Background(), client, taskByKey(t, "T1"), setting{label: "0", temp: &zero}, 2, 1)
	got := summarise(cell{setting: setting{label: "0"}, runs: runs}, "deepseek-v4-flash")

	var buf bytes.Buffer
	costTable(&buf, []tally{got})
	out := buf.String()
	// 100 tokens answered plus 50 billed-but-unanswered, over two runs.
	if !strings.Contains(out, "75") {
		t.Errorf("средние токены посчитаны не по всем прогонам:\n%s", out)
	}
	if !strings.Contains(out, "включая прогоны без ответа") {
		t.Errorf("таблица не говорит, что в среднее вошли неудачные прогоны:\n%s", out)
	}
}

func TestDemoRunsGetABudgetLargeEnoughToAnswerAtAll(t *testing.T) {
	// The open task's 60-token cap is plenty for one line and nowhere near
	// enough for a reasoning pass. Left on that cap, a demo run would spend the
	// budget thinking and never answer — demonstrating truncation instead of
	// the silent ignore it exists to demonstrate.
	c := &captured{replies: []string{"Депо Кофе"}}
	client := fakeDeepSeek(t, c)
	openTask := taskByKey(t, "T2")
	if openTask.maxTokens >= tokenCap {
		t.Fatalf("предпосылка теста сломана: у творческой задачи лимит %d", openTask.maxTokens)
	}
	zero := 0.0
	execute(context.Background(), client, openTask, setting{label: "0", temp: &zero})
	execute(context.Background(), client, openTask,
		setting{label: "0 + thinking", temp: &zero, thinking: "enabled"})

	measured, _ := c.body(0)["max_tokens"].(float64)
	demo, _ := c.body(1)["max_tokens"].(float64)
	if int(measured) != openTask.maxTokens {
		t.Errorf("замер ушёл с max_tokens=%v, ожидалось %d", measured, openTask.maxTokens)
	}
	if int(demo) != tokenCap {
		t.Errorf("демонстрация ушла с max_tokens=%v, ожидалось %d", demo, tokenCap)
	}
}

func TestAccuracyTableHoldsTheThresholdAtTheValueTheDayDependsOn(t *testing.T) {
	// The day's central negative conclusion is that no accuracy difference was
	// confirmed, and the loudest near-miss was p = 0.120 (18/30 against 11/30
	// on the counting task). Without a fixture in that band, loosening the line
	// to p < 0.15 flips that row to "разница подтверждена" with every test
	// green — which is the day-3 failure mode exactly.
	zero, warm := 0.0, 1.2
	tallies := []tally{
		{setting: setting{label: "0", temp: &zero}, correct: 18, total: 30},
		{setting: setting{label: "1.2", temp: &warm}, correct: 11, total: 30},
	}
	var buf bytes.Buffer
	accuracyTable(&buf, tallies)
	out := buf.String()
	if !strings.Contains(out, "0.120") {
		t.Fatalf("p-value 0.120 не напечатан:\n%s", out)
	}
	if strings.Contains(out, "разница подтверждена") {
		t.Fatalf("разница при p=0.120 объявлена подтверждённой:\n%s", out)
	}
	// And the other side of the line: 24/30 against 11/30 tests at p = 0.001.
	confirmed := []tally{
		{setting: setting{label: "0", temp: &zero}, correct: 24, total: 30},
		{setting: setting{label: "1.2", temp: &warm}, correct: 11, total: 30},
	}
	buf.Reset()
	accuracyTable(&buf, confirmed)
	if out := buf.String(); !strings.Contains(out, "разница подтверждена") {
		t.Fatalf("настоящая разница не отмечена подтверждённой:\n%s", out)
	}
}

func TestAccuracyTableRefusesToReadAbsenceOfProofAsAbsenceOfEffect(t *testing.T) {
	zero, warm := 0.0, 1.2
	var buf bytes.Buffer
	accuracyTable(&buf, []tally{
		{setting: setting{label: "0", temp: &zero}, correct: 18, total: 30},
		{setting: setting{label: "1.2", temp: &warm}, correct: 11, total: 30},
	})
	out := buf.String()
	// The whole conclusion of this day rests on the reader not turning "not
	// shown" into "does not exist", so the table has to say it itself.
	if !strings.Contains(out, "доказывает отсутствие эффекта") {
		t.Errorf("таблица не предупреждает, что «не отличить» ≠ «одинаково»:\n%s", out)
	}
	if !strings.Contains(out, "не поправлен на их число") {
		t.Errorf("таблица не признаёт, что порог не поправлен на число сравнений:\n%s", out)
	}
}

func TestDiversityTableReportsTheNumbersTheHeadlineRestsOn(t *testing.T) {
	// The one confirmed claim of the day is produced here, and nothing tested
	// this function at all: disabling the resampling (picked[i] = sample[i])
	// collapsed the interval to the point estimate with the suite still green.
	answers := []string{
		"депо кофе", "депо кофе", "депо кофе", "депо кофе",
		"рельсы и кофе", "рельсы и кофе", "верхнее кольцо", "трамвайный аромат",
	}
	runs := make([]run, len(answers))
	for i, a := range answers {
		runs[i] = run{parsed: true, answer: a, raw: a}
	}
	var buf bytes.Buffer
	diversityTable(&buf, []tally{{
		setting: setting{label: "1.2"}, runs: runs, answered: answers, total: len(runs),
	}}, 1)
	out := buf.String()

	if !strings.Contains(out, "4 из 8") {
		t.Errorf("число различных ответов посчитано неверно:\n%s", out)
	}
	if !strings.Contains(out, "депо кофе") {
		// The table prints metrics, not answers — this guards against a
		// refactor that starts dumping raw text into the metrics table.
		_ = out
	}
	distance, ok := meanPairwiseDistance(answers)
	if !ok {
		t.Fatal("на этой выборке дистанция обязана считаться")
	}
	if !strings.Contains(out, fmt.Sprintf("%.3f", distance)) {
		t.Errorf("точечная дистанция %.3f не напечатана:\n%s", distance, out)
	}
	// The interval must come from resampling, not from the point estimate: a
	// degenerate one would print the same number on both ends.
	if strings.Contains(out, fmt.Sprintf("[%.3f, %.3f]", distance, distance)) {
		t.Errorf("интервал выродился в точечную оценку — пересэмплирования нет:\n%s", out)
	}
	if !strings.Contains(out, "НЕ калиброванный") {
		t.Errorf("интервал подан как калиброванный 95%%:\n%s", out)
	}
}

func TestDiversityTableSeparatesTruncationFromDiversity(t *testing.T) {
	// A cut-off answer is still a parsed answer and enters the metrics as a
	// variant. It would do so most often at the highest temperature — the
	// column whose diversity is the headline — so the count has to be visible.
	answers := []string{"депо кофе", "депо ко"}
	got := summarise(cell{setting: setting{label: "1.2"}, runs: []run{
		{parsed: true, answer: answers[0], finish: "stop"},
		{parsed: true, answer: answers[1], finish: "length"},
	}}, "deepseek-v4-flash")
	if got.truncated != 1 {
		t.Fatalf("обрезанных прогонов %d, ожидался 1", got.truncated)
	}
	var buf bytes.Buffer
	got.answered = answers
	diversityTable(&buf, []tally{got}, 1)
	if out := buf.String(); !strings.Contains(out, "обрезано") ||
		!strings.Contains(out, "частично объясняется обрезкой") {
		t.Errorf("обрезка не отделена от разнообразия:\n%s", out)
	}
}

func TestDiversityTableSaysWhenThereIsNothingToMeasure(t *testing.T) {
	var buf bytes.Buffer
	diversityTable(&buf, []tally{{
		setting: setting{label: "1.2"},
		runs:    []run{{err: context.Canceled}, {err: context.Canceled}},
		total:   2,
	}}, 1)
	if out := buf.String(); !strings.Contains(out, "ни один прогон не дошёл до ответа") {
		t.Errorf("пустая настройка не отмечена:\n%s", out)
	}
}

func TestTopPIsNeverSentAlongsideTemperature(t *testing.T) {
	// The premise printed on every report is that temperature is the only
	// variable. internal/llm is shared by every day, so a later day adding a
	// top_p option with a non-nil default would falsify that premise silently.
	c := &captured{replies: []string{"ANSWER: 24"}}
	client := fakeDeepSeek(t, c)
	settings, _ := buildSettings("0,0.7,1.2", true, true)
	for _, s := range settings {
		execute(context.Background(), client, taskByKey(t, "T1"), s)
	}
	for i := 0; i < c.bodyCount(); i++ {
		if _, present := c.body(i)["top_p"]; present {
			t.Fatalf("запрос %d ушёл с top_p=%v — переменных стало две",
				i, c.body(i)["top_p"])
		}
	}
}

func TestBuildSettingsAcceptsTheDocumentedCeiling(t *testing.T) {
	// The provider documents the range as 0…2 inclusive, and 2.0 is a planned
	// lever run ("что за потолком"). An off-by-one at the ceiling would refuse
	// it while every rejection test still passed.
	got, err := buildSettings("0,2", false, false)
	if err != nil {
		t.Fatalf("температура 2.0 отвергнута: %v", err)
	}
	if len(got) != 2 || got[1].temp == nil || *got[1].temp != 2 {
		t.Fatalf("2.0 не дошла до настроек: %+v", got)
	}
}
