package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type dumpExpected struct {
	IsError    bool   `json:"isError"`
	Text       string `json:"text"`
	Structured any    `json:"structured"`
}
type dumpCase struct {
	Name          string         `json:"name"`
	Fixture       *string        `json:"fixture"`
	FixtureBase64 *string        `json:"fixtureBase64"`
	Upstream      string         `json:"upstream"`
	Today         string         `json:"today"`
	Tool          string         `json:"tool"`
	Arguments     map[string]any `json:"arguments"`
	Compare       string         `json:"compare"`
	Expected      dumpExpected   `json:"expected"`
}
type dumpDefinitions struct {
	ServerInfo    map[string]string      `json:"serverInfo"`
	Tools         any                    `json:"tools"`
	SystemPrompt  string                 `json:"systemPrompt"`
	MaxModelCalls int                    `json:"maxModelCalls"`
	Temperature   int                    `json:"temperature"`
	Model         string                 `json:"model"`
	Pricing       map[string]llm.Pricing `json:"pricing"`
	Peak          map[string]any         `json:"peak"`
	Dates         map[string]any         `json:"dates"`
	Cases         []dumpCase             `json:"cases"`
}

type dumpSpec struct {
	name, fixture, upstream, tool, compare string
	args                                   map[string]any
}

func writeDump(w io.Writer) error {
	tools, serverInfo, err := dumpTools()
	if err != nil {
		return err
	}
	cases := make([]dumpCase, 0, len(dumpSpecs()))
	for _, spec := range dumpSpecs() {
		result, err := runDumpCase(spec)
		if err != nil {
			return err
		}
		item := dumpCase{Name: spec.name, Upstream: spec.upstream, Today: "2026-09-22T12:00:00+03:00", Tool: spec.tool, Arguments: spec.args, Compare: spec.compare, Expected: dumpExpected{IsError: result.IsError, Text: mcpclient.ToolText(result), Structured: result.StructuredContent}}
		if spec.upstream == "xml" {
			raw, err := ratesmcp.Fixtures.ReadFile("fixtures/" + spec.fixture)
			if err != nil {
				return err
			}
			name, encoded := spec.fixture, base64.StdEncoding.EncodeToString(raw)
			item.Fixture, item.FixtureBase64 = &name, &encoded
		}
		cases = append(cases, item)
	}
	pricing := llm.PricingTable()
	dump := dumpDefinitions{ServerInfo: serverInfo, Tools: tools, SystemPrompt: systemPrompt, MaxModelCalls: toolagent.MaxModelCalls, Temperature: 0, Model: llm.DefaultModel, Pricing: map[string]llm.Pricing{"deepseek-flash": pricing["deepseek-flash"], "deepseek-v4-flash": pricing["deepseek-v4-flash"]}, Peak: map[string]any{"weekdaysOnly": true, "hoursUTC": [][]int{{1, 4}, {6, 10}}}, Dates: map[string]any{"min": cbr.MinDate, "maxDaysAfterToday": cbr.MaxDaysAfterToday, "zone": "UTC+3"}, Cases: cases}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(dump)
}

// dumpTools returns what the real server says about itself: its tools/list and the
// serverInfo from its initialize answer — both read over MCP, neither typed here.
func dumpTools() (any, map[string]string, error) {
	session, closeSession, err := openDumpServer("2026-09-01.xml", nil)
	if err != nil {
		return nil, nil, err
	}
	defer closeSession()
	listed, err := session.ListTools(context.Background())
	if err != nil {
		return nil, nil, err
	}
	return listed.Tools, map[string]string{"name": session.ServerName, "version": session.ServerVersion}, nil
}

// Every case opens a fresh CBR server: cached previous answers would make an
// upstream fixture a false claim about the particular case being exported.
func runDumpCase(spec dumpSpec) (*mcp.CallToolResult, error) {
	var source error
	if spec.upstream == "http500" {
		source = cbr.StatusError(500)
	}
	if spec.upstream == "network" {
		source = errors.New("network unavailable")
	}
	session, closeSession, err := openDumpServer(spec.fixture, source)
	if err != nil {
		return nil, err
	}
	defer closeSession()
	return session.CallTool(context.Background(), spec.tool, spec.args)
}

