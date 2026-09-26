package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcprouter"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

func TestOrderRuleCDateSchemaWithoutArgument(t *testing.T) {
	tools := []toolagent.Tool{
		{Name: "clock__current_date", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "clock__shift_date", InputSchema: json.RawMessage(`{"type":"object","properties":{"date":{"type":"string"},"days":{"type":"integer"}}}`)},
		{Name: "rates__convert_currency", InputSchema: json.RawMessage(`{"type":"object","properties":{"date":{"type":"string"}}}`)},
	}
	current := toolagent.ToolCall{Step: 1, Name: "clock__current_date", ResultText: `{"date":"2026-09-25"}`}
	convertWithoutDate := func(step int) toolagent.ToolCall {
		return toolagent.ToolCall{Step: step, Name: "rates__convert_currency",
			Arguments: map[string]any{"amount": 1000, "from": "USD", "to": "EUR"}}
	}
	convertWithDate := toolagent.ToolCall{Step: 2, Name: "rates__convert_currency",
		Arguments: map[string]any{"amount": 1000, "from": "USD", "to": "EUR", "date": "2026-09-25"}}
	shiftWithoutDate := toolagent.ToolCall{Step: 1, Name: "clock__shift_date", Arguments: map[string]any{"days": -1}}
	rejectedConvert := convertWithoutDate(1)
	rejectedConvert.Rejected = "аргументы не разобраны как JSON"
	errorConvert := convertWithoutDate(1)
	errorConvert.IsError = true
	for _, test := range []struct {
		name          string
		calls         []toolagent.ToolCall
		want          bool
		violationCall string
	}{
		{"live_same_step_without_date", []toolagent.ToolCall{current, convertWithoutDate(1)}, false, "rates__convert_currency"},
		{"different_schema_tool_same_step", []toolagent.ToolCall{current, shiftWithoutDate}, false, "clock__shift_date"},
		{"agent_rejected_call_is_not_executed", []toolagent.ToolCall{current, rejectedConvert}, true, ""},
		{"executed_tool_error_still_counts", []toolagent.ToolCall{current, errorConvert}, false, "rates__convert_currency"},
		{"later_step_without_date", []toolagent.ToolCall{current, convertWithoutDate(2)}, true, ""},
		{"later_step_with_date", []toolagent.ToolCall{current, convertWithDate}, true, ""},
		{"no_current_date", []toolagent.ToolCall{convertWithoutDate(1)}, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			checks := evaluateChecks("Сколько сегодня стоят 1000 долларов?",
				toolagent.Trace{Tools: tools, ToolCalls: test.calls}, nil, nil)
			if checks.OrderOK != test.want {
				t.Fatalf("orderOK=%v want=%v checks=%+v", checks.OrderOK, test.want, checks)
			}
			if !test.want {
				if len(checks.OrderViolations) != 1 || !strings.Contains(checks.OrderViolations[0], test.violationCall) {
					t.Fatalf("violations=%v", checks.OrderViolations)
				}
			} else if len(checks.OrderViolations) != 0 {
				t.Fatalf("unexpected violations=%v", checks.OrderViolations)
			}
		})
	}
}

func TestOrderAndProvenanceGoldenTraces(t *testing.T) {
	servers := []mcprouter.Server{
		{Alias: "clock", ToolNames: []string{"clock__current_date", "clock__shift_date"}},
		{Alias: "rates", ToolNames: []string{"rates__convert_currency"}},
		{Alias: "pipeline", ToolNames: []string{"pipeline__summarize_rates"}},
	}
	journal := []mcprouter.JournalEntry{
		{Name: "clock__current_date", Alias: "clock", Outcome: "ok"},
		{Name: "clock__shift_date", Alias: "clock", Outcome: "ok"},
		{Name: "pipeline__summarize_rates", Alias: "pipeline", Outcome: "ok"},
	}
	base := []toolagent.ToolCall{
		{Step: 1, Name: "clock__current_date", ResultText: `{"date":"2026-09-25"}`},
		{Step: 2, Name: "clock__shift_date", Arguments: map[string]any{"date": "2026-09-25"}, ResultText: `{"date":"2026-09-18","dataset_id":"ds_ok"}`},
		{Step: 3, Name: "pipeline__summarize_rates", Arguments: map[string]any{"dataset_id": "ds_ok", "date": "2026-09-18"}},
	}
	checks := evaluateChecks("неделю назад", toolagent.Trace{ToolCalls: base}, servers, journal)
	if !checks.OrderOK || !checks.ProvenanceStrict || !checks.RoutedOK {
		t.Fatalf("valid=%+v", checks)
	}

	sameStep := append([]toolagent.ToolCall(nil), base...)
	sameStep[2].Step = 2
	checks = evaluateChecks("неделю назад", toolagent.Trace{ToolCalls: sameStep}, servers, journal)
	if checks.OrderOK {
		t.Fatal("same-step id/date accepted")
	}

	invented := append([]toolagent.ToolCall(nil), base...)
	invented[2].Arguments = map[string]any{"dataset_id": "ds_fake", "date": "2026-09-19"}
	checks = evaluateChecks("неделю назад", toolagent.Trace{ToolCalls: invented}, servers, journal)
	if checks.OrderOK || checks.ProvenanceStrict {
		t.Fatalf("invented=%+v", checks)
	}

	questionDate := toolagent.Trace{ToolCalls: []toolagent.ToolCall{{Step: 1, Name: "rates__convert_currency", Arguments: map[string]any{"date": "2026-09-15"}}}}
	checks = evaluateChecks("курс на 15.09.2026", questionDate, servers, nil)
	if !checks.OrderOK || !checks.ProvenanceStrict {
		t.Fatalf("question date=%+v", checks)
	}

	mental := toolagent.Trace{ToolCalls: []toolagent.ToolCall{
		{Step: 1, Name: "clock__current_date", ResultText: `{"date":"2026-09-25"}`},
		{Step: 2, Name: "rates__convert_currency", Arguments: map[string]any{"date": "2026-09-18"}},
	}}
	checks = evaluateChecks("неделю назад", mental, servers, nil)
	if !checks.OrderOK || checks.ProvenanceStrict {
		t.Fatalf("mental=%+v", checks)
	}
}

func TestRoutedOKRejectsForeignServer(t *testing.T) {
	servers := []mcprouter.Server{{Alias: "rates", ToolNames: []string{"rates__convert_currency"}}}
	checks := evaluateChecks("", toolagent.Trace{}, servers, []mcprouter.JournalEntry{{
		Name: "rates__convert_currency", Alias: "pipeline", Outcome: "ok",
	}})
	if checks.RoutedOK {
		t.Fatal("foreign route accepted")
	}
}
