package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
		c.mu.Unlock()
		defer func() { c.mu.Lock(); c.inFlight--; c.mu.Unlock() }()

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
	c := &captured{replies: []string{"Депо Кофе"}}
	client := fakeDeepSeek(t, c)
	zero := 0.0
	runs := runCell(context.Background(), client, taskByKey(t, "T2"), setting{label: "0", temp: &zero}, 9, 3)
	if len(runs) != 9 {
		t.Fatalf("прогонов %d, ожидалось 9", len(runs))
	}
	c.mu.Lock()
	peak := c.peak
	c.mu.Unlock()
	if peak > 3 {
		t.Fatalf("одновременно в воздухе было %d запросов при -workers 3", peak)
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
