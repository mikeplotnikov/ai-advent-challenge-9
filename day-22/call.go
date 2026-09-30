package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

var zeroTemperature float64

func callModel(ctx context.Context, client *llm.Client, mode string, repeat int, messages []llm.Message) (Call, error) {
	record := Call{StartedAt: time.Now().UTC(), Mode: mode, Repeat: repeat, Messages: messages}
	costSeen := false
	for attempt := 0; attempt < 2; attempt++ {
		started := time.Now().UTC()
		answer, err := client.AskWith(ctx, messages, llm.Options{Temperature: &zeroTemperature, MaxTokens: 600})
		finished := time.Now()
		cost, known := llm.CostAt(answer.Model, answer.Usage, started)
		item := Attempt{StartedAt: started, DurationMS: finished.Sub(started).Milliseconds(), Usage: answer.Usage, Model: answer.Model, CostUSD: cost, CostKnown: known}
		if err != nil {
			item.Error = err.Error()
		}
		record.Attempts = append(record.Attempts, item)
		addUsage(&record.Usage, answer.Usage)
		record.CostUSD += cost
		if answer.Model != "" {
			if !costSeen {
				record.CostKnown = known
				costSeen = true
			} else {
				record.CostKnown = record.CostKnown && known
			}
		}
		record.DurationMS += item.DurationMS
		if answer.Model != "" {
			record.Model = answer.Model
		}
		if errors.Is(err, llm.ErrEmptyContent) {
			record.FinishReason = answer.FinishReason
			record.Score = Score{Outcome: "empty"}
			return record, nil
		}
		if err == nil {
			record.Response = answer.Content
			record.FinishReason = answer.FinishReason
			return record, nil
		}
		if attempt == 1 {
			record.Error = err.Error()
			return record, err
		}
	}
	return record, fmt.Errorf("вызов модели не выполнен")
}

func addUsage(total *llm.Usage, value llm.Usage) {
	total.PromptTokens += value.PromptTokens
	total.CompletionTokens += value.CompletionTokens
	total.TotalTokens += value.TotalTokens
	total.PromptCacheHitTokens += value.PromptCacheHitTokens
	total.PromptCacheMissTokens += value.PromptCacheMissTokens
	total.CompletionDetails.ReasoningTokens += value.CompletionDetails.ReasoningTokens
}

func printPrompt(w io.Writer, mode string, messages []llm.Message) {
	fmt.Fprintf(w, "[%s · system]\n%s\n[%s · user]\n%s\n", mode, messages[0].Content, mode, messages[1].Content)
}

func printCall(w io.Writer, call Call) {
	if call.Error != "" {
		fmt.Fprintf(w, "[%s] ошибка: %s\n", call.Mode, rag.TerminalSafe(call.Error, 1000))
		return
	}
	answer := call.Response
	if call.Score.Outcome == "empty" {
		answer = "(пустой ответ)"
	}
	fmt.Fprintf(w, "[%s]\n%s\nтокены: %d вход / %d выход (кэш: %d) · цена: ", call.Mode,
		rag.TerminalSafe(answer, len([]rune(answer))), call.Usage.PromptTokens, call.Usage.CompletionTokens, call.Usage.PromptCacheHitTokens)
	if call.CostKnown {
		fmt.Fprintf(w, "$%.6f", call.CostUSD)
	} else {
		fmt.Fprint(w, "неизвестна")
	}
	fmt.Fprintf(w, " · время: %s\n", (time.Duration(call.DurationMS) * time.Millisecond).Round(time.Millisecond))
}
