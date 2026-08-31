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
}

type Client struct {
	APIKey string
	Model  string
	URL    string
	HTTP   *http.Client
}

// New builds a client from the environment: DEEPSEEK_API_KEY and optional DEEPSEEK_MODEL.
func New() (*Client, error) {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("DEEPSEEK_API_KEY не задан: скопируй .env.example в .env и впиши ключ")
	}
	model := os.Getenv("DEEPSEEK_MODEL")
	if model == "" {
		model = DefaultModel
	}
	return &Client{
		APIKey: key,
		Model:  model,
		URL:    DefaultURL,
		HTTP:   &http.Client{Timeout: 120 * time.Second},
	}, nil
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

// Ask sends the whole conversation and returns the model's reply.
func (c *Client) Ask(ctx context.Context, messages []Message) (string, Usage, error) {
	body, err := json.Marshal(chatRequest{Model: c.Model, Messages: messages, Stream: false})
	if err != nil {
		return "", Usage{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", Usage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", Usage{}, fmt.Errorf("API вернул %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", Usage{}, fmt.Errorf("не разобрать ответ: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", Usage{}, fmt.Errorf("модель вернула пустой ответ")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), parsed.Usage, nil
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
