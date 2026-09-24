package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/pipelinemcp"
)

func TestDumpIsDeterministicAndComplete(t *testing.T) {
	var first, second bytes.Buffer
	if err := writeDump(&first); err != nil {
		t.Fatal(err)
	}
	if err := writeDump(&second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("dump differs between runs")
	}
	var dump dumpDefinitions
	if err := json.Unmarshal(first.Bytes(), &dump); err != nil {
		t.Fatal(err)
	}
	if dump.ServerInfo["name"] != "cbr-pipeline" || dump.SystemPrompt != systemPrompt || dump.Model != modelName || dump.Temperature != 0 || len(dump.Errors) != 13 || !dump.Golden.Verify.OK || dump.Golden.Report == "" {
		t.Fatalf("incomplete dump: %#v", dump)
	}
	tools, ok := dump.Tools.([]any)
	if !ok || len(tools) != 3 || dump.Limits["maxModelCalls"] != float64(6) || dump.Limits["maxPeriodDays"] != float64(93) || dump.Limits["maxUpstreamBytes"] != float64(1<<20) || dump.Limits["maxReportBytes"] != float64(64<<10) || dump.Limits["minDate"] != cbr.MinDate || dump.Limits["maxDaysAfterToday"] != float64(cbr.MaxDaysAfterToday) || len(dump.Pricing) != 2 {
		t.Fatalf("tools/limits/pricing incomplete: tools=%T %#v limits=%#v pricing=%#v", dump.Tools, dump.Tools, dump.Limits, dump.Pricing)
	}
	pricing := llm.PricingTable()
	for _, model := range []string{"deepseek-flash", "deepseek-v4-flash"} {
		if dump.Pricing[model] != pricing[model] {
			t.Errorf("pricing for %s differs: got=%+v want=%+v", model, dump.Pricing[model], pricing[model])
		}
	}
	var dataset pipelinemcp.Dataset
	if err := json.Unmarshal([]byte(dump.Golden.DatasetJSON), &dataset); err != nil {
		t.Fatal(err)
	}
	datasetJSON, datasetSHA, err := pipelinemcp.HashCanonical(dataset)
	if err != nil || datasetJSON != dump.Golden.DatasetJSON || datasetSHA != dump.Golden.DatasetSHA256 {
		t.Fatalf("dataset golden inconsistent: err=%v", err)
	}
	var summary pipelinemcp.Summary
	if err := json.Unmarshal([]byte(dump.Golden.SummaryJSON), &summary); err != nil {
		t.Fatal(err)
	}
	summaryJSON, summarySHA, err := pipelinemcp.HashCanonical(summary)
	if err != nil || summaryJSON != dump.Golden.SummaryJSON || summarySHA != dump.Golden.SummarySHA256 || rawSHA256([]byte(dump.Golden.Report)) != dump.Golden.ReportSHA256 {
		t.Fatalf("summary/report golden inconsistent: err=%v", err)
	}
	wantTamper := map[string]bool{"row_rate": true, "summary_number": true, "hash": true, "human_table": true}
	for name, result := range dump.Golden.Tamper {
		if !wantTamper[name] {
			t.Errorf("unexpected tamper %s", name)
		}
		delete(wantTamper, name)
		if result.OK {
			t.Errorf("tamper %s passed", name)
		}
	}
	if len(wantTamper) != 0 {
		t.Errorf("missing tampers: %v", wantTamper)
	}
	wantErrors := []string{"unknown_currency", "from_after_to", "over_93_days", "before_min", "future", "no_publications", "unknown_dataset_id", "wrong_dataset_id_kind", "unknown_summary_id", "wrong_summary_id_kind", "network", "http_500", "malformed_xml"}
	for _, name := range wantErrors {
		if dump.Errors[name] == "" {
			t.Errorf("missing error %s", name)
		}
	}
}

func TestDumpMatchesTheCopyTheShowcaseChecksAgainst(t *testing.T) {
	const copyPath = "../../uchebnik-ai-advent/challeng/test/day19-definitions.json"
	committed, err := os.ReadFile(copyPath)
	if err != nil {
		t.Skipf("копии витрины нет рядом (%v)", err)
	}
	var got bytes.Buffer
	if err := writeDump(&got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(committed), bytes.TrimSpace(got.Bytes())) {
		t.Errorf("выгрузка разошлась с копией витрины. Обнови её: go run ./day-19 -dump > %s", copyPath)
	}
}
