package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/clockmcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcprouter"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/pipelinemcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed servers.json
var dumpFS embed.FS

type dumpDefinitions struct {
	Registry     mcprouter.Registry       `json:"registry"`
	Servers      []mcprouter.Server       `json:"servers"`
	Tools        []*mcp.Tool              `json:"tools"`
	SystemPrompt string                   `json:"systemPrompt"`
	Limits       map[string]any           `json:"limits"`
	Model        string                   `json:"model"`
	Temperature  int                      `json:"temperature"`
	Pricing      map[string]llm.Pricing   `json:"pricing"`
	Clock        map[string]any           `json:"clock"`
	RefusalTexts map[string]string        `json:"refusalTexts"`
	RefusalNote  string                   `json:"refusalNote"`
	Sequences    map[string]any           `json:"sequences"`
	TraceGoldens map[string]dumpTraceCase `json:"traceGoldens"`
}

type dumpTraceCase struct {
	Question string          `json:"question"`
	Trace    toolagent.Trace `json:"trace"`
	Checks   Checks          `json:"checks"`
}

func writeDump(w io.Writer) error {
	rawRegistry, err := dumpFS.ReadFile("servers.json")
	if err != nil {
		return err
	}
	registry, err := mcprouter.ParseRegistry(rawRegistry)
	if err != nil {
		return err
	}
	router, closes, err := openDumpRouter()
	if err != nil {
		return err
	}
	defer func() {
		_ = router.Close()
		for _, closeFn := range closes {
			closeFn()
		}
	}()
	listed, err := router.ListTools(context.Background())
	if err != nil {
		return err
	}
	clockGoldens, err := dumpClock(router)
	if err != nil {
		return err
	}
	refusalTexts, sequences, err := deriveDumpRefusals(router)
	if err != nil {
		return err
	}
	pricing := llm.PricingTable()
	definitions := dumpDefinitions{
		Registry: registry, Servers: router.Servers(), Tools: listed.Tools, SystemPrompt: systemPrompt,
		Limits: map[string]any{"maxModelCalls": modelCallLimit, "maxToolCalls": mcprouter.MaxCalls,
			"toolCallTimeoutSeconds": int(mcprouter.DefaultCallTimeout / time.Second)},
		Model: modelName, Temperature: 0,
		Pricing: map[string]llm.Pricing{"deepseek-flash": pricing["deepseek-flash"],
			"deepseek-v4-flash": pricing["deepseek-v4-flash"]},
		Clock:        clockGoldens,
		RefusalTexts: refusalTexts,
		RefusalNote:  "Текст после «недоступен: » приходит от транспорта и отличается между средами выполнения.",
		Sequences:    sequences,
		TraceGoldens: dumpTraceGoldens(router.Servers(), listed.Tools),
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(definitions)
}

type dumpSession struct {
	callErr error
}

func (s *dumpSession) ListTools(context.Context) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{Tools: []*mcp.Tool{{
		Name: "probe", InputSchema: map[string]any{"type": "object"},
	}}}, nil
}

