package main

import (
	"context"
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// Creativity has no ground truth, so this file does not measure it. It collects
// a second opinion and labels it as one. The challenge chat drew the line in the
// same place: asked whether the evaluation could be handed to an LLM, the answer
// was "всё равно финальное решение за тобой" [чат #1644-1647].
//
// Two properties make the opinion worth having at all:
//
//  1. It is blind. The judge sees the answers shuffled and stripped of any
//     mention of temperature. A judge told "this one came from temperature 1.2"
//     would be scoring the label.
//  2. It scores distinct answers, not runs. The same name produced by two
//     settings gets one score by construction, so the judge's own noise cannot
//     become a difference between temperatures. What separates them is which
//     answers they produced and how often.

const judgeSystem = "Ты редактор, оцениваешь названия для кофейни. " +
	"Для каждого варианта поставь оценку от 1 до 5 за оригинальность и неожиданность: " +
	"1 — банально и предсказуемо, 5 — неожиданно и запоминается. " +
	"Ответь только строками вида «номер: оценка», по одной на каждый вариант, без пояснений."

// slate is the blind presentation: unique answers in shuffled order.
type slate struct {
	answers []string
	// index maps a normalised answer back to its position on the slate.
	index map[string]int
}

// buildSlate collects the distinct answers across every setting and shuffles
// them with an explicit seed, so a printed report can be reproduced.
func buildSlate(perSetting [][]string, seed int64) slate {
	seen := map[string]bool{}
	var unique []string
	for _, answers := range perSetting {
		for _, a := range answers {
			if a != "" && !seen[a] {
				seen[a] = true
				unique = append(unique, a)
			}
		}
	}
	// Sorted before shuffling so the slate is a function of the set of answers
	// and nothing else. Collected in encounter order it would also depend on
	// which setting happened to produce an answer first, and then rerunning
	// with -temps "1.2,0" would hand the judge a different slate for the same
	// data — the scores could no longer be compared across runs.
	sort.Strings(unique)
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(unique), func(i, j int) { unique[i], unique[j] = unique[j], unique[i] })

	index := make(map[string]int, len(unique))
	for i, a := range unique {
		index[a] = i
	}
	return slate{answers: unique, index: index}
}

func (s slate) prompt() string {
	var b strings.Builder
	b.WriteString("Варианты названий:\n")
	for i, a := range s.answers {
		fmt.Fprintf(&b, "%d. %s\n", i+1, a)
	}
	return b.String()
}

var scoreLine = regexp.MustCompile(`(?m)^\s*\*{0,2}(\d+)\*{0,2}\s*[:.\)]\s*\*{0,2}\s*([1-5])`)

// parseScores reads the judge's reply. It returns what it actually found rather
// than filling gaps: an unscored variant must stay unscored, or the average
// would quietly be taken over a different set than the one presented.
func parseScores(reply string, size int) map[int]int {
	out := map[int]int{}
	for _, m := range scoreLine.FindAllStringSubmatch(reply, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 || n > size {
			continue
		}
		score, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		out[n-1] = score
	}
	return out
}

// judgeRun is one pass of the judge over the whole slate.
type judgeRun struct {
	scores map[int]int
	usage  llm.Usage
	err    error
}

// runJudge asks the same slate several times. The spread between passes is the
// judge's own noise, and it is printed: a difference smaller than that noise is
// not a difference in the answers.
func runJudge(ctx context.Context, c *llm.Client, s slate, passes int) []judgeRun {
	zero := 0.0
	runs := make([]judgeRun, 0, passes)
	for i := 0; i < passes; i++ {
		reply, err := c.AskWith(ctx, []llm.Message{
			{Role: "system", Content: judgeSystem},
			{Role: "user", Content: s.prompt()},
		}, llm.Options{
			MaxTokens: 40*len(s.answers) + 200,
			// The judge is held at temperature 0 on purpose: it is the
			// instrument, and an instrument that drifts cannot measure drift.
			// Its remaining variation is then a property of the model, which is
			// exactly why it runs more than once.
			Temperature: &zero,
		})
		if err != nil {
			runs = append(runs, judgeRun{usage: reply.Usage, err: err})
			continue
		}
		runs = append(runs, judgeRun{scores: parseScores(reply.Content, len(s.answers)), usage: reply.Usage})
	}
	return runs
}

// meanScore averages the judge's score over the runs of one setting. Runs are
// the unit, not distinct answers: a setting that repeats one dull name thirty
// times is not as creative as one that found it once among thirty.
//
// scored reports how many of those runs the judge actually covered, so a partial
// reply is visible instead of being averaged away.
func meanScore(answers []string, s slate, scores map[int]int) (mean float64, scored int) {
	sum := 0
	for _, a := range answers {
		pos, ok := s.index[a]
		if !ok {
			continue
		}
		score, ok := scores[pos]
		if !ok {
			continue
		}
		sum += score
		scored++
	}
	if scored == 0 {
		return 0, 0
	}
	return float64(sum) / float64(scored), scored
}
