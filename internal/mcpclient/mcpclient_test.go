package mcpclient

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
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func validPair() (map[string]any, map[string]any) {
	return map[string]any{"jsonrpc": "2.0", "id": float64(1), "method": "tools/list"}, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "result": map[string]any{"tools": []any{map[string]any{"name": "search", "inputSchema": map[string]any{"type": "object"}}}},
	}
}

func TestCriterionPositive(t *testing.T) {
	request, response := validPair()
	if verdict := Evaluate(request, response); !verdict.Passed {
		t.Fatalf("positive verdict: %#v", verdict)
	}
}

func TestCriterionNegativeControls(t *testing.T) {
	request, _ := validPair()
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"R1", func(r map[string]any) { r["jsonrpc"] = "1.0" }},
		{"R2", func(r map[string]any) { r["id"] = float64(2) }},
		{"R3", func(r map[string]any) { r["result"] = map[string]any{"tools": []any{}} }},
		{"R4", func(r map[string]any) {
			r["result"].(map[string]any)["tools"] = []any{map[string]any{"name": "", "inputSchema": map[string]any{}}}
		}},
		{"R5", func(r map[string]any) {
			r["result"].(map[string]any)["tools"] = []any{map[string]any{"name": "search", "inputSchema": "schema"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, response := validPair()
			tc.mutate(response)
			verdict := Evaluate(request, response)
			if verdict.Passed {
				t.Fatalf("negative control passed: %#v", verdict)
			}
			for _, rule := range verdict.Rules {
				if rule.Passed == (rule.ID == tc.name) {
					t.Fatalf("%s unexpected verdict: %#v", tc.name, verdict)
				}
			}
		})
	}
}

func TestNewTransportTable(t *testing.T) {
	cases := []struct {
		endpoint, command, wantEndpoint, wantDescription string
		wantError                                        error
	}{
		{"", "", DefaultEndpoint, "Streamable HTTP", nil},
		{"http://example.test/mcp", "", "http://example.test/mcp", "Streamable HTTP", nil},
		{"", "echo hello", "", "stdio", nil},
		{"http://example.test/mcp", "echo hello", "", "", ErrTransportChoice},
	}
	for _, tc := range cases {
		t.Run(tc.endpoint+tc.command, func(t *testing.T) {
			got, err := NewTransport(tc.endpoint, tc.command)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("NewTransport error = %v, want %v", err, tc.wantError)
			}
			if err == nil && (got.Endpoint != tc.wantEndpoint || got.Description != tc.wantDescription) {
				t.Fatalf("transport = %#v", got)
			}
		})
	}
}

func TestParseTimeout(t *testing.T) {
	// Pinned to the literal the spec and the README promise, not to the constant itself:
	// comparing DefaultTimeout with DefaultTimeout.String() would follow any drift.
	if DefaultTimeout != 30*time.Second {
		t.Fatalf("умолчание разъехалось с документированным 30s: %v", DefaultTimeout)
	}
	if got, err := ParseTimeout("30s"); err != nil || got != 30*time.Second {
		t.Fatalf("default = %v, %v", got, err)
	}
	if _, err := ParseTimeout("ерунда"); err == nil {
		t.Fatal("garbage timeout accepted")
	}
}

func TestFormatTruncatesAndCountsBytes(t *testing.T) {
	result := Result{Tools: &mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "search", Description: strings.Repeat("x", 140), InputSchema: map[string]any{"required": []any{"query"}}}}}, ServerName: "test", ServerVersion: "1", ProtocolVersion: "2025-11-25", Transport: Transport{Description: "stdio", Command: "demo"}}
	formatted, err := Format(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(formatted.Text, "…") || strings.Contains(formatted.Text, "\n2.") {
		t.Fatalf("description was not one-line/truncated: %q", formatted.Text)
	}
	if !strings.Contains(formatted.Text, "обязательные: query") || formatted.ToolsBytes <= 0 || formatted.ResponseBytes <= formatted.ToolsBytes {
		t.Fatalf("formatted = %#v", formatted)
	}
}

