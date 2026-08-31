// Day 1, CLI — the smallest thing that talks to an LLM: send a prompt over REST,
// print what comes back.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

const systemPrompt = "Ты дружелюбный ассистент. Отвечай кратко и по делу, на русском языке."

func main() {
	llm.LoadDotEnv(".env")

	prompt := strings.TrimSpace(strings.Join(os.Args[1:], " "))
	if prompt == "" {
		prompt = readStdin()
	}
	if prompt == "" {
		fmt.Fprintln(os.Stderr, `usage: go run ./day-01 "твой вопрос"`)
		os.Exit(2)
	}

	client, err := llm.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	answer, usage, err := client.Ask(context.Background(), []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}

	fmt.Println(answer)
	fmt.Fprintf(os.Stderr, "\n[%s: %d токенов промпта + %d токенов ответа]\n",
		client.Model, usage.PromptTokens, usage.CompletionTokens)
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
