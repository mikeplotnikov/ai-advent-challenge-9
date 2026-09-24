package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/pipelinemcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type dumpGolden struct {
	DatasetJSON   string                              `json:"datasetJSON"`
	DatasetSHA256 string                              `json:"datasetSHA256"`
	SummaryJSON   string                              `json:"summaryJSON"`
	SummarySHA256 string                              `json:"summarySHA256"`
	Report        string                              `json:"report"`
	ReportSHA256  string                              `json:"reportSHA256"`
	Verify        pipelinemcp.VerifyResult            `json:"verify"`
	Tamper        map[string]pipelinemcp.VerifyResult `json:"tamper"`
}

type dumpDefinitions struct {
	ServerInfo   map[string]string      `json:"serverInfo"`
	Tools        any                    `json:"tools"`
	SystemPrompt string                 `json:"systemPromptTemplate"`
	Limits       map[string]any         `json:"limits"`
	Model        string                 `json:"model"`
	Temperature  int                    `json:"temperature"`
	Pricing      map[string]llm.Pricing `json:"pricing"`
	Golden       dumpGolden             `json:"golden"`
	Errors       map[string]string      `json:"errors"`
}

func writeDump(w io.Writer) error {
	session, closeSession, err := openFixtureServer("dynamic-usd.xml", nil, nil, "reports")
	if err != nil {
		return err
	}
	defer closeSession()
	listed, err := session.ListTools(context.Background())
	if err != nil {
		return err
	}
	fetch, err := session.CallTool(context.Background(), "fetch_rates", map[string]any{"currency": "USD", "date_from": "2026-09-10", "date_to": "2026-09-24"})
	if err != nil || fetch.IsError {
		return fmt.Errorf("golden fetch_rates: %v %s", err, mcpclient.ToolText(fetch))
	}
	var fetched pipelinemcp.FetchOutput
	if err := decodeStructured(fetch, &fetched); err != nil {
		return err
	}
	summaryResult, err := session.CallTool(context.Background(), "summarize_rates", map[string]any{"dataset_id": fetched.DatasetID})
	if err != nil || summaryResult.IsError {
		return fmt.Errorf("golden summarize_rates: %v %s", err, mcpclient.ToolText(summaryResult))
	}
	var summarized pipelinemcp.SummarizeOutput
	if err := decodeStructured(summaryResult, &summarized); err != nil {
		return err
	}
	datasetJSON, datasetSHA, err := pipelinemcp.HashCanonical(fetched.Dataset)
	if err != nil {
		return err
	}
	summaryJSON, summarySHA, err := pipelinemcp.HashCanonical(summarized.Summary)
	if err != nil {
		return err
	}
	report, err := pipelinemcp.RenderReport(fetched.Dataset, datasetSHA, summarized.Summary, summarySHA)
	if err != nil {
		return err
	}
	tamper := make(map[string]pipelinemcp.VerifyResult)
	for _, kind := range []string{"row_rate", "summary_number", "hash", "human_table"} {
		tampered, err := tamperReport(report, kind, datasetSHA)
		if err != nil {
			return err
		}
		tamper[kind] = pipelinemcp.VerifyReport(tampered)
	}
	errorsMap, err := dumpErrors()
	if err != nil {
		return err
	}
	pricing := llm.PricingTable()
	dump := dumpDefinitions{
		ServerInfo:   map[string]string{"name": session.ServerName, "version": session.ServerVersion},
		Tools:        listed.Tools,
		SystemPrompt: systemPrompt,
		Limits: map[string]any{
			"maxModelCalls": day19ModelCalls, "maxPeriodDays": pipelinemcp.MaxRangeDays,
			"maxUpstreamBytes": 1 << 20, "maxReportBytes": pipelinemcp.MaxReportBytes,
			"minDate": cbr.MinDate, "maxDaysAfterToday": cbr.MaxDaysAfterToday,
		},
		Model:       modelName,
		Temperature: 0,
		Pricing:     map[string]llm.Pricing{"deepseek-flash": pricing["deepseek-flash"], "deepseek-v4-flash": pricing["deepseek-v4-flash"]},
		Golden:      dumpGolden{DatasetJSON: datasetJSON, DatasetSHA256: datasetSHA, SummaryJSON: summaryJSON, SummarySHA256: summarySHA, Report: string(report), ReportSHA256: sha256Hex(report), Verify: pipelinemcp.VerifyReport(report), Tamper: tamper},
		Errors:      errorsMap,
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(dump)
}

func openFixtureServer(dynamicFixture string, dailyErr, rangeErr error, reports string) (*mcpclient.Session, func(), error) {
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	daily := cbr.FetchFunc(func(context.Context, string) ([]byte, error) {
		if dailyErr != nil {
			return nil, dailyErr
		}
		return pipelinemcp.Fixtures.ReadFile("fixtures/daily.xml")
	})
	rangeFetch := cbr.RangeFetchFunc(func(context.Context, string, string, string) ([]byte, error) {
		if rangeErr != nil {
			return nil, rangeErr
		}
		return pipelinemcp.Fixtures.ReadFile("fixtures/" + dynamicFixture)
	})
	server := pipelinemcp.NewServer(pipelinemcp.Options{CBR: cbr.Options{Fetcher: daily, RangeFetcher: rangeFetch, Now: func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, cbr.Moscow) }}, ReportsDir: reports})
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		return nil, nil, err
	}
	session, err := mcpclient.NewNamed("day-19-dump", "1").Open(context.Background(), mcpclient.Transport{MCP: clientTransport})
	if err != nil {
		_ = serverSession.Close()
		return nil, nil, err
	}
	return session, func() { _ = session.Close(); _ = serverSession.Close() }, nil
}

