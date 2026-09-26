package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/pipelinemcp"
)

var (
	testBinDir string
	testBins   map[string]string
)

func TestMain(m *testing.M) {
	var err error
	testBinDir, err = os.MkdirTemp("", "day20-test-bins-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testBins = map[string]string{}
	packages := map[string]string{
		"clock": "./clock-server", "rates": "../day-17/mcp-server",
		"pipeline": "../day-19/mcp-server", "testserver": "../internal/mcprouter/testserver",
	}
	for name, pkg := range packages {
		path := filepath.Join(testBinDir, name)
		command := exec.Command("go", "build", "-o", path, pkg)
		command.Env = os.Environ()
		if output, buildErr := command.CombinedOutput(); buildErr != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", pkg, buildErr, output)
			_ = os.RemoveAll(testBinDir)
			os.Exit(1)
		}
		testBins[name] = path
	}
	code := m.Run()
	_ = os.RemoveAll(testBinDir)
	os.Exit(code)
}

func TestCLIIntegrationL1AndSave_AC11_AC13(t *testing.T) {
	cbrServer := fakeDay20CBR(t)
	defer cbrServer.Close()
	setCBREnv(t, cbrServer.URL)
	provider := newScriptedProvider(t, "l1")
	defer provider.Close()
	t.Setenv("DEEPSEEK_API_URL", provider.URL())
	const key = "day20-integration-secret"
	t.Setenv("DEEPSEEK_API_KEY_DAY20", key)
	reports := t.TempDir()
	registry := writeTestRegistry(t, reports, testBins["rates"])
	tracePath := filepath.Join(t.TempDir(), "trace.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-servers", registry, "-save", tracePath, defaultQuestion}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if provider.Calls() != 6 {
		t.Fatalf("model calls=%d", provider.Calls())
	}
	for _, want := range []string{"→ clock]", "→ rates]", "→ pipeline]", "routedOK=true", "orderOK=true"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	entries, err := os.ReadDir(reports)
	if err != nil || len(entries) != 1 {
		t.Fatalf("reports=%d err=%v", len(entries), err)
	}
	if !strings.Contains(stdout.String(), entries[0].Name()) {
		t.Fatalf("answer does not name %s:\n%s", entries[0].Name(), stdout.String())
	}
	raw, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(key)) || strings.Contains(stdout.String(), key) {
		t.Fatal("provider key escaped into output")
	}
	if !bytes.Contains(raw, []byte("[REDACTED]")) || !strings.Contains(stdout.String(), "[REDACTED]") {
		t.Fatal("reflected provider key did not traverse the real redaction path")
	}
	var saved Day20Trace
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Trace.ModelCalls) != 6 || len(saved.Trace.ToolCalls) != 6 || len(saved.Servers) != 3 ||
		len(saved.Journal) != 6 || !saved.Checks.RoutedOK || !saved.Checks.OrderOK {
		t.Fatalf("incomplete saved trace: %+v", saved)
	}
	if saved.Trace.ToolCalls[1].Step != saved.Trace.ToolCalls[2].Step {
		t.Fatalf("expected two tool calls in one model turn: %+v", saved.Trace.ToolCalls)
	}
	currentDate := fmt.Sprint(saved.Trace.ToolCalls[0].Structured.(map[string]any)["date"])
	shiftedDate := fmt.Sprint(saved.Trace.ToolCalls[1].Structured.(map[string]any)["date"])
	if fmt.Sprint(saved.Trace.ToolCalls[2].Arguments["date"]) != currentDate ||
		fmt.Sprint(saved.Trace.ToolCalls[3].Arguments["date_to"]) != currentDate ||
		fmt.Sprint(saved.Trace.ToolCalls[3].Arguments["date_from"]) != shiftedDate ||
		fmt.Sprint(saved.Trace.ToolCalls[1].Arguments["date"]) != currentDate ||
		fmt.Sprint(saved.Trace.ToolCalls[1].Arguments["days"]) != "-13" {
		t.Fatalf("dates do not derive from clock results: %+v", saved.Trace.ToolCalls)
	}
	provider.AssertContract(t)
}

