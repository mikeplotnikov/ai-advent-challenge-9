// Package stats holds the arithmetic that keeps a comparison table from claiming
// more than its runs support. It lives outside any one day because day 3 learned
// the lesson the expensive way: its submitted conclusion called a one-run gap a
// difference, and the correction needed exactly these functions.
package stats

import (
	"math"
	"math/rand"
	"sort"
)

// Wilson returns a 95% confidence interval for a success rate. The Wilson score
// interval is used rather than the textbook normal approximation because the
// latter is badly wrong exactly where these days live — small samples and rates
// near 0 or 1, where it can even produce bounds outside [0, 1].
func Wilson(successes, n int) (float64, float64) {
	if n == 0 {
		return 0, 0
	}
	const z = 1.959964 // 95%
	p := float64(successes) / float64(n)
	fn := float64(n)
	den := 1 + z*z/fn
	centre := (p + z*z/(2*fn)) / den
	halfWidth := z * math.Sqrt(p*(1-p)/fn+z*z/(4*fn*fn)) / den
	return math.Max(0, centre-halfWidth), math.Min(1, centre+halfWidth)
}

// FisherTwoSided returns the p-value of Fisher's exact test on the 2x2 table
// [[a, b], [c, d]] — successes and failures of two settings. Exact rather than
// chi-square: with tens of runs the approximation is not trustworthy, and this
// number decides whether a difference gets reported as real.
func FisherTwoSided(a, b, c, d int) float64 {
	n := a + b + c + d
	if n == 0 {
		return 1
	}
	row1, row2, col1 := a+b, c+d, a+c

	prob := func(x int) float64 {
		return math.Exp(logChoose(row1, x) + logChoose(row2, col1-x) - logChoose(n, col1))
	}

	observed := prob(a)
	lo := max(0, col1-row2)
	hi := min(row1, col1)
	total := 0.0
	for x := lo; x <= hi; x++ {
		// Every table at least as extreme as the observed one, in either
		// direction — that is what makes the test two-sided.
		if p := prob(x); p <= observed*(1+1e-9) {
			total += p
		}
	}
	return math.Min(1, total)
}

func logChoose(n, k int) float64 {
	if k < 0 || k > n {
		return math.Inf(-1)
	}
	lg := func(x int) float64 { v, _ := math.Lgamma(float64(x) + 1); return v }
	return lg(n) - lg(k) - lg(n-k)
}

// BootstrapCI returns a 95% percentile interval for any statistic of a sample of
// n observations. Fisher and Wilson only speak about counts; a diversity score is
// a continuous number, and without an interval it would be reported as if the
// third decimal meant something.
//
// The statistic is handed indices rather than values, and is recomputed on every
// resample, because the interesting diversity metric is pairwise: the distances
// between answers are not independent observations, so resampling the distances
// would understate the spread. Resampling the answers and recomputing is the
// version that is actually correct.
//
// seed is explicit so a printed report can be reproduced. Passing the same seed
// and sample must give the same interval, or the numbers in a video could not be
// checked afterwards.
func BootstrapCI(n, resamples int, seed int64, statistic func(sample []int) float64) (float64, float64) {
	if n == 0 || resamples < 2 || statistic == nil {
		return 0, 0
	}
	rng := rand.New(rand.NewSource(seed))
	values := make([]float64, 0, resamples)
	sample := make([]int, n)
	for i := 0; i < resamples; i++ {
		for j := range sample {
			sample[j] = rng.Intn(n)
		}
		v := statistic(sample)
		if math.IsNaN(v) {
			// A statistic that cannot be computed on a resample is skipped
			// rather than scored zero: zero is a value, and a missing one is
			// not. Note what this does NOT cover — the pairwise distance of n
			// copies of one answer is a real 0, not a missing value, so those
			// resamples are folded in, and they are what puts the lower bound
			// at 0.000 wherever one answer dominates. See the caller.
			continue
		}
		values = append(values, v)
	}
	if len(values) < 2 {
		return 0, 0
	}
	sort.Float64s(values)
	return percentile(values, 0.025), percentile(values, 0.975)
}

// percentile reads a sorted slice at a fraction, interpolating between the two
// neighbouring observations.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// Holm reports, for each p-value given, whether it survives the Holm-Bonferroni
// step-down procedure at level alpha. The result is in the same order as the input.
//
// It exists because day 4 named this hole and did not close it: its report made
// six comparisons against a flat 0.05 threshold, and noted that with six of them
// one false "confirmed" is expected in roughly a quarter of such runs. Day 5 makes
// two dozen, where a flat threshold is not a rounding error but a promise the
// numbers cannot keep.
//
// Holm rather than plain Bonferroni because it is uniformly more powerful at the
// same guarantee: the smallest p-value is tested against alpha/m, the next against
// alpha/(m-1), and so on, stopping at the first failure — everything from there
// down is rejected too, which is what makes the family-wide error rate hold.
func Holm(p []float64, alpha float64) []bool {
	type indexed struct {
		p float64
		i int
	}
	order := make([]indexed, len(p))
	for i, v := range p {
		order[i] = indexed{p: v, i: i}
	}
	sort.Slice(order, func(a, b int) bool { return order[a].p < order[b].p })

	survives := make([]bool, len(p))
	m := len(p)
	for rank, item := range order {
		threshold := alpha / float64(m-rank)
		if item.p > threshold {
			// Step-down: once one fails, every larger p-value fails with it.
			break
		}
		survives[item.i] = true
	}
	return survives
}
