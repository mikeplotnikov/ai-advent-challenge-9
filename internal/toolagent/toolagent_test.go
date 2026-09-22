package toolagent

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeLLM struct {
	answers  []llm.Answer
	messages [][]llm.ToolMessage
	options  []llm.Options
}

func (f *fakeLLM) Converse(_ context.Context, messages []llm.ToolMessage, options llm.Options) (llm.Answer, error) {
	f.messages = append(f.messages, append([]llm.ToolMessage(nil), messages...))
	f.options = append(f.options, options)
	if len(f.answers) == 0 {
		return llm.Answer{}, context.Canceled
	}
	answer := f.answers[0]
	f.answers = f.answers[1:]
	return answer, nil
}
func answer(content string, calls ...llm.ToolCall) llm.Answer {
	return llm.Answer{Model: "deepseek-flash", Content: content, ToolCalls: calls}
}
func call(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Type: "function", Function: llm.ToolCallFunction{Name: name, Arguments: args}}
}

type countingSession struct {
	inner Session
	calls []string
}

func (s *countingSession) ListTools(ctx context.Context) (*mcp.ListToolsResult, error) {
	return s.inner.ListTools(ctx)
}
func (s *countingSession) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	s.calls = append(s.calls, name)
	return s.inner.CallTool(ctx, name, args)
}

type forbiddenSession struct{ t *testing.T }

func (s forbiddenSession) ListTools(context.Context) (*mcp.ListToolsResult, error) {
	s.t.Fatal("NoTools listed MCP tools")
	return nil, nil
}
func (s forbiddenSession) CallTool(context.Context, string, map[string]any) (*mcp.CallToolResult, error) {
	s.t.Fatal("NoTools called MCP tool")
	return nil, nil
}

func realSession(t *testing.T) (*countingSession, func()) {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	fetch := cbr.FetchFunc(func(context.Context, string) ([]byte, error) {
		return ratesmcp.Fixtures.ReadFile("fixtures/2026-09-01.xml")
	})
	server := ratesmcp.NewServer(ratesmcp.Options{CBR: cbr.Options{Fetcher: fetch, Now: func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, cbr.Moscow) }}})
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcpclient.NewNamed("toolagent-test", "1").Open(context.Background(), mcpclient.Transport{MCP: clientTransport})
	if err != nil {
		t.Fatal(err)
	}
	return &countingSession{inner: session}, func() { _ = session.Close(); _ = serverSession.Close() }
}
func assertTemperature(t *testing.T, model *fakeLLM) {
	t.Helper()
	for _, opts := range model.options {
		if opts.Temperature == nil || *opts.Temperature != 0 {
			t.Fatalf("temperature=%v", opts.Temperature)
		}
	}
}

