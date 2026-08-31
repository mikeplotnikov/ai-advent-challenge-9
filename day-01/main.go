// Day 1 — the smallest thing that talks to an LLM: send a prompt over REST,
// print what comes back.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	apiURL       = "https://api.deepseek.com/chat/completions"
	defaultModel = "deepseek-v4-flash"
	systemPrompt = "Ты дружелюбный ассистент. Отвечай кратко и по делу, на русском языке."
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func main() {
	loadDotEnv(".env")

	prompt := strings.TrimSpace(strings.Join(os.Args[1:], " "))
	if prompt == "" {
		prompt = readStdin()
	}
	if prompt == "" {
		fmt.Fprintln(os.Stderr, `usage: go run ./day-01 "твой вопрос"`)
		os.Exit(2)
	}

	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "DEEPSEEK_API_KEY не задан: скопируй .env.example в .env и впиши ключ")
		os.Exit(1)
	}

	model := os.Getenv("DEEPSEEK_MODEL")
	if model == "" {
		model = defaultModel
	}

	answer, usage, err := ask(apiKey, model, prompt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}

	fmt.Println(answer)
	fmt.Fprintf(os.Stderr, "\n[%s: %d токенов промпта + %d токенов ответа]\n",
		model, usage.prompt, usage.completion)
}

type tokenUsage struct{ prompt, completion int }

func ask(apiKey, model, prompt string) (string, tokenUsage, error) {
	body, err := json.Marshal(chatRequest{
		Model: model,
		Messages: []message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		},
		Stream: false,
	})
	if err != nil {
		return "", tokenUsage{}, err
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return "", tokenUsage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", tokenUsage{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", tokenUsage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", tokenUsage{}, fmt.Errorf("API вернул %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", tokenUsage{}, fmt.Errorf("не разобрать ответ: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", tokenUsage{}, fmt.Errorf("модель вернула пустой ответ")
	}

	usage := tokenUsage{parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), usage, nil
}

// loadDotEnv fills in variables from a .env file without overriding the real environment.
func loadDotEnv(path string) {
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

func readStdin() string {
	stat, err := os.Stdin.Stat()
	if err != nil || stat.Mode()&os.ModeCharDevice != 0 {
		return ""
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