func TestWriteCaptureExplainsStdioLimitation(t *testing.T) {
	var output bytes.Buffer
	if err := WriteCapture(&output, Transport{Description: "stdio", Command: "demo"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "только для HTTP-транспорта") {
		t.Fatalf("stdio capture = %q", output.String())
	}
}

func testServer(withTool bool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1"}, nil)
	if withTool {
		mcp.AddTool(server, &mcp.Tool{Name: "search", Description: "search documents"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	}
	return server
}

func localHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("песочница не разрешает открыть localhost для HTTP-интеграции: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server
}

func TestListWithInMemoryTransport(t *testing.T) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := testServer(true).Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client, _ := New()
	result, err := client.List(context.Background(), Transport{MCP: clientTransport, Description: "in-memory"})
	if err != nil || len(result.Tools.Tools) != 1 || !result.Verdict.Passed {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestListWithStreamableHTTPAndCapture(t *testing.T) {
	httpServer := localHTTPServer(t, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return testServer(true) }, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	defer httpServer.Close()
	transport, err := NewTransport(httpServer.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	client, _ := New()
	result, err := client.List(context.Background(), transport)
	if err != nil || !result.Verdict.Passed {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if _, ok, err := EvaluateCapturedToolsList(transport.Capture.Exchanges()); err != nil || !ok {
		t.Fatalf("capture = ok:%t err:%v", ok, err)
	}
}

func TestListRejectsZeroTools(t *testing.T) {
	httpServer := localHTTPServer(t, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return testServer(false) }, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	defer httpServer.Close()
	transport, _ := NewTransport(httpServer.URL, "")
	client, _ := New()
	if _, err := client.List(context.Background(), transport); err == nil || !strings.Contains(err.Error(), "R3") {
		t.Fatalf("zero tools error = %v", err)
	}
}

func TestListRejectsHTTPFailureAndClosedPort(t *testing.T) {
	failingServer := localHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "broken", http.StatusInternalServerError) }))
	defer failingServer.Close()
	for _, endpoint := range []string{failingServer.URL, "http://127.0.0.1:1"} {
		transport, _ := NewTransport(endpoint, "")
		client, _ := New()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := client.List(ctx, transport)
		cancel()
		if err == nil {
			t.Fatalf("endpoint %s unexpectedly succeeded", endpoint)
		}
	}
}

func TestListRejectsMissingCommand(t *testing.T) {
	transport, _ := NewTransport("", "/no/such/binary")
	client, _ := New()
	if _, err := client.List(context.Background(), transport); err == nil {
		t.Fatal("missing binary unexpectedly succeeded")
	}
}

// This uses a real subprocess, so run it with -count=1: Go's test cache cannot
// know that the external example binary was rebuilt or changed.
func TestListWithSDKEverythingOverStdio(t *testing.T) {
	transport, err := NewTransport("", "go run github.com/modelcontextprotocol/go-sdk/examples/server/everything")
	if err != nil {
		t.Fatal(err)
	}
	client, _ := New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := client.List(ctx, transport)
	if err != nil || len(result.Tools.Tools) == 0 || !result.Verdict.Passed {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

// R1 and R2 live in the JSON-RPC envelope the SDK already consumed. On a transport
// without a raw capture they must be reported unmeasured, never passed: a synthetic
// envelope would only assert that our own scaffolding is well-formed.
func TestStdioPathReportsEnvelopeRulesUnmeasured(t *testing.T) {
	result := &mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "search", InputSchema: map[string]any{"type": "object"}}}}
	verdict, err := EvaluateToolsResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.Passed {
		t.Fatalf("measured rules should pass: %#v", verdict)
	}
	if got := verdict.UnmeasuredRules(); len(got) != 2 || got[0] != "R1" || got[1] != "R2" {
		t.Fatalf("unmeasured = %v, want [R1 R2]", got)
	}
	for _, rule := range verdict.Rules {
		if (rule.ID == "R1" || rule.ID == "R2") && rule.Passed {
			t.Fatalf("%s must not claim to have passed without evidence: %#v", rule.ID, rule)
		}
	}
	formatted, err := Format(Result{Tools: result, Verdict: verdict, Transport: Transport{Description: "stdio", Command: "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(formatted.Text, "R1, R2 не измерены") {
		t.Fatalf("human output hides the gap: %q", formatted.Text)
	}
}

// The HTTP path has the real envelope, so every rule is measured there.
func TestHTTPPathMeasuresEveryRule(t *testing.T) {
	request := map[string]any{"jsonrpc": "2.0", "id": float64(3), "method": "tools/list"}
	response := map[string]any{"jsonrpc": "2.0", "id": float64(3), "result": map[string]any{
		"tools": []any{map[string]any{"name": "search", "inputSchema": map[string]any{"type": "object"}}},
	}}
	verdict := Evaluate(request, response)
	if !verdict.Passed || len(verdict.UnmeasuredRules()) != 0 {
		t.Fatalf("verdict = %#v", verdict)
	}
}

// Third-party text reaches the operator's terminal, so control sequences must be defanged
// before they are printed: an ESC in a tool name can retitle the window or clear the lines
// above it, making the recorded demo show something that never ran.
func TestTerminalEscapesInToolTextAreNeutralised(t *testing.T) {
	evil := "\x1b]0;PWNED\x07tool\x1b[2J"
	result := Result{
		Tools:     &mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: evil, Description: "до\x1b[31mпосле", InputSchema: map[string]any{"required": []any{"a\x1bb"}}}}},
		Transport: Transport{Description: "stdio", Command: "demo"},
	}
	formatted, err := Format(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(formatted.Text, 0x1b) || strings.ContainsRune(formatted.Text, 0x07) {
		t.Fatalf("управляющие символы дошли до вывода: %q", formatted.Text)
	}
	if !strings.Contains(formatted.Text, "tool") {
		t.Fatalf("видимая часть имени потеряна: %q", formatted.Text)
	}
}

// A public server we do not control must not decide how much memory this process takes.
// Checked on the limiter itself: an end-to-end test with a huge body would also fail
// because the body is not valid JSON, i.e. it would pass for the wrong reason.
func TestOversizedResponseIsRefused(t *testing.T) {
	src := io.NopCloser(strings.NewReader(strings.Repeat("x", MaxResponseBytes+1024)))
	read, err := io.Copy(io.Discard, newLimitedBody(src))
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if read != MaxResponseBytes {
		t.Fatalf("прочитано %d байт при потолке %d", read, MaxResponseBytes)
	}
	// And the ordinary case is untouched: a small body reads to EOF as usual.
	small := io.NopCloser(strings.NewReader("{}"))
	if n, err := io.Copy(io.Discard, newLimitedBody(small)); err != nil || n != 2 {
		t.Fatalf("обычный ответ пострадал: n=%d err=%v", n, err)
	}
}

// And the cap must be wired where it matters: the previous version of this test exercised
// the limiting reader in isolation, so removing it from captureTransport.RoundTrip left the
// whole suite green. Here the oversized body is VALID JSON-RPC, so the run can only fail
// because of the cap — not because the bytes were garbage.
func TestOversizedResponseIsRefusedThroughTheRealTransport(t *testing.T) {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return testServer(true) }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	server := localHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if !bytes.Contains(body, []byte("tools/list")) {
			handler.ServeHTTP(w, r)
			return
		}
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("unmarshal request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"search","inputSchema":{"type":"object"},"description":"`, request.ID)
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for written := 0; written < MaxResponseBytes+(1<<20); written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
		fmt.Fprint(w, `"}]}}`)
	}))
	defer server.Close()
	transport, err := NewTransport(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	client, _ := New()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err = client.List(ctx, transport)
	if err == nil {
		t.Fatal("безразмерный ответ принят целиком")
	}
	if !strings.Contains(err.Error(), "больше допустимого размера") {
		t.Fatalf("ответ отвергнут не потолком, а чем-то ещё: %v", err)
	}
}
