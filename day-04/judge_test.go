package main

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestSlateHidesWhichTemperatureProducedWhat(t *testing.T) {
	// A judge told "this one came from temperature 1.2" would be scoring the
	// label. The slate is the only thing it sees, so the label must not be in it.
	perSetting := [][]string{
		{"депо кофе", "депо кофе"},
		{"рельсы и кофе", "верхнее кольцо"},
	}
	board := buildSlate(perSetting, 1)
	prompt := board.prompt()
	for _, forbidden := range []string{"temperature", "температур", "0.7", "1.2", "настройк"} {
		if strings.Contains(strings.ToLower(prompt), forbidden) {
			t.Errorf("в промпте судьи есть %q:\n%s", forbidden, prompt)
		}
	}
	for _, want := range []string{"депо кофе", "рельсы и кофе", "верхнее кольцо"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("вариант %q не попал в промпт судьи:\n%s", want, prompt)
		}
	}
}

func TestSlateDeduplicatesSoJudgeNoiseCannotBecomeADifference(t *testing.T) {
	// The same answer from two settings must get one score. Otherwise the
	// judge's own variation between two identical items would show up as a
	// difference between temperatures.
	perSetting := [][]string{
		{"депо кофе", "депо кофе", "рельсы и кофе"},
		{"депо кофе", "рельсы и кофе"},
	}
	board := buildSlate(perSetting, 1)
	if len(board.answers) != 2 {
		t.Fatalf("на слейте %d вариантов, ожидалось 2: %v", len(board.answers), board.answers)
	}
	if len(board.index) != 2 {
		t.Fatalf("индекс содержит %d записей, ожидалось 2", len(board.index))
	}
	for _, a := range board.answers {
		if _, ok := board.index[a]; !ok {
			t.Errorf("вариант %q не индексирован", a)
		}
	}
}

func TestSlateIsReproducibleForTheSameSeedAndShuffledAtAll(t *testing.T) {
	// The report prints the slate size and the scores read off it; a slate that
	// reshuffled on every run could not be checked afterwards.
	perSetting := [][]string{{"а", "б", "в", "г", "д", "е", "ж", "з"}}
	first := buildSlate(perSetting, 7).answers
	second := buildSlate(perSetting, 7).answers
	if strings.Join(first, "|") != strings.Join(second, "|") {
		t.Fatalf("один seed дал разные слейты:\n%v\n%v", first, second)
	}
	if strings.Join(first, "|") == "а|б|в|г|д|е|ж|з" {
		t.Error("слейт не перемешан: порядок совпал с исходным")
	}
}

func TestSlateDependsOnlyOnTheSetOfAnswers(t *testing.T) {
	// This is what the sort before the shuffle buys. Collected in encounter
	// order, the slate would also depend on which setting answered first, and
	// a rerun with -temps "1.2,0" would give the judge a different slate for
	// the same answers — making the two runs incomparable.
	forward := buildSlate([][]string{{"депо кофе", "рельсы и кофе"}, {"верхнее кольцо"}}, 3)
	reversed := buildSlate([][]string{{"верхнее кольцо"}, {"рельсы и кофе", "депо кофе"}}, 3)
	if strings.Join(forward.answers, "|") != strings.Join(reversed.answers, "|") {
		t.Fatalf("порядок настроек изменил слейт:\n%v\n%v", forward.answers, reversed.answers)
	}
}

func TestParseScoresLeavesGapsInsteadOfFillingThem(t *testing.T) {
	// An unscored variant must stay unscored: filling it with a default would
	// average over a different set than the one presented.
	reply := "1: 4\n2: 2\n4: 5\n"
	got := parseScores(reply, 4)
	if len(got) != 3 {
		t.Fatalf("разобрано %d оценок, ожидалось 3: %v", len(got), got)
	}
	if _, present := got[2]; present {
		t.Error("для третьего варианта появилась оценка, которой судья не давал")
	}
	if got[0] != 4 || got[1] != 2 || got[3] != 5 {
		t.Errorf("оценки разобраны неверно: %v", got)
	}
}

func TestParseScoresSurvivesFormattingAndRejectsNonsense(t *testing.T) {
	reply := "**1:** 5\n2. 3\n3) 1\n7: 4\n4: 9\n5: не знаю\n"
	got := parseScores(reply, 5)
	if got[0] != 5 || got[1] != 3 || got[2] != 1 {
		t.Errorf("оформленные оценки разобраны неверно: %v", got)
	}
	// Item 7 does not exist on a slate of 5, score 9 is outside the 1-5 scale,
	// and "не знаю" is not a score. None may reach the average.
	for _, absent := range []int{3, 4, 6} {
		if _, present := got[absent]; present {
			t.Errorf("мусорная оценка попала в результат для позиции %d: %v", absent, got)
		}
	}
}

