package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// fakeCaller records what the agent sent and returns scripted answers, so the tests
// assert on the request the agent built rather than on a live model's mood.
type fakeCaller struct {
	sent    [][]llm.Message
	opts    []llm.Options
	answers []llm.Answer
	errs    []error
	calls   int
}

func (f *fakeCaller) AskWith(_ context.Context, messages []llm.Message, opts llm.Options) (llm.Answer, error) {
	// Deliberately NOT copied. A transport that defensively copies what it is handed
	// makes the agent's own "returned fresh each time" guarantee unobservable — and
	// a test of that guarantee then passes no matter what the agent does. A review on
	// 2026-09-07 caught exactly that: the copy used to live here, and breaking
	// messagesFor left the whole suite green.
	f.sent = append(f.sent, messages)
	f.opts = append(f.opts, opts)
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return llm.Answer{}, f.errs[i]
	}
	if i < len(f.answers) {
		return f.answers[i], nil
	}
	return llm.Answer{Content: fmt.Sprintf("ответ %d", i+1), Model: "deepseek-v4-flash"}, nil
}

func newAgent(t *testing.T, cfg Config, f *fakeCaller) *Agent {
	t.Helper()
	a, err := New(f, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func roles(ms []llm.Message) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = m.Role
	}
	return strings.Join(parts, ",")
}

// The requirement the host added in chat: the agent assembles the message stack
// itself. The caller passes a string, never a slice of messages.
func TestAgentBuildsTheStackItselfAcrossTurns(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: "ты ассистент"}, f)

	for _, q := range []string{"первый", "второй", "третий"} {
		if _, err := a.Ask(context.Background(), q); err != nil {
			t.Fatalf("Ask(%q): %v", q, err)
		}
	}

	want := []string{
		"system,user",
		"system,user,assistant,user",
		"system,user,assistant,user,assistant,user",
	}
	for i, w := range want {
		if got := roles(f.sent[i]); got != w {
			t.Errorf("вызов %d: роли %q, ожидалось %q", i+1, got, w)
		}
	}
	last := f.sent[2]
	if last[1].Content != "первый" || last[2].Content != "ответ 1" {
		t.Errorf("история потерялась: %+v", last[1:3])
	}
	if last[len(last)-1].Content != "третий" {
		t.Errorf("новый ввод не последний: %q", last[len(last)-1].Content)
	}
	if a.Turns() != 3 {
		t.Errorf("Turns = %d, ожидалось 3", a.Turns())
	}
}

func TestSystemPromptIsNotDuplicatedAndIsOmittedWhenEmpty(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{}, f)
	for i := 0; i < 2; i++ {
		if _, err := a.Ask(context.Background(), "вопрос"); err != nil {
			t.Fatalf("Ask: %v", err)
		}
	}
	for i, sent := range f.sent {
		for _, m := range sent {
			if m.Role == "system" {
				t.Fatalf("вызов %d: system-сообщение отправлено, хотя промпт пуст", i+1)
			}
		}
	}

	f2 := &fakeCaller{}
	a2 := newAgent(t, Config{SystemPrompt: "роль"}, f2)
	for i := 0; i < 3; i++ {
		if _, err := a2.Ask(context.Background(), "вопрос"); err != nil {
			t.Fatalf("Ask: %v", err)
		}
	}
	for i, sent := range f2.sent {
		n := 0
		for _, m := range sent {
			if m.Role == "system" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("вызов %d: system-сообщений %d, ожидалось 1", i+1, n)
		}
		if sent[0].Role != "system" {
			t.Errorf("вызов %d: system не первый, а %q", i+1, sent[0].Role)
		}
	}
}

// MaxTurns drops whole exchanges. A user turn kept without its answer would read to
// the model as a question it ignored.
func TestMaxTurnsDropsWholeExchangesOldestFirst(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: "роль", MaxTurns: 2}, f)

	for _, q := range []string{"один", "два", "три", "четыре"} {
		if _, err := a.Ask(context.Background(), q); err != nil {
			t.Fatalf("Ask(%q): %v", q, err)
		}
	}

	last := f.sent[3]
	if got := roles(last); got != "system,user,assistant,user,assistant,user" {
		t.Fatalf("роли %q", got)
	}
	if last[1].Content != "два" {
		t.Errorf("самый старый оставшийся ход %q, ожидался «два»", last[1].Content)
	}
	for _, m := range last {
		if m.Content == "один" {
			t.Error("ход «один» должен был выпасть за окно")
		}
	}
	// Turns counts the conversation, not what fits in the window.
	if a.Turns() != 4 {
		t.Errorf("Turns = %d, ожидалось 4", a.Turns())
	}
}

