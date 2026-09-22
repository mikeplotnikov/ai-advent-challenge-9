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

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func provider(t *testing.T, replies ...string) *httptest.Server {
	t.Helper()
	var calls int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(atomic.AddInt32(&calls, 1)) - 1
		if index >= len(replies) {
			index = len(replies) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, replies[index])
	}))
}
func toolReply(name, args string) string {
	return `{"model":"deepseek-flash","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"` + name + `","arguments":` + strconvQuote(args) + `}}]}}],"usage":{"prompt_tokens":10,"prompt_cache_hit_tokens":2,"completion_tokens":3}}`
}
func textReply(text string) string {
	return `{"model":"deepseek-flash","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":` + strconvQuote(text) + `}}],"usage":{"prompt_tokens":10,"prompt_cache_hit_tokens":2,"completion_tokens":3}}`
}
func strconvQuote(s string) string {
	var b bytes.Buffer
	_ = json.NewEncoder(&b).Encode(s)
	return strings.TrimSpace(b.String())
}
func setupProvider(t *testing.T, replies ...string) *httptest.Server {
	t.Helper()
	s := provider(t, replies...)
	t.Setenv("DEEPSEEK_API_URL", s.URL)
	t.Setenv("DEEPSEEK_API_KEY_DAY17", "test-day17-key")
	return s
}

func TestUsageExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{{"no question", nil}, {"endpoint command", []string{"-endpoint", "http://example.test/mcp", "-command", "x", "q"}}, {"no tools bench", []string{"-no-tools", "-bench"}}, {"unknown", []string{"-wat"}}} {
		t.Run(tc.name, func(t *testing.T) {
			var out, err bytes.Buffer
			if code := run(tc.args, &out, &err); code != 2 {
				t.Fatalf("code=%d stderr=%s", code, err.String())
			}
		})
	}
}
func TestMCPFailuresExitOne(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY_DAY17", "test-day17-key")
	for _, tc := range []struct {
		name string
		args []string
	}{{"missing command", []string{"-command", "/no/such/day17-server", "q"}}, {"closed endpoint", []string{"-endpoint", "http://127.0.0.1:1/mcp", "q"}}} {
		t.Run(tc.name, func(t *testing.T) {
			var out, err bytes.Buffer
			if code := run(tc.args, &out, &err); code != 1 {
				t.Fatalf("code=%d stderr=%s", code, err.String())
			}
		})
	}
}
func TestProviderAndSaveFailuresExitOneAfterAnswer(t *testing.T) {
	t.Run("provider 500", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "broken", http.StatusInternalServerError) }))
		defer server.Close()
		t.Setenv("DEEPSEEK_API_URL", server.URL)
		t.Setenv("DEEPSEEK_API_KEY_DAY17", "test-day17-key")
		var out, err bytes.Buffer
		if code := run([]string{"-no-tools", "q"}, &out, &err); code != 1 {
			t.Fatalf("code=%d stderr=%s", code, err.String())
		}
	})
	t.Run("save", func(t *testing.T) {
		server := setupProvider(t, textReply("готовый ответ"))
		defer server.Close()
		var out, err bytes.Buffer
		if code := run([]string{"-no-tools", "-save", filepath.Join(t.TempDir(), "missing", "trace.json"), "q"}, &out, &err); code != 1 || !strings.Contains(out.String(), "[ответ]") || !strings.Contains(out.String(), "готовый ответ") {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), err.String())
		}
	})
}
func TestNoToolsDoesNotStartMCPAndSaveHasNoKey(t *testing.T) {
	server := setupProvider(t, textReply("без MCP"))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "trace.json")
	var out, err bytes.Buffer
	if code := run([]string{"-no-tools", "-command", "/no/such/server", "-save", path, "q"}, &out, &err); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, err.String())
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if bytes.Contains(raw, []byte("Bearer")) || bytes.Contains(raw, []byte("test-day17-key")) {
		t.Fatalf("secret escaped into trace: %s", raw)
	}
	// Free of secrets is half of AC10; the other half is that it IS the trace.
	var saved toolagent.Trace
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("трасса не разбирается: %v\n%s", err, raw)
	}
	if saved.FinalAnswer != "без MCP" || saved.Totals.ModelCalls != 1 || len(saved.ModelCalls) != 1 || saved.ModelCalls[0].RequestBody == "" {
		t.Fatalf("в сохранённой трассе не тот прогон: %+v", saved)
	}
}

func TestPrintTraceKeepsToolCallsUnderTheirModelStep(t *testing.T) {
	trace := toolagent.Trace{ModelCalls: []toolagent.ModelCall{{FinishReason: "tool_calls"}, {FinishReason: "tool_calls"}}, ToolCalls: []toolagent.ToolCall{{Step: 2, Name: "second", RawArguments: `{}`, ResultText: "two"}, {Step: 1, Name: "first", RawArguments: `{}`, Rejected: "не существует"}}}
	var out bytes.Buffer
	printTrace(&out, trace)
	text := out.String()
	firstStep, firstCall := strings.Index(text, "шаг 1"), strings.Index(text, "first")
	secondStep, secondCall := strings.Index(text, "шаг 2"), strings.Index(text, "second")
	if !(firstStep < firstCall && firstCall < secondStep && secondStep < secondCall) {
		t.Fatalf("wrong trace order:\n%s", text)
	}
	if !strings.Contains(text, "[агент → модели] вызов отклонён без MCP") {
		t.Fatalf("rejection label missing:\n%s", text)
	}
}

func TestHappyPathOverStdioAndHTTP(t *testing.T) {
	fixture, err := ratesmcp.Fixtures.ReadFile("fixtures/2026-09-01.xml")
	if err != nil {
		t.Fatal(err)
	}
	cbr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fixture) }))
	defer cbr.Close()
	t.Setenv("CBR_URL", cbr.URL)
	provider := setupProvider(t, toolReply("convert_currency", `{"amount":250,"from":"USD","to":"RUB","date":"2026-09-01"}`), textReply("Получится 21594,825 рубля."))
	defer provider.Close()
	assertHappy := func(args []string) {
		var out, stderr bytes.Buffer
		if code := run(args, &out, &stderr); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
		for _, want := range []string{"[MCP] tools/list", "[модель → tool_call] convert_currency", "[MCP tools/call → результат]", "[ответ]"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("stdout misses %q:\n%s", want, out.String())
			}
		}
	}
	binary := filepath.Join(t.TempDir(), "mcp-server")
	build := exec.Command("go", "build", "-o", binary, "./mcp-server")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mcp-server: %v: %s", err, output)
	}
	assertHappy([]string{"-command", binary, "вопрос"})
	server := ratesmcp.NewServer(ratesmcp.Options{})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer httpServer.Close()
	provider2 := setupProvider(t, toolReply("convert_currency", `{"amount":250,"from":"USD","to":"RUB","date":"2026-09-01"}`), textReply("Получится 21594,825 рубля."))
	defer provider2.Close()
	assertHappy([]string{"-endpoint", httpServer.URL, "вопрос"})
}

func TestTheAnswerKeepsLineBreaksButNotControlCharacters(t *testing.T) {
	got := safeMultiline("Курсы:\r\n- CNY‮ 12,858\n- KZT \x1b[31m18,5854")
	want := "Курсы:\n- CNY� 12,858\n- KZT �[31m18,5854"
	if got != want {
		t.Errorf("safeMultiline = %q, want %q", got, want)
	}
}
