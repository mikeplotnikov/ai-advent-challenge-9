// Package llm is the shared DeepSeek client: one place that knows how to send
// a conversation over REST and read the answer back.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	DefaultURL   = "https://api.deepseek.com/chat/completions"
	DefaultModel = "deepseek-v4-flash"
)

// Message is one turn of the conversation: system, user or assistant.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage reports what the call cost in tokens.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// CompletionDetails carries reasoning_tokens when thinking is enabled. Those
	// tokens are billed as output even though they never appear in the answer.
	CompletionDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type Client struct {
	APIKey    string
	Model     string
	URL       string
	MaxTokens int
	HTTP      *http.Client
}

// New builds a client from the environment: DEEPSEEK_API_KEY and optional DEEPSEEK_MODEL.
func New() (*Client, error) {
	return NewWithKeyEnv("")
}

// NewWithKeyEnv is New with a preferred key variable, falling back to DEEPSEEK_API_KEY.
// Day 2 runs on its own key so its spend can be capped and revoked on its own.
func NewWithKeyEnv(preferred string) (*Client, error) {
	key := ""
	if preferred != "" {
		key = os.Getenv(preferred)
	}
	if key == "" {
		key = os.Getenv("DEEPSEEK_API_KEY")
	}
	if key == "" {
		want := "DEEPSEEK_API_KEY"
		if preferred != "" {
			want = preferred + " или DEEPSEEK_API_KEY"
		}
		return nil, fmt.Errorf("%s не задан: скопируй .env.example в .env и впиши ключ", want)
	}
	model := os.Getenv("DEEPSEEK_MODEL")
	if model == "" {
		model = DefaultModel
	}
	return &Client{
		APIKey:    key,
		Model:     model,
		URL:       DefaultURL,
		MaxTokens: 1500,
		HTTP:      &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// Options are the per-request controls the API itself offers. A zero value sends
// none of them, which is what "no constraints" has to mean for an honest comparison.
type Options struct {
	// MaxTokens caps generation. 0 means the field is not sent at all.
	MaxTokens int
	// Stop are sequences the API cuts the answer on. The sequence itself is not
	// returned in the content.
	Stop []string
	// ResponseFormat is "json_object" to force structured output, "" to leave it alone.
	ResponseFormat string
	// Temperature is sent only when set.
	Temperature *float64
	// Thinking is the model's built-in reasoning: "enabled" or "disabled".
	// Empty means "disabled", which is what days 1-2 have always sent — a demo
	// wants the answer, not the deliberation, and on day 3 an unasked-for
	// reasoning pass would quietly turn the plain run into chain-of-thought.
	Thinking string
	// ReasoningEffort ("high", "max") is sent only alongside enabled thinking.
	ReasoningEffort string
}

type thinking struct {
	Type string `json:"type"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model           string          `json:"model"`
	Messages        []Message       `json:"messages"`
	Stream          bool            `json:"stream"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	Stop            []string        `json:"stop,omitempty"`
	ResponseFormat  *responseFormat `json:"response_format,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	// deepseek-v4 models reason before answering, and that reasoning eats the
	// token budget. A chat demo wants the answer, not the deliberation.
	Thinking *thinking `json:"thinking,omitempty"`
}

type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
			// ReasoningContent is the chain of thought, returned as its own
			// field rather than mixed into the answer.
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

// Answer is what came back, including what the API says about itself —
// the model name and request id are the proof of where the text came from.
type Answer struct {
	Content string
	Model   string
	ID      string
	Usage   Usage
	// FinishReason is why generation stopped: "stop" when the model or a stop
	// sequence ended it, "length" when it ran into max_tokens mid-word.
	FinishReason string
	// RequestBody is the JSON actually sent, minus the key — the evidence that
	// the constraints travelled in the request and were not just asked for in prose.
	RequestBody string
	// Reasoning is what the model thought before answering, present only when
	// thinking was enabled.
	Reasoning string
}

// Ask sends the whole conversation with the client's default token cap.
func (c *Client) Ask(ctx context.Context, messages []Message) (Answer, error) {
	return c.AskWith(ctx, messages, Options{MaxTokens: c.MaxTokens})
}

// AskWith sends the conversation under exactly the options given: whatever is
// left at its zero value is not sent to the API at all.
func (c *Client) AskWith(ctx context.Context, messages []Message, opts Options) (Answer, error) {
	mode := opts.Thinking
	if mode == "" {
		mode = "disabled"
	}
	request := chatRequest{
		Model:       c.Model,
		Messages:    messages,
		Stream:      false,
		MaxTokens:   opts.MaxTokens,
		Stop:        opts.Stop,
		Temperature: opts.Temperature,
		Thinking:    &thinking{Type: mode},
	}
	// Documented contract: effort rides along with reasoning, never without it.
	if mode == "enabled" {
		request.ReasoningEffort = opts.ReasoningEffort
	}
	if opts.ResponseFormat != "" {
		request.ResponseFormat = &responseFormat{Type: opts.ResponseFormat}
	}

	body, err := json.Marshal(request)
	if err != nil {
		return Answer{}, err
	}
	pretty, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		pretty = body
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return Answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Answer{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Answer{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Answer{}, fmt.Errorf("API вернул %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Answer{}, fmt.Errorf("не разобрать ответ: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return Answer{}, fmt.Errorf("модель вернула пустой ответ")
	}
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	reasoning := strings.TrimSpace(parsed.Choices[0].Message.ReasoningContent)
	if content == "" {
		if reasoning != "" {
			return Answer{}, fmt.Errorf("модель отдала только рассуждение и не дошла до ответа (finish_reason=%s)",
				parsed.Choices[0].FinishReason)
		}
		return Answer{}, fmt.Errorf("модель вернула пустой текст")
	}
	model := parsed.Model
	if model == "" {
		model = c.Model
	}
	return Answer{
		Content:      content,
		Model:        model,
		ID:           parsed.ID,
		Usage:        parsed.Usage,
		FinishReason: parsed.Choices[0].FinishReason,
		RequestBody:  string(pretty),
		Reasoning:    reasoning,
	}, nil
}

// LoadDotEnv fills in variables from a .env file without overriding the real environment.
func LoadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" && os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
}
