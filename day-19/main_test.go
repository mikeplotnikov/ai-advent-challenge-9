package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/pipelinemcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

func TestUsageAndVerifyExitCodes(t *testing.T) {
	for _, args := range [][]string{nil, {"-direct"}, {"-dump", "question"}, {"-direct", "-dump"}, {"-wat"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
	path := filepath.Join(t.TempDir(), "bad.md")
	if err := os.WriteFile(path, []byte("not a report"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-verify", path}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "проверка: формат") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	dataset := pipelinemcp.Dataset{Currency: "USD", CBRID: "R01235", Name: "Dollar", DateFrom: "2026-09-24", DateTo: "2026-09-24", Source: pipelinemcp.Source, Rows: []pipelinemcp.DatasetRow{{Date: "2026-09-24", Nominal: "1", Value: "84.396900", UnitRate: "84.396900"}}}
	_, datasetSHA, _ := pipelinemcp.HashCanonical(dataset)
	summary, _ := pipelinemcp.Summarize(dataset, datasetSHA)
	_, summarySHA, _ := pipelinemcp.HashCanonical(summary)
	report, _ := pipelinemcp.RenderReport(dataset, datasetSHA, summary, summarySHA)
	validPath := filepath.Join(t.TempDir(), "valid.md")
	if err := os.WriteFile(validPath, report, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-verify", validPath}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "проверка: ok") {
		t.Fatalf("valid report code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func fakeCBR(t *testing.T) *httptest.Server {
	t.Helper()
	daily, err := pipelinemcp.Fixtures.ReadFile("fixtures/daily.xml")
	if err != nil {
		t.Fatal(err)
	}
	dynamic, err := pipelinemcp.Fixtures.ReadFile("fixtures/dynamic-usd.xml")
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "missing UA", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/daily":
			_, _ = w.Write(daily)
		case "/dynamic":
			if r.URL.Query().Get("VAL_NM_RQ") != "R01235" {
				http.Error(w, "wrong id", http.StatusBadRequest)
				return
			}
			_, _ = w.Write(dynamic)
		default:
			http.NotFound(w, r)
		}
	}))
}

func buildServer(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "day19-mcp")
	command := exec.Command("go", "build", "-o", binary, "./mcp-server")
	command.Env = append(os.Environ(), "GOCACHE=/tmp/day19-gocache")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}
	return binary
}

