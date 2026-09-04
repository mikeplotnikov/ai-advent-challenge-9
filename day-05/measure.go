// The measurement machinery both runs share: the pilot that selects classes and
// the grid that measures them. Nothing here decides anything — it sends one call,
// records what came back, and counts. Every field is something a report is allowed
// to claim, and nothing is reconstructed later from something else.
package main

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

// outcome is one call. Every field here is something a report is allowed to
// claim, and nothing is inferred later from something else.
type outcome struct {
	correct   bool
	parsed    bool // produced an answer in the required form at all
	truncated bool // the token cap ended it, which is not a wrong answer
	reasoned  bool // the reasoning switch did not take: this run is not comparable
	elapsed   time.Duration
	usage     llm.Usage
	at        time.Time
	raw       string
	err       error
}

// cell is one class measured on one rung.
type cell struct {
	rung     rung
	task     task
	outcomes []outcome
}

func (c cell) n() int { return len(c.outcomes) }

func (c cell) counts() (correct, unparsed, truncated, reasoned, failed int) {
	for _, o := range c.outcomes {
		// Counted regardless of whether the call succeeded: a run whose reasoning
		// switch failed is not comparable even if it also failed to return an
		// answer, and that is the case most likely to arrive as an error.
		if o.reasoned {
			reasoned++
		}
		if o.err != nil {
			failed++
			continue
		}
		if o.correct {
			correct++
		}
		if !o.parsed {
			unparsed++
		}
		if o.truncated {
			truncated++
		}
	}
	return
}

// rate is the share of correct answers among calls that came back at all. A
// transport failure is not a wrong answer, and counting it as one would let a
// flaky network look like a weak model.
func (c cell) rate() (float64, int, int) {
	correct, _, _, _, failed := c.counts()
	usable := c.n() - failed
	if usable == 0 {
		return 0, 0, 0
	}
	return float64(correct) / float64(usable), correct, usable
}

func (c cell) medianSeconds() float64 { return median(c.seconds()) }

// medianSecondsCI is the 95% bootstrap interval around that median. Without it the
// seconds column is an assertion: at thirty runs a median of 4.8 against 9.9 may or
// may not be a difference, and "стало медленнее" is exactly the kind of claim this
// project has had to retract before. Resampling rather than a formula because the
// latency distribution is skewed and small — the assumptions behind a closed form
// are the ones that fail here.
//
// The seed is fixed so the same measurements always produce the same interval: a
// report whose numbers move when it is regenerated cannot be checked by anyone.
func (c cell) medianSecondsCI() (float64, float64) {
	xs := c.seconds()
	if len(xs) < 3 {
		return 0, 0
	}
	return stats.BootstrapCI(len(xs), 2000, 5, func(sample []int) float64 {
		vals := make([]float64, len(sample))
		for i, idx := range sample {
			vals[i] = xs[idx]
		}
		sort.Float64s(vals)
		return median(vals)
	})
}

