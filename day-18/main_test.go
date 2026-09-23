package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/watch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestUsageExitCodes(t *testing.T) {
	cases := [][]string{nil, {"-tick", "question"}, {"-digest-every", "30s", "-tick"}, {"-digest-every", "25h", "-tick"}, {"-window", "0h", "-tick"}, {"-window", "1h30m", "-tick"}, {"-window", "721h", "-tick"}, {"-wat"}, {"-report"}}
	for _, args := range cases {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 2 || !strings.Contains(err.String(), "usage:") {
			t.Errorf("args=%v code=%d stderr=%s", args, code, err.String())
		}
	}
}

func TestMCPChildEnvironmentExcludesProviderSecrets(t *testing.T) {
	got := withoutProviderSecrets([]string{"PATH=/bin", "DEEPSEEK_API_KEY_DAY18=day", "DEEPSEEK_API_KEY=fallback", "DEEPSEEK_API_KEY_DAY5=other-day", "CBR_URL=http://example.test"})
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "DEEPSEEK_API_KEY") || !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "CBR_URL=") {
		t.Fatalf("environment=%q", joined)
	}
}

func TestNumbersAcceptanceCases(t *testing.T) {
	for _, tc := range []struct {
		text  string
		value float64
		want  bool
	}{{"81,12", 81.1234, true}, {"81 123,4", 81123.4, true}, {"без числа", 81.1234, false}, {"80.1", 81.1234, false}} {
		if got := usesValue(tc.text, tc.value); got != tc.want {
			t.Errorf("usesValue(%q,%v)=%v", tc.text, tc.value, got)
		}
	}
}

func TestDigestChecksQuotedMissingAndFailed(t *testing.T) {
	structured := map[string]any{"now": "2026-09-24T00:00:00Z", "watches": []any{map[string]any{
		"watch_id": "w1", "codes": []any{"USD"}, "every_minutes": 60, "status": "active", "hours": 24,
		"from": "2026-09-23T00:00:00Z", "to": "2026-09-24T00:00:00Z", "polls": map[string]any{}, "publications": []any{}, "missing_codes": []any{},
		"currencies": []any{map[string]any{"code": "USD", "first": map[string]any{"rates_date": "2026-09-24", "unit_rate": 81.1234}, "last": map[string]any{"rates_date": "2026-09-24", "unit_rate": 81.1234}}},
	}}}
	trace := toolagent.Trace{FinalAnswer: "курс не назван", Totals: toolagent.Totals{CostKnown: true}, ToolCalls: []toolagent.ToolCall{{Name: "get_watch_summary", Structured: structured}}}
	digest := digestFromTrace(trace, time.Now(), 3*time.Hour, 24, nil)
	if !digest.Checks.CalledSummary || digest.Checks.QuotesLastRates {
		t.Fatalf("checks=%+v", digest.Checks)
	}
	failed := digestFromTrace(trace, time.Now(), 3*time.Hour, 24, errors.New("model failed"))
	if failed.Checks.CalledSummary || failed.Checks.QuotesLastRates || failed.Summary == nil {
		t.Fatalf("failed=%+v", failed)
	}
	rejected := trace
	rejected.FinalAnswer = "81,12"
	rejected.ToolCalls = []toolagent.ToolCall{{Name: "get_watch_summary", Rejected: "аргументы не разобраны", ParseError: "ожидался JSON-объект"}}
	invalid := digestFromTrace(rejected, time.Now(), 3*time.Hour, 24, nil)
	if invalid.Checks.CalledSummary || invalid.Checks.QuotesLastRates || invalid.Summary != nil {
		t.Fatalf("rejected call counted as successful: %+v", invalid)
	}
}

func buildMCPServer(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "day18-mcp")
	cmd := exec.Command("go", "build", "-o", binary, "./mcp-server")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	return binary
}

func provider(t *testing.T, replies ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		index := int(calls.Add(1)) - 1
		if index >= len(replies) {
			index = len(replies) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, replies[index])
	}))
	return server, &calls
}

func newHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("локальный HTTP-сервер не запущен: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server
}
func quote(value string) string { raw, _ := json.Marshal(value); return string(raw) }
func toolReply(name, args string) string {
	return `{"model":"deepseek-flash","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"` + name + `","arguments":` + quote(args) + `}}]}}],"usage":{"prompt_tokens":100,"prompt_cache_hit_tokens":40,"completion_tokens":5}}`
}
func textReply(text string) string {
	return `{"model":"deepseek-flash","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":` + quote(text) + `}}],"usage":{"prompt_tokens":80,"prompt_cache_hit_tokens":30,"completion_tokens":20}}`
}
func setupProvider(t *testing.T, replies ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	server, calls := provider(t, replies...)
	t.Setenv("DEEPSEEK_API_URL", server.URL)
	t.Setenv("DEEPSEEK_API_KEY_DAY18", "test-day18-key")
	return server, calls
}

func activeStore(t *testing.T, path string, value float64) {
	t.Helper()
	store := watch.NewStore(path)
	now := time.Now().UTC()
	poll := watch.Poll{At: now.Format(time.RFC3339), OK: true, RatesDate: now.Format("2006-01-02"), Rates: map[string]float64{"USD": value}}
	if _, err := store.Create([]string{"USD"}, 60, now, poll); err != nil {
		t.Fatal(err)
	}
}

func TestTickIntegrationAndRepeat(t *testing.T) {
	binary := buildMCPServer(t)
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")
	digestsPath := filepath.Join(dir, "digests.json")
	activeStore(t, storePath, 81.1234)
	server, calls := setupProvider(t, toolReply("get_watch_summary", `{"hours":24}`), textReply("USD: 81,12 рубля."))
	defer server.Close()
	var out, errOut bytes.Buffer
	args := []string{"-tick", "-store", storePath, "-digests", digestsPath, "-command", binary + " -store " + storePath}
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	digests, err := readDigests(digestsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(digests) != 1 || !digests[0].Checks.CalledSummary || !digests[0].Checks.QuotesLastRates || digests[0].Summary == nil || len(digests[0].Tokens.PerCall) != 2 {
		t.Fatalf("%+v", digests)
	}
	digest := digests[0]
	if digest.At == "" || digest.SlotStart == "" || digest.DigestEvery != "3h0m0s" || digest.WindowHours != 24 || digest.Text == "" || digest.Error != "" || digest.Model == "" || digest.ModelCalls != 2 || len(digest.ToolCalls) != 1 || digest.ToolCalls[0].Name != "get_watch_summary" || digest.Tokens.Prompt != 180 || digest.Tokens.Cached != 70 || digest.Tokens.Output != 25 || !digest.CostKnown || digest.Cost <= 0 {
		t.Fatalf("incomplete digest: %+v", digest)
	}
	raw, _ := os.ReadFile(digestsPath)
	if bytes.Contains(raw, []byte("test-day18-key")) || bytes.Contains(raw, []byte("Bearer")) {
		t.Fatal("secret in digests")
	}
	before := calls.Load()
	out.Reset()
	errOut.Reset()
	if code := run(args, &out, &errOut); code != 0 || !strings.Contains(out.String(), "уже есть") || calls.Load() != before {
		t.Fatalf("repeat code=%d calls=%d/%d out=%s err=%s", code, calls.Load(), before, out.String(), errOut.String())
	}
}

func TestTickNoActiveSkipsModelAndProviderFailureIsRecorded(t *testing.T) {
	binary := buildMCPServer(t)
	t.Run("no active", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DEEPSEEK_API_KEY_DAY18", "")
		t.Setenv("DEEPSEEK_API_KEY", "")
		var out, errOut bytes.Buffer
		code := run([]string{"-tick", "-store", filepath.Join(dir, "store.json"), "-digests", filepath.Join(dir, "digests.json"), "-command", binary + " -store " + filepath.Join(dir, "store.json")}, &out, &errOut)
		if code != 0 || !strings.Contains(out.String(), "активных наблюдений нет") {
			t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
		}
	})
	t.Run("provider 500", func(t *testing.T) {
		dir := t.TempDir()
		storePath := filepath.Join(dir, "store.json")
		digestPath := filepath.Join(dir, "digests.json")
		activeStore(t, storePath, 81.1234)
		server := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "broken", 500) }))
		defer server.Close()
		t.Setenv("DEEPSEEK_API_URL", server.URL)
		t.Setenv("DEEPSEEK_API_KEY_DAY18", "test-key")
		var out, errOut bytes.Buffer
		code := run([]string{"-tick", "-store", storePath, "-digests", digestPath, "-command", binary + " -store " + storePath}, &out, &errOut)
		digests, readErr := readDigests(digestPath)
		if code != 1 || readErr != nil || len(digests) != 1 || digests[0].Error == "" || digests[0].Text != "" {
			t.Fatalf("code=%d digests=%+v read=%v err=%s", code, digests, readErr, errOut.String())
		}
	})
}

