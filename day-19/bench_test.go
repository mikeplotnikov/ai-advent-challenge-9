package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/pipelinemcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

func TestBenchQuestionsAreTwentyAndFullySpecified(t *testing.T) {
	questions, err := loadBenchQuestions()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	errors := 0
	for _, question := range questions {
		if question.ID == "" || question.Question == "" || question.Currency == "" || question.DateFrom == "" || question.DateTo == "" || question.Today == "" || seen[question.ID] {
			t.Fatalf("invalid question: %+v", question)
		}
		seen[question.ID] = true
		if question.ExpectError {
			errors++
		}
	}
	if errors != 1 {
		t.Fatalf("error periods=%d", errors)
	}
	for _, currency := range []string{"USD", "EUR", "CNY"} {
		if !seenCurrency(questions, currency) {
			t.Errorf("corpus misses %s", currency)
		}
	}
	for _, wording := range []string{"последние 15 дней", "текущий месяц", "01.09.2026"} {
		if !questionContains(questions, wording) {
			t.Errorf("corpus misses %q wording", wording)
		}
	}
	last := questions[len(questions)-1]
	if !last.ExpectError || last.DateFrom != "2026-09-20" || last.DateTo != "2026-09-21" {
		t.Fatalf("no-publication control is not the fixed weekend: %+v", last)
	}
}

func seenCurrency(questions []BenchQuestion, currency string) bool {
	for _, question := range questions {
		if question.Currency == currency {
			return true
		}
	}
	return false
}

func questionContains(questions []BenchQuestion, fragment string) bool {
	for _, question := range questions {
		if strings.Contains(question.Question, fragment) {
			return true
		}
	}
	return false
}

func validBenchFixture(t *testing.T) (BenchQuestion, toolagent.Trace, string) {
	t.Helper()
	question := BenchQuestion{Currency: "USD", DateFrom: "2026-09-10", DateTo: "2026-09-24"}
	dataset := pipelinemcp.Dataset{Currency: "USD", CBRID: "R01235", Name: "Dollar", DateFrom: question.DateFrom, DateTo: question.DateTo, Source: pipelinemcp.Source, Rows: []pipelinemcp.DatasetRow{{Date: question.DateFrom, Nominal: "1", Value: "84.000000", UnitRate: "84.000000"}}}
	_, datasetSHA, _ := pipelinemcp.HashCanonical(dataset)
	summary, _ := pipelinemcp.Summarize(dataset, datasetSHA)
	_, summarySHA, _ := pipelinemcp.HashCanonical(summary)
	report, _ := pipelinemcp.RenderReport(dataset, datasetSHA, summary, summarySHA)
	reportPath := t.TempDir() + "/report.md"
	if err := os.WriteFile(reportPath, report, 0o644); err != nil {
		t.Fatal(err)
	}
	name := pipelinemcp.ReportName(dataset, summarySHA)
	fetched := pipelinemcp.FetchOutput{Currency: "USD", DateFrom: dataset.DateFrom, DateTo: dataset.DateTo, DatasetID: "ds_" + datasetSHA[:12], DatasetSHA256: datasetSHA, Dataset: dataset}
	summarized := pipelinemcp.SummarizeOutput{DatasetSHA256: datasetSHA, SummaryID: "sm_" + summarySHA[:12], SummarySHA256: summarySHA, Summary: summary}
	saved := pipelinemcp.SaveOutput{Name: name, Path: reportPath, Chain: pipelinemcp.HashChain{DatasetSHA256: datasetSHA, SummarySHA256: summarySHA}}
	trace := toolagent.Trace{FinalAnswer: "Готово: " + name, ToolCalls: []toolagent.ToolCall{
		{Name: "fetch_rates", Arguments: map[string]any{"currency": "USD", "date_from": dataset.DateFrom, "date_to": dataset.DateTo}, Structured: fetched},
		{Name: "summarize_rates", Arguments: map[string]any{"dataset_id": fetched.DatasetID}, Structured: summarized},
		{Name: "save_report", Arguments: map[string]any{"summary_id": summarized.SummaryID}, Structured: saved},
	}}
	return question, trace, reportPath
}

