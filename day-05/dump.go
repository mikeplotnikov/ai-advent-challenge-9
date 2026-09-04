package main

import (
	"encoding/json"
	"io"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

// The showcase page reimplements this day in JavaScript, and the two must not
// quietly disagree about what a task asks, what its truth is, or what a call
// costs. Day 4's mirror kept its own hand-written copy of the prompts and the
// price table; this one is checked against the Go definitions instead, byte for
// byte, by challeng/test/day05-parity.mjs.
//
// So the definitions leave here as data rather than as prose for someone to copy.

type dumpedTask struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	System    string `json:"system"`
	Prompt    string `json:"prompt"`
	Truth     string `json:"truth"`
	MaxTokens int    `json:"maxTokens"`
}

type dumpedRung struct {
	ID      string `json:"id"`
	Tier    string `json:"tier"`
	Params  string `json:"params"`
	Quant   string `json:"quant"`
	Context string `json:"context"`
	Memory  string `json:"memory"`
	Local   bool   `json:"local"`
}

// reference values are computed by the Go implementation on fixed inputs so the JS
// mirror can be checked against what Go actually returns, rather than against
// arithmetic someone did by hand in the test. A hand-computed expectation checks
// the test author; this checks the two implementations against each other.
type dumpedCost struct {
	Model                 string  `json:"model"`
	PromptTokens          int     `json:"promptTokens"`
	PromptCacheHitTokens  int     `json:"promptCacheHitTokens"`
	PromptCacheMissTokens int     `json:"promptCacheMissTokens"`
	CompletionTokens      int     `json:"completionTokens"`
	AtRFC3339             string  `json:"at"`
	Peak                  bool    `json:"peak"`
	Cost                  float64 `json:"cost"`
	Known                 bool    `json:"known"`
	Note                  string  `json:"note"`
}

type dumpedStat struct {
	Name   string    `json:"name"`
	Args   []int     `json:"args"`
	Values []float64 `json:"values"`
}

type dump struct {
	Tasks   []dumpedTask           `json:"tasks"`
	Rungs   []dumpedRung           `json:"rungs"`
	Pricing map[string]llm.Pricing `json:"pricing"`
	Peak    map[string]any         `json:"peakRule"`
	Schema  map[string][]string    `json:"schema"`
	Limits  map[string]int         `json:"limits"`
	Costs   []dumpedCost           `json:"referenceCosts"`
	Stats   []dumpedStat           `json:"referenceStats"`
}

func kindName(k kind) string {
	switch k {
	case exact:
		return "exact"
	case schema:
		return "schema"
	default:
		return "open"
	}
}

func writeDump(w io.Writer) error {
	d := dump{
		Pricing: llm.PricingTable(),
		Peak: map[string]any{
			"utcHours":     []any{[]int{1, 4}, []int{6, 10}},
			"weekdaysOnly": true,
			"note":         "полуоткрытые интервалы: 04:00 и 10:00 UTC уже off-peak",
		},
		Schema: map[string][]string{"keys": schemaKeys},
		Limits: map[string]int{"tokenCap": tokenCap},
	}
	for _, t := range tasks() {
		d.Tasks = append(d.Tasks, dumpedTask{
			Key: t.key, Title: t.title, Kind: kindName(t.kind),
			System: t.system, Prompt: t.prompt, Truth: t.truth, MaxTokens: t.maxTokens,
		})
	}
	for _, r := range ladder {
		d.Rungs = append(d.Rungs, dumpedRung{
			ID: r.id, Tier: r.tier, Params: r.params, Quant: r.quant,
			Context: r.context, Memory: r.memory, Local: r.local,
		})
	}
	// Two instants, both Fridays, one in each band: 17:00 UTC is off-peak and
	// 07:00 UTC falls inside the 06:00-10:00 window.
	off := time.Date(2026, 9, 4, 17, 0, 0, 0, time.UTC)
	on := time.Date(2026, 9, 4, 7, 0, 0, 0, time.UTC)
	weekend := time.Date(2026, 9, 5, 7, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		model string
		u     llm.Usage
		at    time.Time
		note  string
	}{
		{"deepseek-v4-flash", llm.Usage{PromptTokens: 285, PromptCacheHitTokens: 256, PromptCacheMissTokens: 29, CompletionTokens: 100}, off, "разбивка кэша пришла, off-peak"},
		{"deepseek-v4-flash", llm.Usage{PromptTokens: 285, CompletionTokens: 100}, off, "разбивки нет — весь вход по ставке cache miss"},
		{"deepseek-v4-pro", llm.Usage{PromptTokens: 285, PromptCacheHitTokens: 256, PromptCacheMissTokens: 29, CompletionTokens: 100}, on, "peak: все ставки вдвое"},
		{"deepseek-v4-pro", llm.Usage{PromptTokens: 285, PromptCacheHitTokens: 256, PromptCacheMissTokens: 29, CompletionTokens: 100}, weekend, "суббота — peak-окон нет"},
		{"deepseek-v4-flash", llm.Usage{PromptTokens: 285, CompletionTokens: 100}, on, "разбивки нет и peak — дорогая ставка обеих полос"},
		{"deepseek-v4-flash", llm.Usage{PromptTokens: 100, PromptCacheHitTokens: 40, CompletionTokens: 10}, off, "разбивка не сходится с общим числом — считаем по miss"},
		{"deepseek-v4-flash", llm.Usage{PromptCacheHitTokens: 256, PromptCacheMissTokens: 29, CompletionTokens: 10}, off, "разбивка есть, общего числа нет — считаем по miss, не по нулю"},
		{"deepseek-v4-flash", llm.Usage{PromptTokens: -50, CompletionTokens: 10}, off, "отрицательные токены — не скидка, а ноль"},
		{"deepseek-v9-unreleased", llm.Usage{PromptTokens: 10, CompletionTokens: 10}, off, "модели нет в прайсе — цена неизвестна, не ноль"},
		{"qwen3:1.7b", llm.Usage{PromptTokens: 100, CompletionTokens: 500}, on, "локальная — бесплатна по построению, и это известно"},
	} {
		v, known := llm.CostAt(c.model, c.u, c.at)
		d.Costs = append(d.Costs, dumpedCost{
			Model: c.model, PromptTokens: c.u.PromptTokens,
			PromptCacheHitTokens: c.u.PromptCacheHitTokens, PromptCacheMissTokens: c.u.PromptCacheMissTokens,
			CompletionTokens: c.u.CompletionTokens, AtRFC3339: c.at.Format(time.RFC3339),
			Peak: llm.IsPeak(c.at), Cost: v, Known: known, Note: c.note,
		})
	}

	for _, w := range [][2]int{{0, 5}, {3, 5}, {5, 5}, {12, 30}, {24, 30}, {0, 30}, {30, 30}} {
		lo, hi := stats.Wilson(w[0], w[1])
		d.Stats = append(d.Stats, dumpedStat{Name: "wilson", Args: []int{w[0], w[1]}, Values: []float64{lo, hi}})
	}
	for _, f := range [][4]int{
		{24, 6, 12, 18}, {30, 0, 0, 30}, {15, 15, 15, 15}, {1, 4, 0, 5}, {6, 24, 12, 18},
	} {
		p := stats.FisherTwoSided(f[0], f[1], f[2], f[3])
		d.Stats = append(d.Stats, dumpedStat{Name: "fisher", Args: []int{f[0], f[1], f[2], f[3]}, Values: []float64{p}})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}