func TestEmptyAnswerIsAFailureAndIsStillPriced(t *testing.T) {
	f := &fakeCaller{answers: []llm.Answer{{
		Content: "   ",
		Model:   "deepseek-v4-flash",
		Usage:   llm.Usage{PromptTokens: 100, CompletionTokens: 0, TotalTokens: 100, PromptCacheMissTokens: 100},
	}}}
	a := newAgent(t, Config{Model: "deepseek-v4-flash"}, f)

	reply, err := a.Ask(context.Background(), "вопрос")
	if !errors.Is(err, ErrEmptyAnswer) {
		t.Fatalf("ошибка %v, ожидалась ErrEmptyAnswer", err)
	}
	if reply.Usage.PromptTokens != 100 {
		t.Errorf("расход не отдан наружу: %+v", reply.Usage)
	}
	if !reply.Usage.Priced || reply.Usage.Cost <= 0 {
		t.Errorf("неудачный вызов всё равно оплачен, цена должна считаться: %+v", reply.Usage)
	}
	if a.Turns() != 0 {
		t.Errorf("пустой ответ не должен попадать в историю, Turns = %d", a.Turns())
	}
}

// A rejected answer must not poison the next request's context.
func TestRejectedAnswerDoesNotEnterTheStack(t *testing.T) {
	f := &fakeCaller{answers: []llm.Answer{
		{Content: "мусор", Model: "m"},
		{Content: "{\"ok\":true}", Model: "m"},
	}}
	a := newAgent(t, Config{Validate: func(s string) error {
		if !strings.HasPrefix(s, "{") {
			return errors.New("не JSON")
		}
		return nil
	}}, f)

	if _, err := a.Ask(context.Background(), "первый"); err == nil {
		t.Fatal("ожидалась ошибка проверки")
	}
	if a.Turns() != 0 {
		t.Fatalf("отклонённый ответ попал в историю, Turns = %d", a.Turns())
	}
	if _, err := a.Ask(context.Background(), "второй"); err != nil {
		t.Fatalf("второй Ask: %v", err)
	}
	if got := roles(f.sent[1]); got != "user" {
		t.Errorf("второй вызов ушёл с ролями %q, ожидалось только «user»", got)
	}
	for _, m := range f.sent[1] {
		if m.Content == "мусор" || m.Content == "первый" {
			t.Errorf("отклонённый ход просочился в контекст: %q", m.Content)
		}
	}
}

func TestTransportErrorLeavesTheConversationUntouched(t *testing.T) {
	f := &fakeCaller{errs: []error{errors.New("503")}}
	a := newAgent(t, Config{}, f)
	if _, err := a.Ask(context.Background(), "вопрос"); err == nil {
		t.Fatal("ожидалась ошибка транспорта")
	}
	if a.Turns() != 0 {
		t.Errorf("Turns = %d, ожидалось 0", a.Turns())
	}
	if _, err := a.Ask(context.Background(), "второй"); err != nil {
		t.Fatalf("второй Ask: %v", err)
	}
	if len(f.sent[1]) != 1 {
		t.Errorf("после сбоя стек должен быть чистым, ушло %d сообщений", len(f.sent[1]))
	}
}

func TestEmptyInputNeverReachesTheModel(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t "} {
		f := &fakeCaller{}
		a := newAgent(t, Config{}, f)
		if _, err := a.Ask(context.Background(), in); !errors.Is(err, ErrEmptyInput) {
			t.Errorf("Ask(%q): ошибка %v, ожидалась ErrEmptyInput", in, err)
		}
		if f.calls != 0 {
			t.Errorf("Ask(%q): вызовов модели %d, ожидалось 0", in, f.calls)
		}
	}
}

func TestResetClearsTheConversation(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: "роль"}, f)
	for i := 0; i < 2; i++ {
		if _, err := a.Ask(context.Background(), "вопрос"); err != nil {
			t.Fatalf("Ask: %v", err)
		}
	}
	a.Reset()
	if a.Turns() != 0 {
		t.Fatalf("после Reset Turns = %d", a.Turns())
	}
	if _, err := a.Ask(context.Background(), "снова"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := roles(f.sent[2]); got != "system,user" {
		t.Errorf("после Reset роли %q, ожидалось «system,user»", got)
	}
}