func TestEvaluateBenchRejectsEachBrokenSuccessCondition(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*toolagent.Trace, string)
		failed func(BenchRun) bool
	}{
		{name: "chain order", mutate: func(trace *toolagent.Trace, _ string) {
			trace.ToolCalls[1], trace.ToolCalls[2] = trace.ToolCalls[2], trace.ToolCalls[1]
		}, failed: func(run BenchRun) bool { return !run.ChainInOrder }},
		{name: "ids", mutate: func(trace *toolagent.Trace, _ string) {
			trace.ToolCalls[1].Arguments["dataset_id"] = "ds_wrong"
		}, failed: func(run BenchRun) bool { return !run.IDsExact }},
		{name: "file", mutate: func(_ *toolagent.Trace, path string) {
			if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, failed: func(run BenchRun) bool { return !run.FileVerified }},
		{name: "answer filename", mutate: func(trace *toolagent.Trace, _ string) {
			trace.FinalAnswer = "Готово"
		}, failed: func(run BenchRun) bool { return !run.AnswerNamesFile }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			question, trace, path := validBenchFixture(t)
			test.mutate(&trace, path)
			if run := evaluateBench(question, trace); !test.failed(run) {
				t.Fatalf("broken condition passed: %+v", run)
			}
		})
	}
}

func TestDirectControlRequiresTheExpectedNoPublicationError(t *testing.T) {
	errorQuestion := BenchQuestion{ExpectError: true}
	if !directControlOK(errorQuestion, errors.New("ЦБ РФ не публиковал курс за период")) {
		t.Fatal("expected no-publication error was rejected")
	}
	if directControlOK(errorQuestion, errors.New("network unavailable")) || directControlOK(errorQuestion, nil) {
		t.Fatal("unrelated result passed the no-publication control")
	}
	regular := BenchQuestion{}
	if !directControlOK(regular, nil) || directControlOK(regular, errors.New("failed")) {
		t.Fatal("regular direct control classified incorrectly")
	}
}

func TestEvaluateBenchBindsTheExpectedPeriodToTheSavedChain(t *testing.T) {
	question := BenchQuestion{Currency: "USD", DateFrom: "2026-09-10", DateTo: "2026-09-24"}
	dataset := pipelinemcp.Dataset{Currency: "USD", CBRID: "R01235", Name: "Dollar", DateFrom: "2026-09-01", DateTo: "2026-09-09", Source: pipelinemcp.Source, Rows: []pipelinemcp.DatasetRow{{Date: "2026-09-01", Nominal: "1", Value: "84.000000", UnitRate: "84.000000"}}}
	_, datasetSHA, _ := pipelinemcp.HashCanonical(dataset)
	summary, _ := pipelinemcp.Summarize(dataset, datasetSHA)
	_, summarySHA, _ := pipelinemcp.HashCanonical(summary)
	report, _ := pipelinemcp.RenderReport(dataset, datasetSHA, summary, summarySHA)
	reportPath := t.TempDir() + "/report.md"
	if err := os.WriteFile(reportPath, report, 0o644); err != nil {
		t.Fatal(err)
	}
	name := pipelinemcp.ReportName(dataset, summarySHA)
	fetched := pipelinemcp.FetchOutput{Currency: "USD", DateFrom: dataset.DateFrom, DateTo: dataset.DateTo, DatasetID: "ds_" + datasetSHA[:12], DatasetSHA256: datasetSHA, Dataset: dataset}
	summarized := pipelinemcp.SummarizeOutput{DatasetSHA256: datasetSHA, SummaryID: "sm_" + summarySHA[:12], SummarySHA256: summarySHA, Summary: summary}
	saved := pipelinemcp.SaveOutput{Name: name, Path: reportPath, Chain: pipelinemcp.HashChain{DatasetSHA256: datasetSHA, SummarySHA256: summarySHA}}
	trace := toolagent.Trace{FinalAnswer: "Готово: " + name, ToolCalls: []toolagent.ToolCall{
		{Name: "fetch_rates", Arguments: map[string]any{"currency": "USD", "date_from": dataset.DateFrom, "date_to": dataset.DateTo}, Structured: fetched},
		{Name: "summarize_rates", Arguments: map[string]any{"dataset_id": fetched.DatasetID}, Structured: summarized},
		{Name: "save_report", Arguments: map[string]any{"summary_id": summarized.SummaryID}, Structured: saved},
		{Name: "fetch_rates", Arguments: map[string]any{"currency": "USD", "date_from": question.DateFrom, "date_to": question.DateTo}, Structured: pipelinemcp.FetchOutput{Currency: "USD", DateFrom: question.DateFrom, DateTo: question.DateTo}},
	}}
	run := evaluateBench(question, trace)
	if !run.ChainInOrder || !run.IDsExact || !run.FileVerified || !run.AnswerNamesFile || run.PeriodExact {
		t.Fatalf("wrong-period saved chain misclassified: %+v", run)
	}
}