func TestMeanScoreCountsRunsNotDistinctAnswers(t *testing.T) {
	// A setting that repeats one dull name thirty times is not as creative as
	// one that found it once among thirty. Averaging over distinct answers
	// would erase that difference entirely.
	board := buildSlate([][]string{{"скучное", "яркое"}}, 1)
	scores := map[int]int{board.index["скучное"]: 1, board.index["яркое"]: 5}

	repetitive := []string{"скучное", "скучное", "скучное", "яркое"}
	varied := []string{"скучное", "яркое", "яркое", "яркое"}

	meanA, coveredA := meanScore(repetitive, board, scores)
	meanB, coveredB := meanScore(varied, board, scores)
	if coveredA != 4 || coveredB != 4 {
		t.Fatalf("охват %d и %d, ожидалось по 4", coveredA, coveredB)
	}
	if math.Abs(meanA-2.0) > 1e-9 {
		t.Errorf("повторяющаяся выборка: %.3f, ожидалось 2.0", meanA)
	}
	if math.Abs(meanB-4.0) > 1e-9 {
		t.Errorf("разнообразная выборка: %.3f, ожидалось 4.0", meanB)
	}
}

func TestMeanScoreReportsPartialCoverageInsteadOfHidingIt(t *testing.T) {
	board := buildSlate([][]string{{"один", "два"}}, 1)
	scores := map[int]int{board.index["один"]: 3} // судья пропустил "два"
	mean, covered := meanScore([]string{"один", "два", "два"}, board, scores)
	if covered != 1 {
		t.Fatalf("охват %d, ожидался 1: две трети прогонов без оценки", covered)
	}
	if math.Abs(mean-3.0) > 1e-9 {
		t.Errorf("средняя %.3f, ожидалось 3.0 по единственной оценке", mean)
	}
	if mean, covered := meanScore([]string{"три"}, board, map[int]int{}); covered != 0 || mean != 0 {
		t.Errorf("без оценок получено %.3f при охвате %d, ожидались нули", mean, covered)
	}
}

func TestJudgeRunsAtTemperatureZeroWithThinkingDisabled(t *testing.T) {
	// The judge is the instrument. An instrument that drifts cannot measure
	// drift, and one that reasons internally would ignore its own temperature.
	c := &captured{replies: []string{"1: 4\n2: 2"}}
	client := fakeDeepSeek(t, c)
	board := buildSlate([][]string{{"депо кофе", "рельсы и кофе"}}, 1)
	runs := runJudge(context.Background(), client, board, 2)
	if len(runs) != 2 {
		t.Fatalf("проходов судьи %d, ожидалось 2", len(runs))
	}
	if c.bodyCount() != 2 {
		t.Fatalf("вызовов %d, ожидалось 2", c.bodyCount())
	}
	for i := 0; i < c.bodyCount(); i++ {
		temp, present := c.body(i)["temperature"]
		if !present || temp.(float64) != 0 {
			t.Errorf("проход %d: temperature=%v (есть=%v), ожидался 0", i, temp, present)
		}
		thinking, _ := c.body(i)["thinking"].(map[string]any)
		if thinking == nil || thinking["type"] != "disabled" {
			t.Errorf("проход %d ушёл с thinking=%v, ожидался disabled", i, c.body(i)["thinking"])
		}
	}
	for i, r := range runs {
		if r.err != nil {
			t.Errorf("проход %d вернул ошибку: %v", i, r.err)
		}
		if len(r.scores) != 2 {
			t.Errorf("проход %d разобрал %d оценок, ожидалось 2", i, len(r.scores))
		}
	}
}

func TestJudgeReportsAFailedPassInsteadOfSkippingIt(t *testing.T) {
	c := &captured{replies: []string{"1: 4"}, statuses: []int{500}}
	client := fakeDeepSeek(t, c)
	board := buildSlate([][]string{{"депо кофе"}}, 1)
	runs := runJudge(context.Background(), client, board, 1)
	if len(runs) != 1 {
		t.Fatalf("проходов %d, ожидался 1", len(runs))
	}
	if runs[0].err == nil {
		t.Fatal("упавший проход судьи отмечен успешным")
	}
}