func TestRunCallResultAndSchemaFromRealServer(t *testing.T) {
	session, closeSession := realSession(t)
	defer closeSession()
	listed, err := session.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeLLM{answers: []llm.Answer{answer("", call("a", "convert_currency", `{"amount":250,"from":"USD","to":"RUB","date":"2026-09-01"}`)), answer("Итог 21594,825")}}
	trace, err := Run(context.Background(), model, session, Input{SystemPrompt: "s", Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.ToolCalls) != 1 || trace.FinalAnswer == "" {
		t.Fatalf("trace=%#v", trace)
	}
	if len(model.options[0].Tools) != len(listed.Tools) {
		t.Fatal("tools/list was not sent")
	}
	for i, tool := range listed.Tools {
		want, _ := json.Marshal(tool.InputSchema)
		if !bytes.Equal(want, model.options[0].Tools[i].Function.Parameters) {
			t.Fatalf("schema for %s differs", tool.Name)
		}
	}
	got := model.messages[1][len(model.messages[1])-1]
	if got.Role != "tool" || got.ToolCallID != "a" || got.Content == "" {
		t.Fatalf("tool result lost: %#v", got)
	}
	expected, err := session.CallTool(context.Background(), "convert_currency", map[string]any{"amount": 250.0, "from": "USD", "to": "RUB", "date": "2026-09-01"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != mcpclient.ToolText(expected) {
		t.Fatalf("model got %q, expected tool text %q", got.Content, mcpclient.ToolText(expected))
	}
	if !reflect.DeepEqual(model.messages[1][2].ToolCalls, []llm.ToolCall{call("a", "convert_currency", `{"amount":250,"from":"USD","to":"RUB","date":"2026-09-01"}`)}) {
		t.Fatalf("assistant calls changed: %#v", model.messages[1][2].ToolCalls)
	}
	assertTemperature(t, model)
}

func TestRunExecutesTwoCallsInOrder(t *testing.T) {
	session, closeSession := realSession(t)
	defer closeSession()
	model := &fakeLLM{answers: []llm.Answer{answer("", call("first", "get_currency_rates", `{"date":"2026-09-01","codes":["USD"]}`), call("second", "convert_currency", `{"amount":1,"from":"USD","to":"RUB","date":"2026-09-01"}`)), answer("готово")}}
	trace, err := Run(context.Background(), model, session, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := session.calls, []string{"get_currency_rates", "convert_currency"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("calls=%v", got)
	}
	history := model.messages[1]
	if history[len(history)-2].ToolCallID != "first" || history[len(history)-1].ToolCallID != "second" {
		t.Fatalf("ids lost: %#v", history)
	}
	if trace.ToolCalls[0].Step != 1 || trace.ToolCalls[1].Step != 1 {
		t.Fatalf("steps=%#v", trace.ToolCalls)
	}
	if !reflect.DeepEqual(history[2].ToolCalls, []llm.ToolCall{call("first", "get_currency_rates", `{"date":"2026-09-01","codes":["USD"]}`), call("second", "convert_currency", `{"amount":1,"from":"USD","to":"RUB","date":"2026-09-01"}`)}) {
		t.Fatalf("assistant calls changed: %#v", history[2].ToolCalls)
	}
}

func TestRunSendsToolErrorToModel(t *testing.T) {
	session, closeSession := realSession(t)
	defer closeSession()
	model := &fakeLLM{answers: []llm.Answer{answer("", call("bad", "convert_currency", `{"amount":0,"from":"USD","to":"RUB","date":"2026-09-01"}`)), answer("исправить нельзя")}}
	trace, err := Run(context.Background(), model, session, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if !trace.ToolCalls[0].IsError {
		t.Fatal("expected tool error")
	}
	got := model.messages[1][len(model.messages[1])-1].Content
	if len(got) < len("ОШИБКА ИНСТРУМЕНТА: ") || got[:len("ОШИБКА ИНСТРУМЕНТА: ")] != "ОШИБКА ИНСТРУМЕНТА: " {
		t.Fatalf("prefix absent: %q", got)
	}
	expected, err := session.CallTool(context.Background(), "convert_currency", map[string]any{"amount": 0.0, "from": "USD", "to": "RUB", "date": "2026-09-01"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "ОШИБКА ИНСТРУМЕНТА: "+mcpclient.ToolText(expected) {
		t.Fatalf("wrong error text: %q", got)
	}
}

func TestRunStopsAfterFiveToolTurns(t *testing.T) {
	session, closeSession := realSession(t)
	defer closeSession()
	answers := make([]llm.Answer, MaxModelCalls)
	for i := range answers {
		answers[i] = answer("", call("x", "get_currency_rates", `{"date":"2026-09-01"}`))
	}
	model := &fakeLLM{answers: answers}
	_, err := Run(context.Background(), model, session, Input{})
	if err == nil || err.Error() != "модель не дала ответа за 5 обращений" {
		t.Fatalf("err=%v", err)
	}
	if len(model.options) != MaxModelCalls {
		t.Fatalf("calls=%d", len(model.options))
	}
}

func TestRunDirectTextAndNoTools(t *testing.T) {
	session, closeSession := realSession(t)
	defer closeSession()
	model := &fakeLLM{answers: []llm.Answer{answer("текст")}}
	trace, err := Run(context.Background(), model, session, Input{})
	if err != nil || len(trace.ToolCalls) != 0 || len(session.calls) != 0 {
		t.Fatalf("trace=%#v err=%v", trace, err)
	}
	noTools := &fakeLLM{answers: []llm.Answer{answer("без инструментов")}}
	_, err = Run(context.Background(), noTools, forbiddenSession{t}, Input{NoTools: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(noTools.options[0].Tools) != 0 {
		t.Fatal("NoTools sent tools")
	}
	assertTemperature(t, noTools)
}

func TestRunRejectsUnknownAndMalformedWithoutMCP(t *testing.T) {
	session, closeSession := realSession(t)
	defer closeSession()
	model := &fakeLLM{answers: []llm.Answer{answer("", call("unknown", "not-a-tool", `{}`)), answer("после неизвестного")}}
	trace, err := Run(context.Background(), model, session, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.calls) != 0 || trace.ToolCalls[0].Rejected == "" {
		t.Fatalf("unknown reached MCP: %#v", trace.ToolCalls[0])
	}
	// The rejection must reach the model, under the call's id, or it cannot correct itself.
	assertLastToolMessage(t, model, "unknown", `инструмент "not-a-tool" не существует; доступны: convert_currency, get_currency_rates`)
	model = &fakeLLM{answers: []llm.Answer{answer("", call("broken", "convert_currency", `{"amount": 25`)), answer("после ошибки")}}
	trace, err = Run(context.Background(), model, session, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.calls) != 0 || trace.ToolCalls[0].ParseError == "" || trace.ToolCalls[0].Rejected == "" {
		t.Fatalf("broken reached MCP: %#v", trace.ToolCalls[0])
	}
	assertLastToolMessage(t, model, "broken", "аргументы не разобраны как JSON: ")
	if trace.FinalAnswer != "после ошибки" {
		t.Fatalf("цикл не продолжился после отказа: %q", trace.FinalAnswer)
	}
}

// assertLastToolMessage checks what the SECOND model call received: the last message of
// its history must be the tool message for the rejected call.
func assertLastToolMessage(t *testing.T, model *fakeLLM, id, prefix string) {
	t.Helper()
	if len(model.messages) < 2 {
		t.Fatalf("второго обращения к модели не было: %d", len(model.messages))
	}
	history := model.messages[1]
	last := history[len(history)-1]
	if last.Role != "tool" || last.ToolCallID != id || !strings.HasPrefix(last.Content, prefix) {
		t.Fatalf("модель не получила отказ: %#v", last)
	}
	previous := history[len(history)-2]
	if previous.Role != "assistant" || len(previous.ToolCalls) != 1 || previous.ToolCalls[0].ID != id {
		t.Fatalf("в истории нет хода модели с этим вызовом: %#v", previous)
	}
}
