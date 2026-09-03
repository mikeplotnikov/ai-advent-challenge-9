package main

import (
	"math"
	"testing"
)

func TestDistinctAndLargestRepeatOnAKnownSample(t *testing.T) {
	// Values taken from the live calibration on 2026-09-03, temperature 0:
	// five identical answers and three of another. That sample is the evidence
	// that temperature 0 is not deterministic, so the two functions that report
	// it are checked against it directly.
	sample := []string{
		"депо кофе", "депо кофе", "депо кофе", "депо кофе", "депо кофе",
		"рельсы и кофе", "рельсы и кофе", "рельсы и кофе",
	}
	if got := distinct(sample); got != 2 {
		t.Errorf("различных %d, ожидалось 2", got)
	}
	if got := largestRepeat(sample); got != 5 {
		t.Errorf("крупнейшая группа %d, ожидалось 5", got)
	}
}

func TestLargestRepeatIsOneWhenNothingRepeats(t *testing.T) {
	if got := largestRepeat([]string{"а", "б", "в"}); got != 1 {
		t.Fatalf("получено %d, ожидалась 1: ни один ответ не повторился", got)
	}
}

func TestPairwiseDistanceIsZeroForIdenticalAnswers(t *testing.T) {
	d, ok := meanPairwiseDistance([]string{"депо кофе", "депо кофе", "депо кофе"})
	if !ok || d != 0 {
		t.Fatalf("получено %.4f (ok=%v), ожидался ноль", d, ok)
	}
}

func TestPairwiseDistanceMatchesAHandComputedValue(t *testing.T) {
	// Three answers, three pairs, distances worked out by hand:
	//   "кот" vs "код":  замена т→д               = 1 правка  / 3 рун = 0.3333
	//   "кот" vs "лось": к→л, т→с, вставка ь      = 3 правки / 4 руны = 0.7500
	//   "код" vs "лось": к→л, д→с, вставка ь      = 3 правки / 4 руны = 0.7500
	// среднее = (0.3333 + 0.75 + 0.75) / 3 = 0.6111
	got, ok := meanPairwiseDistance([]string{"кот", "код", "лось"})
	want := (1.0/3 + 0.75 + 0.75) / 3
	if !ok || math.Abs(got-want) > 1e-9 {
		t.Fatalf("получено %.6f (ok=%v), ожидалось %.6f", got, ok, want)
	}
}

func TestPairwiseDistanceSaysWhenThereIsNoPair(t *testing.T) {
	// Returning 0 here would claim one answer has no diversity, when the truth
	// is that the question does not apply.
	for _, sample := range [][]string{nil, {"один"}} {
		if got, ok := meanPairwiseDistance(sample); ok {
			t.Errorf("для %d ответов вернулось %.4f, ожидался отказ", len(sample), got)
		}
	}
}

func TestNormalisedDistanceIsOneWhenNothingMatches(t *testing.T) {
	if got := normalisedDistance("абв", "гдж"); math.Abs(got-1) > 1e-9 {
		t.Fatalf("получено %.4f, ожидалась 1", got)
	}
	if got := normalisedDistance("", ""); got != 0 {
		t.Fatalf("две пустые строки дали %.4f, ожидался ноль", got)
	}
	// Different lengths must stay comparable: "кот" inside "коты" is one
	// insertion out of four runes.
	if got := normalisedDistance("кот", "коты"); math.Abs(got-0.25) > 1e-9 {
		t.Fatalf("получено %.4f, ожидалось 0.25", got)
	}
}

func TestLevenshteinCountsRunesNotBytes(t *testing.T) {
	// Byte-wise, one Cyrillic substitution costs two edits, and every distance
	// in the report would be inflated.
	if got := levenshtein([]rune("кот"), []rune("код")); got != 1 {
		t.Fatalf("получено %d правок, ожидалась 1", got)
	}
}

func TestVocabularyRatioCountsWordsAcrossAllAnswers(t *testing.T) {
	// Six words in total, four of them unique.
	ratio, ok := vocabularyRatio([]string{"депо кофе", "депо аромата", "рельсы кофе"})
	if !ok || math.Abs(ratio-4.0/6.0) > 1e-9 {
		t.Fatalf("получено %.4f (ok=%v), ожидалось %.4f", ratio, ok, 4.0/6.0)
	}
	if _, ok := vocabularyRatio([]string{"", "   "}); ok {
		t.Error("пустая выборка получила долю уникальных слов")
	}
}

func TestVocabularyRatioAndDistanceMoveIndependently(t *testing.T) {
	// The two metrics answer different questions, which is why they are printed
	// side by side instead of averaged into one "diversity score". These two
	// samples have the same pairwise distance profile and different vocabulary.
	repeatedWords := []string{"кофе кофе", "кофе кофе", "депо депо"}
	variedWords := []string{"кофе рельсы", "кофе рельсы", "депо трамвай"}
	dA, _ := meanPairwiseDistance(repeatedWords)
	dB, _ := meanPairwiseDistance(variedWords)
	vA, _ := vocabularyRatio(repeatedWords)
	vB, _ := vocabularyRatio(variedWords)
	if vA >= vB {
		t.Fatalf("доля уникальных слов не различила выборки: %.3f против %.3f", vA, vB)
	}
	if dA == 0 || dB == 0 {
		t.Fatalf("дистанции выродились: %.3f и %.3f", dA, dB)
	}
}
