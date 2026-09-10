package agent

// Day 8: tokens as a resource with a price and a ceiling.
//
// The task asks the agent to count tokens "для текущего запроса", "для всей истории
// диалога" and "для ответа модели". Two of those three the provider reports itself,
// and the host said in the chat that the API's own statistics are enough and that
// there is no hidden meaning in the wording (2026-09-09, msgs 2420 → 2427). What the
// provider cannot report is the weight of a request that has not been sent yet — and
// that is the only number an agent can act on: refusing, trimming or warning after
// the answer has arrived is refusing after paying.
//
// So this file adds the one thing the response cannot give: a local estimate, made
// before the call, split into system prompt / history / new question, plus the
// ceiling policy that estimate exists to serve.

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// Token weight per character, by script.
//
// DeepSeek publishes only two ratios: "a Chinese character is approximately 0.6
// tokens and an English character is approximately 0.3 tokens", with the warning
// that the ratio differs between models because the tokenisers differ
// (https://api-docs.deepseek.com/quick_start/token_usage, checked 2026-09-09).
// Russian — which is the whole of this agent's traffic — is not in that table at
// all, so the Cyrillic weight is ours, not the provider's.
//
// An approximation of somebody else's algorithm has a safe side and an unsafe one.
// Under-counting is the unsafe one: it lets a request that the provider will refuse
// through the ceiling, which is exactly the failure the ceiling exists to prevent.
// Every weight here therefore over-counts, and by how much is measured rather than
// assumed — every turn of every day-8 run records the estimate next to the
// prompt_tokens the API reported, and day-08/RESULTS.md carries the distribution.
//
// weightCyrillic is the one number here that is ours. It was fitted on the 25-turn
// growth run of 2026-09-09 (day-08/growth.jsonl, 25 turns, 90 → 7740 input tokens):
// holding the other classes at the values above, the largest weight that could still
// have under-counted any turn was 0.2687, and the median turn needed 0.2464. The
// shipped 0.32 clears the worst observed turn by 19%.
//
// What that calibration does NOT cover, stated rather than papered over: the sample
// is Russian prose written by this model. Code, tables, mixed scripts and other
// languages were not in it, which is why the Latin weight is left where the provider
// put it instead of being scaled by our Russian evidence. Chinese has no class of its
// own here at all — a Han character falls into "other" and is charged 1.00 against
// the 0.6 the provider documents. That over-counts, which is the safe side, and it
// is the honest state of this counter rather than a claim to have covered CJK.
const (
	weightCyrillic = 0.32
	weightLatin    = 0.30
	weightDigit    = 0.50
	weightSpace    = 0.25
	weightOther    = 1.00
)

// Per-message and per-request overhead. The chat format wraps every message in role
// markers and delimiters that no character count sees, and the reply is primed with
// a few more. Both are rounded up for the same reason the weights are.
const (
	tokensPerMessage = 5
	tokensPerReply   = 3
	// tokensForResponseFormat is what asking for json_object costs on the input side.
	// The provider adds an instruction of its own that never appears in our messages,
	// and it is not free: the same request measured twice, once plain and once with
	// response_format, came back 20, 20 and 21 tokens heavier (2026-09-09, three
	// pairs on deepseek-v4-flash). 24 keeps the counter on its safe side.
	//
	// This was found by running the day's own demo, not by the measurements: every
	// probe ran in plain text mode, so the whole calibration had nothing to say about
	// the one option that changes the input. The counter under-counted by 6 tokens on
	// camera, against a promise that it never under-counts.
	tokensForResponseFormat = 24
)

// EstimateTokens is the local counter: what this text will weigh, worked out without
// asking the provider. It is an estimate by construction — the exact number is the
// provider's tokeniser, and it arrives only in the response.
func EstimateTokens(s string) int {
	return int(math.Ceil(estimateWeight(s)))
}

// estimateWeight is the same count before it is rounded up. It exists because
// rounding is not additive: the sum of per-line ceilings is larger than the ceiling
// of the sum, so anything that builds a text to a target weight has to add up the
// weights and round once at the end. Filler did it the other way round and produced
// blobs up to 1% lighter than asked — invisible in a growth run, and exactly the
// wrong direction for a rung meant to exceed the provider's window.
func estimateWeight(s string) float64 {
	var sum float64
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			sum += weightSpace
		case unicode.Is(unicode.Cyrillic, r):
			sum += weightCyrillic
		case unicode.Is(unicode.Latin, r):
			sum += weightLatin
		case unicode.IsDigit(r):
			sum += weightDigit
		default:
			// Punctuation, emoji, box drawing, CJK: everything that is usually a
			// token of its own or several. Charged the most, because this is the
			// class where a cheap estimate is most likely to be wrong.
			sum += weightOther
		}
	}
	return sum
}

