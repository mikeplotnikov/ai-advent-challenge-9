package pipelinemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func fixtureSession(t *testing.T, reports, dynamic string, rangeErr error) *mcpclient.Session {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	daily := cbr.FetchFunc(func(context.Context, string) ([]byte, error) { return Fixtures.ReadFile("fixtures/daily.xml") })
	rangeFetch := cbr.RangeFetchFunc(func(context.Context, string, string, string) ([]byte, error) {
		if rangeErr != nil {
			return nil, rangeErr
		}
		return Fixtures.ReadFile("fixtures/" + dynamic)
	})
	server := NewServer(Options{CBR: cbr.Options{Fetcher: daily, RangeFetcher: rangeFetch, Now: func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, cbr.Moscow) }}, ReportsDir: reports})
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcpclient.NewNamed("pipeline-test", "1").Open(context.Background(), mcpclient.Transport{MCP: clientTransport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(); _ = serverSession.Close() })
	return session
}

func decodeResult(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}

func runChain(t *testing.T, session *mcpclient.Session) (FetchOutput, SummarizeOutput, SaveOutput) {
	t.Helper()
	fetch, err := session.CallTool(context.Background(), "fetch_rates", map[string]any{"currency": "USD", "date_from": "2026-09-10", "date_to": "2026-09-24"})
	if err != nil || fetch.IsError {
		t.Fatalf("fetch: %v %s", err, mcpclient.ToolText(fetch))
	}
	var fetched FetchOutput
	decodeResult(t, fetch, &fetched)
	summary, err := session.CallTool(context.Background(), "summarize_rates", map[string]any{"dataset_id": fetched.DatasetID})
	if err != nil || summary.IsError {
		t.Fatalf("summary: %v %s", err, mcpclient.ToolText(summary))
	}
	var summarized SummarizeOutput
	decodeResult(t, summary, &summarized)
	save, err := session.CallTool(context.Background(), "save_report", map[string]any{"summary_id": summarized.SummaryID})
	if err != nil || save.IsError {
		t.Fatalf("save: %v %s", err, mcpclient.ToolText(save))
	}
	var saved SaveOutput
	decodeResult(t, save, &saved)
	return fetched, summarized, saved
}

