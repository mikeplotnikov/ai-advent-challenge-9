package main

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

func TestUsesValueHandlesRussianFormatting(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want float64
		ok   bool
	}{{"Ответ: 69183.34", 69183.3375, true}, {"Ответ: 69 183,34", 69183.3375, true}, {"Ответ: 21,594.83", 21594.825, true}, {"Дата 2026 86,38", 86.3793, true}, {"86,20", 86.3793, false}, {"нет чисел", 86.3793, false}, {"12345.67", 12345.67, true}, {"Итог: 21,594 рублей", 21594, true}, {"Курс 86,379 рубля", 86.3793, true}, {"Итог: 21,594 рублей", 21594.825, false}} {
		if got := usesValue(tc.s, tc.want); got != tc.ok {
			t.Errorf("usesValue(%q)=%v, want %v", tc.s, got, tc.ok)
		}
	}
}
func TestResultsGeneratorUsesFixture(t *testing.T) {
	path := t.TempDir() + "/RESULTS.md"
	known := toolagent.Trace{Totals: toolagent.Totals{ModelCalls: 2, PromptTokens: 10, CachedTokens: 3, OutputTokens: 4, Cost: .000012, CostKnown: true}, ModelCalls: []toolagent.ModelCall{{Model: "deepseek-flash", Usage: llm.Usage{PromptTokens: 10}}}}
	b := Bench{Revision: "fixture", Started: "2026-09-22T12:00:00+03:00", Runs: []BenchRun{
		{Group: "R", Arm: "tools", CalledAny: true, CalledExpected: true, ArgsValid: true, UsedResult: true, Trace: known},
		{Group: "R", Arm: "tools", CalledAny: true, CalledExpected: true, ArgsValid: true, UsedResult: false, Trace: known},
		{Group: "R", Arm: "tools", Trace: known},
		{Group: "R", Arm: "tools", Error: "timeout", Trace: known},
		{Group: "R", Arm: "no-tools", UsedResult: false, Trace: known},
		{Group: "C", Arm: "tools", DeadTurn: true, Trace: known}, {Group: "N", Arm: "tools", FalseCall: true, Trace: known},
	}}
	if err := writeResults(path, b); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2/3", "95% 20.8–93.9%", "Контроль `-no-tools`", "Стоимость: $0.000084", "| R | 3 | 1 |", "- R: 0/1 (0.0%"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("report missing %q:\n%s", want, raw)
		}
	}
}

func TestRatesUsedResultAcceptsUnitRate(t *testing.T) {
	question := benchQuestion{group: "R", tool: "get_currency_rates", code: "JPY"}
	trace := toolagent.Trace{ToolCalls: []toolagent.ToolCall{{Name: "get_currency_rates", Arguments: map[string]any{"date": "", "codes": []any{"JPY"}}, ResultText: `{"rates":[{"code":"JPY","value":55,"unit_rate":0.55}]}`}}}
	values := toolValues(trace, question)
	if !usesAnyValue("Курс за одну иену — 0,55 рубля.", values) {
		t.Fatalf("unit_rate was not accepted: %v", values)
	}
}

func TestToolValuesIgnoreEarlierExpectedCallWithInvalidArguments(t *testing.T) {
	question := benchQuestion{group: "R", tool: "get_currency_rates", code: "USD", date: "2026-09-01"}
	trace := toolagent.Trace{ToolCalls: []toolagent.ToolCall{
		{Name: "get_currency_rates", Arguments: map[string]any{"date": "2026-09-02", "codes": []any{"USD"}}, ResultText: `{"rates":[{"code":"USD","value":1,"unit_rate":1}]}`},
		{Name: "get_currency_rates", Arguments: map[string]any{"date": "2026-09-01", "codes": []any{"USD"}}, ResultText: `{"rates":[{"code":"USD","value":86.3793,"unit_rate":86.3793}]}`},
	}}
	values := toolValues(trace, question)
	if len(values) == 0 || values[0] != 86.3793 {
		t.Fatalf("wrong control values: %v", values)
	}
}

// scriptedLLM answers with a fixed script; bench tests drive measure() through it so the
// verdict wiring is tested, not only the helpers it calls.
type scriptedLLM struct{ answers []llm.Answer }

func (s *scriptedLLM) Converse(context.Context, []llm.ToolMessage, llm.Options) (llm.Answer, error) {
	answer := s.answers[0]
	s.answers = s.answers[1:]
	return answer, nil
}

