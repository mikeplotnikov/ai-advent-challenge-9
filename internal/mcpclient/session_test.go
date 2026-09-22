package mcpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoIn struct {
	Word string `json:"word" jsonschema:"слово, которое вернуть"`
}

type echoOut struct {
	Echo string `json:"echo"`
}

// echoServer is a real SDK server over Streamable HTTP with one tool that echoes
// its argument and fails on the word "fail", so both result kinds cross the wire.
func echoServer(t *testing.T) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "echo", Version: "0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Повторяет слово"},
		func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
			if in.Word == "fail" {
				return nil, echoOut{}, errors.New("инструмент отказал")
			}
			return nil, echoOut{Echo: in.Word}, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}

func TestASessionListsCallsAndReportsToolFailuresInTheResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport, err := NewTransport(echoServer(t), "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewNamed("test-agent", "1").Open(ctx, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.ServerName != "echo" || session.ServerVersion != "0.1" || session.ProtocolVersion == "" {
		t.Errorf("метаданные инициализации: %+v", session)
	}

	tools, err := session.ListTools(ctx)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" {
		t.Fatalf("tools/list: %v %+v", err, tools)
	}

	ok, err := session.CallTool(ctx, "echo", map[string]any{"word": "привет"})
	if err != nil || ok.IsError {
		t.Fatalf("успешный вызов: %v %+v", err, ok)
	}
	if text := ToolText(ok); !strings.Contains(text, `"echo":"привет"`) {
		t.Errorf("текст результата = %q", text)
	}

	failed, err := session.CallTool(ctx, "echo", map[string]any{"word": "fail"})
	if err != nil {
		t.Fatalf("отказ инструмента стал ошибкой транспорта: %v", err)
	}
	if !failed.IsError || !strings.Contains(ToolText(failed), "инструмент отказал") {
		t.Errorf("отказ инструмента не дошёл как isError: %+v %q", failed, ToolText(failed))
	}

	// The SDK validates arguments against the schema before the handler runs; a
	// wrong type must come back as a tool error the model can read, not a crash.
	invalid, err := session.CallTool(ctx, "echo", map[string]any{"word": 42})
	if err != nil {
		t.Fatalf("неверный тип аргумента стал ошибкой транспорта: %v", err)
	}
	if !invalid.IsError {
		t.Errorf("неверный тип аргумента не отвергнут: %q", ToolText(invalid))
	}

	if _, err := session.CallTool(ctx, "echo", nil); !errors.Is(err, ErrNilArguments) {
		t.Errorf("nil-аргументы: err = %v", err)
	}
}
