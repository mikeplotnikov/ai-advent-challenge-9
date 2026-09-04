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

// Price is the three rates a provider charges per 1M tokens. Input is split
// because a repeated prompt is served from the provider's cache at a fraction of
// the price — but only past a threshold, and the threshold is the whole story.
//
// Measured against deepseek-v4-flash on 2026-09-04, the same prompt sent three
// times in a row:
//
//	23-token prompt:   0 of 23 cached on every call — never any hit at all
//	285-token prompt:  call 1 cached 0, calls 2 and 3 cached 256 of 285
//
// 256 is four times 64, and 285 is not a multiple of 64: the cache works in
// 64-token blocks and the remainder is always billed as a miss. So a grid of
// short identical prompts gets no discount whatsoever, while a long one drops
// from $0.000063 to $0.000009 per call — 7x on the input side. Which of the two
// a given day is in cannot be assumed; it has to be read off the usage fields.
type Price struct {
	CacheHit  float64 `json:"cacheHit"`
	CacheMiss float64 `json:"cacheMiss"`
	Output    float64 `json:"output"`
}

// Pricing is what one model costs, at both rates the provider bills at.
// Free is not "price 0" and not "price unknown": a model running on the owner's
// own machine costs no dollars by construction, and day 5's task asks for cost
// only "if the model is paid". Printing $0.0000 for a local model, an unpriced
// one and a genuinely free one identically would erase that difference.
type Pricing struct {
	Free    bool  `json:"free"`
	OffPeak Price `json:"offPeak"`
	Peak    Price `json:"peak"`
}

// pricing is quoted, not derived. Source: the provider's published table at
// https://api-docs.deepseek.com/quick_start/pricing/ — fetched 2026-09-04,
// sha256 cf2c6fb2dd8a32a538f12a8176175b8809a3516326a5cb30dfe52d63c490a968.
// Both rate sets are written out as the page prints them rather than computed
// from its sentence "off-peak rates are half of the peak rates", so no
// arithmetic of ours sits between the source and this table.
//
// This table replaced {1.74, 3.48} for pro and {0.14, 0.28} for flash, recorded
// on 2026-09-02, which match no row of the published page. Which of the two
// happened — the earlier reading was wrong, or the provider changed its prices —
// cannot be told apart from here, and the page itself warns that "product prices
// may vary". day-03/RESULTS.md keeps the dollars it was submitted with.
var pricing = map[string]Pricing{
	"deepseek-v4-flash": {
		OffPeak: Price{CacheHit: 0.007, CacheMiss: 0.22, Output: 0.66},
		Peak:    Price{CacheHit: 0.014, CacheMiss: 0.44, Output: 1.32},
	},
	"deepseek-v4-pro": {
		OffPeak: Price{CacheHit: 0.022, CacheMiss: 0.66, Output: 1.98},
		Peak:    Price{CacheHit: 0.044, CacheMiss: 1.32, Output: 3.96},
	},
	"deepseek-v4-flash-vision-exp": {
		OffPeak: Price{CacheHit: 0.007, CacheMiss: 0.22, Output: 0.66},
		Peak:    Price{CacheHit: 0.014, CacheMiss: 0.44, Output: 1.32},
	},
}

// IsPeak reports whether the provider's higher rates were in force at t.
//
// Quoted rule: "Peak hours are 01:00 - 04:00 and 06:00 - 10:00 UTC, Monday
// through Friday (all other hours are off-peak)."
//
// The page does not say which side of an endpoint belongs to which band, so the
// ranges are read half-open here — 04:00:00 UTC prices as off-peak. A call
// landing exactly on a boundary is the one case this function cannot settle from
// the source, and it is deliberately not hidden: a run that straddles 01:00 or
// 06:00 UTC has to say so in its report.
func IsPeak(t time.Time) bool {
	u := t.UTC()
	if d := u.Weekday(); d == time.Saturday || d == time.Sunday {
		return false
	}
	h := u.Hour()
	return (h >= 1 && h < 4) || (h >= 6 && h < 10)
}

// FreeModel registers a model that costs nothing to call — a local server. The
// day that runs a ladder of local models owns that list, not this package: what
// is on the owner's disk is not a property of the provider's API.
func FreeModel(model string) {
	pricing[model] = Pricing{Free: true}
}

// CostAt returns what one call cost, at the rates in force when it was made,
// and whether the model has a price here at all. The second value is not
// decoration: reporting an unknown model as $0.0000 would read as "this call was
// free" instead of "we do not know".
//
// Input tokens the provider did not classify are billed at the CACHE MISS rate.
// That is the expensive end on purpose — an unreported split must never quietly
// price as the cheap one, or a missing field turns into a discount.
func CostAt(model string, u Usage, when time.Time) (float64, bool) {
	p, ok := pricing[model]
	if !ok {
		return 0, false
	}
	if p.Free {
		return 0, true
	}
	rate := p.OffPeak
	if IsPeak(when) {
		rate = p.Peak
	}
	hit, miss := u.PromptCacheHitTokens, u.PromptCacheMissTokens
	if hit+miss != u.PromptTokens {
		hit, miss = 0, u.PromptTokens
	}
	return float64(hit)/1e6*rate.CacheHit +
		float64(miss)/1e6*rate.CacheMiss +
		float64(u.CompletionTokens)/1e6*rate.Output, true
}

// Cost prices a call at the rates in force right now. Callers that recorded when
// the call happened should use CostAt: a grid run across 01:00 or 06:00 UTC on a
// weekday is billed at two different rates, and pricing it all at "now" is wrong
// in whichever direction the run ended.
func Cost(model string, u Usage) (float64, bool) {
	return CostAt(model, u, time.Now())
}

