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

// callModel is day 22's call with one retry, generalised to any stage. validate, when
// set, rejects a completed answer the stage cannot use (the rerank JSON); a rejected
// answer is retried once like a transport failure, and both attempts are paid for.
func callModel(ctx context.Context, client *llm.Client, stage string, messages []llm.Message, opts llm.Options, validate func(string) error) (Call, error) {
	record := Call{StartedAt: time.Now().UTC(), Stage: stage, Messages: messages}
	costSeen := false
	for attempt := 0; attempt < 2; attempt++ {
		started := time.Now().UTC()
		answer, err := client.AskWith(ctx, messages, opts)
		finished := time.Now()
		if err == nil && validate != nil {
			if invalid := validate(answer.Content); invalid != nil {
				err = invalid
			}
		}
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
			record.Model = answer.Model
		}
		record.DurationMS += item.DurationMS
		if errors.Is(err, llm.ErrEmptyContent) && validate == nil {
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

func answerOptions() llm.Options {
	return llm.Options{Temperature: &zeroTemperature, MaxTokens: answerMaxTokens}
}

func rewriteOptions() llm.Options {
	return llm.Options{Temperature: &zeroTemperature, MaxTokens: rewriteMaxTokens}
}

func rerankOptions() llm.Options {
	return llm.Options{Temperature: &zeroTemperature, MaxTokens: rerankMaxTokens, ResponseFormat: "json_object"}
}

func addUsage(total *llm.Usage, value llm.Usage) {
	total.PromptTokens += value.PromptTokens
	total.CompletionTokens += value.CompletionTokens
	total.TotalTokens += value.TotalTokens
	total.PromptCacheHitTokens += value.PromptCacheHitTokens
	total.PromptCacheMissTokens += value.PromptCacheMissTokens
	total.CompletionDetails.ReasoningTokens += value.CompletionDetails.ReasoningTokens
}

func callCostLine(call Call) string {
	cost := "неизвестна"
	if call.CostKnown {
		cost = fmt.Sprintf("$%.6f", call.CostUSD)
	}
	return fmt.Sprintf("токены: %d вход / %d выход (кэш: %d) · цена: %s · время: %s", call.Usage.PromptTokens,
		call.Usage.CompletionTokens, call.Usage.PromptCacheHitTokens, cost,
		(time.Duration(call.DurationMS) * time.Millisecond).Round(time.Millisecond))
}

func printAnswer(w io.Writer, call Call) {
	switch {
	case call.Error != "":
		fmt.Fprintf(w, "ответ: ошибка: %s\n", rag.TerminalSafeMultiline(call.Error, 1000))
	case call.FilteredOut:
		fmt.Fprintf(w, "ответ: %s (фильтр отсёк все чанки — модель не вызывалась)\n", noDataMarker)
	case call.Score.Outcome == "empty":
		fmt.Fprintf(w, "ответ: (пустой ответ)\n%s\n", callCostLine(call))
	default:
		fmt.Fprintf(w, "ответ:\n%s\n%s\n", rag.TerminalSafeMultiline(call.Response, len([]rune(call.Response))), callCostLine(call))
	}
}