func dumpErrors() (map[string]string, error) {
	type errorCase struct {
		name, fixture, tool string
		args                map[string]any
		dailyErr, rangeErr  error
	}
	cases := []errorCase{
		{name: "unknown_currency", fixture: "dynamic-usd.xml", tool: "fetch_rates", args: map[string]any{"currency": "XXX", "date_from": "2026-09-10", "date_to": "2026-09-24"}},
		{name: "from_after_to", fixture: "dynamic-usd.xml", tool: "fetch_rates", args: map[string]any{"currency": "USD", "date_from": "2026-09-24", "date_to": "2026-09-10"}},
		{name: "over_93_days", fixture: "dynamic-usd.xml", tool: "fetch_rates", args: map[string]any{"currency": "USD", "date_from": "2026-01-01", "date_to": "2026-04-04"}},
		{name: "before_min", fixture: "dynamic-usd.xml", tool: "fetch_rates", args: map[string]any{"currency": "USD", "date_from": "1997-12-31", "date_to": "1998-01-01"}},
		{name: "future", fixture: "dynamic-usd.xml", tool: "fetch_rates", args: map[string]any{"currency": "USD", "date_from": "2026-09-25", "date_to": "2026-09-26"}},
		{name: "no_publications", fixture: "dynamic-empty.xml", tool: "fetch_rates", args: map[string]any{"currency": "USD", "date_from": "2026-09-20", "date_to": "2026-09-21"}},
		{name: "unknown_dataset_id", fixture: "dynamic-usd.xml", tool: "summarize_rates", args: map[string]any{"dataset_id": "ds_000000000000"}},
		{name: "wrong_dataset_id_kind", fixture: "dynamic-usd.xml", tool: "summarize_rates", args: map[string]any{"dataset_id": "sm_000000000000"}},
		{name: "unknown_summary_id", fixture: "dynamic-usd.xml", tool: "save_report", args: map[string]any{"summary_id": "sm_000000000000"}},
		{name: "wrong_summary_id_kind", fixture: "dynamic-usd.xml", tool: "save_report", args: map[string]any{"summary_id": "ds_000000000000"}},
		{name: "network", fixture: "dynamic-usd.xml", tool: "fetch_rates", args: map[string]any{"currency": "USD", "date_from": "2026-09-10", "date_to": "2026-09-24"}, rangeErr: errors.New("network unavailable")},
		{name: "http_500", fixture: "dynamic-usd.xml", tool: "fetch_rates", args: map[string]any{"currency": "USD", "date_from": "2026-09-10", "date_to": "2026-09-24"}, rangeErr: cbr.StatusError(500)},
		{name: "malformed_xml", fixture: "malformed.xml", tool: "fetch_rates", args: map[string]any{"currency": "USD", "date_from": "2026-09-10", "date_to": "2026-09-24"}},
	}
	result := make(map[string]string, len(cases))
	for _, item := range cases {
		session, closeSession, err := openFixtureServer(item.fixture, item.dailyErr, item.rangeErr, "reports")
		if err != nil {
			return nil, err
		}
		called, callErr := session.CallTool(context.Background(), item.tool, item.args)
		closeSession()
		if callErr != nil {
			return nil, callErr
		}
		if !called.IsError {
			return nil, fmt.Errorf("dump error case %s unexpectedly succeeded", item.name)
		}
		result[item.name] = mcpclient.ToolText(called)
	}
	return result, nil
}

func tamperReport(raw []byte, kind, datasetSHA string) ([]byte, error) {
	copyRaw := append([]byte(nil), raw...)
	var old, replacement string
	switch kind {
	case "row_rate":
		old, replacement = `"date":"2026-09-10","nominal":"1","unit_rate":"85.459400"`, `"date":"2026-09-10","nominal":"1","unit_rate":"85.459401"`
	case "summary_number":
		old, replacement = `"mean":"84.370691"`, `"mean":"84.370692"`
	case "hash":
		old, replacement = `"dataset_sha256":"`+datasetSHA+`"`, `"dataset_sha256":"`+strings.Repeat("0", 64)+`"`
	case "human_table":
		old, replacement = "| 2026-09-10 | 1 | 85.459400 | 85.459400 |", "| 2026-09-10 | 1 | 85.459400 | 00.000000 |"
	default:
		return nil, fmt.Errorf("unknown tamper %s", kind)
	}
	updated := bytes.Replace(copyRaw, []byte(old), []byte(replacement), 1)
	if bytes.Equal(updated, copyRaw) {
		return nil, fmt.Errorf("tamper %s did not match", kind)
	}
	return updated, nil
}

func sha256Hex(raw []byte) string {
	return rawSHA256(raw)
}