func TestMissingCommandExitOne(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY_DAY18", "test-key")
	var out, errOut bytes.Buffer
	if code := run([]string{"-command", "/no/such/day18-server", "question"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "MCP:") {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
}

func TestProviderFailureExitOneWithoutAnswer(t *testing.T) {
	binary := buildMCPServer(t)
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")
	activeStore(t, storePath, 81)
	server := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "broken", 500) }))
	defer server.Close()
	t.Setenv("DEEPSEEK_API_URL", server.URL)
	t.Setenv("DEEPSEEK_API_KEY_DAY18", "test-key")
	var out, errOut bytes.Buffer
	if code := run([]string{"-command", binary + " -store " + storePath, "question"}, &out, &errOut); code != 1 || strings.Contains(out.String(), "[ответ]") || !strings.Contains(errOut.String(), "500") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
}

func TestAgentForwardsSchedulerStderr(t *testing.T) {
	fixture, err := ratesmcp.Fixtures.ReadFile("fixtures/2026-09-01.xml")
	if err != nil {
		t.Fatal(err)
	}
	cbrServer := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fixture) }))
	defer cbrServer.Close()
	t.Setenv("CBR_URL", cbrServer.URL)
	provider, _ := setupProvider(t, toolReply("list_watches", `{}`), textReply("готово"))
	defer provider.Close()
	binary := buildMCPServer(t)
	storePath := filepath.Join(t.TempDir(), "store.json")
	store := watch.NewStore(storePath)
	at := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := store.Create([]string{"USD"}, 60, at, watch.Poll{At: at.Format(time.RFC3339), OK: true, RatesDate: "2026-09-01", Rates: map[string]float64{"USD": 80}}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"-command", binary + " -store " + storePath + " -check-every 1s", "покажи состояние"}, &stdout, &stderr)
	for _, want := range []string{"[MCP] tools/list", "[модель · шаг 1]", "[модель → tool_call]", "[MCP tools/call → результат]", "[ответ]", "готово"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q: %s", want, stdout.String())
		}
	}
	if code != 0 || !strings.Contains(stderr.String(), "[планировщик") || !strings.Contains(stderr.String(), "[day-18:") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestTickBusyDigestLockExitsZeroWithoutModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "digests.json")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	model := &digestLLM{}
	var stdout, stderr bytes.Buffer
	code := runTick(context.Background(), &fakeSession{}, model, path, time.Hour, 24, &stdout, &stderr)
	if code != 0 || model.calls.Load() != 0 || !strings.Contains(stdout.String(), "другой процесс уже выпускает сводку") {
		t.Fatalf("code=%d calls=%d stdout=%q stderr=%q", code, model.calls.Load(), stdout.String(), stderr.String())
	}
}

func TestCorruptDigestsRemainUnchanged(t *testing.T) {
	binary := buildMCPServer(t)
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")
	activeStore(t, storePath, 81)
	digestPath := filepath.Join(dir, "digests.json")
	raw := []byte("broken")
	if err := os.WriteFile(digestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY_DAY18", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	var out, errOut bytes.Buffer
	code := run([]string{"-tick", "-store", storePath, "-digests", digestPath, "-command", binary + " -store " + storePath}, &out, &errOut)
	after, _ := os.ReadFile(digestPath)
	if code != 1 || !bytes.Equal(raw, after) || !strings.Contains(errOut.String(), "файл не изменён") {
		t.Fatalf("code=%d after=%q err=%s", code, after, errOut.String())
	}
}

type fakeSession struct {
	mu        sync.Mutex
	calls     int
	failAfter int
}

func (s *fakeSession) ListTools(context.Context) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "get_watch_summary", Description: "summary", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"hours": map[string]any{"type": "integer"}}}}}}, nil
}

