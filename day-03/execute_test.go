package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// The headline numbers of this day — hit rate, token count, price — are produced
// in execute(), not in the helpers around it. These tests stand a fake DeepSeek
// in front of it so a regression there cannot pass unnoticed.

type captured struct {
	mu       sync.Mutex
	bodies   []map[string]any
	replies  []string
	reasons  []string
	inTokens []int
}

func (c *captured) systemOf(i int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	messages, _ := c.bodies[i]["messages"].([]any)
	for _, m := range messages {
		msg, _ := m.(map[string]any)
		if msg["role"] == "system" {
			return msg["content"].(string)
		}
	}
	return ""
}

func fakeDeepSeek(t *testing.T, c *captured) *llm.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("не разобрать тело запроса: %v", err)
		}
		c.mu.Lock()
		n := len(c.bodies)
		c.bodies = append(c.bodies, body)
		reply, reasoning, in := c.replies[n], "", 10*(n+1)
		if n < len(c.reasons) {
			reasoning = c.reasons[n]
		}
		c.inTokens = append(c.inTokens, in)
		c.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "test",
			"model": "fake",
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": reply, "reasoning_content": reasoning},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens": in, "completion_tokens": 100 * (n + 1), "total_tokens": in + 100*(n+1),
				"completion_tokens_details": map[string]any{"reasoning_tokens": 7 * (n + 1)},
			},
		})
	}))
	t.Cleanup(server.Close)
	return &llm.Client{APIKey: "test", Model: "fake", URL: server.URL, HTTP: server.Client()}
}

func TestMetaPromptPaysForBothCalls(t *testing.T) {
	// If only the second call were counted, the most expensive method in the day
	// would show up as one of the cheapest, and the price column would rank the
	// methods wrongly.
	c := &captured{replies: []string{"Реши задачу очень внимательно, перебирая числа.", "Считаю.\nANSWER: 24"}}
	client := fakeDeepSeek(t, c)

	var meta method
	for _, m := range methods() {
		if m.key == "C" {
			meta = m
		}
	}
	got := execute(context.Background(), client, meta, 24)

	if got.err != nil {
		t.Fatalf("неожиданная ошибка: %v", got.err)
	}
	if got.calls != 2 {
		t.Fatalf("учтено %d вызовов, ожидалось 2", got.calls)
	}
	if got.usage.PromptTokens != 10+20 || got.usage.CompletionTokens != 100+200 {
		t.Fatalf("токены обоих вызовов не сложились: %+v", got.usage)
	}
	if got.usage.CompletionDetails.ReasoningTokens != 7+14 {
		t.Fatalf("токены рассуждения обоих вызовов не сложились: %d",
			got.usage.CompletionDetails.ReasoningTokens)
	}
	if !got.correct || got.answer != 24 {
		t.Fatalf("ответ %d, верно=%v, ожидалось 24/верно", got.answer, got.correct)
	}
}

func TestMetaPromptCarriesTheGeneratedPromptAndTheOutputRule(t *testing.T) {
	// The model remembers nothing between calls, so the prompt it wrote has to be
	// carried over by us. And it may say nothing about where the answer goes —
	// without the shared output rule appended, C's answers become unparseable and
	// the method scores zero for a reason that is our bug, not its reasoning.
	written := "Ты внимательный счетовод. Перебирай числа по одному."
	c := &captured{replies: []string{written, "ANSWER: 24"}}
	client := fakeDeepSeek(t, c)

	var meta method
	for _, m := range methods() {
		if m.key == "C" {
			meta = m
		}
	}
	got := execute(context.Background(), client, meta, 24)
	if got.err != nil {
		t.Fatalf("неожиданная ошибка: %v", got.err)
	}
	if got.generated != written {
		t.Fatalf("сгенерированный промпт не сохранён: %q", got.generated)
	}

	second := c.systemOf(1)
	if !strings.Contains(second, written) {
		t.Fatalf("второй вызов ушёл без промпта, который написала модель:\n%s", second)
	}
	if !strings.Contains(second, outputRule) {
		t.Fatalf("второй вызов ушёл без общего правила вывода:\n%s", second)
	}
	if first := c.systemOf(0); !strings.Contains(first, "инженер по промптам") {
		t.Fatalf("первый вызов ушёл не с промптом-писателем:\n%s", first)
	}
}

func TestExecuteScoresAgainstTheTruth(t *testing.T) {
	cases := []struct {
		name        string
		reply       string
		wantParsed  bool
		wantCorrect bool
	}{
		{"верный ответ", "ANSWER: 24", true, true},
		{"неверный ответ", "ANSWER: 25", true, false},
		{"ответа нет", "Их примерно двадцать пять.", false, false},
		{"жирный номер", "Итог.\nANSWER: **24**", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &captured{replies: []string{tc.reply}}
			client := fakeDeepSeek(t, c)
			got := execute(context.Background(), client, methods()[0], 24)
			if got.err != nil {
				t.Fatalf("неожиданная ошибка: %v", got.err)
			}
			if got.parsed != tc.wantParsed || got.correct != tc.wantCorrect {
				t.Fatalf("parsed=%v correct=%v, ожидалось parsed=%v correct=%v",
					got.parsed, got.correct, tc.wantParsed, tc.wantCorrect)
			}
			if got.calls != 1 {
				t.Fatalf("одношаговый способ сделал %d вызовов", got.calls)
			}
		})
	}
}

func TestBuiltInReasoningIsReadBackSeparately(t *testing.T) {
	// Method E's whole point is that the chain of thought arrives in its own
	// field and is billed as output without appearing in the answer.
	c := &captured{
		replies: []string{"ANSWER: 24"},
		reasons: []string{"Считаю кратные 12 и выбрасываю те, где есть восьмёрка."},
	}
	client := fakeDeepSeek(t, c)

	var reasoning method
	for _, m := range methods() {
		if m.key == "E" {
			reasoning = m
		}
	}
	got := execute(context.Background(), client, reasoning, 24)
	if got.err != nil {
		t.Fatalf("неожиданная ошибка: %v", got.err)
	}
	if !strings.Contains(got.reasoning, "восьмёрка") {
		t.Fatalf("рассуждение не прочитано: %q", got.reasoning)
	}
	if strings.Contains(got.text, "восьмёрка") {
		t.Fatal("рассуждение просочилось в текст ответа")
	}
	if c.bodies[0]["thinking"].(map[string]any)["type"] != "enabled" {
		t.Fatalf("E ушёл без thinking=enabled: %v", c.bodies[0]["thinking"])
	}
	if c.bodies[0]["reasoning_effort"] != "high" {
		t.Fatalf("E ушёл без reasoning_effort: %v", c.bodies[0]["reasoning_effort"])
	}
}

func TestPlainMethodsKeepThinkingDisabled(t *testing.T) {
	// The honesty condition of the whole day: with reasoning on, "прямой ответ
	// без инструкций" would secretly be chain-of-thought and A vs B would
	// measure nothing.
	for _, m := range methods() {
		if m.key == "E" {
			continue
		}
		c := &captured{replies: []string{"ANSWER: 24", "ANSWER: 24"}}
		client := fakeDeepSeek(t, c)
		execute(context.Background(), client, m, 24)
		for i := range c.bodies {
			if c.bodies[i]["thinking"].(map[string]any)["type"] != "disabled" {
				t.Fatalf("способ %s, вызов %d ушёл с включённым рассуждением", m.key, i+1)
			}
			if _, sent := c.bodies[i]["reasoning_effort"]; sent {
				t.Fatalf("способ %s, вызов %d отправил reasoning_effort без thinking", m.key, i+1)
			}
		}
	}
}
