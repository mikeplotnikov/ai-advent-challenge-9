package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcprouter"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

func TestBenchScenariosAndRelativeDates(t *testing.T) {
	scenarios, err := loadBenchScenarios()
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 18 {
		t.Fatalf("count=%d", len(scenarios))
	}
	if benchRepeats != 3 {
		t.Fatalf("repeats=%d", benchRepeats)
	}
	if scenarios[0].Pipelines[0].DateFrom != "2026-09-12" || scenarios[0].Pipelines[0].DateTo != benchToday {
		t.Fatalf("L1=%+v", scenarios[0])
	}
	if scenarios[4].Conversions[0].Date != "2026-09-18" {
		t.Fatalf("week ago=%+v", scenarios[4])
	}
	if scenarios[8].Conversions[0].Date != "2026-09-24" {
		t.Fatalf("yesterday=%+v", scenarios[8])
	}
	if scenarios[11].Pipelines[0].DateFrom != "2026-08-27" {
		t.Fatalf("30 days=%+v", scenarios[11])
	}
}

func TestProductionBenchExecutionIs18By3_AC15(t *testing.T) {
	config, err := productionBenchExecution(time.Second, nil, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.scenarios) != 18 || config.repeats != 3 {
		t.Fatalf("production workload=%d x %d", len(config.scenarios), config.repeats)
	}
}

func TestBenchRestartPredicate_AC15(t *testing.T) {
	for _, outcome := range []string{"timeout", "unavailable", "dead"} {
		if !needsRegistryRestart([]mcprouter.JournalEntry{{Outcome: outcome}}) {
			t.Errorf("%s did not request restart", outcome)
		}
	}
	for _, outcome := range []string{"ok", "tool_error", "rpc_error", "limit"} {
		if needsRegistryRestart([]mcprouter.JournalEntry{{Outcome: outcome}}) {
			t.Errorf("%s unexpectedly requested restart", outcome)
		}
	}
}

func TestArgsExactCodesRule_AC14(t *testing.T) {
	s := BenchScenario{Rates: []ExpectedRate{{Currency: "USD", Date: "2026-09-18"}}}
	makeTrace := func(codes any, include bool) []toolagent.ToolCall {
		args := map[string]any{"date": "2026-09-18"}
		if include {
			args["codes"] = codes
		}
		return []toolagent.ToolCall{{Name: "rates__get_currency_rates", Arguments: args}}
	}
	for _, calls := range [][]toolagent.ToolCall{makeTrace(nil, false), makeTrace([]any{}, true), makeTrace([]any{"USD"}, true), makeTrace([]any{"usd", "EUR"}, true)} {
		if !argsExact(s, calls) {
			t.Fatalf("should pass: %#v", calls)
		}
	}
	if argsExact(s, makeTrace([]any{"EUR"}, true)) {
		t.Fatal("codes without USD accepted")
	}
}

func TestAnswerNumberCalibration_AC14(t *testing.T) {
	passes := []struct {
		text             string
		value, tolerance float64
	}{
		{"1 234,56", 1234.56, .5}, {"1234.56", 1234.56, .5}, {"1 234 руб.", 1234, .5},
		{"84,40", 84.3969, .01}, {"84.3969", 84.3969, .01},
	}
	for _, test := range passes {
		if !answerHasNearNumber(test.text, test.value, test.tolerance) {
			t.Errorf("did not parse %q", test.text)
		}
	}
	for _, text := range []string{"83,20", "85,40"} {
		if answerHasNearNumber(text, 84.3969, .01) {
			t.Errorf("accepted %q", text)
		}
	}
	if answerHasToday("24 сентября", benchToday) {
		t.Fatal("wrong day accepted")
	}
}