func openDumpServer(fixture string, source error) (*mcpclient.Session, func(), error) {
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	fetch := cbr.FetchFunc(func(_ context.Context, _ string) ([]byte, error) {
		if source != nil {
			return nil, source
		}
		return ratesmcp.Fixtures.ReadFile("fixtures/" + fixture)
	})
	server := ratesmcp.NewServer(ratesmcp.Options{CBR: cbr.Options{Fetcher: fetch, Now: func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, cbr.Moscow) }}})
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		return nil, nil, err
	}
	session, err := mcpclient.NewNamed("day-17-dump", "1").Open(context.Background(), mcpclient.Transport{MCP: clientTransport})
	if err != nil {
		_ = serverSession.Close()
		return nil, nil, err
	}
	return session, func() { _ = session.Close(); _ = serverSession.Close() }, nil
}

func dumpSpecs() []dumpSpec {
	return []dumpSpec{
		{"rates-usd-kzt", "2026-09-01.xml", "xml", "get_currency_rates", "exact", map[string]any{"date": "2026-09-01", "codes": []any{"USD", "KZT"}}},
		{"rates-empty-codes", "2026-09-01.xml", "xml", "get_currency_rates", "exact", map[string]any{"date": "2026-09-01", "codes": []any{}}},
		{"rates-lowercase", "2026-09-01.xml", "xml", "get_currency_rates", "exact", map[string]any{"date": "2026-09-01", "codes": []any{"usd"}}},
		{"rates-unknown", "2026-09-01.xml", "xml", "get_currency_rates", "exact", map[string]any{"date": "2026-09-01", "codes": []any{"XXX"}}},
		{"rates-weekend", "2026-09-20-weekend.xml", "xml", "get_currency_rates", "exact", map[string]any{"date": "2026-09-20"}},
		{"rates-latest", "2026-09-01.xml", "xml", "get_currency_rates", "exact", map[string]any{"date": ""}},
		{"date-tomorrow", "2026-09-01.xml", "xml", "get_currency_rates", "exact", map[string]any{"date": "2026-09-23"}},
		{"date-future", "2026-09-01.xml", "xml", "get_currency_rates", "exact", map[string]any{"date": "2026-09-24"}},
		{"date-before-min", "2026-09-01.xml", "xml", "get_currency_rates", "exact", map[string]any{"date": "1997-12-31"}},
		{"date-invalid", "2026-09-01.xml", "xml", "get_currency_rates", "exact", map[string]any{"date": "2026-13-01"}},
		{"convert-usd-rub", "2026-09-01.xml", "xml", "convert_currency", "exact", map[string]any{"amount": 250, "from": "USD", "to": "RUB", "date": "2026-09-01"}},
		{"convert-cny-kzt", "2026-09-01.xml", "xml", "convert_currency", "exact", map[string]any{"amount": 1000, "from": "CNY", "to": "KZT", "date": "2026-09-01"}},
		{"convert-lowercase", "2026-09-01.xml", "xml", "convert_currency", "exact", map[string]any{"amount": 1, "from": "usd", "to": "rub", "date": "2026-09-01"}},
		{"convert-zero", "2026-09-01.xml", "xml", "convert_currency", "exact", map[string]any{"amount": 0, "from": "EUR", "to": "RUB", "date": "2026-09-01"}},
		{"convert-same", "2026-09-01.xml", "xml", "convert_currency", "exact", map[string]any{"amount": 1, "from": "EUR", "to": "EUR", "date": "2026-09-01"}},
		{"convert-unknown-code", "2026-09-01.xml", "xml", "convert_currency", "exact", map[string]any{"amount": 1, "from": "XXX", "to": "RUB", "date": "2026-09-01"}},
		{"empty", "empty.xml", "xml", "get_currency_rates", "exact", map[string]any{"date": "2026-09-01"}},
		{"error-in-parameters", "error-in-parameters.xml", "xml", "get_currency_rates", "exact", map[string]any{"date": "2026-09-01"}},
		{"http500", "", "http500", "get_currency_rates", "exact", map[string]any{"date": "2026-09-01"}},
		{"network", "", "network", "get_currency_rates", "errorPrefix", map[string]any{"date": "2026-09-01"}},
		{"schema-amount", "2026-09-01.xml", "xml", "convert_currency", "isError", map[string]any{"amount": "abc", "from": "USD", "to": "RUB"}},
		{"schema-extra", "2026-09-01.xml", "xml", "get_currency_rates", "isError", map[string]any{"extra": true}},
	}
}