func TestDirectModeOverStdioCreatesVerifiableReport(t *testing.T) {
	upstream := fakeCBR(t)
	defer upstream.Close()
	t.Setenv("CBR_URL", upstream.URL+"/daily")
	t.Setenv("CBR_DYNAMIC_URL", upstream.URL+"/dynamic")
	reports := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"-command", buildServer(t), "-reports", reports, "-direct", "-currency", "USD", "-from", "2026-09-10", "-to", "2026-09-24"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "fetch_rates") || !strings.Contains(stdout.String(), "summarize_rates") || !strings.Contains(stdout.String(), "save_report") || !strings.Contains(stdout.String(), "проверка: ok") {
		t.Fatalf("code=%d\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	entries, _ := os.ReadDir(reports)
	if len(entries) != 1 {
		t.Fatalf("reports=%d", len(entries))
	}
}

func providerReply(tool, args string) string {
	quoted, _ := json.Marshal(args)
	return `{"model":"deepseek-flash","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"` + tool + `","arguments":` + string(quoted) + `}}]}}],"usage":{"prompt_tokens":10,"prompt_cache_hit_tokens":2,"completion_tokens":3}}`
}

func finalReply(text string) string {
	quoted, _ := json.Marshal(text)
	return `{"model":"deepseek-flash","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":` + string(quoted) + `}}],"usage":{"prompt_tokens":10,"prompt_cache_hit_tokens":2,"completion_tokens":3}}`
}

func TestAgentRunsTheThreeToolChainFromOneQuestion(t *testing.T) {
	upstream := fakeCBR(t)
	defer upstream.Close()
	t.Setenv("CBR_URL", upstream.URL+"/daily")
	t.Setenv("CBR_DYNAMIC_URL", upstream.URL+"/dynamic")
	replies := []string{
		providerReply("fetch_rates", `{"currency":"USD","date_from":"2026-09-10","date_to":"2026-09-24"}`),
		providerReply("summarize_rates", `{"dataset_id":"ds_69036041ed5d"}`),
		providerReply("save_report", `{"summary_id":"sm_313e6c62b52e"}`),
		finalReply("Готово: USD_2026-09-10_2026-09-24_313e6c62b52e.md"),
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		index := int(calls.Add(1)) - 1
		if index == 0 {
			var request struct {
				Model       string                           `json:"model"`
				Temperature *float64                         `json:"temperature"`
				Messages    []struct{ Role, Content string } `json:"messages"`
				Tools       []any                            `json:"tools"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Errorf("request JSON: %v", err)
			} else if request.Model != modelName || request.Temperature == nil || *request.Temperature != 0 || len(request.Messages) < 2 || !strings.Contains(request.Messages[0].Content, "fetch_rates → summarize_rates → save_report") || !strings.Contains(request.Messages[0].Content, "последние 30") || !strings.Contains(request.Messages[0].Content, "ошибку") || !strings.Contains(request.Messages[0].Content, time.Now().In(cbr.Moscow).Format("2006-01-02")) || len(request.Tools) != 3 {
				t.Errorf("runtime agent contract missing: %+v", request)
			}
		}
		if index >= len(replies) {
			http.Error(w, "too many calls", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, replies[index])
	}))
	defer provider.Close()
	t.Setenv("DEEPSEEK_API_URL", provider.URL)
	t.Setenv("DEEPSEEK_API_KEY_DAY19", "test-key")
	reports := t.TempDir()
	tracePath := filepath.Join(t.TempDir(), "trace.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-command", buildServer(t), "-reports", reports, "-save", tracePath, "Сводка по доллару, сохрани отчёт"}, &stdout, &stderr)
	if code != 0 || calls.Load() != 4 || !strings.Contains(stdout.String(), "fetch_rates") || !strings.Contains(stdout.String(), "summarize_rates") || !strings.Contains(stdout.String(), "save_report") || !strings.Contains(stdout.String(), "проверка: ok") || !strings.Contains(stdout.String(), "USD_2026-09-10_2026-09-24_313e6c62b52e.md") {
		t.Fatalf("code=%d calls=%d\nstdout=%s\nstderr=%s", code, calls.Load(), stdout.String(), stderr.String())
	}
	trace, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(trace, []byte("test-key")) || bytes.Contains(trace, []byte("Bearer")) {
		t.Fatalf("secret escaped into trace: %s", trace)
	}
	var savedTrace toolagent.Trace
	if err := json.Unmarshal(trace, &savedTrace); err != nil {
		t.Fatalf("saved trace is not JSON: %v", err)
	}
	if len(savedTrace.ModelCalls) != 4 || len(savedTrace.ToolCalls) != 3 || savedTrace.Totals.ModelCalls != 4 || savedTrace.Totals.ToolCalls != 3 || !strings.Contains(savedTrace.FinalAnswer, "USD_2026-09-10_2026-09-24_313e6c62b52e.md") {
		t.Fatalf("saved trace lost calls or answer: %+v", savedTrace)
	}
	if output, ok := savedOutput(savedTrace); !ok || output.Name != "USD_2026-09-10_2026-09-24_313e6c62b52e.md" {
		t.Fatalf("saved trace lost report output: ok=%v output=%+v", ok, output)
	}
}

func TestAgentRejectsFinalAnswerWithoutTheSavedFilename(t *testing.T) {
	upstream := fakeCBR(t)
	defer upstream.Close()
	t.Setenv("CBR_URL", upstream.URL+"/daily")
	t.Setenv("CBR_DYNAMIC_URL", upstream.URL+"/dynamic")
	replies := []string{
		providerReply("fetch_rates", `{"currency":"USD","date_from":"2026-09-10","date_to":"2026-09-24"}`),
		providerReply("summarize_rates", `{"dataset_id":"ds_69036041ed5d"}`),
		providerReply("save_report", `{"summary_id":"sm_313e6c62b52e"}`),
		finalReply("Готово"),
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		index := int(calls.Add(1)) - 1
		if index >= len(replies) {
			http.Error(w, "too many calls", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, replies[index])
	}))
	defer provider.Close()
	t.Setenv("DEEPSEEK_API_URL", provider.URL)
	t.Setenv("DEEPSEEK_API_KEY_DAY19", "test-key")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-command", buildServer(t), "-reports", t.TempDir(), "Сводка по доллару, сохрани отчёт"}, &stdout, &stderr)
	if code != 1 || calls.Load() != 4 || !strings.Contains(stderr.String(), "не называет сохранённый файл") || !strings.Contains(stdout.String(), "проверка: ok") {
		t.Fatalf("code=%d calls=%d\nstdout=%s\nstderr=%s", code, calls.Load(), stdout.String(), stderr.String())
	}
}

func TestAgentRejectsAnIncompletePipeline(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, finalReply("Готово без инструментов"))
	}))
	defer provider.Close()
	t.Setenv("DEEPSEEK_API_URL", provider.URL)
	t.Setenv("DEEPSEEK_API_KEY_DAY19", "test-key")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-command", buildServer(t), "-reports", t.TempDir(), "Сохрани отчёт"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "не завершил цепочку") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestAgentPassesTheSixCallLimitToToolagent(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, providerReply("not_a_tool", `{}`))
	}))
	defer provider.Close()
	t.Setenv("DEEPSEEK_API_URL", provider.URL)
	t.Setenv("DEEPSEEK_API_KEY_DAY19", "test-key")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-command", buildServer(t), "-reports", t.TempDir(), "Сохрани отчёт"}, &stdout, &stderr)
	if code != 1 || calls.Load() != 6 || !strings.Contains(stderr.String(), "6 обращений") {
		t.Fatalf("code=%d calls=%d stdout=%s stderr=%s", code, calls.Load(), stdout.String(), stderr.String())
	}
}

func TestPromptUsesMoscowDateAndSixCallLimit(t *testing.T) {
	got := promptToday(time.Date(2026, 9, 23, 22, 30, 0, 0, time.UTC))
	if !strings.Contains(got, "2026-09-24") || day19ModelCalls != 6 {
		t.Fatalf("prompt=%q limit=%d", got, day19ModelCalls)
	}
}