type scriptedSession struct {
	result *mcp.CallToolResult
	calls  int
}

func (s *scriptedSession) ListTools(context.Context) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{Tools: []*mcp.Tool{
		{Name: "convert_currency", InputSchema: map[string]any{"type": "object"}},
		{Name: "get_currency_rates", InputSchema: map[string]any{"type": "object"}},
	}}, nil
}

func (s *scriptedSession) CallTool(context.Context, string, map[string]any) (*mcp.CallToolResult, error) {
	s.calls++
	return s.result, nil
}

func toolCallAnswer(name, arguments string) llm.Answer {
	return llm.Answer{Model: "deepseek-flash", ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: name, Arguments: arguments}}}}
}

var convertQuestion = benchQuestion{group: "C", question: "q", tool: "convert_currency", date: "2026-09-01", from: "USD", to: "RUB", amount: 250}

func TestTheControlArmChecksTheAnswerAgainstTheToolsArmValue(t *testing.T) {
	// Risk 6: the control is what shows the checker can say "not used". Both outcomes
	// must be reachable through measure() itself, and a missing reference must not
	// count as used.
	for _, tc := range []struct {
		answer    string
		reference float64
		want      bool
	}{
		{"Около 21 594,83 рубля.", 21594.825, true},
		{"Курс на эту дату мне неизвестен.", 21594.825, false},
		{"Около 21 594,83 рубля.", 0, false},
		// No reference was captured: an answer that happens to contain 0 must not match it.
		{"Курс сегодня: 0 изменений, данных нет.", 0, false},
	} {
		model := &scriptedLLM{answers: []llm.Answer{{Model: "deepseek-flash", Content: tc.answer}}}
		run := measure(model, nil, convertQuestion, 1, "no-tools", true, tc.reference, time.Second)
		if run.Error != "" || run.UsedResult != tc.want || run.CalledAny {
			t.Errorf("%q ref=%v: %+v", tc.answer, tc.reference, run)
		}
	}
}

func TestArgumentsThatErrorAreNotValidArguments(t *testing.T) {
	session := &scriptedSession{result: &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "ЦБ недоступен: ЦБ ответил HTTP 500"}}}}
	model := &scriptedLLM{answers: []llm.Answer{
		toolCallAnswer("convert_currency", `{"amount":250,"from":"USD","to":"RUB","date":"2026-09-01"}`),
		{Model: "deepseek-flash", Content: "ЦБ недоступен."},
	}}
	run := measure(model, session, convertQuestion, 1, "tools", false, 0, time.Second)
	if !run.CalledExpected || run.ArgsValid || run.UsedResult || session.calls != 1 {
		t.Fatalf("ошибка инструмента засчитана как валидные аргументы: %+v", run)
	}
}

func TestConvertGroupArgumentsAndResult(t *testing.T) {
	good := map[string]any{"amount": 250.0, "from": "usd", "to": "RUB", "date": "2026-09-01"}
	if !validArgs(good, convertQuestion) {
		t.Error("верные аргументы пересчёта отвергнуты")
	}
	for name, bad := range map[string]map[string]any{
		"сумма":  {"amount": 25.0, "from": "USD", "to": "RUB", "date": "2026-09-01"},
		"откуда": {"amount": 250.0, "from": "EUR", "to": "RUB", "date": "2026-09-01"},
		"куда":   {"amount": 250.0, "from": "USD", "to": "CNY", "date": "2026-09-01"},
		"дата":   {"amount": 250.0, "from": "USD", "to": "RUB", "date": "2026-09-02"},
	} {
		if validArgs(bad, convertQuestion) {
			t.Errorf("неверные аргументы (%s) приняты", name)
		}
	}
	session := &scriptedSession{result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"amount":250,"from":"USD","result":21594.825,"to":"RUB"}`}}}}
	model := &scriptedLLM{answers: []llm.Answer{
		toolCallAnswer("convert_currency", `{"amount":250,"from":"USD","to":"RUB","date":"2026-09-01"}`),
		{Model: "deepseek-flash", Content: "Это 21 594,83 рубля."},
	}}
	run := measure(model, session, convertQuestion, 1, "tools", false, 0, time.Second)
	if !run.CalledExpected || !run.ArgsValid || !run.UsedResult || run.DeadTurn {
		t.Fatalf("верный прогон пересчёта: %+v", run)
	}
}