// Estimate is the weight of one request as the agent works it out before sending it.
// The three parts are separate because the task lists them separately and because
// they behave differently: the system prompt is constant, the history grows by a
// whole exchange per turn, and the question is the only part the user controls.
type Estimate struct {
	System int
	// Summary is the compressed older history. It travels inside the system message
	// but is reported separately from the role instruction and the raw tail.
	Summary int
	History int
	Input   int
	// Overhead is the chat format's own cost: role markers and delimiters that no
	// character count sees.
	Overhead int
	Total    int
	// Messages is how many messages the request will carry, system prompt included.
	Messages int
	// Exchanges is how many complete past exchanges are inside History.
	Exchanges int
}

// String is the one-line form the CLI prints before a call.
func (e Estimate) String() string {
	return fmt.Sprintf("система %d + summary %d + история %d (%d обменов) + вопрос %d + формат %d = %d токенов",
		e.System, e.Summary, e.History, e.Exchanges, e.Input, e.Overhead, e.Total)
}

// OverflowPolicy is what the agent does when the request does not fit the ceiling it
// was given. The choice belongs to whoever configures the agent, because all three
// answers are defensible and they lose different things: money, history, or nothing
// but the illusion that the limit was enforced.
type OverflowPolicy string

const (
	// OverflowRefuse does not call the model at all. Nothing is spent and nothing is
	// forgotten; the caller gets an error naming both numbers.
	OverflowRefuse OverflowPolicy = "refuse"
	// OverflowTrim drops the oldest exchanges until the request fits. The agent keeps
	// answering and pays for it in memory — and, on this provider, in cache: the
	// prefix changes, so the discount on everything that follows it is lost.
	OverflowTrim OverflowPolicy = "trim"
	// OverflowWarn sends the request anyway and says so. It exists because a ceiling
	// that is not the provider's is a guess, and a caller may prefer the provider's
	// verdict to ours.
	OverflowWarn OverflowPolicy = "warn"
)

// ErrContextOverflow is returned when the request does not fit the agent's ceiling
// and the policy is to refuse. It is deliberately not the provider's error: this one
// costs nothing, because the call never happens.
var ErrContextOverflow = errors.New("запрос не помещается в потолок контекста агента")

// ErrInputAlone is returned when the request does not fit even with the whole history
// dropped. Trimming cannot help here — the system prompt and the question alone are
// over the ceiling — and pretending it can would send a request that is known not to
// fit.
var ErrInputAlone = errors.New("вопрос и системный промпт не помещаются в потолок даже без истории")

// Preflight is what the next request would weigh, without sending it. This is the
// number the response cannot give: prompt_tokens arrives after the money is spent.
func (a *Agent) Preflight(input string) Estimate {
	return a.estimate(strings.TrimSpace(input), a.stack)
}

// estimate weighs the request that messagesFor would build from this stack. The two
// must agree on what goes into a request; the test that counts messages both ways is
// what keeps them from drifting apart.
func (a *Agent) estimate(input string, stack []llm.Message) Estimate {
	var e Estimate
	if a.cfg.SystemPrompt != "" {
		e.System = EstimateTokens(a.cfg.SystemPrompt)
	}
	if summary := a.summaryContext(); summary != "" {
		e.Summary = EstimateTokens(summary)
	}
	if e.System > 0 || e.Summary > 0 {
		e.Messages++
	}
	for _, m := range stack {
		e.History += EstimateTokens(m.Content)
	}
	e.Messages += len(stack)
	e.Exchanges = len(stack) / 2
	e.Input = EstimateTokens(input)
	e.Messages++
	e.Overhead = e.Messages*tokensPerMessage + tokensPerReply
	if a.cfg.ResponseFormat != "" {
		e.Overhead += tokensForResponseFormat
	}
	e.Total = e.System + e.Summary + e.History + e.Input + e.Overhead
	return e
}

