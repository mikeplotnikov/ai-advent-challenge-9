package main

import "strings"

// "Разнообразие" is a word until something computes it, and the definition has
// to be visible next to the number — otherwise the report says "разнообразие
// 0.42" and nobody can tell what would have made it 0.43. Everything here is
// deterministic and involves no model: these are properties of the collected
// answers, not opinions about them.

// distinct counts how many different answers the N runs produced. For a task
// with one correct answer this is the defect count; for an open one it is the
// point.
func distinct(answers []string) int {
	seen := map[string]bool{}
	for _, a := range answers {
		seen[a] = true
	}
	return len(seen)
}

// largestRepeat is the size of the biggest group of identical answers. It is
// what answers the question temperature 0 is supposed to settle: calibration on
// 2026-09-03 gave 5 identical answers out of 8 at temperature 0, so "ноль
// детерминирован" is measurably false rather than arguably false. DeepSeek's
// chat-completions API has no seed parameter, so there was never a contract
// promising otherwise.
func largestRepeat(answers []string) int {
	counts := map[string]int{}
	best := 0
	for _, a := range answers {
		counts[a]++
		if counts[a] > best {
			best = counts[a]
		}
	}
	return best
}

// meanPairwiseDistance is the average normalised edit distance over every pair
// of answers: 0 when all N answers are identical, 1 when no pair shares
// anything. Averaged over pairs rather than measured against some "typical"
// answer, because there is no reference answer to measure against.
//
// It returns false when there is no pair to measure — one answer has no
// diversity, and returning 0 would claim it had none rather than that the
// question does not apply.
func meanPairwiseDistance(answers []string) (float64, bool) {
	if len(answers) < 2 {
		return 0, false
	}
	sum, pairs := 0.0, 0
	for i := 0; i < len(answers); i++ {
		for j := i + 1; j < len(answers); j++ {
			sum += normalisedDistance(answers[i], answers[j])
			pairs++
		}
	}
	return sum / float64(pairs), true
}

// normalisedDistance is Levenshtein distance divided by the longer string, so
// answers of different lengths stay comparable.
func normalisedDistance(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 && len(rb) == 0 {
		return 0
	}
	d := levenshtein(ra, rb)
	longest := len(ra)
	if len(rb) > longest {
		longest = len(rb)
	}
	return float64(d) / float64(longest)
}

func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

// vocabularyRatio is unique words over total words across all answers. It moves
// for a different reason than the distance does: N answers can all be the same
// length and shape and still reach for a wider vocabulary. Reported alongside,
// never merged into one "diversity score" — averaging two different questions
// produces a number that answers neither.
func vocabularyRatio(answers []string) (float64, bool) {
	unique := map[string]bool{}
	total := 0
	for _, a := range answers {
		for _, w := range strings.Fields(a) {
			unique[w] = true
			total++
		}
	}
	if total == 0 {
		return 0, false
	}
	return float64(len(unique)) / float64(total), true
}