func TestEvaluateBenchErrorRunDoesNotCountAsNoInvention(t *testing.T) {
	question := BenchQuestion{ExpectError: true, Currency: "USD", DateFrom: "2026-09-20", DateTo: "2026-09-21"}
	run := evaluateBench(question, toolagent.Trace{})
	run.Error = "provider failed"
	if run.DidNotInvent {
		t.Fatal("empty failed run counted as no invention")
	}
	if include, _ := metricValue("didNotInventRate", run); include {
		t.Fatal("failed run entered the metric denominator")
	}
}

func TestInventedRateDetectorCalibration(t *testing.T) {
	for _, answer := range []string{"Курс 84,3969 рубля.", "Значение 12.345 за единицу."} {
		if !containsInventedRate(answer) {
			t.Errorf("must match %q", answer)
		}
	}
	for _, answer := range []string{"Данных нет за 20.09.2026 и 2026-09-21.", "Период не длиннее 93 дня.", "Валюта USD не найдена."} {
		if containsInventedRate(answer) {
			t.Errorf("must not match %q", answer)
		}
	}
}

func TestReadmeCarriesTheGeneratedResultsTable(t *testing.T) {
	results, err := os.ReadFile("RESULTS.md")
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	for _, line := range strings.Split(string(results), "\n") {
		if strings.HasPrefix(line, "|") {
			rows++
			if !strings.Contains(string(readme), line) {
				t.Errorf("README misses generated row: %s", line)
			}
		}
	}
	if rows != 9 {
		t.Fatalf("table rows=%d", rows)
	}
}

func TestBenchResultsUseWilsonAndSyncTheReadmeTable(t *testing.T) {
	report := BenchReport{Revision: "fixture", Started: "2026-09-24T12:00:00+03:00", Runs: []BenchRun{
		{Question: BenchQuestion{ExpectError: false}, ChainInOrder: true, IDsExact: true, PeriodExact: true, FileVerified: true, AnswerNamesFile: true, DirectOK: true},
		{Question: BenchQuestion{ExpectError: false}, ChainInOrder: false, IDsExact: false, PeriodExact: false, FileVerified: false, AnswerNamesFile: false, DirectOK: true},
		{Question: BenchQuestion{ExpectError: true}, PeriodExact: true, DidNotInvent: true, DirectOK: true},
	}}
	dir := t.TempDir()
	resultsPath := dir + "/RESULTS.md"
	if err := writeBenchResults(resultsPath, report); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| chainInOrder | 1/2 (50.0%; 95% 9.5–90.5%) |", "| didNotInventRate | 1/1", "| directControl | 3/3"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("results miss %q:\n%s", want, raw)
		}
	}
	readmePath := dir + "/README.md"
	if err := os.WriteFile(readmePath, []byte("before\n\n| Метрика | Результат |\n|---|---:|\n| old | — |\n\nafter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syncReadmeResultsTable(readmePath, resultsPath); err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(readmePath)
	if !strings.Contains(string(updated), "| directControl | 3/3") || strings.Contains(string(updated), "| old |") {
		t.Fatalf("README table was not synced:\n%s", updated)
	}
}
