package stats

import (
	"math"
	"testing"
)

func TestFisherMatchesKnownTables(t *testing.T) {
	// Reference values computed independently (SciPy's fisher_exact gives the
	// same numbers for these tables). The first case is this day's own result:
	// 6/10 against 9/10 — a difference that looks convincing and tests at 0.30.
	cases := []struct {
		name       string
		a, b, c, d int
		want       float64
	}{
		{"6/10 против 9/10", 6, 4, 9, 1, 0.303},
		{"6/10 против 5/10", 6, 4, 5, 5, 1.000},
		{"6/10 против 10/10", 6, 4, 10, 0, 0.087},
		{"18/30 против 27/30", 18, 12, 27, 3, 0.015},
		{"полное разделение 10/10 против 0/10", 10, 0, 0, 10, 0.000},
	}
	for _, c := range cases {
		got := FisherTwoSided(c.a, c.b, c.c, c.d)
		if math.Abs(got-c.want) > 0.002 {
			t.Errorf("%s: получено p=%.4f, ожидалось %.3f", c.name, got, c.want)
		}
	}
}

func TestFisherIsSymmetric(t *testing.T) {
	// Swapping the two methods must not change whether their difference counts.
	if a, b := FisherTwoSided(6, 4, 9, 1), FisherTwoSided(9, 1, 6, 4); math.Abs(a-b) > 1e-9 {
		t.Fatalf("тест несимметричен: %.6f против %.6f", a, b)
	}
}

func TestWilsonBracketsTheRate(t *testing.T) {
	cases := []struct{ s, n int }{{6, 10}, {9, 10}, {10, 10}, {0, 10}, {18, 30}}
	for _, c := range cases {
		lo, hi := Wilson(c.s, c.n)
		p := float64(c.s) / float64(c.n)
		if lo < 0 || hi > 1 {
			t.Errorf("%d/%d: интервал [%.3f, %.3f] выходит за [0,1]", c.s, c.n, lo, hi)
		}
		if p < lo-1e-9 || p > hi+1e-9 {
			t.Errorf("%d/%d: доля %.2f вне своего интервала [%.3f, %.3f]", c.s, c.n, p, lo, hi)
		}
	}
	// The interval must narrow as the sample grows — that is the whole reason to
	// run more repeats.
	loSmall, hiSmall := Wilson(6, 10)
	loBig, hiBig := Wilson(18, 30)
	if (hiBig - loBig) >= (hiSmall - loSmall) {
		t.Fatalf("интервал не сузился: 10 прогонов %.3f, 30 прогонов %.3f",
			hiSmall-loSmall, hiBig-loBig)
	}
}

func TestWilsonHandlesAnEmptySample(t *testing.T) {
	if lo, hi := Wilson(0, 0); lo != 0 || hi != 0 {
		t.Fatalf("пустая выборка дала интервал [%.3f, %.3f]", lo, hi)
	}
}

// meanOf is the statistic used by the bootstrap tests: the plain mean of the
// resampled observations, which has a known answer to check against.
func meanOf(values []float64) func([]int) float64 {
	return func(sample []int) float64 {
		sum := 0.0
		for _, i := range sample {
			sum += values[i]
		}
		return sum / float64(len(sample))
	}
}

func TestBootstrapCollapsesWhenEveryObservationIsEqual(t *testing.T) {
	// Eight identical answers carry no spread, and an interval that suggested
	// otherwise would be inventing uncertainty.
	values := []float64{0.42, 0.42, 0.42, 0.42, 0.42, 0.42, 0.42, 0.42}
	lo, hi := BootstrapCI(len(values), 500, 1, meanOf(values))
	if math.Abs(lo-0.42) > 1e-9 || math.Abs(hi-0.42) > 1e-9 {
		t.Fatalf("интервал [%.6f, %.6f], ожидался [0.42, 0.42]", lo, hi)
	}
}

func TestBootstrapBracketsTheObservedValue(t *testing.T) {
	values := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0}
	point := meanOf(values)([]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	lo, hi := BootstrapCI(len(values), 2000, 7, meanOf(values))
	if point < lo || point > hi {
		t.Fatalf("наблюдённое значение %.3f вне интервала [%.3f, %.3f]", point, lo, hi)
	}
}