func (s *dumpSession) CallTool(context.Context, string, map[string]any) (*mcp.CallToolResult, error) {
	if s.callErr != nil {
		return nil, s.callErr
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
}

func (*dumpSession) Close() error { return nil }

type dumpScriptedLLM struct {
	toolCalls []llm.ToolCall
	seenTools []string
	turn      int
}

func (m *dumpScriptedLLM) Converse(_ context.Context, messages []llm.ToolMessage, _ llm.Options) (llm.Answer, error) {
	m.turn++
	if m.turn == 1 {
		return llm.Answer{Model: modelName, FinishReason: "tool_calls", ToolCalls: m.toolCalls}, nil
	}
	for _, message := range messages {
		if message.Role == "tool" {
			m.seenTools = append(m.seenTools, message.Content)
		}
	}
	return llm.Answer{Model: modelName, FinishReason: "stop", Content: "done"}, nil
}

func deriveDumpRefusals(available *mcprouter.Router) (map[string]string, map[string]any, error) {
	limitRouter, err := newDumpRouter(nil)
	if err != nil {
		return nil, nil, err
	}
	defer limitRouter.Close()
	for range mcprouter.MaxCalls {
		if _, err := limitRouter.CallTool(context.Background(), "rates__probe", map[string]any{}); err != nil {
			return nil, nil, err
		}
	}
	limitTrace, limitMessages, err := runDumpAgent(limitRouter, dumpCall("limit", "rates__probe"))
	if err != nil {
		return nil, nil, err
	}
	if len(limitTrace.ToolCalls) != 1 || len(limitMessages) != 1 {
		return nil, nil, errors.New("dump limit fixture did not execute one model tool call")
	}

	deadRouter, err := newDumpRouter(errors.New("EOF"))
	if err != nil {
		return nil, nil, err
	}
	defer deadRouter.Close()
	_, deadMessages, err := runDumpAgent(deadRouter,
		dumpCall("unavailable", "rates__probe"), dumpCall("dead", "rates__probe"))
	if err != nil {
		return nil, nil, err
	}
	if len(deadMessages) != 2 {
		return nil, nil, errors.New("dump dead fixture did not produce two tool messages")
	}

	timeoutRouter, err := newDumpRouter(context.DeadlineExceeded)
	if err != nil {
		return nil, nil, err
	}
	defer timeoutRouter.Close()
	_, timeoutMessages, err := runDumpAgent(timeoutRouter, dumpCall("timeout", "rates__probe"))
	if err != nil {
		return nil, nil, err
	}
	if len(timeoutMessages) != 1 {
		return nil, nil, errors.New("dump timeout fixture did not produce one tool message")
	}

	unknownTrace, _, err := runDumpAgent(available, dumpCall("unknown", "unknown"))
	if err != nil {
		return nil, nil, err
	}
	if len(unknownTrace.ToolCalls) != 1 || unknownTrace.ToolCalls[0].Rejected == "" {
		return nil, nil, errors.New("dump unknown fixture was not rejected by toolagent")
	}
	unknownText := unknownTrace.ToolCalls[0].Rejected
	limitJournal := limitRouter.Journal()
	deadJournal := deadRouter.Journal()
	if len(limitJournal) != mcprouter.MaxCalls+1 || len(deadJournal) != 2 {
		return nil, nil, errors.New("dump router fixtures produced an unexpected journal")
	}
	refusals := map[string]string{
		"limit":       limitMessages[0],
		"unavailable": deadMessages[0],
		"dead":        deadMessages[1],
		"timeout":     timeoutMessages[0],
		"unknown":     unknownText,
	}
	sequences := map[string]any{
		"limit":      dumpJournalSequence(limitJournal),
		"deadServer": dumpJournalSequence(deadJournal),
		"unknownAfterLimit": []map[string]string{
			{"outcome": limitJournal[len(limitJournal)-1].Outcome, "text": limitJournal[len(limitJournal)-1].Text},
			dumpRejectedSequence(unknownTrace.ToolCalls[0]),
		},
	}
	return refusals, sequences, nil
}

func dumpRejectedSequence(call toolagent.ToolCall) map[string]string {
	result := map[string]string{"text": call.Rejected}
	if call.Rejected != "" {
		result["outcome"] = "rejected"
	}
	return result
}

func newDumpRouter(callErr error) (*mcprouter.Router, error) {
	return mcprouter.NewForSessions([]mcprouter.SessionDescriptor{{
		Alias: "rates", ServerInfo: mcprouter.ServerInfo{Name: "dump-fixture", Version: "1"},
		Protocol: "fixture", Transport: "stdio", Session: &dumpSession{callErr: callErr},
	}}, mcprouter.DefaultCallTimeout)
}

func dumpCall(id, name string) llm.ToolCall {
	return llm.ToolCall{ID: id, Type: "function", Function: llm.ToolCallFunction{Name: name, Arguments: `{}`}}
}

func runDumpAgent(router *mcprouter.Router, calls ...llm.ToolCall) (toolagent.Trace, []string, error) {
	model := &dumpScriptedLLM{toolCalls: calls}
	trace, err := toolagent.Run(context.Background(), model, router, toolagent.Input{
		SystemPrompt: systemPrompt, Question: "dump fixture", ModelCallLimit: 2,
	})
	return trace, model.seenTools, err
}

func dumpJournalSequence(journal []mcprouter.JournalEntry) []map[string]any {
	result := make([]map[string]any, 0, len(journal))
	for _, entry := range journal {
		item := map[string]any{"seq": entry.Seq, "outcome": entry.Outcome}
		if entry.Outcome != "ok" {
			item["text"] = entry.Text
		}
		result = append(result, item)
	}
	return result
}

func openDumpRouter() (*mcprouter.Router, []func(), error) {
	now := func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, cbr.Moscow) }
	daily := cbr.FetchFunc(func(context.Context, string) ([]byte, error) {
		return ratesmcp.Fixtures.ReadFile("fixtures/2026-09-01.xml")
	})
	rangeFetch := cbr.RangeFetchFunc(func(context.Context, string, string, string) ([]byte, error) {
		return pipelinemcp.Fixtures.ReadFile("fixtures/dynamic-usd.xml")
	})
	servers := []struct {
		alias  string
		server *mcp.Server
	}{
		{"clock", clockmcp.NewServer(clockmcp.Options{Now: now})},
		{"rates", ratesmcp.NewServer(ratesmcp.Options{CBR: cbr.Options{Fetcher: daily, Now: now}})},
		{"pipeline", pipelinemcp.NewServer(pipelinemcp.Options{CBR: cbr.Options{
			Fetcher: daily, RangeFetcher: rangeFetch, Now: now,
		}, ReportsDir: "reports"})},
	}
	var descriptors []mcprouter.SessionDescriptor
	var closes []func()
	for _, item := range servers {
		session, closeFn, err := openMemorySession(item.server)
		if err != nil {
			for _, fn := range closes {
				fn()
			}
			return nil, nil, err
		}
		closes = append(closes, closeFn)
		descriptors = append(descriptors, mcprouter.SessionDescriptor{
			Alias: item.alias, ServerInfo: mcprouter.ServerInfo{Name: session.ServerName, Version: session.ServerVersion},
			Protocol: session.ProtocolVersion, Transport: "stdio", Session: session,
		})
	}
	router, err := mcprouter.NewForSessions(descriptors, mcprouter.DefaultCallTimeout)
	if err != nil {
		for _, fn := range closes {
			fn()
		}
		return nil, nil, err
	}
	return router, closes, nil
}

