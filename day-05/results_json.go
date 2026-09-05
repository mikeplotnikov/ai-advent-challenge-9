package main

import (
	"encoding/json"
	"io"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

// The showcase page shows the recorded grid next to its live calls, because four of
// six rungs are local models a serverless function cannot reach. Those numbers are
// emitted here, from the same run and the same cell methods that write measured.md,
// rather than typed into the page. A number that appears in two places by hand is a
// number that will eventually disagree with itself — this day already found that in
// the price table it inherited.

type jsonCell struct {
	Rung           string      `json:"rung"`
	Tier           string      `json:"tier"`
	Correct        int         `json:"correct"`
	Usable         int         `json:"usable"`
	Rate           *float64    `json:"rate"`
	Wilson         []float64   `json:"wilson,omitempty"`
	MedianSeconds  float64     `json:"medianSeconds"`
	MedianCI       []float64   `json:"medianCI,omitempty"`
	Slowest        float64     `json:"slowest"`
	OutputTokens   int         `json:"outputTokens"`
	Chars          int         `json:"chars"`
	CostPerCall    *float64    `json:"costPerCall"`
	CostPerCorrect *float64    `json:"costPerCorrect"`
	Free           bool        `json:"free"`
	Unparsed       int         `json:"unparsed"`
	Truncated      int         `json:"truncated"`
	Reasoned       int         `json:"reasoned"`
	Failed         int         `json:"failed"`
	Samples        []string    `json:"samples,omitempty"`
	Window         []time.Time `json:"window,omitempty"`
}

type jsonClass struct {
	Key   string     `json:"key"`
	Title string     `json:"title"`
	Kind  string     `json:"kind"`
	Truth string     `json:"truth"`
	Note  string     `json:"note"`
	Cells []jsonCell `json:"cells"`
}

type jsonComparison struct {
	Task  string  `json:"task"`
	Label string  `json:"label"`
	RateA float64 `json:"rateA"`
	RateB float64 `json:"rateB"`
	P     float64 `json:"p"`
	Holm  bool    `json:"holm"`
}

type jsonResults struct {
	Repeat      int              `json:"repeat"`
	Calls       int              `json:"calls"`
	StartedAt   time.Time        `json:"startedAt"`
	FinishedAt  time.Time        `json:"finishedAt"`
	TotalCost   float64          `json:"totalCost"`
	PeakCrossed bool             `json:"peakCrossed"`
	Rungs       []dumpedRung     `json:"rungs"`
	Classes     []jsonClass      `json:"classes"`
	Comparisons []jsonComparison `json:"comparisons"`
}

func writeResultsJSON(run gridRun, w io.Writer) error {
	out := jsonResults{
		Repeat:      run.repeat,
		Calls:       countOutcomes(run),
		StartedAt:   run.startedAt,
		FinishedAt:  run.finishedAt,
		PeakCrossed: run.crossedPeakBoundary(),
	}
	for _, r := range ladder {
		out.Rungs = append(out.Rungs, dumpedRung{
			ID: r.id, Tier: r.tier, Params: r.params, Quant: r.quant,
			Context: r.context, Memory: r.memory, Local: r.local,
		})
	}

	for _, t := range run.tasks {
		class := jsonClass{Key: t.key, Title: t.title, Kind: kindName(t.kind), Truth: t.truth, Note: t.note}
		for _, r := range run.rungs {
			c := run.cell(t, r)
			correct, unparsed, truncated, reasoned, failed := c.counts()
			cell := jsonCell{
				Rung: r.id, Tier: r.tier, Correct: correct, Usable: len(c.usable()),
				MedianSeconds: c.medianSeconds(), Slowest: c.slowest(),
				OutputTokens: c.avgOutputTokens(), Chars: c.avgChars(),
				Free: llm.IsFree(r.id), Unparsed: unparsed, Truncated: truncated,
				Reasoned: reasoned, Failed: failed,
			}
			if c.measured() {
				rate, _, usable := c.rate()
				cell.Rate = &rate
				lo, hi := stats.Wilson(correct, usable)
				cell.Wilson = []float64{lo, hi}
				if secLo, secHi := c.medianSecondsCI(); secHi > 0 {
					cell.MedianCI = []float64{secLo, secHi}
				}
				first, last := c.window()
				cell.Window = []time.Time{first, last}
			}
			if v, ok := c.costPerCall(); ok {
				cell.CostPerCall = &v
			}
			if v, ok := c.costPerCorrect(); ok {
				cell.CostPerCorrect = &v
			}
			if total, ok := c.cost(); ok {
				out.TotalCost += total
			}
			// A few distinct raw answers, so the page can show what a rung actually
			// said rather than only how often it was right. Distinct, because thirty
			// copies of one answer tell a reader nothing they cannot see in the count.
			seen := map[string]bool{}
			for _, o := range c.usable() {
				line := oneLine(o.raw, 160)
				if line == "" || seen[line] {
					continue
				}
				seen[line] = true
				cell.Samples = append(cell.Samples, line)
				if len(cell.Samples) == 3 {
					break
				}
			}
			class.Cells = append(class.Cells, cell)
		}
		out.Classes = append(out.Classes, class)
	}

	for _, c := range family(run) {
		out.Comparisons = append(out.Comparisons, jsonComparison{
			Task: c.task.key, Label: c.label, RateA: c.rateA, RateB: c.rateB,
			P: c.pValue, Holm: c.holm,
		})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	return enc.Encode(out)
}