// IsFree reports whether the model is one that costs nothing by construction,
// so a report can print "$0, local" instead of a rounded-to-zero price.
func IsFree(model string) bool {
	p, ok := pricing[model]
	return ok && p.Free
}

// Dialect is how a provider spells "answer without reasoning first". The
// spelling is neither cosmetic nor guessable. Measured 2026-09-04 against ollama
// 0.33.2 and qwen3:1.7b, one prompt ("Сколько будет 17 умножить на 23?"), every
// call returning HTTP 200:
//
//	/api/chat, nothing sent                          reasoned, 971 output tokens
//	/api/chat, "think": false                        did NOT reason, 89 tokens
//	/v1/chat/completions, nothing sent               reasoned, 667 output tokens
//	/v1/chat/completions, "think": false             reasoned, 1063 output tokens
//	/v1/chat/completions, "reasoning": {enabled:false}  reasoned, 1061 output tokens
//	/v1/chat/completions, "reasoning_effort": "none" did NOT reason, 89 tokens
//
// Two spellings out of three are accepted on the OpenAI-compatible endpoint and
// do nothing at all — no error, no warning, an 11x difference in what the call
// generates. So the switch is never assumed to have worked: Answer.Reasoned
// reports what actually came back, and a comparison across providers has to
// check it rather than trust the request it sent.
type Dialect string

const (
	// DialectDeepSeek switches reasoning off with thinking: {"type": "disabled"}.
	DialectDeepSeek Dialect = "deepseek"
	// DialectOllama switches it off with reasoning_effort: "none". A local
	// server needs no credential; its docs call the key "required but ignored".
	DialectOllama Dialect = "ollama"
)

// OllamaURL is the OpenAI-compatible endpoint of a local ollama server. One
// dialect and one transport for every rung of the ladder is the point: when the
// only thing that differs between two measurements is the model name, the
// difference measured is the model.
const OllamaURL = "http://127.0.0.1:11434/v1/chat/completions"

// NewLocal builds a client against a local ollama server. No key is read from
// the environment: there is none, and inventing an empty one would make a
// missing DEEPSEEK_API_KEY look like a local model.
func NewLocal(model string) *Client {
	return &Client{
		APIKey:    "ollama", // required by the endpoint, ignored by it
		Model:     model,
		URL:       OllamaURL,
		Dialect:   DialectOllama,
		MaxTokens: 1500,
		// A 14B model on this machine answers in tens of seconds, not seconds.
		HTTP: &http.Client{Timeout: 600 * time.Second},
	}
}

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
	// PromptCacheHitTokens and PromptCacheMissTokens split the input by whether
	// the provider had the prefix cached: "the API response includes two fields
	// in the usage section to indicate cache status: prompt_cache_hit_tokens for
	// tokens that hit the cache, and prompt_cache_miss_tokens for tokens that did
	// not" (https://api-docs.deepseek.com/guides/kv_cache, checked 2026-09-04).
	// They are the difference between a repeated prompt costing $0.007 and $0.22
	// per 1M, and days 1-4 never read them.
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
	// CompletionDetails carries reasoning_tokens when thinking is enabled. Those
	// tokens are billed as output even though they never appear in the answer.
	CompletionDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type Client struct {
	APIKey string
	Model  string
	URL    string
	// Dialect selects how this provider is told not to reason. Empty means
	// DeepSeek, which is what days 1-4 were built against.
	Dialect   Dialect
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
			// field rather than mixed into the answer. The two providers spell it
			// differently — DeepSeek reasoning_content, ollama reasoning — and both
			// are read so that "did this model reason?" is answered by the response
			// instead of by the request that hoped it would not.
			ReasoningContent string `json:"reasoning_content"`
			ReasoningAlt     string `json:"reasoning"`
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

// Reasoned reports whether the model deliberated before answering. A run that
// asked for no reasoning and got some has not measured what it set out to: the
// comparison would be between one model thinking and another not.
func (a Answer) Reasoned() bool {
	return strings.TrimSpace(a.Reasoning) != ""
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
	}
	switch c.Dialect {
	case DialectOllama:
		// "none" is the only spelling of this that the endpoint acts on; see Dialect.
		if mode == "disabled" {
			request.ReasoningEffort = "none"
		} else {
			request.ReasoningEffort = opts.ReasoningEffort
		}
	default:
		request.Thinking = &thinking{Type: mode}
		// Documented contract: effort rides along with reasoning, never without it.
		if mode == "enabled" {
			request.ReasoningEffort = opts.ReasoningEffort
		}
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
	if reasoning == "" {
		reasoning = strings.TrimSpace(parsed.Choices[0].Message.ReasoningAlt)
	}
	if content == "" {
		if reasoning != "" {
			// The provider billed this call: the answer never came, the reasoning
			// tokens did. Returning the usage alongside the error is what lets a
			// caller report what the failed attempt cost.
			return Answer{
				Usage:        parsed.Usage,
				FinishReason: parsed.Choices[0].FinishReason,
				Reasoning:    reasoning,
				Model:        parsed.Model,
			}, fmt.Errorf("модель отдала только рассуждение и не дошла до ответа (finish_reason=%s)",
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

// PricingTable returns a copy of the price table so another implementation can be
// checked against it instead of being trusted to have copied it correctly. The
// showcase runs a JS mirror of each day, and day 4's mirror carried its own hand-
// written copy of the prices: two copies of a number that must agree is one copy
// too many. The map is rebuilt on every call so a caller cannot reach in and edit
// what everything else prices against.
func PricingTable() map[string]Pricing {
	out := make(map[string]Pricing, len(pricing))
	for model, p := range pricing {
		out[model] = p
	}
	return out
}