func TestCLIListPreservesRegistryAndToolOrderWithoutModel_AC1(t *testing.T) {
	provider := newScriptedProvider(t, "unused")
	defer provider.Close()
	t.Setenv("DEEPSEEK_API_URL", provider.URL())
	t.Setenv("DEEPSEEK_API_KEY_DAY20", "must-not-be-used")
	registry := writeTestRegistry(t, t.TempDir(), testBins["rates"])
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-servers", registry, "-list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if provider.Calls() != 0 {
		t.Fatalf("model requests=%d", provider.Calls())
	}
	if count := strings.Count(stdout.String(), "\n  "); count != 7 {
		t.Fatalf("listed tool lines=%d, want 7:\n%s", count, stdout.String())
	}
	assertTextOrder(t, stdout.String(), []string{"[MCP] clock ", "[MCP] rates ", "[MCP] pipeline "})
	assertTextOrder(t, stdout.String(), []string{
		"  clock__current_date ", "  clock__shift_date ",
		"  rates__convert_currency ", "  rates__get_currency_rates ",
		"  pipeline__fetch_rates ", "  pipeline__save_report ", "  pipeline__summarize_rates ",
	})
}

func assertTextOrder(t *testing.T, text string, values []string) {
	t.Helper()
	position := -1
	for _, value := range values {
		next := strings.Index(text[position+1:], value)
		if next < 0 {
			t.Fatalf("missing %q after byte %d:\n%s", value, position, text)
		}
		position += next + 1
	}
}

