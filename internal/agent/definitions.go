package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// The showcase page reimplements this agent in JavaScript, and the two must not
// quietly disagree about what the agent sends. Day 4's mirror kept a hand-written
// copy of the definitions and drifted; since day 5 the definitions leave the Go side
// as data, and challeng/test/day06-parity.mjs checks the mirror against them.
//
// For day 6 what has to match is not a prompt or a truth value — it is the SHAPE of
// the request: which roles, in which order, and what falls out of the window when
// the conversation gets long. So these are worked examples produced by the real
// agent, not a description of it. Change the assembly and the examples change with
// it; the parity test then fails until the mirror is brought back in line.
//
// It lives in this package because the recorder has to implement Caller, and Caller
// speaks llm types. Putting it in day-06 would have made the interface import the
// transport package — the very thing the day's encapsulation test forbids.

// DumpedMessage is one message as it went over the wire.
type DumpedMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// DumpedStack is one worked example: this config, these turns, this is what the
// agent actually sent on the next call.
type DumpedStack struct {
	Case     string          `json:"case"`
	System   string          `json:"system"`
	MaxTurns int             `json:"maxTurns"`
	Turns    []string        `json:"turns"`
	Next     string          `json:"next"`
	Sent     []DumpedMessage `json:"sent"`
}

// DumpedCost is one priced call: the usage that went in, and what llm.CostAt made of
// it. The JS mirror is checked against these rather than against arithmetic someone
// did by hand in a test — a hand-computed expectation checks the test author, this
// checks the two implementations against each other.
//
// The cases are the ones where the two can quietly disagree, not the ones that are
// obviously right. A review on 2026-09-07 found exactly such a disagreement: the JS
// mirror trusted a cache split that did not add up, and priced the same usage more
// than three times cheaper than Go. The parity test could not see it, because every
// usage it fed to the mirror had a split that added up.
type DumpedCost struct {
	Case                  string  `json:"case"`
	Model                 string  `json:"model"`
	PromptTokens          int     `json:"promptTokens"`
	PromptCacheHitTokens  int     `json:"promptCacheHitTokens"`
	PromptCacheMissTokens int     `json:"promptCacheMissTokens"`
	CompletionTokens      int     `json:"completionTokens"`
	AtRFC3339             string  `json:"at"`
	Cost                  float64 `json:"cost"`
	Priced                bool    `json:"priced"`
}

// Definitions is everything the JS mirror has to agree with.
type Definitions struct {
	DefaultSystem string        `json:"defaultSystem"`
	Stacks        []DumpedStack `json:"stacks"`
	Costs         []DumpedCost  `json:"costs"`
	Rules         []string      `json:"rules"`
}

// recorder answers predictably and keeps the last request, so the examples below are
// the agent's own assembly rather than a story about it.
type recorder struct {
	sent []DumpedMessage
	n    int
}

func (r *recorder) AskWith(_ context.Context, messages []llm.Message, _ llm.Options) (llm.Answer, error) {
	r.sent = r.sent[:0]
	for _, m := range messages {
		r.sent = append(r.sent, DumpedMessage{Role: m.Role, Content: m.Content})
	}
	r.n++
	return llm.Answer{Content: fmt.Sprintf("ответ %d", r.n), Model: llm.DefaultModel}, nil
}