func TestClassifierUsesJournalForFailuresAndExclusions(t *testing.T) {
	s := BenchScenario{ID: "S1", Group: "S", Question: "Какое сегодня число и день недели?", Servers: []string{"clock"}, DateOnly: true}
	trace := toolagent.Trace{FinalAnswer: "Сегодня 25 сентября, пятница.", ToolCalls: []toolagent.ToolCall{{Step: 1, Name: "clock__current_date", ResultText: `{"date":"2026-09-25"}`}}}
	servers := []mcprouter.Server{{Alias: "clock", ToolNames: []string{"clock__current_date"}}}
	journal := []mcprouter.JournalEntry{{Name: "clock__current_date", Alias: "clock", Outcome: "ok"}}
	checks := evaluateChecks(s.Question, trace, servers, journal)
	valid := evaluateBench(s, Day20Trace{Trace: trace, Servers: servers, Journal: journal, Checks: checks})
	if !valid.FlowOK {
		t.Fatalf("valid=%+v", valid)
	}
	foreign := journal
	foreign[0].Alias = "rates"
	checks = evaluateChecks(s.Question, trace, servers, foreign)
	broken := evaluateBench(s, Day20Trace{Trace: trace, Servers: servers, Journal: foreign, Checks: checks})
	if broken.RoutedOK || broken.FlowOK {
		t.Fatalf("foreign=%+v", broken)
	}
	cbrJournal := []mcprouter.JournalEntry{{Name: "clock__current_date", Alias: "clock", Outcome: "tool_error", Text: "ЦБ недоступен: timeout"}}
	excluded := evaluateBench(s, Day20Trace{Trace: trace, Servers: servers, Journal: cbrJournal, Checks: checks})
	if excluded.Excluded != "cbrFailure" {
		t.Fatalf("excluded=%+v", excluded)
	}
	normal := cbrJournal
	normal[0].Text = "неизвестный код валюты"
	if got := evaluateBench(s, Day20Trace{Trace: trace, Servers: servers, Journal: normal, Checks: checks}).Excluded; got != "" {
		t.Fatalf("ordinary tool error excluded as %q", got)
	}
}

func TestBenchCountersAreExact_AC14(t *testing.T) {
	wrapped := Day20Trace{
		Trace: toolagent.Trace{ToolCalls: []toolagent.ToolCall{
			{Name: "extra__one"}, {Name: "extra__two"}, {Name: "extra__three"},
			{Name: "missing__tool", Rejected: "инструмент не существует"},
		}},
		Journal: []mcprouter.JournalEntry{
			{Outcome: "timeout"}, {Outcome: "unavailable"}, {Outcome: "limit"}, {Outcome: "dead"},
		},
		Checks: Checks{OrderOK: true, RoutedOK: true, ProvenanceStrict: true},
	}
	verdict := evaluateBench(BenchScenario{}, wrapped)
	if verdict.ServerFailures != 2 || verdict.RejectedCalls != 3 || verdict.ExtraCalls != 4 {
		t.Fatalf("serverFailures=%d rejectedCalls=%d extraCalls=%d verdict=%+v",
			verdict.ServerFailures, verdict.RejectedCalls, verdict.ExtraCalls, verdict)
	}
}

func TestBenchResultsFormatsExclusionsAndDisclosesPromptCalibration(t *testing.T) {
	const wantDisclosure = "Промпт уточнён по сценариям L1, S3 и S4 этого же замера (decisions.md дня 20), поэтому замер — не отложенная выборка."
	if benchPromptDisclosure != wantDisclosure {
		t.Fatalf("bench disclosure changed\n got: %q\nwant: %q", benchPromptDisclosure, wantDisclosure)
	}
	path := filepath.Join(t.TempDir(), "RESULTS.md")
	report := BenchReport{Revision: "rev", Started: "2026-09-26T12:00:00+03:00", Today: benchToday,
		Runs: []BenchRun{{Verdict: BenchVerdict{}}}}
	if err := writeBenchResults(path, report); err != nil {
		t.Fatal(err)
	}
	results, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := "# День 20 — результаты\n\n" + wantDisclosure + "\n\nДата:"
	if !strings.HasPrefix(string(results), wantPrefix) ||
		!strings.Contains(string(results), "Исключения: нет. Обращения к модели:") {
		t.Fatalf("empty exclusions or disclosure missing:\n%s", results)
	}

	report.Runs = []BenchRun{
		{Verdict: BenchVerdict{Excluded: "unavailable"}},
		{Verdict: BenchVerdict{Excluded: "failedControl"}},
		{Verdict: BenchVerdict{Excluded: "unavailable"}},
	}
	if err := writeBenchResults(path, report); err != nil {
		t.Fatal(err)
	}
	results, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(results), "Исключения: failedControl — 1, unavailable — 2. Обращения к модели:") ||
		strings.Contains(string(results), "map[") {
		t.Fatalf("non-empty exclusions are not stable prose:\n%s", results)
	}
}

