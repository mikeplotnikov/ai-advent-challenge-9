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

func TestSystemPromptUsesConditionalDateRule(t *testing.T) {
	const want = "Ты агент с инструментами нескольких MCP-серверов. Имя инструмента имеет вид <сервер>__<инструмент>; если описание или ошибка называет инструмент коротко, вызывай его с префиксом того же сервера. Сегодняшнюю дату ты не знаешь — узнай её инструментом. Если вопрос о сегодняшнем дне или о периоде относительно сегодня, сначала узнай дату и только потом вызывай инструменты, которым она нужна, передавая её явно; даты, названные в вопросе, бери из вопроса. Курсы валют бери только из инструментов, не из памяти. Вызывай только те инструменты, которые нужны для вопроса. Отвечай по-русски и коротко; если сохранил отчёт, назови имя файла; если какой-то шаг не удался, скажи об этом."
	if systemPrompt != want {
		t.Fatalf("system prompt changed\n got: %q\nwant: %q", systemPrompt, want)
	}
}

func TestDumpRefusalsAndSequencesComeFromExecution(t *testing.T) {
	var raw bytes.Buffer
	if err := writeDump(&raw); err != nil {
		t.Fatal(err)
	}
	var dump dumpDefinitions
	if err := json.Unmarshal(raw.Bytes(), &dump); err != nil {
		t.Fatal(err)
	}
	const (
		limitText   = "лимит вызовов инструментов на запрос исчерпан (16)"
		serverText  = "сервер rates недоступен: EOF"
		timeoutText = "сервер rates не ответил за 45 с"
		unknownText = "инструмент \"unknown\" не существует; доступны: clock__current_date, clock__shift_date, pipeline__fetch_rates, pipeline__save_report, pipeline__summarize_rates, rates__convert_currency, rates__get_currency_rates"
	)
	wantRefusals := map[string]string{
		"dead":        "ОШИБКА ИНСТРУМЕНТА: " + serverText,
		"limit":       "ОШИБКА ИНСТРУМЕНТА: " + limitText,
		"timeout":     "ОШИБКА ИНСТРУМЕНТА: " + timeoutText,
		"unavailable": "ОШИБКА ИНСТРУМЕНТА: " + serverText,
		"unknown":     unknownText,
	}
	wantSequences := map[string]any{
		"limit": []map[string]any{
			{"outcome": "ok", "seq": 1}, {"outcome": "ok", "seq": 2},
			{"outcome": "ok", "seq": 3}, {"outcome": "ok", "seq": 4},
			{"outcome": "ok", "seq": 5}, {"outcome": "ok", "seq": 6},
			{"outcome": "ok", "seq": 7}, {"outcome": "ok", "seq": 8},
			{"outcome": "ok", "seq": 9}, {"outcome": "ok", "seq": 10},
			{"outcome": "ok", "seq": 11}, {"outcome": "ok", "seq": 12},
			{"outcome": "ok", "seq": 13}, {"outcome": "ok", "seq": 14},
			{"outcome": "ok", "seq": 15}, {"outcome": "ok", "seq": 16},
			{"outcome": "limit", "seq": 17, "text": limitText},
		},
		"deadServer": []map[string]any{
			{"outcome": "unavailable", "seq": 1, "text": serverText},
			{"outcome": "dead", "seq": 2, "text": serverText},
		},
		"unknownAfterLimit": []map[string]string{
			{"outcome": "limit", "text": limitText},
			{"outcome": "rejected", "text": unknownText},
		},
	}
	assertSameJSON(t, "refusalTexts", dump.RefusalTexts, wantRefusals)
	assertSameJSON(t, "sequences", dump.Sequences, wantSequences)
	const wantNote = "Текст после «недоступен: » приходит от транспорта и отличается между средами выполнения."
	if dump.RefusalNote != wantNote {
		t.Fatalf("refusalNote=%q", dump.RefusalNote)
	}
}

func TestDumpRefusalsAreNotHandTyped(t *testing.T) {
	source, err := os.ReadFile("dump.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		[]byte("deadText :="),
		[]byte(`"ОШИБКА ИНСТРУМЕНТА:`),
	} {
		if bytes.Contains(source, forbidden) {
			t.Fatalf("dump.go hand-types a model-visible refusal: %s", forbidden)
		}
	}
	for _, required := range [][]byte{
		[]byte(`"unavailable": deadMessages[0],`),
		[]byte(`"dead":        deadMessages[1],`),
	} {
		if !bytes.Contains(source, required) {
			t.Fatalf("dump.go lost the executed refusal mapping: %s", required)
		}
	}
}

func assertSameJSON(t *testing.T, name string, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("%s mismatch\n got: %s\nwant: %s", name, gotJSON, wantJSON)
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