// BuildDefinitions runs the real agent over fixed conversations and records what it
// sent. defaultSystem is the interface's own default prompt, passed in so the
// package does not have to know which application is asking.
func BuildDefinitions(defaultSystem string) (Definitions, error) {
	defs := Definitions{
		DefaultSystem: defaultSystem,
		Rules: []string{
			"системный промпт идёт первым и ровно один раз, даже на десятом ходе",
			"история хранится без системного промпта: смена роли не оставляет старый system в стеке",
			"новый ввод всегда последний",
			"maxTurns режет целыми обменами и с самых старых: пользовательский ход без ответа читался бы моделью как вопрос, который она проигнорировала",
			"пустой ответ модели — ошибка, ход в историю не попадает, но расход отдаётся наружу",
			"отклонённый проверкой ответ в историю не попадает",
		},
	}

	cases := []struct {
		name     string
		system   string
		maxTurns int
		turns    []string
		next     string
	}{
		{"история накапливается", defaultSystem, 0, []string{"первый", "второй"}, "третий"},
		{"окно в два обмена", defaultSystem, 2, []string{"один", "два", "три"}, "четыре"},
		{"без системного промпта", "", 0, []string{"первый"}, "второй"},
	}

	for _, c := range cases {
		rec := &recorder{}
		a, err := New(rec, Config{SystemPrompt: c.system, MaxTurns: c.maxTurns})
		if err != nil {
			return defs, err
		}
		for _, q := range append(append([]string(nil), c.turns...), c.next) {
			if _, err := a.Ask(context.Background(), q); err != nil {
				return defs, err
			}
		}
		if len(rec.sent) == 0 {
			return defs, errors.New("agent: определения пусты, агент ничего не отправил")
		}
		defs.Stacks = append(defs.Stacks, DumpedStack{
			Case:     c.name,
			System:   c.system,
			MaxTurns: c.maxTurns,
			Turns:    c.turns,
			Next:     c.next,
			Sent:     append([]DumpedMessage(nil), rec.sent...),
		})
	}

	defs.Costs = buildCosts()
	return defs, nil
}

// offPeak and peak are a weekday noon and a weekday 02:00 UTC. Every rate doubles in
// the peak band, so a mirror that got the band wrong would be off by exactly two —
// which is why both are dumped rather than one.
var (
	offPeakMoment = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	peakMoment    = time.Date(2026, 9, 7, 2, 0, 0, 0, time.UTC)
)

func buildCosts() []DumpedCost {
	type sample struct {
		name  string
		model string
		usage llm.Usage
		at    time.Time
	}
	honest := llm.Usage{PromptTokens: 300, PromptCacheHitTokens: 256, PromptCacheMissTokens: 44, CompletionTokens: 20}
	samples := []sample{
		{"обычный сплит, вне peak", llm.DefaultModel, honest, offPeakMoment},
		{"тот же вызов в peak", llm.DefaultModel, honest, peakMoment},
		{"сплит не сходится с промптом", llm.DefaultModel,
			llm.Usage{PromptTokens: 300, PromptCacheHitTokens: 50, PromptCacheMissTokens: 50, CompletionTokens: 20}, offPeakMoment},
		{"сплит есть, итога нет", llm.DefaultModel,
			llm.Usage{PromptTokens: 0, PromptCacheHitTokens: 100, PromptCacheMissTokens: 100, CompletionTokens: 10}, offPeakMoment},
		{"итог есть, сплита нет", llm.DefaultModel,
			llm.Usage{PromptTokens: 300, CompletionTokens: 20}, offPeakMoment},
		{"отрицательные значения", llm.DefaultModel,
			llm.Usage{PromptTokens: 300, PromptCacheHitTokens: -50, PromptCacheMissTokens: 350, CompletionTokens: -5}, offPeakMoment},
		{"дорогая модель, обычный сплит", "deepseek-v4-pro", honest, offPeakMoment},
		{"модели нет в прайсе", "нет-такой-модели", honest, offPeakMoment},
	}
	out := make([]DumpedCost, 0, len(samples))
	for _, s := range samples {
		cost, priced := llm.CostAt(s.model, s.usage, s.at)
		out = append(out, DumpedCost{
			Case:                  s.name,
			Model:                 s.model,
			PromptTokens:          s.usage.PromptTokens,
			PromptCacheHitTokens:  s.usage.PromptCacheHitTokens,
			PromptCacheMissTokens: s.usage.PromptCacheMissTokens,
			CompletionTokens:      s.usage.CompletionTokens,
			AtRFC3339:             s.at.Format(time.RFC3339),
			Cost:                  cost,
			Priced:                priced,
		})
	}
	return out
}

// WriteDefinitions renders the definitions as indented JSON. Serialisation lives
// here for the same reason the recorder does: day-06's encapsulation test bans
// encoding/json from the interface, as a proxy for "the interface never handles the
// model's wire format". Rather than carve an exception into that rule, the agent
// writes its own dump.
func WriteDefinitions(w io.Writer, defaultSystem string) error {
	defs, err := BuildDefinitions(defaultSystem)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(defs)
}