// Every week-1 control reaches the request. This is the host's "всё, что мы делали
// в прошлой неделе, в виде конфига агента" — checked, not asserted in prose.
func TestWeekOneControlsTravelInTheRequest(t *testing.T) {
	temp := 0.3
	f := &fakeCaller{}
	a := newAgent(t, Config{
		Temperature:     &temp,
		MaxTokens:       120,
		Stop:            []string{"<<<END>>>"},
		ResponseFormat:  "json_object",
		Thinking:        "enabled",
		ReasoningEffort: "high",
	}, f)
	if _, err := a.Ask(context.Background(), "вопрос"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got := f.opts[0]
	if got.Temperature == nil || *got.Temperature != temp {
		t.Errorf("temperature = %v", got.Temperature)
	}
	if got.MaxTokens != 120 || got.ResponseFormat != "json_object" ||
		got.Thinking != "enabled" || got.ReasoningEffort != "high" ||
		len(got.Stop) != 1 || got.Stop[0] != "<<<END>>>" {
		t.Errorf("параметры недели 1 не доехали: %+v", got)
	}
}

func TestUsageReportsTheCacheSplit(t *testing.T) {
	f := &fakeCaller{answers: []llm.Answer{{
		Content: "ответ",
		Model:   "deepseek-v4-flash",
		Usage: llm.Usage{
			PromptTokens: 300, CompletionTokens: 20, TotalTokens: 320,
			PromptCacheHitTokens: 256, PromptCacheMissTokens: 44,
		},
	}}}
	a := newAgent(t, Config{}, f)
	reply, err := a.Ask(context.Background(), "вопрос")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Usage.CachedTokens != 256 || reply.Usage.MissedTokens != 44 {
		t.Errorf("сплит кэша потерян: %+v", reply.Usage)
	}
	if !reply.Usage.Priced || reply.Usage.Cost <= 0 {
		t.Errorf("цена не посчитана: %+v", reply.Usage)
	}
}

// An unknown model must report "price unknown", not "free". Day 5's price table is
// the only source of truth, and a silent zero would read as a free call.
func TestUnknownModelIsUnpricedNotFree(t *testing.T) {
	f := &fakeCaller{answers: []llm.Answer{{
		Content: "ответ",
		Model:   "модель-которой-нет-в-прайсе",
		Usage:   llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}}}
	a := newAgent(t, Config{}, f)
	reply, err := a.Ask(context.Background(), "вопрос")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Usage.Priced {
		t.Errorf("неизвестная модель помечена как оценённая: %+v", reply.Usage)
	}
	if reply.Usage.Cost != 0 {
		t.Errorf("Cost = %v при неизвестном прайсе", reply.Usage.Cost)
	}
}

func TestCallerIsRequiredAndMaxTurnsMustNotBeNegative(t *testing.T) {
	if _, err := New(nil, Config{}); err == nil {
		t.Error("New(nil) должен вернуть ошибку")
	}
	if _, err := New(&fakeCaller{}, Config{MaxTurns: -1}); err == nil {
		t.Error("отрицательный MaxTurns должен вернуть ошибку")
	}
}

// The slice handed to the transport must not be the agent's own storage: a caller
// that keeps it must not see it mutate on the next turn.
func TestSentStackIsACopy(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: "роль"}, f)
	if _, err := a.Ask(context.Background(), "первый"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	first := f.sent[0]
	if _, err := a.Ask(context.Background(), "второй"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(first) != 2 || first[1].Content != "первый" {
		t.Errorf("первый отправленный стек изменился: %+v", first)
	}
}

// The same guarantee from the inside, and specifically under the condition that makes
// aliasing observable at all.
//
// The black-box test above cannot catch this on its own, and neither can a naive
// white-box one: when a slice is at capacity, `append` allocates and copies anyway,
// so aliasing code accidentally behaves correctly. The bug only bites when the stack
// has spare capacity — so the stack is given some here on purpose, which is also the
// realistic case, because `append` grows capacity ahead of length as a conversation
// runs. Verified by mutation: rewriting messagesFor to build on `a.stack` instead of
// a fresh slice fails this test and nothing else in the suite.
func TestMessagesForDoesNotAliasTheStack(t *testing.T) {
	for _, system := range []string{"", "роль"} {
		f := &fakeCaller{}
		a := newAgent(t, Config{SystemPrompt: system}, f)

		// Spare capacity is the condition; without it append copies and the defect
		// hides. A running conversation reaches this state on its own.
		a.stack = make([]llm.Message, 0, 16)
		a.stack = append(a.stack,
			llm.Message{Role: "user", Content: "первый"},
			llm.Message{Role: "assistant", Content: "ответ 1"},
		)
		a.turns = 1
		before := append([]llm.Message(nil), a.stack...)

		out := a.messagesFor("второй")
		for i := range out {
			out[i] = llm.Message{Role: "затёрто", Content: "затёрто"}
		}

		if len(a.stack) != len(before) {
			t.Fatalf("system=%q: длина стека изменилась: %d вместо %d", system, len(a.stack), len(before))
		}
		for i := range before {
			if a.stack[i] != before[i] {
				t.Errorf("system=%q: сообщение %d затёрлось через возвращённый срез: %+v вместо %+v",
					system, i, a.stack[i], before[i])
			}
		}
	}
}

// The host's own operational test of "агент — отдельная сущность, а не просто один
// вызов API", sharpened in the challenge chat on the evening of 2026-09-07 while a
// participant defended a CLI utility that did everything procedurally:
//
//	[2164] «есть ли какая-то сущность которая централизованно всем этим управляет»
//	[2165] «То есть не процедурный подход, а объектный»
//	[2175] «Если тебе нужно будет моментально заспавнить 100 агентов с разными
//	        конфигами у тебя это можно сделать?»
//	[2177] «у тебя на это поднимется 100 инстансов апликухи?»
//	[2179] «лучше чтобы инстанс был один, а уже внутри было поднято сто инстансов агента»
//
// So the property is: one process, one transport, a hundred agents that differ in
// configuration and do not leak state into each other. The package was built this way
// and passed on the first run — but "was built this way" is a claim, and this project
// does not ship claims it has not checked. Run with -race: the failure mode this is
// really guarding against is shared mutable state, which a sequential test would miss.
func TestManyAgentsInOneProcessDoNotShareState(t *testing.T) {
	const n = 100
	shared := &countingCaller{}

	agents := make([]*Agent, n)
	for i := range agents {
		temp := float64(i) / float64(n)
		a, err := New(shared, Config{
			Name:         fmt.Sprintf("агент-%d", i),
			SystemPrompt: fmt.Sprintf("роль-%d", i),
			Temperature:  &temp,
			MaxTurns:     i%5 + 1,
		})
		if err != nil {
			t.Fatalf("агент %d: New: %v", i, err)
		}
		agents[i] = a
	}

	const turnsEach = 3
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i, a := range agents {
		wg.Add(1)
		go func(i int, a *Agent) {
			defer wg.Done()
			for turn := 0; turn < turnsEach; turn++ {
				if _, err := a.Ask(context.Background(), fmt.Sprintf("вопрос %d агенту %d", turn, i)); err != nil {
					errs[i] = err
					return
				}
			}
		}(i, a)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("агент %d: %v", i, err)
		}
	}
	if got := shared.calls(); got != n*turnsEach {
		t.Errorf("через общий транспорт прошло %d вызовов, ожидалось %d", got, n*turnsEach)
	}

	for i, a := range agents {
		if a.Turns() != turnsEach {
			t.Errorf("агент %d: ходов %d, ожидалось %d", i, a.Turns(), turnsEach)
		}
		// Each agent's own history, not somebody else's: the system prompt names the
		// agent, and every user message in its stack names it too.
		want := fmt.Sprintf("роль-%d", i)
		if a.cfg.SystemPrompt != want {
			t.Errorf("агент %d: системный промпт %q, ожидался %q", i, a.cfg.SystemPrompt, want)
		}
		for j, m := range a.stack {
			if m.Role != "user" {
				continue
			}
			if !strings.HasSuffix(m.Content, fmt.Sprintf("агенту %d", i)) {
				t.Errorf("агент %d: в стеке чужой ход на позиции %d: %q", i, j, m.Content)
			}
		}
	}
}

// countingCaller is safe for concurrent use, because the agents under test are not
// sharing it politely — they are sharing it the way a hundred spawned agents would.
type countingCaller struct {
	mu sync.Mutex
	n  int
}

func (c *countingCaller) AskWith(_ context.Context, messages []llm.Message, _ llm.Options) (llm.Answer, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	last := messages[len(messages)-1].Content
	return llm.Answer{Content: "ответ на " + last, Model: llm.DefaultModel}, nil
}

func (c *countingCaller) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