func TestBootstrapIsReproducibleForTheSameSeed(t *testing.T) {
	// A report prints this interval. If the same data gave a different interval
	// on a second run, nobody could check the number in a video afterwards.
	values := []float64{0.2, 0.5, 0.9, 0.1, 0.7}
	lo1, hi1 := BootstrapCI(len(values), 1000, 99, meanOf(values))
	lo2, hi2 := BootstrapCI(len(values), 1000, 99, meanOf(values))
	if lo1 != lo2 || hi1 != hi2 {
		t.Fatalf("одинаковый seed дал разные интервалы: [%.6f, %.6f] и [%.6f, %.6f]",
			lo1, hi1, lo2, hi2)
	}
	if lo3, _ := BootstrapCI(len(values), 1000, 100, meanOf(values)); lo3 == lo1 {
		t.Fatalf("другой seed дал ровно тот же нижний край %.6f — пересэмплирования нет", lo1)
	}
}

func TestBootstrapNarrowsWithMoreObservations(t *testing.T) {
	small := []float64{0.1, 0.5, 0.9}
	big := make([]float64, 0, 60)
	for i := 0; i < 20; i++ {
		big = append(big, 0.1, 0.5, 0.9)
	}
	loS, hiS := BootstrapCI(len(small), 3000, 3, meanOf(small))
	loB, hiB := BootstrapCI(len(big), 3000, 3, meanOf(big))
	if (hiB - loB) >= (hiS - loS) {
		t.Fatalf("интервал не сузился: 3 наблюдения %.4f, 60 наблюдений %.4f",
			hiS-loS, hiB-loB)
	}
}

func TestBootstrapDropsDegenerateResamplesInsteadOfScoringThemZero(t *testing.T) {
	// A pairwise statistic is undefined when a resample happens to draw the same
	// answer n times. Counting that as 0 would pull the interval towards a value
	// that was never measured, so those resamples are dropped.
	// Three observations, not eight: a degenerate draw has to be common enough
	// to reach the 2.5th percentile, or the check would pass on luck. With
	// n = 3 one resample in nine is degenerate, well past that edge.
	values := []float64{0.8, 0.8, 0.8}
	statistic := func(sample []int) float64 {
		distinct := map[int]bool{}
		for _, i := range sample {
			distinct[i] = true
		}
		if len(distinct) < 2 {
			return math.NaN()
		}
		return meanOf(values)(sample)
	}
	lo, hi := BootstrapCI(len(values), 2000, 5, statistic)
	// NaN fails every comparison, so a bare range check here would pass no
	// matter what the function did with the degenerate resamples. Reject the
	// non-numbers first, then the range.
	if math.IsNaN(lo) || math.IsNaN(hi) {
		t.Fatalf("вырожденное пересэмплирование дошло до интервала как NaN: [%v, %v]", lo, hi)
	}
	if lo < 0.79 || hi > 0.81 {
		t.Fatalf("вырожденные пересэмплирования просочились в интервал [%.4f, %.4f]", lo, hi)
	}
}

func TestBootstrapWidthMatchesTheAnalyticInterval(t *testing.T) {
	// Pins the interval to the 2.5 and 97.5 percentiles. Taking the extremes of
	// the resampled means instead would still look like an interval, still
	// bracket the estimate and still narrow with more data — this is the check
	// that tells the two apart, against a width computed independently here.
	const n = 100
	values := make([]float64, n)
	for i := range values {
		values[i] = (float64(i) + 0.5) / n
	}
	mean, variance := 0.0, 0.0
	for _, v := range values {
		mean += v
	}
	mean /= n
	for _, v := range values {
		variance += (v - mean) * (v - mean)
	}
	want := 2 * 1.959964 * math.Sqrt(variance/n) / math.Sqrt(n)

	lo, hi := BootstrapCI(n, 20000, 11, meanOf(values))
	if got := hi - lo; math.Abs(got-want) > 0.15*want {
		t.Fatalf("ширина интервала %.4f, аналитическая оценка %.4f — расхождение больше 15%%", got, want)
	}
}

func TestBootstrapRefusesAnImpossibleRequest(t *testing.T) {
	values := []float64{0.5, 0.6}
	cases := []struct {
		name         string
		n, resamples int
		statistic    func([]int) float64
	}{
		{"нет наблюдений", 0, 1000, meanOf(values)},
		{"одно пересэмплирование", 2, 1, meanOf(values)},
		{"нет статистики", 2, 1000, nil},
	}
	for _, c := range cases {
		if lo, hi := BootstrapCI(c.n, c.resamples, 1, c.statistic); lo != 0 || hi != 0 {
			t.Errorf("%s: получен интервал [%.4f, %.4f], ожидался нулевой", c.name, lo, hi)
		}
	}
}

