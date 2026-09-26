package mcprouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMain(m *testing.M) {
	temp, err := os.MkdirTemp("", "mcprouter-testserver-build-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	command := exec.Command("go", "build", "-o", filepath.Join(temp, "testserver"), "./testserver")
	command.Env = os.Environ()
	if output, buildErr := command.CombinedOutput(); buildErr != nil {
		fmt.Fprintf(os.Stderr, "warm testserver build cache: %v\n%s", buildErr, output)
		_ = os.RemoveAll(temp)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(temp)
	os.Exit(code)
}

type fakeSession struct {
	mu     sync.Mutex
	name   string
	tools  []*mcp.Tool
	calls  int
	closed bool
	call   func(context.Context, string, map[string]any) (*mcp.CallToolResult, error)
}

func (f *fakeSession) ListTools(context.Context) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{Tools: f.tools}, nil
}
func (f *fakeSession) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.call != nil {
		return f.call(ctx, name, args)
	}
	return textResult(f.name, false), nil
}
func (f *fakeSession) Close() error { f.mu.Lock(); f.closed = true; f.mu.Unlock(); return nil }

func TestSameToolNameRoutesToOwningServer(t *testing.T) {
	a, b := fakeToolSession("a"), fakeToolSession("b")
	router, err := NewForSessions([]SessionDescriptor{
		{Alias: "a", ServerInfo: ServerInfo{Name: "server-a", Version: "1"}, Transport: "stdio", Session: a},
		{Alias: "b", ServerInfo: ServerInfo{Name: "server-b", Version: "1"}, Transport: "stdio", Session: b},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	listed, _ := router.ListTools(context.Background())
	if listed.Tools[0].Name != "a__ping" || listed.Tools[1].Name != "b__ping" {
		t.Fatalf("tools=%#v", listed.Tools)
	}
	for _, test := range []struct{ name, want string }{{"a__ping", "a"}, {"b__ping", "b"}} {
		result, err := router.CallTool(context.Background(), test.name, map[string]any{})
		if err != nil || mcpclient.ToolText(result) != test.want {
			t.Fatalf("%s: %v %s", test.name, err, mcpclient.ToolText(result))
		}
	}
	journal := router.Journal()
	if journal[0].Alias != "a" || journal[1].Alias != "b" {
		t.Fatalf("journal=%#v", journal)
	}
}

func TestLimitRejectsSeventeenthWithoutServerContact(t *testing.T) {
	session := fakeToolSession("ok")
	router, _ := NewForSessions([]SessionDescriptor{{Alias: "a", ServerInfo: ServerInfo{Name: "a"}, Transport: "stdio", Session: session}}, time.Second)
	defer router.Close()
	for i := 0; i < 17; i++ {
		result, err := router.CallTool(context.Background(), "a__ping", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if i == 16 && (!result.IsError || mcpclient.ToolText(result) != LimitText) {
			t.Fatalf("17th=%#v", result)
		}
	}
	if session.calls != 16 || router.Journal()[16].Outcome != "limit" {
		t.Fatalf("calls=%d journal=%#v", session.calls, router.Journal()[16])
	}
}

func TestOutcomeClassesAndDeadServer(t *testing.T) {
	t.Run("tool_error", func(t *testing.T) {
		s := fakeToolSession("x")
		s.call = func(context.Context, string, map[string]any) (*mcp.CallToolResult, error) {
			return textResult("bad args", true), nil
		}
		r := oneRouter(t, s, time.Second)
		defer r.Close()
		result, _ := r.CallTool(context.Background(), "a__ping", map[string]any{})
		if !result.IsError || r.Journal()[0].Outcome != "tool_error" {
			t.Fatalf("result=%#v journal=%#v", result, r.Journal())
		}
	})
	t.Run("rpc_error_stays_live", func(t *testing.T) {
		s := fakeToolSession("x")
		first := true
		s.call = func(context.Context, string, map[string]any) (*mcp.CallToolResult, error) {
			if first {
				first = false
				return nil, &jsonrpc.Error{Code: -32601, Message: "unknown tool"}
			}
			return textResult("ok", false), nil
		}
		r := oneRouter(t, s, time.Second)
		defer r.Close()
		firstResult, _ := r.CallTool(context.Background(), "a__ping", map[string]any{})
		second, _ := r.CallTool(context.Background(), "a__ping", map[string]any{})
		if !firstResult.IsError || second.IsError || r.Journal()[0].Outcome != "rpc_error" || r.Journal()[1].Outcome != "ok" {
			t.Fatalf("journal=%#v", r.Journal())
		}
	})
	t.Run("timeout_then_dead", func(t *testing.T) {
		s := fakeToolSession("x")
		s.call = func(ctx context.Context, _ string, _ map[string]any) (*mcp.CallToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		r := oneRouter(t, s, 10*time.Millisecond)
		defer r.Close()
		first, _ := r.CallTool(context.Background(), "a__ping", map[string]any{})
		second, _ := r.CallTool(context.Background(), "a__ping", map[string]any{})
		if !strings.Contains(mcpclient.ToolText(first), "не ответил за 10ms") || mcpclient.ToolText(second) != mcpclient.ToolText(first) || s.calls != 1 || r.Journal()[1].Outcome != "dead" {
			t.Fatalf("calls=%d journal=%#v", s.calls, r.Journal())
		}
	})
	t.Run("transport_then_dead", func(t *testing.T) {
		s := fakeToolSession("x")
		s.call = func(context.Context, string, map[string]any) (*mcp.CallToolResult, error) {
			return nil, errors.New("broken pipe")
		}
		r := oneRouter(t, s, time.Second)
		defer r.Close()
		first, _ := r.CallTool(context.Background(), "a__ping", map[string]any{})
		second, _ := r.CallTool(context.Background(), "a__ping", map[string]any{})
		if !strings.Contains(mcpclient.ToolText(first), "сервер a недоступен: broken pipe") || mcpclient.ToolText(second) != mcpclient.ToolText(first) || s.calls != 1 {
			t.Fatalf("calls=%d journal=%#v", s.calls, r.Journal())
		}
	})
	t.Run("run_context_cancel", func(t *testing.T) {
		s := fakeToolSession("x")
		s.call = func(ctx context.Context, _ string, _ map[string]any) (*mcp.CallToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		r := oneRouter(t, s, time.Second)
		defer r.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := r.CallTool(ctx, "a__ping", map[string]any{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		_, _ = r.CallTool(context.Background(), "a__ping", map[string]any{})
		if s.calls != 2 {
			t.Fatalf("server marked dead after run cancel; calls=%d", s.calls)
		}
	})
}

func TestSubprocessEnvAndArgumentAreExact(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY_ROUTER_TEST", "top-secret")
	registry := Registry{Servers: []Entry{{Alias: "probe", Command: "go", Args: []string{"run", "./testserver", "-arg", "value with a space"}, Env: map[string]string{"PROBE_VALUE": "entry"}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	router, err := Open(ctx, registry, Options{StartTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	result, err := router.CallTool(ctx, "probe__probe", map[string]any{})
	if err != nil || result.IsError {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	var output struct {
		Argument string            `json:"argument"`
		Env      map[string]string `json:"env"`
	}
	decodeResult(t, result, &output)
	if output.Argument != "value with a space" || output.Env["PROBE_VALUE"] != "entry" {
		t.Fatalf("output=%#v", output)
	}
	for name := range output.Env {
		if strings.HasPrefix(name, "DEEPSEEK_API_KEY") {
			t.Fatalf("child received %s", name)
		}
	}
	if os.Getenv("DEEPSEEK_API_KEY_ROUTER_TEST") != "top-secret" {
		t.Fatal("global environment changed")
	}
}

func TestGoRunProcessGroupKilledAfterHungCall(t *testing.T) {
	pidFile := t.TempDir() + "/pid"
	registry := Registry{Servers: []Entry{{Alias: "probe", Command: "go", Args: []string{"run", "./testserver", "-pid-file", pidFile}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	router, err := Open(ctx, registry, Options{StartTimeout: 20 * time.Second, CallTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := router.CallTool(ctx, "probe__probe", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		PID int `json:"pid"`
	}
	decodeResult(t, probe, &output)
	result, err := router.CallTool(ctx, "probe__hang", map[string]any{})
	if err != nil || !result.IsError {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	waitGone(t, output.PID)
	_ = router.Close()
}

func TestStartupTimeoutKillsGoRunGrandchild(t *testing.T) {
	pidFile := t.TempDir() + "/pid"
	registry := Registry{Servers: []Entry{{Alias: "probe", Command: "go", Args: []string{"run", "./testserver", "-pid-file", pidFile, "-hang-start"}}}}
	_, err := Open(context.Background(), registry, Options{StartTimeout: 2 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "probe") {
		t.Fatalf("err=%v", err)
	}
	raw := waitForFile(t, pidFile, 2*time.Second)
	pid, _ := strconv.Atoi(string(raw))
	if pid <= 0 {
		t.Fatalf("invalid pid %q", raw)
	}
	waitGone(t, pid)
}

func TestStartupRejectsMissingCommandAndInvalidMergedName(t *testing.T) {
	_, err := Open(context.Background(), Registry{Servers: []Entry{{Alias: "bad", Command: "command-that-does-not-exist-day20"}}}, Options{StartTimeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("missing command err=%v", err)
	}
	s := &fakeSession{tools: []*mcp.Tool{{Name: "has.dot", InputSchema: map[string]any{"type": "object"}}}}
	_, err = NewForSessions([]SessionDescriptor{{Alias: "bad", ServerInfo: ServerInfo{Name: "bad"}, Transport: "stdio", Session: s}}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "правило DeepSeek") {
		t.Fatalf("invalid name err=%v", err)
	}
}

func fakeToolSession(name string) *fakeSession {
	return &fakeSession{name: name, tools: []*mcp.Tool{{Name: "ping", Description: "ping", InputSchema: map[string]any{"type": "object"}}}}
}
func oneRouter(t *testing.T, session *fakeSession, timeout time.Duration) *Router {
	t.Helper()
	router, err := NewForSessions([]SessionDescriptor{{Alias: "a", ServerInfo: ServerInfo{Name: "a"}, Transport: "stdio", Session: session}}, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return router
}
func textResult(text string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: isError, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
func decodeResult(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	raw, _ := json.Marshal(result.StructuredContent)
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d still exists", pid)
}

func waitForFile(t *testing.T, path string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && len(raw) > 0 {
			return raw
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("wait for %s: %v", path, lastErr)
	return nil
}
