package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	for _, want := range []string{"Сейчас 2026-10-16T14:00:00+03:00 — момент среза", "заблокировано до 2026-10-16T14:17:00+03:00", "Ввожу код из письма"} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt lacks %q:\n%s", want, system)
		}
	}
	var roles []string
	for _, m := range model.messages[0] {
		roles = append(roles, m.Role+":"+m.Content)
	}
	if got := strings.Join(roles[1:], "|"); got != "user:раньше|assistant:ответ раньше|user:Почему не работает авторизация?" {
		t.Fatalf("history roles and order: %s", got)
	}
	rag := 0
	for _, step := range out.Steps {
		if step.Kind == "rag" {
			rag++
			if !strings.Contains(system, step.Detail) {
				t.Fatalf("retrieved fragment %q is not in the prompt", step.Title)
			}
		}
	}
	if rag != a.k {
		t.Fatalf("rag steps: %d, want %d", rag, a.k)
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

func TestModelGetUserIsPinnedAndServiceStatusIsNot(t *testing.T) {
	model := &fakeLLM{answers: []llm.Answer{
		{Model: "deepseek-v4-flash", ToolCalls: []llm.ToolCall{
			toolCall("1", "get_user", `{"user_id":"u2"}`),
			toolCall("2", "service_status", `{}`),
		}},
		{Model: "deepseek-v4-flash", Content: "готово"},
	}}
	a := newAssistant(t, model)
	if _, err := a.reply(context.Background(), request{UserID: "u1", TicketID: "T-101", Message: "профиль u2 и статус"}); err != nil {
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
	if !strings.Contains(results[0], "Анна Смирнова") || strings.Contains(results[0], "Борис") {
		t.Fatalf("get_user was not pinned to u1: %s", results[0])
	}
	// service_status takes no arguments: a user_id added to it is rejected by the schema.
	if strings.HasPrefix(results[1], "ОШИБКА") || !strings.Contains(results[1], "INC-17") {
		t.Fatalf("service_status through the scope: %s", results[1])
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
		"long history":      {UserID: "u1", TicketID: "T-101", Message: "вопрос", History: slices.Repeat([]turn{{Role: "user", Content: "вопрос"}}, maxHistory+1)},
	}
	for name, req := range bad {
		if err := validate(req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := validate(request{UserID: "u1", TicketID: "T-101", Message: strings.Repeat("я", maxMessageRunes)}); err != nil {
		t.Fatalf("message at the limit rejected: %v", err)
	}
	// What the page sends from the second question on: up to 12 user/assistant turns.
	full := make([]turn, 0, maxHistory)
	for len(full) < maxHistory {
		full = append(full, turn{Role: "user", Content: "вопрос"}, turn{Role: "assistant", Content: strings.Repeat("я", maxTurnRunes)})
	}
	if err := validate(request{UserID: "u1", TicketID: "T-101", Message: "вопрос", History: full}); err != nil {
		t.Fatalf("a full valid history rejected: %v", err)
	}
	long := []turn{{Role: "assistant", Content: strings.Repeat("я", maxTurnRunes+1)}}
	if err := validate(request{UserID: "u1", TicketID: "T-101", Message: "вопрос", History: long}); err == nil {
		t.Fatal("a history turn over the limit accepted")
	}
}

func TestRateLimitIsASlidingMinute(t *testing.T) {
	start := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	now := start
	h := &handler{now: func() time.Time { return now }}
	for i := 0; i < ratePerMinute; i++ {
		if i == ratePerMinute/2 {
			now = start.Add(30 * time.Second)
		}
		if !h.allow() {
			t.Fatalf("request %d refused", i+1)
		}
	}
	if h.allow() {
		t.Fatal("request over the limit allowed")
	}
	// At 14:01:00 only the first half has aged out: exactly that many fit again.
	now = start.Add(time.Minute)
	for i := 0; i < ratePerMinute/2; i++ {
		if !h.allow() {
			t.Fatalf("freed slot %d refused", i+1)
		}
	}
	if h.allow() {
		t.Fatal("hits from 14:00:30 were dropped too early")
	}
}

func TestChatAnswers429WhenTheLimitIsSpent(t *testing.T) {
	now := time.Now()
	h := &handler{slot: make(chan struct{}, 1), now: func() time.Time { return now }}
	for range ratePerMinute {
		h.allow()
	}
	rec := httptest.NewRecorder()
	h.chat(rec, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"user_id":"u1","ticket_id":"T-101","message":"вопрос"}`)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUsersSendsEmptyTicketListsNotNull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crm.json")
	if err := os.WriteFile(path, []byte(`{"as_of":"x","users":[{"id":"u9","name":"Без тикетов","plan":"Старт"}],"tickets":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	(&handler{a: &assistant{dataPath: path}}).users(rec, httptest.NewRequest(http.MethodGet, "/api/users", nil))
	if body := rec.Body.String(); !strings.Contains(body, `"tickets":[]`) {
		t.Fatalf("users: %s", body)
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
	// A forbidden phrase at the start of a sentence still counts.
	if f := check(scenario{MustNot: []string{"передам"}}, reply{Answer: "Передам тикет."}); len(f) != 1 {
		t.Fatalf("capitalised must_not missed: %v", f)
	}
	// 60 Cyrillic runes are 120 bytes: the limit counts runes.
	if f := check(scenario{MaxRunes: 60}, reply{Answer: strings.Repeat("я", 60)}); len(f) != 0 {
		t.Fatalf("60 runes over a 60-rune limit: %v", f)
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
