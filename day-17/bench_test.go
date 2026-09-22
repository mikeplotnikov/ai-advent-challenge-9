package main

import (
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
	}{{"Ответ: 69183.34", 69183.3375, true}, {"Ответ: 69 183,34", 69183.3375, true}, {"Ответ: 21,594.83", 21594.825, true}, {"Дата 2026 86,38", 86.3793, true}, {"86,20", 86.3793, false}, {"нет чисел", 86.3793, false}, {"12345.67", 12345.67, true}} {
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
	for _, want := range []string{"2/3", "95% 20.8–93.9%", "Контроль `-no-tools`", "Стоимость: $0.000084", "исключено"} {
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
