package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// recordingProvider answers with a fixed body and keeps the exact bytes it was sent,
// so a test can assert on the wire rather than on the pretty-printed copy.
func recordingProvider(t *testing.T, reply string) (*Client, *string) {
	t.Helper()
	sent := new(string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*sent = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(server.Close)
	return &Client{
		APIKey: "k", Model: "deepseek-v4-flash", URL: server.URL,
		HTTP: &http.Client{Timeout: 5 * time.Second},
	}, sent
}

func TestMessageStaysComparable(t *testing.T) {
	// internal/agent compares histories with ==; a slice field would not compile there.
	a, b := Message{Role: "user", Content: "x"}, Message{Role: "user", Content: "x"}
	if a != b {
		t.Fatal("одинаковые сообщения не равны")
	}
}

func TestARequestWithoutToolsIsByteForByteWhatItWasBeforeToolCalling(t *testing.T) {
	// Captured on the code before tool calling existed (2026-09-22). Days 1-16 send
	// exactly this shape; a new field that leaked as "tool_calls":null or "tools":null
	// would change every one of their requests and their recorded runs.
	const before = `{"model":"deepseek-v4-flash","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"},{"role":"assistant","content":""}],"stream":false,"max_tokens":50,"stop":["X"],"temperature":0,"thinking":{"type":"disabled"}}`
	client, sent := recordingProvider(t, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	temp := 0.0
	_, err := client.AskWith(context.Background(),
		[]Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}, {Role: "assistant", Content: ""}},
		Options{MaxTokens: 50, Temperature: &temp, Stop: []string{"X"}})
	if err != nil {
		t.Fatal(err)
	}
	if *sent != before {
		t.Errorf("тело запроса без инструментов изменилось:\nбыло  %s\nстало %s", before, *sent)
	}
}

func TestToolsTravelInTheDocumentedShapeAndTheSchemaIsNotReencoded(t *testing.T) {
	client, sent := recordingProvider(t, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	// Key order and an unknown keyword are kept on purpose: the schema is someone
	// else's document and must arrive as written.
	schema := json.RawMessage(`{"type":"object","x-extra":true,"properties":{"date":{"type":"string"}}}`)
	_, err := client.Converse(context.Background(), []ToolMessage{{Role: "user", Content: "u"}}, Options{
		Tools: []Tool{{Type: "function", Function: ToolFunction{
			Name: "get_currency_rates", Description: "курсы", Parameters: schema,
		}}},
		ToolChoice: "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `"tools":[{"type":"function","function":{"name":"get_currency_rates","description":"курсы","parameters":{"type":"object","x-extra":true,"properties":{"date":{"type":"string"}}}}}],"tool_choice":"auto"`
	if !strings.Contains(*sent, want) {
		t.Errorf("tools ушли не в той форме:\n%s", *sent)
	}
}

func TestAToolCallTurnWithEmptyContentIsAnAnswerNotAnError(t *testing.T) {
	// The shape a live call returned on 2026-09-22: finish_reason tool_calls, content "",
	// and an index field the client does not need.
	client, _ := recordingProvider(t, `{"id":"r1","model":"deepseek-flash","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"index":0,"id":"call_00_abc","type":"function","function":{"name":"convert_currency","arguments":"{\"amount\": 250, \"from\": \"USD\", \"to\": \"RUB\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":387,"completion_tokens":92}}`)
	answer, err := client.Converse(context.Background(), []ToolMessage{{Role: "user", Content: "u"}}, Options{})
	if err != nil {
		t.Fatalf("ход из одних вызовов объявлен ошибкой: %v", err)
	}
	if len(answer.ToolCalls) != 1 {
		t.Fatalf("вызовов = %d, want 1", len(answer.ToolCalls))
	}
	call := answer.ToolCalls[0]
	if call.ID != "call_00_abc" || call.Type != "function" || call.Function.Name != "convert_currency" {
		t.Errorf("вызов разобран неверно: %+v", call)
	}
	if call.Function.Arguments != `{"amount": 250, "from": "USD", "to": "RUB"}` {
		t.Errorf("аргументы изменены: %q", call.Function.Arguments)
	}
	if answer.FinishReason != "tool_calls" || answer.Usage.PromptTokens != 387 || answer.Model != "deepseek-flash" {
		t.Errorf("служебные поля потеряны: %+v", answer)
	}
	if !strings.Contains(answer.ResponseBody, `"finish_reason":"tool_calls"`) {
		t.Errorf("сырой ответ не сохранён: %q", answer.ResponseBody)
	}
}

func TestAnEmptyAnswerWithoutToolCallsIsStillAnError(t *testing.T) {
	// The negative control for the test above: the relaxation is for tool calls only.
	client, _ := recordingProvider(t, `{"choices":[{"message":{"content":"","tool_calls":[]},"finish_reason":"stop"}]}`)
	_, err := client.Converse(context.Background(), []ToolMessage{{Role: "user", Content: "u"}}, Options{})
	if !errors.Is(err, ErrEmptyContent) {
		t.Errorf("пустой ответ без вызовов: err = %v, want ErrEmptyContent", err)
	}
}

func TestToolHistoryMessagesUseTheProviderFormat(t *testing.T) {
	client, sent := recordingProvider(t, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	history := []ToolMessage{
		{Role: "user", Content: "u"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{
			ID: "call_1", Type: "function",
			Function: ToolCallFunction{Name: "convert_currency", Arguments: `{"amount":1}`},
		}}},
		{Role: "tool", ToolCallID: "call_1", Content: `{"result":86.3793}`},
	}
	if _, err := client.Converse(context.Background(), history, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"convert_currency","arguments":"{\"amount\":1}"}}]}`,
		`{"role":"tool","content":"{\"result\":86.3793}","tool_call_id":"call_1"}`,
		`{"role":"user","content":"u"}`,
	} {
		if !strings.Contains(*sent, want) {
			t.Errorf("в запросе нет %s\n%s", want, *sent)
		}
	}
}
