package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestUsageFailuresNeverRunNetwork(t *testing.T) {
	for _, args := range [][]string{{"-endpoint", "http://127.0.0.1:1", "-command", "echo hi"}, {"-timeout", "ерунда"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("%v: code = %d, stderr = %q", args, code, stderr.String())
		}
	}
}

func TestTimeoutUsesFlagDeadline(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("песочница не разрешает открыть localhost для HTTP-интеграции: %v", err)
	}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "timeout-test", Version: "1"}, nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte("tools/list")) {
			<-r.Context().Done()
			return
		}
		handler.ServeHTTP(w, r)
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	// Один замер под широким потолком ничего не доказывает: фиксированный дедлайн в 1.5 с
	// проходил обе проверки. Сравниваем РАЗНИЦУ двух прогонов — она обязана повторять
	// разницу флагов, а у зашитого дедлайна она равна нулю независимо от постоянных
	// накладных расходов на рукопожатие.
	measure := func(flag string) time.Duration {
		t.Helper()
		var stdout, stderr bytes.Buffer
		started := time.Now()
		code := run([]string{"-endpoint", server.URL, "-timeout", flag}, &stdout, &stderr)
		elapsed := time.Since(started)
		if code != 1 || !strings.Contains(stderr.String(), "таймаут") {
			t.Fatalf("%s: code=%d stderr=%q", flag, code, stderr.String())
		}
		if elapsed < 0 {
			t.Fatalf("%s: отрицательное время", flag)
		}
		return elapsed
	}
	short, long := measure("200ms"), measure("1200ms")
	delta := long - short
	if delta < 700*time.Millisecond || delta > 1800*time.Millisecond {
		t.Fatalf("дедлайн не следует за флагом: 200ms → %s, 1200ms → %s, разница %s (ожидалась около 1s)", short, long, delta)
	}
	if short < 200*time.Millisecond {
		t.Fatalf("прогон с -timeout 200ms завершился раньше собственного дедлайна: %s", short)
	}
}

func TestReadableFailures(t *testing.T) {
	for _, args := range [][]string{{"-endpoint", "http://127.0.0.1:1"}, {"-command", "/no/such/binary"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 1 || strings.Count(strings.TrimSpace(stderr.String()), "\n") != 0 {
			t.Fatalf("%v: code=%d stderr=%q", args, code, stderr.String())
		}
	}
}

// A failing -save reports itself, but never replaces the connection's own outcome:
// both failures are named, each on its own line.
func TestSaveFailureIsReportedNextToTheRealOutcome(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-endpoint", "http://127.0.0.1:1", "-save", "/"}, &stdout, &stderr)
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if code != 1 || len(lines) != 2 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(lines[0], "подключиться") || !strings.Contains(lines[1], "сохранить") {
		t.Fatalf("причины перепутаны: %q", stderr.String())
	}
	// И вторая строка не должна утверждать того, чего не было: списка здесь нет.
	if strings.Contains(stderr.String(), "список получен") {
		t.Fatalf("после провала соединения заявлен полученный список: %q", stderr.String())
	}
}

// The defect this locks down: a successful, criterion-passing list must still be printed
// when -save cannot write its file. Previously the save error returned first and threw the
// whole result away.
func TestSaveFailureNeverSwallowsASuccessfulList(t *testing.T) {
	server := localMCPServer(t)
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := run([]string{"-endpoint", server.URL, "-save", "/no/such/dir/exchange.json"}, &stdout, &stderr)
	if !strings.Contains(stdout.String(), "search") {
		t.Fatalf("список не напечатан: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "список получен") {
		t.Fatalf("ошибка сохранения не названа своими словами: %q", stderr.String())
	}
	if code != 1 {
		t.Fatalf("запрошенный файл не создан, это провал прогона: code=%d", code)
	}
}

// The zero-tools case at the CLI level: the criterion's whole point is that an empty list
// is a failure, and the operator must see WHICH rule failed rather than "не подключились".
func TestZeroToolsFailsWithTheCriterionMessage(t *testing.T) {
	server := localMCPServerWithoutTools(t)
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := run([]string{"-endpoint", server.URL}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "критерий") || !strings.Contains(stderr.String(), "R3") {
		t.Fatalf("stderr не называет провалившееся правило: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "подключиться") {
		t.Fatalf("провал критерия выдан за провал соединения: %q", stderr.String())
	}
}

func localMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	return mcpServer(t, true)
}

func localMCPServerWithoutTools(t *testing.T) *httptest.Server {
	t.Helper()
	return mcpServer(t, false)
}

func mcpServerNamed(t *testing.T, name string) *httptest.Server {
	t.Helper()
	return mcpServerWith(t, true, name)
}

func mcpServer(t *testing.T, withTool bool) *httptest.Server {
	t.Helper()
	return mcpServerWith(t, withTool, "cli-test")
}

func mcpServerWith(t *testing.T, withTool bool, name string) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("песочница не разрешает открыть localhost: %v", err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: name, Version: "1"}, nil)
	if withTool {
		mcp.AddTool(server, &mcp.Tool{Name: "search", Description: "search documents"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	}
	httpServer := httptest.NewUnstartedServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	httpServer.Listener = listener
	httpServer.Start()
	return httpServer
}

// The summary line on stderr prints the server's own name, so it needs the same guard as
// the list on stdout: a sanitizer reachable from only one of two print sites protects neither.
func TestSummaryLineSanitisesTheServerName(t *testing.T) {
	server := mcpServerNamed(t, "evil\x1b]0;PWNED\x07wiki")
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-endpoint", server.URL}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, out := range []string{stdout.String(), stderr.String()} {
		if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x07) {
			t.Fatalf("управляющие символы дошли до терминала: %q", out)
		}
	}
	if !strings.Contains(stderr.String(), "wiki") {
		t.Fatalf("видимая часть имени сервера потеряна: %q", stderr.String())
	}
}

// An error line that cannot tell a closed port from an unresolvable host is useless
// during a live demo, so the cause travels with the message.
func TestErrorLineCarriesItsCause(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"closed port", []string{"-endpoint", "http://127.0.0.1:1", "-timeout", "5s"}, "refused"},
		{"missing command", []string{"-command", "/no/such/binary", "-timeout", "5s"}, "/no/such/binary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tc.args, &stdout, &stderr); code != 1 {
				t.Fatalf("code = %d, stderr = %q", code, stderr.String())
			}
			line := strings.TrimSpace(stderr.String())
			if strings.Count(line, "\n") != 0 {
				t.Fatalf("error must stay one line: %q", line)
			}
			if !strings.Contains(line, tc.want) {
				t.Fatalf("cause %q missing from %q", tc.want, line)
			}
		})
	}
}
