package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcprouter"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

var (
	isoDatePattern = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
	dmyDatePattern = regexp.MustCompile(`\b(\d{2})\.(\d{2})\.(\d{4})\b`)
)

type Checks struct {
	RoutedOK         bool     `json:"routedOK"`
	OrderOK          bool     `json:"orderOK"`
	ProvenanceStrict bool     `json:"provenanceStrict"`
	RejectedCalls    int      `json:"rejectedCalls"`
	OrderViolations  []string `json:"orderViolations,omitempty"`
}

type Day20Trace struct {
	Trace   toolagent.Trace          `json:"trace"`
	Servers []mcprouter.Server       `json:"servers"`
	Journal []mcprouter.JournalEntry `json:"journal"`
	Checks  Checks                   `json:"checks"`
}

func evaluateChecks(question string, trace toolagent.Trace, servers []mcprouter.Server,
	journal []mcprouter.JournalEntry) Checks {
	checks := Checks{RoutedOK: true, OrderOK: true, ProvenanceStrict: true}
	declared := map[string]string{}
	for _, server := range servers {
		for _, name := range server.ToolNames {
			declared[name] = server.Alias
		}
	}
	for _, entry := range journal {
		switch entry.Outcome {
		case "ok", "tool_error", "rpc_error":
			if declared[entry.Name] != entry.Alias {
				checks.RoutedOK = false
			}
		case "limit", "dead":
			checks.RejectedCalls++
		}
	}
	for _, call := range trace.ToolCalls {
		if call.Rejected != "" {
			checks.RejectedCalls++
		}
	}
	questionDates := datesInQuestion(question)
	dateTools := toolsDeclaringProperty(trace.Tools, "date")
	clockStep := 0
	for _, call := range trace.ToolCalls {
		if call.Name == "clock__current_date" && successful(call) {
			if clockStep == 0 || call.Step < clockStep {
				clockStep = call.Step
			}
		}
	}
	for _, call := range trace.ToolCalls {
		for _, key := range sortedArgumentKeys(call.Arguments) {
			value := call.Arguments[key]
			if strings.HasSuffix(key, "_id") {
				id := fmt.Sprint(value)
				if id == "" || !earlierResultContains(trace.ToolCalls, call.Step, id) {
					checks.OrderOK = false
					checks.OrderViolations = append(checks.OrderViolations,
						fmt.Sprintf("(а) %s: %s=%q не найден в результате более раннего хода", call.Name, key, id))
				}
			}
		}
		if clockStep > 0 && call.Step <= clockStep && call.Rejected == "" && dateTools[call.Name] {
			if _, hasDate := call.Arguments["date"]; !hasDate {
				checks.OrderOK = false
				checks.OrderViolations = append(checks.OrderViolations,
					fmt.Sprintf("(в) %s: date не передана в ходе %d, первый clock__current_date — ход %d",
						call.Name, call.Step, clockStep))
			}
		}
		for _, date := range datesInValue(call.Arguments) {
			if questionDates[date] {
				continue
			}
			if clockStep == 0 || call.Step <= clockStep {
				checks.OrderOK = false
				checks.OrderViolations = append(checks.OrderViolations,
					fmt.Sprintf("(б) %s: дата %s использована в ходе %d не позже первого clock__current_date (ход %d)",
						call.Name, date, call.Step, clockStep))
			}
			if !earlierResultContains(trace.ToolCalls, call.Step, date) {
				checks.ProvenanceStrict = false
			}
		}
	}
	return checks
}

func toolsDeclaringProperty(tools []toolagent.Tool, property string) map[string]bool {
	result := map[string]bool{}
	for _, tool := range tools {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(tool.InputSchema, &schema) != nil {
			continue
		}
		if _, ok := schema.Properties[property]; ok {
			result[tool.Name] = true
		}
	}
	return result
}

func sortedArgumentKeys(arguments map[string]any) []string {
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func successful(call toolagent.ToolCall) bool {
	return call.Rejected == "" && !call.IsError
}

func earlierResultContains(calls []toolagent.ToolCall, step int, needle string) bool {
	for _, call := range calls {
		if call.Step >= step || !successful(call) {
			continue
		}
		raw, _ := json.Marshal(call.Structured)
		if strings.Contains(call.ResultText, needle) || strings.Contains(string(raw), needle) {
			return true
		}
	}
	return false
}

func datesInQuestion(question string) map[string]bool {
	result := map[string]bool{}
	for _, value := range isoDatePattern.FindAllString(question, -1) {
		if _, err := time.Parse("2006-01-02", value); err == nil {
			result[value] = true
		}
	}
	for _, match := range dmyDatePattern.FindAllStringSubmatch(question, -1) {
		value := match[3] + "-" + match[2] + "-" + match[1]
		if _, err := time.Parse("2006-01-02", value); err == nil {
			result[value] = true
		}
	}
	return result
}

func datesInValue(value any) []string {
	raw, _ := json.Marshal(value)
	seen := map[string]bool{}
	for _, date := range isoDatePattern.FindAllString(string(raw), -1) {
		if _, err := time.Parse("2006-01-02", date); err == nil {
			seen[date] = true
		}
	}
	result := make([]string, 0, len(seen))
	for date := range seen {
		result = append(result, date)
	}
	sort.Strings(result)
	return result
}

func hasFatalJournal(journal []mcprouter.JournalEntry) bool {
	for _, entry := range journal {
		switch entry.Outcome {
		case "timeout", "unavailable", "dead", "limit":
			return true
		}
	}
	return false
}