func TestWilsonBoundsMatchIndependentlyComputedValues(t *testing.T) {
	// Pins the 95% level itself. Without this, dropping z to 1.645 — a 90%
	// interval printed under a "95%" heading — passes every other test here:
	// the rate still sits inside, the bounds stay in [0,1], and more runs still
	// narrow it.
	//
	// The expected values were recomputed from the formula in Python with
	// 30-digit decimal arithmetic, not copied from this implementation:
	//
	//   z = Decimal("1.959964"); p = s/n; den = 1 + z*z/n
	//   centre = (p + z*z/(2*n)) / den
	//   half   = z * sqrt(p*(1-p)/n + z*z/(4*n*n)) / den
	//
	// The counts are the ones this challenge actually printed: 18/30 and 11/30
	// are day 4's counting task at temperature 0 and 1.2, 2/30 is its lowest
	// diversity cell, and 0/30 and 30/30 are the ends where the textbook normal
	// approximation would put a bound outside [0, 1].
	cases := []struct {
		s, n           int
		wantLo, wantHi float64
	}{
		{18, 30, 0.4232, 0.7541},
		{11, 30, 0.2187, 0.5449},
		{2, 30, 0.0185, 0.2132},
		{30, 30, 0.8865, 1.0000},
		{0, 30, 0.0000, 0.1135},
	}
	for _, c := range cases {
		lo, hi := Wilson(c.s, c.n)
		if math.Abs(lo-c.wantLo) > 0.0001 || math.Abs(hi-c.wantHi) > 0.0001 {
			t.Errorf("%d/%d: получено [%.4f, %.4f], ожидалось [%.4f, %.4f]",
				c.s, c.n, lo, hi, c.wantLo, c.wantHi)
		}
	}
}

func TestHolmIsStricterThanAFlatThresholdAndOrderPreserving(t *testing.T) {
	// Four comparisons. 0.01 passes 0.05/4 = 0.0125; 0.02 would pass a flat 0.05
	// and does not pass 0.05/3 = 0.0167, so the step-down stops there and the two
	// larger ones fail with it — which is the whole point of the correction.
	p := []float64{0.20, 0.01, 0.04, 0.02}
	got := Holm(p, 0.05)
	want := []bool{false, true, false, false}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("p=%.3f (позиция %d): %v, ожидалось %v", p[i], i, got[i], want[i])
		}
	}
}

func TestHolmAcceptsEveryComparisonWhenAllAreOverwhelming(t *testing.T) {
	p := []float64{0.0001, 0.0002, 0.0003}
	for i, ok := range Holm(p, 0.05) {
		if !ok {
			t.Errorf("p=%g не прошёл, хотя мал даже для alpha/m", p[i])
		}
	}
}

func TestHolmOnASingleComparisonIsJustTheThreshold(t *testing.T) {
	if got := Holm([]float64{0.049}, 0.05); !got[0] {
		t.Error("единственное сравнение с p=0.049 должно проходить при alpha=0.05")
	}
	if got := Holm([]float64{0.051}, 0.05); got[0] {
		t.Error("единственное сравнение с p=0.051 не должно проходить")
	}
}

func TestHolmHandlesTiesAndEmptyInput(t *testing.T) {
	// Ties must not let two comparisons share one slot: with m=2 and both at 0.03,
	// the first is tested against 0.025 and fails, so neither survives.
	for i, ok := range Holm([]float64{0.03, 0.03}, 0.05) {
		if ok {
			t.Errorf("позиция %d: связка p=0.03 при m=2 не должна проходить", i)
		}
	}
	if got := Holm(nil, 0.05); len(got) != 0 {
		t.Errorf("для пустого входа вернулось %d значений", len(got))
	}
}

func TestHolmAcceptsWhatBonferroniWouldReject(t *testing.T) {
	// Every earlier Holm test here passes with the threshold mutated to a flat
	// alpha/m, which is Bonferroni — so the suite could not tell the two apart while
	// the report claims to apply Holm.
	//
	// With m = 2 and alpha = 0.05, Bonferroni tests both against 0.025 and rejects
	// 0.03. Holm tests the smaller against 0.05/2 = 0.025 and the larger against
	// 0.05/1 = 0.05, so both survive. That gap is the whole reason to prefer it.
	got := Holm([]float64{0.02, 0.03}, 0.05)
	if !got[0] || !got[1] {
		t.Fatalf("Holm отверг то, что должен принять: %v", got)
	}
	for i, p := range []float64{0.02, 0.03} {
		if p > 0.05/2 && got[i] == false {
			t.Errorf("p=%.3f отвергнут по плоскому порогу alpha/m — это Бонферрони, не Holm", p)
		}
	}
}
