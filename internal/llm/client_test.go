package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeProvider answers with whatever body it is given, so the branches that decide
// what a failed call reports can be triggered rather than reasoned about.
func fakeProvider(t *testing.T, body any) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	return &Client{
		APIKey: "k", Model: "deepseek-v4-flash", URL: server.URL,
		MaxTokens: 100, HTTP: &http.Client{Timeout: 5 * time.Second},
	}
}

func TestAnEmptyAnswerIsAnErrorThatStillReportsWhatItCost(t *testing.T) {
	// The provider charges for a call that produced nothing. Both empty-answer
	// branches must hand the usage back with the error, or the charge disappears from
	// every report while the call still shows up on the bill. The reasoning branch
	// always did; the plain-empty one returned a zero Answer and dropped it.
	cases := []struct {
		name      string
		message   map[string]any
		wantWords string
		reasoned  bool
	}{
		{
			name:      "только рассуждение, ответа нет",
			message:   map[string]any{"role": "assistant", "content": "", "reasoning_content": "я думал"},
			wantWords: "не дошла до ответа",
			reasoned:  true,
		},
		{
			name:      "пусто и без рассуждения",
			message:   map[string]any{"role": "assistant", "content": "   "},
			wantWords: "пустой текст",
			reasoned:  false,
		},
		{
			name:      "рассуждение в написании ollama",
			message:   map[string]any{"role": "assistant", "content": "", "reasoning": "я думал"},
			wantWords: "не дошла до ответа",
			reasoned:  true,
		},
	}
	for _, c := range cases {
		client := fakeProvider(t, map[string]any{
			"model":   "deepseek-v4-flash",
			"choices": []any{map[string]any{"message": c.message, "finish_reason": "length"}},
			"usage": map[string]any{
				"prompt_tokens": 40, "prompt_cache_miss_tokens": 40, "completion_tokens": 4000,
			},
		})
		answer, err := client.AskWith(context.Background(),
			[]Message{{Role: "user", Content: "вопрос"}}, Options{MaxTokens: 100})
		if err == nil {
			t.Errorf("%s: ошибки нет, а ответа не было", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantWords) {
			t.Errorf("%s: ошибка %q не содержит %q", c.name, err, c.wantWords)
		}
		if answer.Usage.CompletionTokens != 4000 || answer.Usage.PromptTokens != 40 {
			t.Errorf("%s: расход потерян: %+v", c.name, answer.Usage)
		}
		cost, known := CostAt("deepseek-v4-flash", answer.Usage, offPeak)
		if !known || cost <= 0 {
			t.Errorf("%s: провалившийся вызов не оценивается: %f (known=%v)", c.name, cost, known)
		}
		if answer.Reasoned() != c.reasoned {
			t.Errorf("%s: Reasoned() = %v, ожидалось %v", c.name, answer.Reasoned(), c.reasoned)
		}
		if answer.FinishReason != "length" {
			t.Errorf("%s: finish_reason потерян: %q", c.name, answer.FinishReason)
		}
	}
}

func TestASuccessfulAnswerCarriesTheRequestItWasSent(t *testing.T) {
	// RequestBody is the evidence that the constraints travelled in the request rather
	// than being asked for in prose. It is what the demo shows on screen.
	client := fakeProvider(t, map[string]any{
		"model":   "deepseek-v4-flash",
		"choices": []any{map[string]any{"message": map[string]any{"content": "ANSWER: 24"}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
	})
	answer, err := client.AskWith(context.Background(),
		[]Message{{Role: "user", Content: "вопрос"}}, Options{MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Content != "ANSWER: 24" {
		t.Errorf("ответ = %q", answer.Content)
	}
	if answer.Reasoned() {
		t.Error("рассуждение объявлено при пустом поле")
	}
	if !strings.Contains(answer.RequestBody, `"max_tokens": 100`) {
		t.Errorf("в записанном запросе нет max_tokens:\n%s", answer.RequestBody)
	}
	if !strings.Contains(answer.RequestBody, `"thinking"`) {
		t.Errorf("в записанном запросе нет выключателя размышления:\n%s", answer.RequestBody)
	}
	if strings.Contains(answer.RequestBody, "k") && strings.Contains(answer.RequestBody, "Bearer") {
		t.Error("в записанный запрос попал ключ")
	}
}

func TestTheOllamaDialectSendsReasoningEffortAndNoThinkingBlock(t *testing.T) {
	// Measured, not assumed: on that endpoint "think": false returns 200 and does
	// nothing, and only reasoning_effort "none" switches reasoning off. If this ever
	// regresses, every local rung silently starts reasoning and the comparison stops
	// being about the model.
	var sent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		sent = string(body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "qwen3:1.7b",
			"choices": []any{map[string]any{"message": map[string]any{"content": "391"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 3},
		})
	}))
	defer server.Close()

	client := NewLocal("qwen3:1.7b")
	client.URL = server.URL
	if _, err := client.AskWith(context.Background(),
		[]Message{{Role: "user", Content: "вопрос"}}, Options{MaxTokens: 50}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent, `"reasoning_effort":"none"`) {
		t.Errorf("в запросе нет reasoning_effort none:\n%s", sent)
	}
	if strings.Contains(sent, `"thinking"`) {
		t.Errorf("в запрос к ollama уехал блок thinking, которого он не понимает:\n%s", sent)
	}
}