func (s *fakeSession) CallTool(_ context.Context, name string, _ map[string]any) (*mcp.CallToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failAfter > 0 && s.calls >= s.failAfter {
		return nil, errors.New("transport closed")
	}
	if name == "list_watches" {
		return toolResult(map[string]any{"watches": []any{map[string]any{"status": "active"}}}), nil
	}
	now := time.Now().UTC()
	summary := map[string]any{"now": now.Format(time.RFC3339), "watches": []any{map[string]any{
		"watch_id": "w1", "codes": []any{"USD"}, "every_minutes": 1, "status": "active", "hours": 1,
		"from": now.Add(-time.Hour).Format(time.RFC3339), "to": now.Format(time.RFC3339),
		"polls":         map[string]any{"total": 1, "ok": 1, "failed": 0, "last_at": now.Format(time.RFC3339), "last_error": ""},
		"publications":  []any{},
		"currencies":    []any{map[string]any{"code": "USD", "first": map[string]any{"rates_date": "2026-09-24", "unit_rate": 81.1234}, "last": map[string]any{"rates_date": "2026-09-24", "unit_rate": 81.1234}, "min": 81.1234, "max": 81.1234, "avg": 81.1234, "change": 0, "change_pct": 0}},
		"missing_codes": []any{},
	}}}
	return toolResult(summary), nil
}

func toolResult(value any) *mcp.CallToolResult {
	raw, _ := json.Marshal(value)
	return &mcp.CallToolResult{StructuredContent: value, Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
}

type digestLLM struct {
	fail  bool
	calls atomic.Int32
}

type midDigestFailureSession struct {
	listCalls    int
	summaryCalls int
}

func (s *midDigestFailureSession) ListTools(context.Context) (*mcp.ListToolsResult, error) {
	return (&fakeSession{}).ListTools(context.Background())
}

func (s *midDigestFailureSession) CallTool(_ context.Context, name string, _ map[string]any) (*mcp.CallToolResult, error) {
	if name == "list_watches" {
		s.listCalls++
		if s.listCalls == 3 {
			return nil, errors.New("transport closed")
		}
		return toolResult(map[string]any{"watches": []any{map[string]any{"status": "active"}}}), nil
	}
	s.summaryCalls++
	return nil, errors.New("summary transport failure")
}

type maliciousErrorLLM struct{}

func (maliciousErrorLLM) Converse(context.Context, []llm.ToolMessage, llm.Options) (llm.Answer, error) {
	return llm.Answer{}, errors.New("provider \x1b[31m‮ forged")
}

func TestProviderErrorIsSafeForTerminal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	result := issueDigest(context.Background(), &fakeSession{}, maliciousErrorLLM{}, filepath.Join(t.TempDir(), "digests.json"), 3*time.Hour, 1, time.Now, &stdout, &stderr)
	if result.err == nil || strings.ContainsRune(stderr.String(), '\x1b') || strings.Contains(stderr.String(), "‮") {
		t.Fatalf("result=%+v stderr=%q", result, stderr.String())
	}
}

func (m *digestLLM) Converse(_ context.Context, messages []llm.ToolMessage, _ llm.Options) (llm.Answer, error) {
	m.calls.Add(1)
	if m.fail {
		return llm.Answer{}, errors.New("deepseek failed")
	}
	last := messages[len(messages)-1]
	usage := llm.Usage{PromptTokens: 10, PromptCacheHitTokens: 2, CompletionTokens: 3}
	if last.Role == "tool" {
		return llm.Answer{Content: "USD 81,12", Model: "deepseek-flash", FinishReason: "stop", Usage: usage}, nil
	}
	return llm.Answer{Model: "deepseek-flash", FinishReason: "tool_calls", Usage: usage, ToolCalls: []llm.ToolCall{{ID: "1", Type: "function", Function: llm.ToolCallFunction{Name: "get_watch_summary", Arguments: `{"hours":1}`}}}}, nil
}

