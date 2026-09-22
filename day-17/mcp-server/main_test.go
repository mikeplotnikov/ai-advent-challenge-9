package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBinaryServesStdioMCP(t *testing.T) {
	fixture, err := ratesmcp.Fixtures.ReadFile("fixtures/2026-09-01.xml")
	if err != nil {
		t.Fatal(err)
	}
	// The fake counts what it served: a green result alone does not prove the child
	// used it — with CBR_URL ignored the child would reach the real cbr.ru, which
	// answers 2026-09-01 just as validly, and the test would pass for the wrong reason.
	var served atomic.Int32
	cbr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.URL.Query().Get("date_req"); got != "01/09/2026" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		served.Add(1)
		_, _ = w.Write(fixture)
	}))
	defer cbr.Close()

	binary := filepath.Join(t.TempDir(), "mcp-server")
	command := exec.Command("go", "build", "-o", binary, ".")
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	child := exec.Command(binary)
	child.Env = append(os.Environ(), "CBR_URL="+cbr.URL)
	session := commandSession(t, child)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatalf("tools/list = %#v, %v", tools, err)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "convert_currency", Arguments: map[string]any{"amount": 1, "from": "USD", "to": "RUB", "date": "2026-09-01"}})
	if err != nil || result.IsError {
		t.Fatalf("tools/call = %#v, %v", result, err)
	}
	if got := served.Load(); got != 1 {
		t.Fatalf("фейковый ЦБ обслужил %d запросов, want 1: подпроцесс ходил не туда", got)
	}
}

func TestStreamableHTTPHandler(t *testing.T) {
	fixture, err := ratesmcp.Fixtures.ReadFile("fixtures/2026-09-01.xml")
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fixture) }))
	defer upstream.Close()
	t.Setenv("CBR_URL", upstream.URL)
	server := ratesmcp.NewServer(ratesmcp.Options{})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatalf("tools/list = %#v, %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_currency_rates", Arguments: map[string]any{"date": "2026-09-01", "codes": []string{"USD"}}})
	if err != nil || result.IsError {
		t.Fatalf("tools/call = %#v, %v", result, err)
	}
}

func TestBinaryHTTPModeServesMCPAtPath(t *testing.T) {
	fixture, err := ratesmcp.Fixtures.ReadFile("fixtures/2026-09-01.xml")
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fixture) }))
	defer upstream.Close()
	binary := filepath.Join(t.TempDir(), "mcp-server")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-http", address)
	command.Env = append(os.Environ(), "CBR_URL="+upstream.URL)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	})
	endpoint := "http://" + address + "/mcp"
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := http.Get(endpoint)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("GET /mcp status = %d", response.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP server did not start: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatalf("tools/list = %#v, %v", tools, err)
	}
}

func commandSession(t *testing.T, command *exec.Cmd) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
