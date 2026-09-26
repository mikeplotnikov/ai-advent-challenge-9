package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcprouter"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
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
	if len(dump.Registry.Servers) != 3 || len(dump.Servers) != 3 || len(dump.Tools) != 7 ||
		dump.SystemPrompt != systemPrompt || dump.Model != modelName || dump.Temperature != 0 ||
		dump.Limits["maxModelCalls"] != float64(10) || dump.Limits["maxToolCalls"] != float64(16) ||
		dump.Limits["toolCallTimeoutSeconds"] != float64(45) || len(dump.Pricing) != 2 {
		t.Fatalf("incomplete dump: %#v", dump)
	}
	if dump.TraceGoldens["sameStepDate"].Checks.OrderOK {
		t.Fatal("same-step golden accepted")
	}
	missingDate := dump.TraceGoldens["sameStepMissingDate"]
	if missingDate.Checks.OrderOK || len(missingDate.Checks.OrderViolations) != 1 ||
		!bytes.Contains([]byte(missingDate.Checks.OrderViolations[0]), []byte("rates__convert_currency")) {
		t.Fatalf("same-step missing-date golden=%+v", missingDate)
	}
	if dump.RefusalTexts["limit"] != "ОШИБКА ИНСТРУМЕНТА: лимит вызовов инструментов на запрос исчерпан (16)" {
		t.Fatalf("limit=%q", dump.RefusalTexts["limit"])
	}
}

func TestDumpMatchesTheCopyTheShowcaseChecksAgainst(t *testing.T) {
	const copyPath = "../../uchebnik-ai-advent/challeng/test/day20-definitions.json"
	committed, err := os.ReadFile(copyPath)
	if err != nil {
		t.Skipf("копии витрины нет рядом (%v)", err)
	}
	var got bytes.Buffer
	if err := writeDump(&got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(committed), bytes.TrimSpace(got.Bytes())) {
		t.Errorf("выгрузка разошлась с копией витрины. Обнови её: go run ./day-20 -dump > %s", copyPath)
	}
}

func TestReadmeCarriesGeneratedMetricsTable(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(readme, []byte("<!-- metrics:start -->")) || !bytes.Contains(readme, []byte("<!-- metrics:end -->")) {
		t.Fatal("README metrics markers missing")
	}
}

func TestTraceAndJSONWriterRedactProviderKeys(t *testing.T) {
	const key = "day20-secret-value"
	t.Setenv("DEEPSEEK_API_KEY_DAY20", key)
	trace := Day20Trace{Trace: toolagent.Trace{FinalAnswer: "echo " + key,
		ModelCalls: []toolagent.ModelCall{{RequestBody: key, ResponseBody: "Bearer " + key}}},
		Journal: []mcprouter.JournalEntry{{Text: key}}}
	redactTrace(&trace, key)
	raw, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(key)) {
		t.Fatal("redactTrace retained the provider key")
	}
	path := t.TempDir() + "/trace.json"
	if err := writeJSONAtomic(path, map[string]string{"reflected": key}); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(saved, []byte(key)) || !bytes.Contains(saved, []byte("[REDACTED]")) {
		t.Fatalf("saved=%s", saved)
	}
}