func TestDaemonTwoSlotsCancellationLivenessAndModelFailure(t *testing.T) {
	t.Run("two slots", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2300*time.Millisecond)
		defer cancel()
		path := filepath.Join(t.TempDir(), "digests.json")
		var out, errOut bytes.Buffer
		code := runDaemon(ctx, &fakeSession{}, &digestLLM{}, path, 2*time.Second, 1, time.Second, 100*time.Millisecond, &out, &errOut)
		digests, err := readDigests(path)
		if code != 0 || err != nil || len(digests) < 2 {
			t.Fatalf("code=%d n=%d err=%v stderr=%s", code, len(digests), err, errOut.String())
		}
	})
	t.Run("liveness", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var out, errOut bytes.Buffer
		code := runDaemon(ctx, &fakeSession{failAfter: 1}, &digestLLM{}, filepath.Join(t.TempDir(), "digests.json"), 2*time.Second, 1, time.Second, 100*time.Millisecond, &out, &errOut)
		if code != 1 || !strings.Contains(errOut.String(), "MCP-сервер недоступен") {
			t.Fatalf("code=%d err=%s", code, errOut.String())
		}
	})
	t.Run("model failure continues", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2300*time.Millisecond)
		defer cancel()
		path := filepath.Join(t.TempDir(), "digests.json")
		var out, errOut bytes.Buffer
		code := runDaemon(ctx, &fakeSession{}, &digestLLM{fail: true}, path, 2*time.Second, 1, time.Second, 100*time.Millisecond, &out, &errOut)
		digests, err := readDigests(path)
		if code != 0 || err != nil || len(digests) < 2 || digests[0].Error == "" {
			t.Fatalf("code=%d n=%d read=%v err=%s", code, len(digests), err, errOut.String())
		}
	})
	t.Run("summary MCP failure is followed by liveness check", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		session := &midDigestFailureSession{}
		var out, errOut bytes.Buffer
		code := runDaemon(ctx, session, &digestLLM{}, filepath.Join(t.TempDir(), "digests.json"), 2*time.Second, 1, time.Second, 100*time.Millisecond, &out, &errOut)
		if code != 1 || session.listCalls != 3 || session.summaryCalls != 1 || !strings.Contains(errOut.String(), "MCP-сервер недоступен") {
			t.Fatalf("code=%d list=%d summary=%d stderr=%q", code, session.listCalls, session.summaryCalls, errOut.String())
		}
	})
}

var _ = fmt.Sprintf

// The digest itself asks the server which watches are active. A transport failure there is
// a dead server, not "no active watches": -tick must fail loudly and the daemon must exit,
// both without spending a model call (test review, wave 1: this path was swallowable).
func TestDigestListFailureIsALivenessFailure(t *testing.T) {
	t.Run("tick", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "digests.json")
		model := &digestLLM{}
		var out, errOut bytes.Buffer
		code := runTick(context.Background(), &fakeSession{failAfter: 1}, model, path, time.Hour, 1, &out, &errOut)
		if code != 1 || !strings.Contains(errOut.String(), "MCP-сервер недоступен") || strings.Contains(out.String(), "активных наблюдений нет") {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
		}
		if model.calls.Load() != 0 {
			t.Fatalf("model called %d times", model.calls.Load())
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("digests file written: %v", err)
		}
	})
	t.Run("daemon, after its own liveness check passed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "digests.json")
		model := &digestLLM{}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var out, errOut bytes.Buffer
		// Call 1 is the daemon's liveness check, call 2 is the digest's own list_watches.
		code := runDaemon(ctx, &fakeSession{failAfter: 2}, model, path, time.Hour, 1, time.Second, 100*time.Millisecond, &out, &errOut)
		if code != 1 || !strings.Contains(errOut.String(), "MCP-сервер недоступен") {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
		}
		if model.calls.Load() != 0 {
			t.Fatalf("model called %d times", model.calls.Load())
		}
	})
}
