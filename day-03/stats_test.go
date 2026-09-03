package main

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
		got := fisherTwoSided(c.a, c.b, c.c, c.d)
		if math.Abs(got-c.want) > 0.002 {
			t.Errorf("%s: получено p=%.4f, ожидалось %.3f", c.name, got, c.want)
		}
	}
}

func TestFisherIsSymmetric(t *testing.T) {
	// Swapping the two methods must not change whether their difference counts.
	if a, b := fisherTwoSided(6, 4, 9, 1), fisherTwoSided(9, 1, 6, 4); math.Abs(a-b) > 1e-9 {
		t.Fatalf("тест несимметричен: %.6f против %.6f", a, b)
	}
}

func TestWilsonBracketsTheRate(t *testing.T) {
	cases := []struct{ s, n int }{{6, 10}, {9, 10}, {10, 10}, {0, 10}, {18, 30}}
	for _, c := range cases {
		lo, hi := wilson(c.s, c.n)
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
	loSmall, hiSmall := wilson(6, 10)
	loBig, hiBig := wilson(18, 30)
	if (hiBig - loBig) >= (hiSmall - loSmall) {
		t.Fatalf("интервал не сузился: 10 прогонов %.3f, 30 прогонов %.3f",
			hiSmall-loSmall, hiBig-loBig)
	}
}

func TestWilsonHandlesAnEmptySample(t *testing.T) {
	if lo, hi := wilson(0, 0); lo != 0 || hi != 0 {
		t.Fatalf("пустая выборка дала интервал [%.3f, %.3f]", lo, hi)
	}
}