func openMemorySession(server *mcp.Server) (*mcpclient.Session, func(), error) {
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		return nil, nil, err
	}
	session, err := mcpclient.NewNamed("day-20-dump", "1").Open(context.Background(),
		mcpclient.Transport{MCP: clientTransport})
	if err != nil {
		_ = serverSession.Close()
		return nil, nil, err
	}
	return session, func() { _ = session.Close(); _ = serverSession.Close() }, nil
}

func dumpClock(router *mcprouter.Router) (map[string]any, error) {
	result := map[string]any{}
	cases := []struct {
		name, tool string
		args       map[string]any
	}{
		{"current", "clock__current_date", map[string]any{}},
		{"minus13", "clock__shift_date", map[string]any{"date": "2026-09-25", "days": -13}},
		{"minus7", "clock__shift_date", map[string]any{"date": "2026-09-25", "days": -7}},
		{"minus1", "clock__shift_date", map[string]any{"date": "2026-09-25", "days": -1}},
		{"zero", "clock__shift_date", map[string]any{"date": "2026-09-25", "days": 0}},
		{"month", "clock__shift_date", map[string]any{"date": "2026-03-01", "days": -1}},
		{"year", "clock__shift_date", map[string]any{"date": "2026-01-01", "days": -1}},
		{"leap", "clock__shift_date", map[string]any{"date": "2024-02-28", "days": 1}},
		{"badFormat", "clock__shift_date", map[string]any{"date": "25.09.2026", "days": 1}},
		{"badDate", "clock__shift_date", map[string]any{"date": "2026-02-30", "days": 1}},
		{"daysRange", "clock__shift_date", map[string]any{"date": "2026-09-25", "days": 3661}},
		{"resultRange", "clock__shift_date", map[string]any{"date": "9999-12-31", "days": 1}},
		{"missingDays", "clock__shift_date", map[string]any{"date": "2026-09-25"}},
		{"fractionalDays", "clock__shift_date", map[string]any{"date": "2026-09-25", "days": 1.5}},
	}
	for _, item := range cases {
		called, err := router.CallTool(context.Background(), item.tool, item.args)
		if err != nil {
			return nil, err
		}
		result[item.name] = map[string]any{"isError": called.IsError, "text": mcpclient.ToolText(called),
			"structured": called.StructuredContent}
	}
	return result, nil
}