func TestCLIDirectUsesThreeServersAndSavesReport_AC10(t *testing.T) {
	cbrServer := fakeDay20CBR(t)
	defer cbrServer.Close()
	setCBREnv(t, cbrServer.URL)
	reports := t.TempDir()
	registry := writeTestRegistry(t, reports, testBins["rates"])
	var stdout, stderr bytes.Buffer
	code := run([]string{"-servers", registry, "-direct"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	for _, name := range []string{"clock__current_date", "clock__shift_date", "rates__convert_currency",
		"pipeline__fetch_rates", "pipeline__summarize_rates", "pipeline__save_report"} {
		if !strings.Contains(stdout.String(), name) {
			t.Errorf("missing %s", name)
		}
	}
	if count := strings.Count(stdout.String(), "[direct →"); count != 6 {
		t.Fatalf("direct calls=%d\n%s", count, stdout.String())
	}
	entries, _ := os.ReadDir(reports)
	if len(entries) != 1 {
		t.Fatalf("reports=%d", len(entries))
	}
}

func TestCLIExitOneForStartupOrderAndDeadServer_AC2_AC6_AC11(t *testing.T) {
	cbrServer := fakeDay20CBR(t)
	defer cbrServer.Close()
	setCBREnv(t, cbrServer.URL)
	t.Run("startup", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "registry.json")
		_ = os.WriteFile(path, []byte(`{"mcpServers":{"bad":{"command":"missing-day20-command"}}}`), 0o600)
		var stdout, stderr bytes.Buffer
		if code := run([]string{"-servers", path, "question"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "bad") {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
	})
	t.Run("same_step_date", func(t *testing.T) {
		provider := newScriptedProvider(t, "same-step")
		defer provider.Close()
		t.Setenv("DEEPSEEK_API_URL", provider.URL())
		t.Setenv("DEEPSEEK_API_KEY_DAY20", "test-key")
		var stdout, stderr bytes.Buffer
		code := run([]string{"-servers", writeTestRegistry(t, t.TempDir(), testBins["rates"]), "relative date"}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stdout.String(), "orderOK=false") {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	})
	t.Run("same_step_missing_date", func(t *testing.T) {
		provider := newScriptedProvider(t, "same-step-missing-date")
		defer provider.Close()
		t.Setenv("DEEPSEEK_API_URL", provider.URL())
		t.Setenv("DEEPSEEK_API_KEY_DAY20", "test-key")
		var stdout, stderr bytes.Buffer
		code := run([]string{"-servers", writeTestRegistry(t, t.TempDir(), testBins["rates"]), "today conversion"}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stdout.String(), "orderOK=false") ||
			!strings.Contains(stdout.String(), "(в) rates__convert_currency") {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	})
	t.Run("server_dies_but_model_finishes", func(t *testing.T) {
		provider := newScriptedProvider(t, "dead")
		defer provider.Close()
		t.Setenv("DEEPSEEK_API_URL", provider.URL())
		t.Setenv("DEEPSEEK_API_KEY_DAY20", "test-key")
		var stdout, stderr bytes.Buffer
		registry := writeTestRegistry(t, t.TempDir(), testBins["testserver"]+" --rates-die")
		code := run([]string{"-servers", registry, "convert after clock"}, &stdout, &stderr)
		if code != 1 || provider.Calls() != 3 || !strings.Contains(stdout.String(), "недоступен") ||
			!strings.Contains(stdout.String(), "[ответ]") {
			t.Fatalf("code=%d calls=%d\nstdout=%s\nstderr=%s", code, provider.Calls(), stdout.String(), stderr.String())
		}
	})
}

type scriptedProvider struct {
	server   *httptest.Server
	mode     string
	calls    atomic.Int32
	mu       sync.Mutex
	contract []byte
}

func newScriptedProvider(t *testing.T, mode string) *scriptedProvider {
	t.Helper()
	p := &scriptedProvider{mode: mode}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		step := int(p.calls.Add(1))
		if step == 1 {
			p.mu.Lock()
			p.contract = append([]byte(nil), body...)
			p.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		reflectedKey := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		_, _ = io.WriteString(w, p.reply(t, step, body, reflectedKey))
	}))
	return p
}

func (p *scriptedProvider) Close()      { p.server.Close() }
func (p *scriptedProvider) URL() string { return p.server.URL }
func (p *scriptedProvider) Calls() int  { return int(p.calls.Load()) }

func (p *scriptedProvider) reply(t *testing.T, step int, body []byte, reflectedKey string) string {
	t.Helper()
	switch p.mode {
	case "l1":
		switch step {
		case 1:
			return providerTools([]testToolCall{{"current", "clock__current_date", `{}`}})
		case 2:
			return providerTools([]testToolCall{
				{"shift", "clock__shift_date", `{"date":"2026-09-25","days":-13}`},
				{"convert", "rates__convert_currency", `{"amount":1000,"from":"USD","to":"EUR","date":"2026-09-25"}`},
			})
		case 3:
			return providerTools([]testToolCall{{"fetch", "pipeline__fetch_rates", `{"currency":"EUR","date_from":"2026-09-12","date_to":"2026-09-25"}`}})
		case 4:
			return providerTools([]testToolCall{{"summary", "pipeline__summarize_rates", `{"dataset_id":"` + extract(body, `dataset_id: (ds_[a-f0-9]+)`) + `"}`}})
		case 5:
			return providerTools([]testToolCall{{"save", "pipeline__save_report", `{"summary_id":"` + extract(body, `summary_id: (sm_[a-f0-9]+)`) + `"}`}})
		case 6:
			return providerFinal("Готово: " + extract(body, `name: ([^\\n]+\.md)`) + ". Provider echo: " + reflectedKey)
		}
	case "same-step":
		if step == 1 {
			return providerTools([]testToolCall{{"current", "clock__current_date", `{}`},
				{"convert", "rates__convert_currency", `{"amount":1,"from":"USD","to":"EUR","date":"2026-09-25"}`}})
		}
		return providerFinal("Завершено")
	case "same-step-missing-date":
		if step == 1 {
			return providerTools([]testToolCall{{"current", "clock__current_date", `{}`},
				{"convert", "rates__convert_currency", `{"amount":1,"from":"USD","to":"EUR"}`}})
		}
		return providerFinal("Завершено")
	case "dead":
		if step == 1 {
			return providerTools([]testToolCall{{"current", "clock__current_date", `{}`}})
		}
		if step == 2 {
			return providerTools([]testToolCall{{"convert", "rates__convert_currency", `{"amount":1,"from":"USD","to":"EUR","date":"2026-09-25"}`}})
		}
		return providerFinal("Не удалось выполнить конвертацию")
	}
	t.Fatalf("unexpected provider step mode=%s step=%d", p.mode, step)
	return providerFinal("unexpected")
}

func (p *scriptedProvider) AssertContract(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	raw := append([]byte(nil), p.contract...)
	p.mu.Unlock()
	var request struct {
		Model       string   `json:"model"`
		Temperature *float64 `json:"temperature"`
		Messages    []struct {
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if request.Model != modelName || request.Temperature == nil || *request.Temperature != 0 ||
		len(request.Messages) < 1 || request.Messages[0].Content != systemPrompt || len(request.Tools) != 7 {
		t.Fatalf("request contract: %+v", request)
	}
	want := []string{"clock__current_date", "clock__shift_date", "rates__convert_currency",
		"rates__get_currency_rates", "pipeline__fetch_rates", "pipeline__save_report", "pipeline__summarize_rates"}
	for i := range want {
		if request.Tools[i].Function.Name != want[i] {
			t.Fatalf("tool[%d]=%s want=%s", i, request.Tools[i].Function.Name, want[i])
		}
	}
}

type testToolCall struct{ id, name, args string }

func providerTools(calls []testToolCall) string {
	items := make([]string, 0, len(calls))
	for _, call := range calls {
		args, _ := json.Marshal(call.args)
		items = append(items, `{"id":"`+call.id+`","type":"function","function":{"name":"`+call.name+`","arguments":`+string(args)+`}}`)
	}
	return `{"model":"deepseek-flash","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[` + strings.Join(items, ",") + `]}}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`
}

func providerFinal(text string) string {
	quoted, _ := json.Marshal(text)
	return `{"model":"deepseek-flash","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":` + string(quoted) + `}}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`
}

func extract(body []byte, pattern string) string {
	match := regexp.MustCompile(pattern).FindSubmatch(body)
	if len(match) != 2 {
		return "missing"
	}
	return string(match[1])
}

func fakeDay20CBR(t *testing.T) *httptest.Server {
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
			http.Error(w, "missing User-Agent", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/daily":
			_, _ = w.Write(daily)
		case "/dynamic":
			id := r.URL.Query().Get("VAL_NM_RQ")
			if id == "" {
				http.Error(w, "missing currency id", http.StatusBadRequest)
				return
			}
			_, _ = w.Write(bytes.ReplaceAll(dynamic, []byte("R01235"), []byte(id)))
		default:
			http.NotFound(w, r)
		}
	}))
}

func setCBREnv(t *testing.T, base string) {
	t.Helper()
	t.Setenv("CBR_URL", base+"/daily")
	t.Setenv("CBR_DYNAMIC_URL", base+"/dynamic")
}

func writeTestRegistry(t *testing.T, reports, ratesCommand string) string {
	t.Helper()
	ratesParts := strings.Split(ratesCommand, " ")
	commandJSON := func(command string, args []string) string {
		encodedCommand, _ := json.Marshal(command)
		encodedArgs, _ := json.Marshal(args)
		return `{"command":` + string(encodedCommand) + `,"args":` + string(encodedArgs) + `}`
	}
	raw := `{"mcpServers":{` +
		`"clock":` + commandJSON(testBins["clock"], []string{"-now", "2026-09-25T12:00:00+03:00"}) + `,` +
		`"rates":` + commandJSON(ratesParts[0], ratesParts[1:]) + `,` +
		`"pipeline":` + commandJSON(testBins["pipeline"], []string{"-reports", reports}) + `}}`
	path := filepath.Join(t.TempDir(), "servers.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
