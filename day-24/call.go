package main

import (
	"context"
	"fmt"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"time"
)

var zeroTemperature float64

func answerOptions() llm.Options {
	return llm.Options{Temperature: &zeroTemperature, MaxTokens: 1200, ResponseFormat: "json_object"}
}
func rewriteOptions() llm.Options { return llm.Options{Temperature: &zeroTemperature, MaxTokens: 200} }
func rerankOptions() llm.Options {
	return llm.Options{Temperature: &zeroTemperature, MaxTokens: 400, ResponseFormat: "json_object"}
}
func addUsage(t *llm.Usage, u llm.Usage) {
	t.PromptTokens += u.PromptTokens
	t.CompletionTokens += u.CompletionTokens
	t.TotalTokens += u.TotalTokens
	t.PromptCacheHitTokens += u.PromptCacheHitTokens
	t.PromptCacheMissTokens += u.PromptCacheMissTokens
	t.CompletionDetails.ReasoningTokens += u.CompletionDetails.ReasoningTokens
}
func callModel(ctx context.Context, client *llm.Client, stage string, messages []llm.Message, opts llm.Options, validate func(string) error) (Call, error) {
	c := Call{Stage: stage, Messages: messages, CostKnown: true, Attempts: []Attempt{}}
	for i := 0; i < 2; i++ {
		start := time.Now().UTC()
		a, e := client.AskWith(ctx, messages, opts)
		if e == nil && validate != nil {
			e = validate(a.Content)
		}
		cost, known := llm.CostAt(a.Model, a.Usage, start)
		at := Attempt{StartedAt: start, DurationMS: time.Since(start).Milliseconds(), Response: a.Content, FinishReason: a.FinishReason, Usage: a.Usage, Model: a.Model, CostUSD: cost, CostKnown: known}
		if e != nil {
			at.Error = e.Error()
		}
		c.Attempts = append(c.Attempts, at)
		c.Response = a.Content
		c.FinishReason = a.FinishReason
		c.Model = a.Model
		addUsage(&c.Usage, a.Usage)
		c.CostUSD += cost
		c.CostKnown = c.CostKnown && known
		if e == nil {
			return c, nil
		}
		if i == 1 {
			c.Error = e.Error()
			return c, fmt.Errorf("%s: %w", stage, e)
		}
	}
	return c, fmt.Errorf("unreachable")
}
