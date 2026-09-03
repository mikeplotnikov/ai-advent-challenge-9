package main

import "math"

// A hit rate is an estimate, not a verdict. Ten runs that split 6 against 9 look
// like a difference and are not one: the same coin lands that way often enough.
// These two functions are what keeps the comparison table from claiming more
// than the runs support.

// wilson returns a 95% confidence interval for a success rate. The Wilson score
// interval is used rather than the textbook normal approximation because the
// latter is badly wrong exactly where this day lives — small samples and rates
// near 0 or 1, where it can even produce bounds outside [0, 1].
func wilson(successes, n int) (float64, float64) {
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

// fisherTwoSided returns the p-value of Fisher's exact test on the 2x2 table
// [[a, b], [c, d]] — successes and failures of two methods. Exact rather than
// chi-square: with ten runs per method the approximation is not trustworthy,
// and this number decides whether a difference gets reported as real.
func fisherTwoSided(a, b, c, d int) float64 {
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