func dumpTraceGoldens(servers []mcprouter.Server, mergedTools []*mcp.Tool) map[string]dumpTraceCase {
	current := toolagent.ToolCall{Step: 1, Name: "clock__current_date", ResultText: `{"date":"2026-09-25"}`,
		Structured: map[string]any{"date": "2026-09-25"}}
	convert := toolagent.ToolCall{Step: 2, Name: "rates__convert_currency",
		Arguments: map[string]any{"date": "2026-09-25"}, ResultText: `{"result":84000}`, Structured: map[string]any{"result": 84000}}
	convertWithoutDate := toolagent.ToolCall{Step: 1, Name: "rates__convert_currency",
		Arguments:  map[string]any{"amount": 1000, "from": "USD", "to": "EUR"},
		ResultText: `{"result":84000}`, Structured: map[string]any{"result": 84000}}
	journal := []mcprouter.JournalEntry{
		{Name: current.Name, Alias: "clock", Outcome: "ok"}, {Name: convert.Name, Alias: "rates", Outcome: "ok"},
	}
	validTrace := toolagent.Trace{ToolCalls: []toolagent.ToolCall{current, convert}}
	sameStep := validTrace
	sameStep.ToolCalls = append([]toolagent.ToolCall(nil), validTrace.ToolCalls...)
	sameStep.ToolCalls[1].Step = 1
	missingDate := toolagent.Trace{ToolCalls: []toolagent.ToolCall{current, convertWithoutDate}}
	question := "Сколько сегодня стоят 1000 долларов?"
	return map[string]dumpTraceCase{
		"valid": {Question: question, Trace: validTrace,
			Checks: evaluateChecks(question, traceWithSchemas(validTrace, mergedTools), servers, journal)},
		"sameStepDate": {Question: question, Trace: sameStep,
			Checks: evaluateChecks(question, traceWithSchemas(sameStep, mergedTools), servers, journal)},
		"sameStepMissingDate": {Question: question, Trace: missingDate,
			Checks: evaluateChecks(question, traceWithSchemas(missingDate, mergedTools), servers, journal)},
	}
}

func traceWithSchemas(trace toolagent.Trace, mergedTools []*mcp.Tool) toolagent.Trace {
	trace.Tools = make([]toolagent.Tool, 0, len(mergedTools))
	for _, tool := range mergedTools {
		schema, _ := json.Marshal(tool.InputSchema)
		trace.Tools = append(trace.Tools, toolagent.Tool{Name: tool.Name, Description: tool.Description, InputSchema: schema})
	}
	return trace
}