func (c cell) avgOutputTokens() int {
	sum, n := 0, 0
	for _, o := range c.outcomes {
		if o.err == nil {
			sum += o.usage.CompletionTokens
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

// cost sums what the provider charged, including calls that came back as errors.
// A model that burned its whole budget without producing an answer was still
// billed for it, and a dollar column that quietly dropped those would understate
// exactly the runs that cost the most.
func (c cell) cost() (float64, bool) {
	total := 0.0
	known := false
	for _, o := range c.outcomes {
		if v, ok := llm.CostAt(c.rung.id, o.usage, o.at); ok {
			total += v
			known = true
		}
	}
	return total, known
}

// measured reports whether anything came back at all. Without it a rung whose every
// call failed prints as 0% next to a rung that genuinely answered wrong thirty
// times, and those are not the same statement.
func (c cell) measured() bool { return len(c.usable()) > 0 }

// runOnce sends one class to one rung a single time. Both runs go through here,
// so the pilot that picks the classes and the grid that measures them cannot
// drift into asking different questions.
func runOnce(ctx context.Context, client *llm.Client, t task) outcome {
	messages := []llm.Message{
		{Role: "system", Content: t.system},
		{Role: "user", Content: t.prompt},
	}
	started := time.Now()
	answer, err := client.AskWith(ctx, messages, llm.Options{MaxTokens: t.maxTokens})
	o := outcome{elapsed: time.Since(started), at: started, err: err}
	// Usage and the reasoning flag are read even when the call failed. The client
	// deliberately returns both when a model spent its whole budget reasoning and
	// never reached an answer — which is the most likely shape of a reasoning
	// switch that did not take, and exactly the case this day must not miss.
	// Recording it as a bare transport failure would hide the defect and the money
	// the provider charged for it.
	o.usage = answer.Usage
	o.reasoned = answer.Reasoned()
	if err != nil {
		return o
	}
	o.raw = answer.Content
	o.truncated = answer.FinishReason == "length"
	o.reasoned = answer.Reasoned()
	if t.kind == open {
		o.parsed = strings.TrimSpace(answer.Content) != ""
		return o
	}
	o.correct, o.parsed = t.check(answer.Content)
	return o
}

// runCell sends one class to one rung repeat times, strictly one call at a time.
// Sequential everywhere on purpose. Local rungs share the same 11.8 GiB of GPU
// memory, so parallel calls would queue inside the server and every second
// measured would belong to the queue rather than the model. Running the cloud
// rungs any differently would then make their seconds mean something else than
// the local ones — and the day is a comparison of seconds across rungs.
func runCell(ctx context.Context, client *llm.Client, r rung, t task, repeat int) cell {
	c := cell{rung: r, task: t}
	for i := 0; i < repeat; i++ {
		c.outcomes = append(c.outcomes, runOnce(ctx, client, t))
	}
	return c
}

// warm loads a local model's weights before anything is timed. The smoke run
// measured qwen3:8b taking 4.14 s to return a 4-token answer, nearly all of it
// load: leaving that in would make the first class of the first rung look slow
// for a reason that has nothing to do with the model.
func warm(ctx context.Context, client *llm.Client, r rung) {
	if !r.local {
		return
	}
	_, _ = client.AskWith(ctx, []llm.Message{{Role: "user", Content: "привет"}},
		llm.Options{MaxTokens: 8})
}

// --- what a cell may report about itself -------------------------------------

// usable returns the calls that came back at all. A transport failure is not a
// wrong answer, and folding it into one would let a flaky network read as a weak
// model.
func (c cell) usable() []outcome {
	out := make([]outcome, 0, len(c.outcomes))
	for _, o := range c.outcomes {
		if o.err == nil {
			out = append(out, o)
		}
	}
	return out
}

func (c cell) seconds() []float64 {
	xs := make([]float64, 0, len(c.outcomes))
	for _, o := range c.usable() {
		xs = append(xs, o.elapsed.Seconds())
	}
	sort.Float64s(xs)
	return xs
}

// slowest is reported instead of a p95. At thirty runs the 95th percentile is the
// second-slowest observation whichever way it is interpolated, so calling it a
// percentile would dress one data point as a distribution.
func (c cell) slowest() float64 {
	xs := c.seconds()
	if len(xs) == 0 {
		return 0
	}
	return xs[len(xs)-1]
}

// tokensPerSecond is the median of the per-call rates, not total tokens over total
// time: one long answer would otherwise dominate. Calls shorter than minTokens are
// dropped because on a four-token answer the rate is mostly startup, and the count
// of what survived is returned so a report can say how thin the number is.
func (c cell) tokensPerSecond(minTokens int) (float64, int) {
	var rates []float64
	for _, o := range c.usable() {
		if o.usage.CompletionTokens < minTokens || o.elapsed.Seconds() <= 0 {
			continue
		}
		rates = append(rates, float64(o.usage.CompletionTokens)/o.elapsed.Seconds())
	}
	if len(rates) == 0 {
		return 0, 0
	}
	sort.Float64s(rates)
	return median(rates), len(rates)
}

func (c cell) avgChars() int {
	sum, n := 0, 0
	for _, o := range c.usable() {
		sum += len([]rune(o.raw))
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

// costPerCorrect is the number that actually decides which model to use. A model
// three times cheaper that is right half as often is not cheaper. It is undefined
// when nothing was answered correctly — and undefined is reported as such, never
// as zero, which would read as "free".
func (c cell) costPerCorrect() (float64, bool) {
	total, known := c.cost()
	if !known {
		return 0, false
	}
	correct, _, _, _, _ := c.counts()
	if correct == 0 {
		return 0, false
	}
	return total / float64(correct), true
}

// costPerCall divides the whole bill by the calls that produced an answer: what a
// reader wants is the price of getting an answer, not the price of a request.
func (c cell) costPerCall() (float64, bool) {
	total, known := c.cost()
	if !known || len(c.usable()) == 0 {
		return 0, false
	}
	return total / float64(len(c.usable())), true
}

// window is when this cell's calls happened. Printed per rung so a reader can see
// whether two rungs were measured in the same conditions, and whether a block
// crossed the provider's peak-hour boundary.
func (c cell) window() (time.Time, time.Time) {
	var first, last time.Time
	for _, o := range c.outcomes {
		if first.IsZero() || o.at.Before(first) {
			first = o.at
		}
		if o.at.After(last) {
			last = o.at
		}
	}
	return first, last
}

func median(sorted []float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