func TestServerListsExactlyThreeDescribedTools(t *testing.T) {
	session := fixtureSession(t, t.TempDir(), "dynamic-usd.xml", nil)
	listed, err := session.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 3 {
		t.Fatalf("tools=%d", len(listed.Tools))
	}
	want := map[string]bool{"fetch_rates": true, "summarize_rates": true, "save_report": true}
	for i, tool := range listed.Tools {
		if !want[tool.Name] || tool.Description == "" {
			t.Fatalf("tool %d=%#v", i, tool)
		}
		delete(want, tool.Name)
		schema, _ := json.Marshal(tool.InputSchema)
		var decoded struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatal(err)
		}
		for name, property := range decoded.Properties {
			if property.Description == "" {
				t.Errorf("%s.%s has no description", tool.Name, name)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}

func TestChainTransfersIDsHashesAndWritesOneVerifiedFile(t *testing.T) {
	reports := t.TempDir()
	session := fixtureSession(t, reports, "dynamic-usd.xml", nil)
	fetched, summarized, saved := runChain(t, session)
	if fetched.DatasetID != "ds_"+fetched.DatasetSHA256[:12] || summarized.SummaryID != "sm_"+summarized.SummarySHA256[:12] {
		t.Fatalf("ids=%s %s", fetched.DatasetID, summarized.SummaryID)
	}
	if summarized.DatasetSHA256 != fetched.DatasetSHA256 || saved.Chain.DatasetSHA256 != fetched.DatasetSHA256 || saved.Chain.SummarySHA256 != summarized.SummarySHA256 || saved.ReportSHA256 != saved.Chain.ReportSHA256 {
		t.Fatalf("broken chain: %#v %#v %#v", fetched, summarized, saved)
	}
	if filepath.Dir(saved.Path) != reports || filepath.Base(saved.Path) != saved.Name {
		t.Fatalf("path escaped reports dir: %s", saved.Path)
	}
	raw, err := os.ReadFile(saved.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != saved.Bytes || reportSHA(raw) != saved.ReportSHA256 || !VerifyReport(raw).OK {
		t.Fatalf("saved report does not verify: %+v", VerifyReport(raw))
	}
	for _, required := range []string{"# Отчёт по USD:", "За период курс USD изменился", "## Сводка", "| Показатель | Значение |", "## Все публикации", "| Дата | Номинал | Курс, руб. | За единицу, руб. |", "## Provenance", "```json"} {
		if !bytes.Contains(raw, []byte(required)) {
			t.Errorf("report misses %q:\n%s", required, raw)
		}
	}
	_, _, repeated := runChain(t, session)
	if repeated.Name != saved.Name || repeated.ReportSHA256 != saved.ReportSHA256 {
		t.Fatalf("repeat=%#v want=%#v", repeated, saved)
	}
	entries, _ := os.ReadDir(reports)
	if len(entries) != 1 {
		t.Fatalf("files=%d", len(entries))
	}
}

func TestFetchTextIsBriefButStructuredCarriesEveryRow(t *testing.T) {
	session := fixtureSession(t, t.TempDir(), "dynamic-usd.xml", nil)
	result, err := session.CallTool(context.Background(), "fetch_rates", map[string]any{"currency": "USD", "date_from": "2026-09-10", "date_to": "2026-09-24"})
	if err != nil || result.IsError {
		t.Fatal(err, mcpclient.ToolText(result))
	}
	text := mcpclient.ToolText(result)
	for _, required := range []string{"dataset_id: ds_", "dataset_sha256:", "first:", "last:", "count: 11"} {
		if !strings.Contains(text, required) {
			t.Errorf("text misses %q: %s", required, text)
		}
	}
	if strings.Contains(text, "2026-09-11") || strings.Contains(text, "2026-09-23") {
		t.Fatalf("intermediate rows leaked into model text: %s", text)
	}
	var output FetchOutput
	decodeResult(t, result, &output)
	if len(output.Dataset.Rows) != 11 {
		t.Fatalf("structured rows=%d", len(output.Dataset.Rows))
	}
}

func TestWrongAndUnknownIDsNameThePreviousTool(t *testing.T) {
	session := fixtureSession(t, t.TempDir(), "dynamic-usd.xml", nil)
	for _, tc := range []struct{ tool, key, id, hint string }{
		{"summarize_rates", "dataset_id", "sm_abc", "fetch_rates"},
		{"summarize_rates", "dataset_id", "ds_abc", "fetch_rates"},
		{"save_report", "summary_id", "ds_abc", "summarize_rates"},
		{"save_report", "summary_id", "sm_abc", "summarize_rates"},
	} {
		result, err := session.CallTool(context.Background(), tc.tool, map[string]any{tc.key: tc.id})
		if err != nil || !result.IsError || !strings.Contains(mcpclient.ToolText(result), tc.hint) {
			t.Errorf("%s(%s): result=%#v err=%v text=%q", tc.tool, tc.id, result, err, mcpclient.ToolText(result))
		}
	}
}

func TestCBRFailuresAreToolErrors(t *testing.T) {
	for name, source := range map[string]error{"network": errors.New("network unavailable"), "status": cbr.StatusError(500)} {
		t.Run(name, func(t *testing.T) {
			session := fixtureSession(t, t.TempDir(), "dynamic-usd.xml", source)
			result, err := session.CallTool(context.Background(), "fetch_rates", map[string]any{"currency": "USD", "date_from": "2026-09-10", "date_to": "2026-09-24"})
			if err != nil || !result.IsError || !strings.Contains(mcpclient.ToolText(result), "ЦБ недоступен") {
				t.Fatalf("result=%#v err=%v text=%q", result, err, mcpclient.ToolText(result))
			}
		})
	}
}

func TestCanonicalJSONAndIntegerRoundingContract(t *testing.T) {
	canonical, err := CanonicalJSON(map[string]any{"z": "<&>", "a": map[string]any{"b": "x", "a": "y"}})
	if err != nil {
		t.Fatal(err)
	}
	if canonical != `{"a":{"a":"y","b":"x"},"z":"<&>"}` {
		t.Fatalf("canonical=%s", canonical)
	}
	dataset := Dataset{Rows: []DatasetRow{{Date: "a", UnitRate: "1.000000"}, {Date: "b", UnitRate: "2.000001"}}}
	summary, err := Summarize(dataset, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Mean != "1.500001" || summary.ChangePct != "100.000100" {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestVerifyRejectsFourTamperKindsWithNamedChecks(t *testing.T) {
	session := fixtureSession(t, t.TempDir(), "dynamic-usd.xml", nil)
	fetched, summarized, saved := runChain(t, session)
	raw, _ := os.ReadFile(saved.Path)
	tests := []struct{ name, old, replacement, check string }{
		{"row", `"date":"2026-09-10","nominal":"1","unit_rate":"85.459400"`, `"date":"2026-09-10","nominal":"1","unit_rate":"85.459401"`, "dataset_sha256"},
		{"summary", `"mean":"84.370691"`, `"mean":"84.370692"`, "сводка"},
		{"hash", `"dataset_sha256":"` + fetched.DatasetSHA256 + `"`, `"dataset_sha256":"` + strings.Repeat("0", 64) + `"`, "dataset_sha256"},
		{"human", "| 2026-09-10 | 1 | 85.459400 | 85.459400 |", "| 2026-09-10 | 1 | 85.459400 | 00.000000 |", "файл"},
	}
	_ = summarized
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tampered := bytes.Replace(raw, []byte(tc.old), []byte(tc.replacement), 1)
			if bytes.Equal(tampered, raw) {
				t.Fatal("tamper did not match")
			}
			result := VerifyReport(tampered)
			if result.OK || !failedCheck(result, tc.check) {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestVerifyRejectsNonReportsAsFormat(t *testing.T) {
	for name, raw := range map[string][]byte{
		"empty":       nil,
		"non_utf8":    {0xff, 0xfe},
		"no_block":    []byte("# text"),
		"broken_json": []byte("# x\n\n" + provenanceStart + "{broken" + provenanceEnd),
	} {
		t.Run(name, func(t *testing.T) {
			result := VerifyReport(raw)
			if result.OK || len(result.Checks) != 1 || result.Checks[0].Name != "формат" || result.Checks[0].Reason == "" {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestVerifyBoundsInputAndRejectsOverflowingRates(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), MaxReportBytes+1)
	result := VerifyReport(oversized)
	if result.OK || result.Checks[0].Name != "формат" || !strings.Contains(result.Checks[0].Reason, "64 КиБ") {
		t.Fatalf("oversized=%+v", result)
	}
	if _, err := parseMicros("9223372036854.775808"); err == nil {
		t.Fatal("overflowing fixed-point value was accepted")
	}
}

func TestMaximum93DayReportFitsVerificationLimit(t *testing.T) {
	dataset := Dataset{Currency: "USD", CBRID: "R01235", Name: "Dollar", DateFrom: "2026-01-01", DateTo: "2026-04-03", Source: Source}
	start, _ := time.Parse("2006-01-02", dataset.DateFrom)
	for i := 0; i < 93; i++ {
		dataset.Rows = append(dataset.Rows, DatasetRow{Date: start.AddDate(0, 0, i).Format("2006-01-02"), Nominal: "1", Value: fmt.Sprintf("84.%06d", i), UnitRate: fmt.Sprintf("84.%06d", i)})
	}
	_, datasetSHA, _ := HashCanonical(dataset)
	summary, err := Summarize(dataset, datasetSHA)
	if err != nil {
		t.Fatal(err)
	}
	_, summarySHA, _ := HashCanonical(summary)
	report, err := RenderReport(dataset, datasetSHA, summary, summarySHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) >= 64*1024 || !VerifyReport(report).OK {
		t.Fatalf("report bytes=%d verify=%+v", len(report), VerifyReport(report))
	}
}

func failedCheck(result VerifyResult, name string) bool {
	for _, check := range result.Checks {
		if check.Name == name && !check.OK {
			return true
		}
	}
	return false
}