// fit applies the ceiling policy and returns the stack that should actually travel.
//
// It never mutates the agent's own stack: a trim that was decided before the call
// must not take effect if the call then fails. Dropping history for a turn that never
// happened would be the same silent loss day 7 was built to prevent, only with the
// agent as the culprit instead of a crash. The caller commits the drop after the turn
// succeeds.
func (a *Agent) fit(input string) (send []llm.Message, dropped int, warning string, est Estimate, err error) {
	est = a.estimate(input, a.stack)
	limit := a.cfg.MaxContextTokens
	if limit <= 0 || est.Total <= limit {
		return a.stack, 0, "", est, nil
	}

	switch a.overflowPolicy() {
	case OverflowWarn:
		return a.stack, 0, fmt.Sprintf(
			"запрос оценён в %d токенов при потолке %d — отправляю как есть, решение за поставщиком",
			est.Total, limit), est, nil

	case OverflowTrim:
		stack := a.stack
		for len(stack) >= 2 {
			stack = stack[2:]
			dropped++
			est = a.estimate(input, stack)
			if est.Total <= limit {
				return stack, dropped, fmt.Sprintf(
					"история обрезана: отброшено обменов %d, запрос ужат до %d токенов при потолке %d",
					dropped, est.Total, limit), est, nil
			}
		}
		// Nothing left to drop and it still does not fit.
		return nil, 0, "", est, fmt.Errorf("%s: %w: оценка %d токенов при потолке %d",
			a.Name(), ErrInputAlone, est.Total, limit)

	default: // OverflowRefuse
		return nil, 0, "", est, fmt.Errorf("%s: %w: оценка %d токенов (система %d + summary %d + история %d + вопрос %d + формат %d) при потолке %d",
			a.Name(), ErrContextOverflow,
			est.Total, est.System, est.Summary, est.History, est.Input, est.Overhead, limit)
	}
}

func (a *Agent) overflowPolicy() OverflowPolicy {
	if a.cfg.OnOverflow == "" {
		return OverflowRefuse
	}
	return a.cfg.OnOverflow
}

// Totals is everything this conversation has cost so far: not the weight of the
// context, but the sum of every request that was sent and every answer that came
// back. The two are different numbers and the task asks for both — the last request
// weighs N, while the conversation that produced it has been billed 1+2+…+N.
//
// Calls that failed after being billed are counted here too. An empty answer, an
// answer that failed the output policy and a request the provider refused are all
// paid for at the provider's discretion, and a total that quietly omits them would
// under-report the spend exactly when the day is about spend.
// The json tags are not decoration: this struct is written into the session file,
// and day 7 chose JSON precisely so that the file can be opened and read out loud.
// Untagged fields would put Go's capitalised names next to the lowercase keys of
// everything around them.
type Totals struct {
	// Calls is every request the provider was asked to bill, successful or not.
	Calls int `json:"calls"`
	// Failed is how many of those calls did not produce a usable answer.
	Failed           int     `json:"failed"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	ReasoningTokens  int     `json:"reasoningTokens"`
	CachedTokens     int     `json:"cachedTokens"`
	MissedTokens     int     `json:"missedTokens"`
	Cost             float64 `json:"cost"`
	// Unpriced is how many calls used a model that is not in the price table. Cost
	// is then a lower bound, not the price of the conversation.
	Unpriced int `json:"unpriced"`
}

// CacheShare is the fraction of all input this conversation has had served from the
// provider's cache. It returns false when nothing has been sent, so that "no data"
// is never printed as "0% cached".
func (t Totals) CacheShare() (float64, bool) {
	total := t.CachedTokens + t.MissedTokens
	if total == 0 {
		return 0, false
	}
	return float64(t.CachedTokens) / float64(total), true
}

// String is the one-line form the CLI prints on request.
func (t Totals) String() string {
	cache := "нет данных"
	if v, ok := t.CacheShare(); ok {
		cache = fmt.Sprintf("%.0f%%", v*100)
	}
	cost := fmt.Sprintf("$%.6f", t.Cost)
	if t.Unpriced > 0 {
		cost += fmt.Sprintf(" (не меньше: вызовов по неизвестной цене %d)", t.Unpriced)
	}
	return fmt.Sprintf("за беседу: вызовов %d (неудачных %d) · вход %d токенов (из кэша %s) · выход %d · всего %s",
		t.Calls, t.Failed, t.PromptTokens, cache, t.CompletionTokens, cost)
}

// Totals is what this conversation has been billed so far, restored spend included.
func (a *Agent) Totals() Totals { return a.totals }

// record adds one billed call to the running totals. It is called for every call the
// provider was asked to make, including the ones that came back unusable.
func (a *Agent) record(u Usage, failed bool) {
	addUsage(&a.totals, u, failed)
}

// recordSummary adds a provider call to the conversation's total and to the separate
// compression subtotal. Day 9's comparison must charge the summary calls too; showing
// only the smaller answer request would make an expensive compression look free.
func (a *Agent) recordSummary(u Usage, failed bool) {
	addUsage(&a.totals, u, failed)
	addUsage(&a.summarySpend, u, failed)
}

func addUsage(t *Totals, u Usage, failed bool) {
	t.Calls++
	if failed {
		t.Failed++
	}
	t.PromptTokens += u.PromptTokens
	t.CompletionTokens += u.CompletionTokens
	t.ReasoningTokens += u.ReasoningTokens
	t.CachedTokens += u.CachedTokens
	t.MissedTokens += u.MissedTokens
	if u.Priced {
		t.Cost += u.Cost
	} else {
		t.Unpriced++
	}
}
