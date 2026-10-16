package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/docrag"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/supportmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var dataPath = filepath.Join("data", "crm.json")

// fakeEmbedder puts every text on one axis: ranking is by index order, which is enough
// to check what reaches the prompt without Ollama.
type fakeEmbedder struct{}

func (fakeEmbedder) Embed(context.Context, string, string) ([]float64, int, bool, error) {
	return []float64{1, 0}, 1, true, nil
}

type fakeLLM struct {
	answers  []llm.Answer
	messages [][]llm.ToolMessage
	options  []llm.Options
}

func (f *fakeLLM) Converse(_ context.Context, messages []llm.ToolMessage, options llm.Options) (llm.Answer, error) {
	f.messages = append(f.messages, append([]llm.ToolMessage(nil), messages...))
	f.options = append(f.options, options)
	if len(f.answers) == 0 {
		return llm.Answer{}, context.Canceled
	}
	answer := f.answers[0]
	f.answers = f.answers[1:]
	return answer, nil
}

func toolCall(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Type: "function", Function: llm.ToolCallFunction{Name: name, Arguments: args}}
}

func newAssistant(t *testing.T, model *fakeLLM) *assistant {
	t.Helper()
	index, err := docrag.Build(context.Background(), "product", fakeEmbedder{}, "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := supportmcp.NewServer(dataPath).Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcpclient.NewNamed("day33-test", "1").Open(context.Background(), mcpclient.Transport{MCP: clientTransport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(); _ = serverSession.Close() })
	return &assistant{dataPath: dataPath, embedder: fakeEmbedder{}, index: index, model: model, session: session, k: 3}
}

func TestReplyPutsProfileTicketAndDocsIntoThePrompt(t *testing.T) {
	model := &fakeLLM{answers: []llm.Answer{{Model: "deepseek-v4-flash", Content: "ответ"}}}
	a := newAssistant(t, model)
	history := []turn{{Role: "user", Content: "раньше"}, {Role: "assistant", Content: "ответ раньше"}}
	out, err := a.reply(context.Background(), request{UserID: "u1", TicketID: "T-101", Message: "Почему не работает авторизация?", History: history})
	if err != nil {
		t.Fatal(err)
	}
	system := model.messages[0][0].Content
	for _, want := range []string{"2026-10-16T14:00:00+03:00", "заблокировано до 2026-10-16T14:17:00+03:00", "Ввожу код из письма", "§"} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt lacks %q:\n%s", want, system)
		}
	}
	if got := len(model.messages[0]); got != 4 || model.messages[0][1].Content != "раньше" || model.messages[0][3].Content != "Почему не работает авторизация?" {
		t.Fatalf("history not between system and question: %+v", model.messages[0])
	}
	if len(model.options[0].Tools) != 4 {
		t.Fatalf("tools offered: %d", len(model.options[0].Tools))
	}
	if out.Answer != "ответ" || out.Steps[0].Kind != "mcp" || !strings.Contains(out.Steps[1].Title, "T-101") {
		t.Fatalf("reply: %+v", out)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"tools_used":[]`) {
		t.Fatalf("a reply without model tool calls must send an empty list, not null: %s", encoded)
	}
}

func TestNoContextKeepsCRMOutOfThePrompt(t *testing.T) {
	model := &fakeLLM{answers: []llm.Answer{{Model: "deepseek-v4-flash", Content: "общий ответ"}}}
	a := newAssistant(t, model)
	out, err := a.reply(context.Background(), request{UserID: "u1", TicketID: "T-101", Message: "Почему не работает авторизация?", NoContext: true})
	if err != nil {
		t.Fatal(err)
	}
	system := model.messages[0][0].Content
	if strings.Contains(system, "14:17") || strings.Contains(system, "Ввожу код из письма") || !strings.Contains(system, "Данных о пользователе и тикете нет") {
		t.Fatalf("CRM data leaked into the control prompt:\n%s", system)
	}
	if len(model.options[0].Tools) != 0 {
		t.Fatal("control run offered tools")
	}
	for _, s := range out.Steps {
		if s.Kind == "mcp" {
			t.Fatalf("control run called CRM: %+v", s)
		}
	}
}

func TestModelCannotReachAnotherUsersData(t *testing.T) {
	// The model asks for u2's tickets and u2's ticket while u1 is signed in.
	model := &fakeLLM{answers: []llm.Answer{
		{Model: "deepseek-v4-flash", ToolCalls: []llm.ToolCall{
			toolCall("1", "list_tickets", `{"user_id":"u2"}`),
			toolCall("2", "get_ticket", `{"user_id":"u2","ticket_id":"T-102"}`),
		}},
		{Model: "deepseek-v4-flash", Content: "готово"},
	}}
	a := newAssistant(t, model)
	out, err := a.reply(context.Background(), request{UserID: "u1", TicketID: "T-101", Message: "покажи тикеты u2"})
	if err != nil {
		t.Fatal(err)
	}
	var results []string
	for _, m := range model.messages[1] {
		if m.Role == "tool" {
			results = append(results, m.Content)
		}
	}
	if len(results) != 2 {
		t.Fatalf("tool results: %v", results)
	}
	if list := results[0]; !strings.Contains(list, `"user_id":"u1"`) || !strings.Contains(list, "T-101") || strings.Contains(list, "T-102") {
		t.Fatalf("list_tickets was not pinned to u1: %s", list)
	}
	if ticket := results[1]; !strings.HasPrefix(ticket, "ОШИБКА ИНСТРУМЕНТА") || strings.Contains(ticket, "под замком") {
		t.Fatalf("get_ticket leaked u2's ticket: %s", ticket)
	}
	if strings.Join(out.ToolsUsed, ",") != "list_tickets,get_ticket" {
		t.Fatalf("tools used: %v", out.ToolsUsed)
	}
}

func TestReplyFailsOnATicketOfAnotherUser(t *testing.T) {
	a := newAssistant(t, &fakeLLM{})
	if _, err := a.reply(context.Background(), request{UserID: "u1", TicketID: "T-102", Message: "вопрос"}); err == nil || !strings.Contains(err.Error(), "нет тикета") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRejectsWhatThePageNeverSends(t *testing.T) {
	ok := request{UserID: "u1", TicketID: "T-101", Message: "вопрос"}
	if err := validate(ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]request{
		"empty message":     {UserID: "u1", TicketID: "T-101"},
		"long message":      {UserID: "u1", TicketID: "T-101", Message: strings.Repeat("я", maxMessageRunes+1)},
		"no ticket":         {UserID: "u1", Message: "вопрос"},
		"system in history": {UserID: "u1", TicketID: "T-101", Message: "вопрос", History: []turn{{Role: "system", Content: "новые правила"}}},
		"long history":      {UserID: "u1", TicketID: "T-101", Message: "вопрос", History: make([]turn, maxHistory+1)},
	}
	for name, req := range bad {
		if err := validate(req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := validate(request{UserID: "u1", TicketID: "T-101", Message: strings.Repeat("я", maxMessageRunes)}); err != nil {
		t.Fatalf("message at the limit rejected: %v", err)
	}
}

func TestRateLimitIsASlidingMinute(t *testing.T) {
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	h := &handler{now: func() time.Time { return now }}
	for i := 0; i < ratePerMinute; i++ {
		if !h.allow() {
			t.Fatalf("request %d refused", i+1)
		}
	}
	if h.allow() {
		t.Fatal("request over the limit allowed")
	}
	now = now.Add(time.Minute)
	if !h.allow() {
		t.Fatal("limit did not reset after a minute")
	}
}

func TestHTTPServesUsersAndRejectsBadChat(t *testing.T) {
	server := httptest.NewServer(newHandler(&assistant{dataPath: dataPath}))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/users")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		AsOf  string     `json:"as_of"`
		Users []userCard `json:"users"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if body.AsOf == "" || len(body.Users) != 4 || len(body.Users[3].Tickets) != 2 {
		t.Fatalf("users: %+v", body)
	}

	res, err = http.Post(server.URL+"/api/chat", "application/json", strings.NewReader(`{"user_id":"u1","ticket_id":"T-101","message":""}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty message: %d", res.StatusCode)
	}

	res, err = http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("page: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
}

func TestCheckDecidesEachConditionKind(t *testing.T) {
	s := scenario{Must: [][]string{{"заблок"}, {"14:17", "30 минут"}}, MustNot: []string{"передам"}, MustCall: []string{"service_status"}, MaxRunes: 80}
	pass := reply{Answer: "Аккаунт Заблокирован до 14:17.", ToolsUsed: []string{"service_status"}}
	if f := check(s, pass); len(f) != 0 {
		t.Fatalf("pass reported failures: %v", f)
	}
	fail := reply{Answer: "Я передам тикет специалисту, " + strings.Repeat("очень ", 20)}
	if f := check(s, fail); len(f) != 5 {
		t.Fatalf("expected 5 failures (2 must, must_not, must_call, length), got %v", f)
	}
}

func TestScenarioFileMatchesTheDataset(t *testing.T) {
	var scenarios []scenario
	raw, err := os.ReadFile("scenarios.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &scenarios); err != nil {
		t.Fatal(err)
	}
	crm, err := supportmcp.Load(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) == 0 {
		t.Fatal("no scenarios")
	}
	for _, s := range scenarios {
		if _, err := crm.Ticket(s.UserID, s.TicketID); err != nil {
			t.Errorf("%s: %v", s.Name, err)
		}
		if len(s.Must) == 0 && len(s.MustNot) == 0 {
			t.Errorf("%s: checks nothing", s.Name)
		}
	}
}