func TestBenchClassifierMutationMatrix_AC14(t *testing.T) {
	scenario, wrapped := validL1ClassifierFixture()
	valid := evaluateBench(scenario, wrapped)
	if !valid.FlowOK || !valid.ProvenanceStrict {
		t.Fatalf("valid fixture: %+v", valid)
	}
	t.Run("extra_server_serversExact_only", func(t *testing.T) {
		got := cloneWrapped(wrapped)
		got.Servers = append(got.Servers, mcprouter.Server{Alias: "extra", ToolNames: []string{"extra__ping"}})
		got.Journal = append(got.Journal, mcprouter.JournalEntry{Name: "extra__ping", Alias: "extra", Outcome: "ok"})
		got.Checks = evaluateChecks(scenario.Question, got.Trace, got.Servers, got.Journal)
		assertOnlyCriterion(t, valid, evaluateBench(scenario, got), "serversExact")
	})
	t.Run("missing_required_tool_requiredTools_only", func(t *testing.T) {
		s := BenchScenario{ID: "S1", Group: "S", Question: "Какое число 2026-09-25?", Servers: []string{"clock"}, DateOnly: true}
		baseTrace := toolagent.Trace{FinalAnswer: "Сегодня 25 сентября"}
		servers := []mcprouter.Server{{Alias: "clock", ToolNames: []string{"clock__current_date", "clock__shift_date"}}}
		baseJournal := []mcprouter.JournalEntry{{Name: "clock__current_date", Alias: "clock", Outcome: "ok"}}
		base := evaluateBench(s, Day20Trace{Trace: toolagent.Trace{FinalAnswer: baseTrace.FinalAnswer,
			ToolCalls: []toolagent.ToolCall{{Step: 1, Name: "clock__current_date", ResultText: `{"date":"2026-09-25"}`}}},
			Servers: servers, Journal: baseJournal, Checks: Checks{RoutedOK: true, OrderOK: true, ProvenanceStrict: true}})
		mutatedTrace := toolagent.Trace{FinalAnswer: baseTrace.FinalAnswer, ToolCalls: []toolagent.ToolCall{{Step: 1,
			Name: "clock__shift_date", Arguments: map[string]any{"date": "2026-09-25", "days": 0}, ResultText: `{"date":"2026-09-25"}`}}}
		mutatedJournal := []mcprouter.JournalEntry{{Name: "clock__shift_date", Alias: "clock", Outcome: "ok"}}
		mutated := evaluateBench(s, Day20Trace{Trace: mutatedTrace, Servers: servers, Journal: mutatedJournal,
			Checks: Checks{RoutedOK: true, OrderOK: true, ProvenanceStrict: true}})
		assertOnlyCriterion(t, base, mutated, "requiredTools")
	})
	for _, test := range []struct {
		name   string
		mutate func(*Day20Trace)
	}{
		{"answer_without_filename_answerOK_only", func(value *Day20Trace) { value.Trace.FinalAnswer = "Результат 872,37" }},
		{"answer_without_number_answerOK_only", func(value *Day20Trace) { value.Trace.FinalAnswer = "Готово: EUR_report.md" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := cloneWrapped(wrapped)
			test.mutate(&got)
			assertOnlyCriterion(t, valid, evaluateBench(scenario, got), criterionFromTest(test.name))
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(*Day20Trace)
	}{
		{"wrong_date_argsExact_only", func(value *Day20Trace) { value.Trace.ToolCalls[1].Arguments["date"] = "2026-09-24" }},
		{"omitted_today_date_argsExact_only", func(value *Day20Trace) { delete(value.Trace.ToolCalls[1].Arguments, "date") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, got := validConversionClassifierFixture()
			base := evaluateBench(s, got)
			test.mutate(&got)
			assertOnlyCriterion(t, base, evaluateBench(s, got), "argsExact")
		})
	}
	t.Run("foreign_route_routedOK_only", func(t *testing.T) {
		got := cloneWrapped(wrapped)
		got.Journal = append(got.Journal, mcprouter.JournalEntry{Name: "rates__convert_currency", Alias: "pipeline", Outcome: "ok"})
		got.Checks = evaluateChecks(scenario.Question, got.Trace, got.Servers, got.Journal)
		assertOnlyCriterion(t, valid, evaluateBench(scenario, got), "routedOK")
	})
	t.Run("same_step_date_orderOK_only", func(t *testing.T) {
		got := cloneWrapped(wrapped)
		got.Trace.ToolCalls[2].Step = 1
		got.Checks = evaluateChecks(scenario.Question, got.Trace, got.Servers, got.Journal)
		assertOnlyCriterion(t, valid, evaluateBench(scenario, got), "orderOK")
	})
	t.Run("invented_id_breaks_order_and_handoff", func(t *testing.T) {
		got := cloneWrapped(wrapped)
		got.Trace.ToolCalls[4].Arguments["dataset_id"] = "ds_invented"
		got.Checks = evaluateChecks(scenario.Question, got.Trace, got.Servers, got.Journal)
		verdict := evaluateBench(scenario, got)
		if verdict.OrderOK || verdict.HandoffExact || !verdict.ServersExact || !verdict.RequiredTools ||
			!verdict.ArgsExact || !verdict.RoutedOK || !verdict.AnswerOK {
			t.Fatalf("invented id verdict=%+v", verdict)
		}
	})
	t.Run("wrong_second_fetch_handoffExact_only", func(t *testing.T) {
		got := cloneWrapped(wrapped)
		wrong := toolagent.ToolCall{Step: 3, Name: "pipeline__fetch_rates", Arguments: map[string]any{
			"currency": "EUR", "date_from": "2026-09-11", "date_to": "2026-09-25"},
			ResultText: `{"dataset_id":"ds_wrong"}`, Structured: map[string]any{"dataset_id": "ds_wrong", "dataset_sha256": "dw"}}
		got.Trace.ToolCalls = append(got.Trace.ToolCalls[:4], append([]toolagent.ToolCall{wrong}, got.Trace.ToolCalls[4:]...)...)
		got.Trace.ToolCalls[5].Arguments["dataset_id"] = "ds_wrong"
		got.Trace.ToolCalls[5].Structured = map[string]any{"summary_id": "sm_wrong", "dataset_sha256": "dw", "summary_sha256": "sw"}
		got.Trace.ToolCalls[5].ResultText = `{"summary_id":"sm_wrong"}`
		got.Trace.ToolCalls[6].Arguments["summary_id"] = "sm_wrong"
		got.Trace.ToolCalls[6].Structured = map[string]any{"name": "EUR_report.md", "path": "/tmp/EUR_report.md",
			"chain": map[string]any{"dataset_sha256": "dw", "summary_sha256": "sw"}}
		got.Checks = evaluateChecks(scenario.Question, got.Trace, got.Servers, got.Journal)
		assertOnlyCriterion(t, valid, evaluateBench(scenario, got), "handoffExact")
	})
}

func TestBenchClassifierExclusionsAndModelExhaustion_AC14(t *testing.T) {
	scenario, wrapped := validL1ClassifierFixture()
	cbr := cloneWrapped(wrapped)
	cbr.Journal = append(cbr.Journal, mcprouter.JournalEntry{Name: "rates__convert_currency", Alias: "rates", Outcome: "tool_error", Text: "ЦБ недоступен: timeout"})
	if got := evaluateBench(scenario, cbr).Excluded; got != "cbrFailure" {
		t.Fatalf("cbr excluded=%q", got)
	}
	ordinary := cloneWrapped(wrapped)
	ordinary.Journal = append(ordinary.Journal, mcprouter.JournalEntry{Name: "rates__convert_currency", Alias: "rates", Outcome: "tool_error", Text: "неизвестный код валюты XXX"})
	if got := evaluateBench(scenario, ordinary).Excluded; got != "" {
		t.Fatalf("ordinary excluded=%q", got)
	}
	if got := exclusionForRunError(errors.New("модель не дала ответа за 10 обращений")); got != "" {
		t.Fatalf("model exhaustion excluded=%q", got)
	}
	preserved := evaluateBench(scenario, cbr)
	applyRunErrorExclusion(&preserved, errors.New("модель не дала ответа за 10 обращений"))
	if preserved.Excluded != "cbrFailure" {
		t.Fatalf("model exhaustion erased infrastructure exclusion: %+v", preserved)
	}
	incomplete := cloneWrapped(wrapped)
	incomplete.Trace.FinalAnswer = ""
	incomplete.Trace.Totals.ModelCalls = modelCallLimit
	verdict := evaluateBench(scenario, incomplete)
	verdict.Excluded = exclusionForRunError(errors.New("модель не дала ответа за 10 обращений"))
	if verdict.FlowOK || verdict.Excluded != "" || incomplete.Trace.Totals.ModelCalls != 10 {
		t.Fatalf("model exhaustion verdict=%+v totals=%+v", verdict, incomplete.Trace.Totals)
	}
}

func TestAnswerRequiresBothSamePairConversions_AC14(t *testing.T) {
	scenario := BenchScenario{Conversions: []ExpectedConversion{
		{Amount: 200, From: "USD", To: "RUB", Date: "2026-09-18"},
		{Amount: 200, From: "USD", To: "RUB", Date: "2026-09-25"},
	}}
	calls := []toolagent.ToolCall{
		{Name: "rates__convert_currency", Arguments: map[string]any{"amount": 200.0, "from": "USD", "to": "RUB", "date": "2026-09-18"}, Structured: map[string]any{"result": 16000.0}},
		{Name: "rates__convert_currency", Arguments: map[string]any{"amount": 200.0, "from": "USD", "to": "RUB", "date": "2026-09-25"}, Structured: map[string]any{"result": 17000.0}},
	}
	if answerOK(scenario, toolagent.Trace{FinalAnswer: "Неделю назад 16 000 руб.", ToolCalls: calls}, nil) {
		t.Fatal("one conversion number satisfied both dated expectations")
	}
	if !answerOK(scenario, toolagent.Trace{FinalAnswer: "Неделю назад 16 000 руб., сегодня 17 000 руб.", ToolCalls: calls}, nil) {
		t.Fatal("both conversion numbers were not accepted")
	}
}

func TestBenchOrchestrationReusesAndRestartsRegistry_AC15(t *testing.T) {
	cbrServer := fakeDay20CBR(t)
	defer cbrServer.Close()
	setCBREnv(t, cbrServer.URL)

	scenario := BenchScenario{ID: "S2", Group: "S",
		Question:    "Сколько будет 250 евро в рублях по курсу ЦБ на 2026-09-01?",
		Servers:     []string{"rates"},
		Conversions: []ExpectedConversion{{Amount: 250, From: "EUR", To: "RUB", Date: "2026-09-01"}}}
	registry := mcprouter.Registry{Servers: []mcprouter.Entry{{Alias: "rates", Command: testBins["rates"]}}}
	open := func() (*mcprouter.Router, error) {
		return mcprouter.Open(context.Background(), registry, mcprouter.Options{})
	}
	model := &benchFakeModel{}
	type event struct {
		phase  string
		repeat int
		pid    int
	}
	var events []event
	hooks := benchHooks{
		beforeRun: func(router *mcprouter.Router, _ BenchScenario, repeat int) {
			pid := router.ProcessIDs()["rates"]
			events = append(events, event{phase: "run", repeat: repeat, pid: pid})
			model.beginRun(repeat, pid)
		},
		beforeControl: func(router *mcprouter.Router, _ BenchScenario, repeat int) {
			pid := router.ProcessIDs()["rates"]
			events = append(events, event{phase: "control", repeat: repeat, pid: pid})
			if repeat == 3 {
				if pid <= 0 {
					t.Fatalf("invalid rates pid %d", pid)
				}
				if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
					t.Fatalf("kill rates before control: %v", err)
				}
				time.Sleep(50 * time.Millisecond)
			}
		},
	}
	dir := t.TempDir()
	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("before\n<!-- metrics:start -->\nold\n<!-- metrics:end -->\nafter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var benchOutput bytes.Buffer
	report, err := executeBench(benchExecution{
		scenarios: []BenchScenario{scenario}, repeats: 4, timeout: 10 * time.Second,
		model: model, open: open, hooks: hooks,
		benchPath: filepath.Join(dir, "bench.json"), resultsPath: filepath.Join(dir, "RESULTS.md"), readmePath: readmePath,
		stdout: &benchOutput, revision: "fixture-revision", started: "2026-09-26T12:00:00+03:00", benchToday: benchToday,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 8 {
		t.Fatalf("events=%+v", events)
	}
	for i, event := range events {
		wantPhase := "run"
		if i%2 == 1 {
			wantPhase = "control"
		}
		wantRepeat := i/2 + 1
		if event.phase != wantPhase || event.repeat != wantRepeat {
			t.Fatalf("control did not immediately follow its run: %+v", events)
		}
	}
	if events[0].pid <= 0 || events[0].pid != events[1].pid || events[0].pid != events[2].pid ||
		events[2].pid != events[3].pid {
		t.Fatalf("registry was not reused through run/control and the next run: %+v", events)
	}
	if events[4].pid <= 0 || events[4].pid == events[2].pid || events[4].pid != events[5].pid {
		t.Fatalf("registry was not replaced after model-run unavailable: %+v", events)
	}
	if events[6].pid <= 0 || events[6].pid == events[4].pid || events[6].pid != events[7].pid {
		t.Fatalf("registry was not replaced after control-only unavailable: %+v", events)
	}
	waitPIDGone(t, events[2].pid, 2*time.Second)
	waitPIDGone(t, events[4].pid, 2*time.Second)
	if len(report.Runs) != 4 || report.Runs[1].Verdict.Excluded != "unavailable" ||
		report.Runs[1].Verdict.DirectControl || report.Runs[2].Verdict.Excluded != "failedControl" ||
		report.Runs[2].Verdict.DirectControl || !report.Runs[0].Verdict.DirectControl || !report.Runs[3].Verdict.DirectControl {
		t.Fatalf("report runs=%+v", report.Runs)
	}
	benchRaw, err := os.ReadFile(filepath.Join(dir, "bench.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved BenchReport
	if err := json.Unmarshal(benchRaw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Revision != report.Revision || saved.Started != report.Started || saved.Today != report.Today || len(saved.Runs) != 4 ||
		saved.Runs[1].Verdict.Excluded != "unavailable" || saved.Runs[2].Verdict.Excluded != "failedControl" {
		t.Fatalf("bench.json=%+v", saved)
	}
	results, err := os.ReadFile(filepath.Join(dir, "RESULTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	expectedCost := 0.0
	for _, run := range report.Runs {
		expectedCost += run.Trace.Trace.Totals.Cost
	}
	for _, want := range []string{
		"fixture-revision", "2026-09-26T12:00:00+03:00", "95% ДИ Уилсона",
		"| flowOK | 2/2 | 34.2%–100.0% |", "| flowOK (S) | 2/2 | 34.2%–100.0% |", "| directControl | 2/4",
		benchPromptDisclosure, "Исключения: failedControl — 1, unavailable — 1.",
		"Обращения к модели: 8; токены: вход 80, выход 24; " +
			fmt.Sprintf("цена: $%.6f.", expectedCost),
	} {
		if !strings.Contains(string(results), want) {
			t.Errorf("RESULTS missing %q:\n%s", want, results)
		}
	}
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readme), "\nold\n") || !strings.Contains(string(readme), "| flowOK | 2/2") ||
		!strings.Contains(string(readme), "| flowOK (S) | 2/2") {
		t.Fatalf("README table was not replaced:\n%s", readme)
	}
}

type benchFakeModel struct {
	repeat int
	call   int
	pid    int
}

func (m *benchFakeModel) beginRun(repeat, pid int) {
	m.repeat, m.call, m.pid = repeat, 0, pid
}

func (m *benchFakeModel) Converse(_ context.Context, messages []llm.ToolMessage, _ llm.Options) (llm.Answer, error) {
	m.call++
	answer := llm.Answer{Model: modelName, FinishReason: "stop", Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 3}}
	if m.call == 1 {
		if m.repeat == 2 {
			if m.pid <= 0 {
				return llm.Answer{}, fmt.Errorf("invalid rates pid %d", m.pid)
			}
			if err := syscall.Kill(-m.pid, syscall.SIGKILL); err != nil {
				return llm.Answer{}, fmt.Errorf("kill rates process group: %w", err)
			}
			time.Sleep(50 * time.Millisecond)
		}
		answer.FinishReason = "tool_calls"
		answer.ToolCalls = []llm.ToolCall{{ID: fmt.Sprintf("convert-%d", m.repeat), Type: "function",
			Function: llm.ToolCallFunction{Name: "rates__convert_currency",
				Arguments: `{"amount":250,"from":"EUR","to":"RUB","date":"2026-09-01"}`}}}
		return answer, nil
	}
	answer.Content = messages[len(messages)-1].Content
	return answer, nil
}

func waitPIDGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still exists", pid)
}

func validL1ClassifierFixture() (BenchScenario, Day20Trace) {
	scenario := BenchScenario{ID: "L1", Group: "L", Question: defaultQuestion,
		Servers:     []string{"clock", "rates", "pipeline"},
		Conversions: []ExpectedConversion{{Amount: 1000, From: "USD", To: "EUR", Date: "2026-09-25"}},
		Pipelines:   []ExpectedPipeline{{Currency: "EUR", DateFrom: "2026-09-12", DateTo: "2026-09-25"}}}
	calls := []toolagent.ToolCall{
		{Step: 1, Name: "clock__current_date", ResultText: `{"date":"2026-09-25"}`, Structured: map[string]any{"date": "2026-09-25"}},
		{Step: 2, Name: "clock__shift_date", Arguments: map[string]any{"date": "2026-09-25", "days": -13}, ResultText: `{"date":"2026-09-12"}`, Structured: map[string]any{"date": "2026-09-12"}},
		{Step: 2, Name: "rates__convert_currency", Arguments: map[string]any{"amount": 1000.0, "from": "USD", "to": "EUR", "date": "2026-09-25"}, ResultText: `{"result":872.3717}`, Structured: map[string]any{"result": 872.3717}},
		{Step: 3, Name: "pipeline__fetch_rates", Arguments: map[string]any{"currency": "EUR", "date_from": "2026-09-12", "date_to": "2026-09-25"}, ResultText: `{"dataset_id":"ds_good"}`, Structured: map[string]any{"dataset_id": "ds_good", "dataset_sha256": "dg"}},
		{Step: 4, Name: "pipeline__summarize_rates", Arguments: map[string]any{"dataset_id": "ds_good"}, ResultText: `{"summary_id":"sm_good"}`, Structured: map[string]any{"summary_id": "sm_good", "dataset_sha256": "dg", "summary_sha256": "sg"}},
		{Step: 5, Name: "pipeline__save_report", Arguments: map[string]any{"summary_id": "sm_good"}, ResultText: `{"name":"EUR_report.md"}`, Structured: map[string]any{"name": "EUR_report.md", "path": "/tmp/EUR_report.md", "chain": map[string]any{"dataset_sha256": "dg", "summary_sha256": "sg"}}},
	}
	servers := []mcprouter.Server{
		{Alias: "clock", ToolNames: []string{"clock__current_date", "clock__shift_date"}},
		{Alias: "rates", ToolNames: []string{"rates__convert_currency", "rates__get_currency_rates"}},
		{Alias: "pipeline", ToolNames: []string{"pipeline__fetch_rates", "pipeline__summarize_rates", "pipeline__save_report"}},
	}
	journal := make([]mcprouter.JournalEntry, 0, len(calls))
	aliases := map[string]string{
		"clock__current_date": "clock", "clock__shift_date": "clock", "rates__convert_currency": "rates",
		"pipeline__fetch_rates": "pipeline", "pipeline__summarize_rates": "pipeline", "pipeline__save_report": "pipeline",
	}
	for _, call := range calls {
		journal = append(journal, mcprouter.JournalEntry{Name: call.Name, Alias: aliases[call.Name], Outcome: "ok"})
	}
	trace := toolagent.Trace{FinalAnswer: "Готово: EUR_report.md. Результат 872,37", Tools: classifierTools(), ToolCalls: calls}
	checks := evaluateChecks(scenario.Question, trace, servers, journal)
	return scenario, Day20Trace{Trace: trace, Servers: servers, Journal: journal, Checks: checks}
}

func validConversionClassifierFixture() (BenchScenario, Day20Trace) {
	scenario := BenchScenario{ID: "S2", Group: "S", Question: "Сколько будет 250 евро в рублях по курсу ЦБ на сегодня?",
		Servers: []string{"clock", "rates"}, Conversions: []ExpectedConversion{{Amount: 250, From: "EUR", To: "RUB", Date: benchToday}}}
	current := toolagent.ToolCall{Step: 1, Name: "clock__current_date", ResultText: `{"date":"2026-09-25"}`,
		Structured: map[string]any{"date": benchToday}}
	call := toolagent.ToolCall{Step: 2, Name: "rates__convert_currency",
		Arguments:  map[string]any{"amount": 250.0, "from": "EUR", "to": "RUB", "date": benchToday},
		ResultText: `{"result":25000}`, Structured: map[string]any{"result": 25000.0}}
	servers := []mcprouter.Server{{Alias: "clock", ToolNames: []string{"clock__current_date"}},
		{Alias: "rates", ToolNames: []string{"rates__convert_currency"}}}
	journal := []mcprouter.JournalEntry{{Name: current.Name, Alias: "clock", Outcome: "ok"},
		{Name: call.Name, Alias: "rates", Outcome: "ok"}}
	trace := toolagent.Trace{FinalAnswer: "Результат 25 000 руб.", Tools: classifierTools(), ToolCalls: []toolagent.ToolCall{current, call}}
	checks := evaluateChecks(scenario.Question, trace, servers, journal)
	return scenario, Day20Trace{Trace: trace, Servers: servers, Journal: journal, Checks: checks}
}

func classifierTools() []toolagent.Tool {
	return []toolagent.Tool{
		{Name: "clock__current_date", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "clock__shift_date", InputSchema: json.RawMessage(`{"type":"object","properties":{"date":{"type":"string"},"days":{"type":"integer"}}}`)},
		{Name: "rates__convert_currency", InputSchema: json.RawMessage(`{"type":"object","properties":{"date":{"type":"string"}}}`)},
		{Name: "pipeline__fetch_rates", InputSchema: json.RawMessage(`{"type":"object","properties":{"date_from":{"type":"string"},"date_to":{"type":"string"}}}`)},
	}
}

func cloneWrapped(value Day20Trace) Day20Trace {
	raw, _ := json.Marshal(value)
	var result Day20Trace
	_ = json.Unmarshal(raw, &result)
	return result
}

func criterionFromTest(name string) string {
	if name == "wrong_date_argsExact_only" || name == "omitted_today_date_argsExact_only" {
		return "argsExact"
	}
	return "answerOK"
}

func assertOnlyCriterion(t *testing.T, before, after BenchVerdict, criterion string) {
	t.Helper()
	if !before.FlowOK || after.FlowOK {
		t.Errorf("flowOK did not follow %s mutation: before=%+v after=%+v", criterion, before, after)
	}
	values := func(v BenchVerdict) map[string]bool {
		return map[string]bool{
			"serversExact": v.ServersExact, "requiredTools": v.RequiredTools, "argsExact": v.ArgsExact,
			"handoffExact": v.HandoffExact, "orderOK": v.OrderOK, "routedOK": v.RoutedOK, "answerOK": v.AnswerOK,
		}
	}
	a, b := values(before), values(after)
	for name, original := range a {
		if name == criterion {
			if !original || b[name] {
				t.Errorf("%s did not flip: before=%+v after=%+v", name, before, after)
			}
		} else if b[name] != original {
			t.Errorf("%s changed with %s mutation: before=%+v after=%+v", name, criterion, before, after)
		}
	}
}
