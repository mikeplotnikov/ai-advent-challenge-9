package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// The oracle is written out by hand rather than recomputed, because a test that
// repeats the implementation's own loop would agree with any bug it contains.
var oracle = []int{12, 24, 36, 60, 72, 96, 120, 132, 144, 156, 192, 204, 216, 240,
	252, 264, 276, 300, 312, 324, 336, 360, 372, 396}

func TestGroundTruthMatchesTheOracle(t *testing.T) {
	got := groundTruthSet()
	if len(got) != len(oracle) {
		t.Fatalf("эталон вернул %d чисел, ожидалось %d: %v", len(got), len(oracle), got)
	}
	for i := range oracle {
		if got[i] != oracle[i] {
			t.Fatalf("на позиции %d получено %d, ожидалось %d", i, got[i], oracle[i])
		}
	}
	if groundTruth() != 24 {
		t.Fatalf("groundTruth() = %d, ожидалось 24", groundTruth())
	}
}

func TestGroundTruthRejectsEachConditionSeparately(t *testing.T) {
	in := map[int]bool{}
	for _, n := range groundTruthSet() {
		in[n] = true
	}
	cases := []struct {
		n    int
		want bool
		why  string
	}{
		{12, true, "делится на 4, сумма 3, без восьмёрки"},
		{8, false, "содержит цифру 8"},
		{48, false, "содержит цифру 8, хотя делится на 4 и сумма 12"},
		{84, false, "содержит цифру 8"},
		{4, false, "сумма цифр 4 не делится на 3"},
		{6, false, "не делится на 4"},
		{400, false, "сумма цифр 4 не делится на 3"},
		{396, true, "верхняя граница диапазона, проходит все три условия"},
		{404, false, "вне диапазона 1..400"},
	}
	for _, c := range cases {
		if in[c.n] != c.want {
			t.Errorf("%d: получено %v, ожидалось %v — %s", c.n, in[c.n], c.want, c.why)
		}
	}
}

func TestTaskTextStatesTheProblemTheTruthIsComputedFor(t *testing.T) {
	// taskText and groundTruthSet are generated from the same constants, so they
	// cannot drift apart silently. This pins the wording itself: changing the
	// task is allowed, changing it by accident is not — every measured number in
	// work/week-1/day-3.md was taken on exactly this problem.
	const expected = "Сколько существует целых чисел от 1 до 400 включительно, " +
		"которые делятся на 4, сумма цифр которых делится на 3 " +
		"и в записи которых нет цифры 8?"
	if taskText != expected {
		t.Fatalf("формулировка задачи изменилась:\n%s", taskText)
	}
}

func TestParseAnswerTakesTheLastLine(t *testing.T) {
	// A step-by-step answer states intermediate results before committing, so
	// reading the first ANSWER would score the model on a number it abandoned.
	cases := []struct {
		name   string
		text   string
		want   int
		wantOK bool
	}{
		{"единственный ответ", "Считаю...\nANSWER: 24", 24, true},
		{"промежуточный и итоговый", "ANSWER: 30\nПроверяю ещё раз.\nANSWER: 24", 24, true},
		{"жирный markdown вокруг", "Итог.\n**ANSWER: 24**", 24, true},
		{"жирная метка", "**ANSWER:** 24", 24, true},
		{"жирное число", "ANSWER: **24**", 24, true},
		{"строчными буквами", "answer: 24", 24, true},
		{"текст после ответа", "ANSWER: 24\n\nПроверка: пересчитал дважды.", 24, true},
		{"лишние пробелы", "ANSWER:    24  ", 24, true},
		{"точка после числа", "ANSWER: 24.", 24, true},
		{"отрицательное число", "ANSWER: -3", -3, true},
		{"ответа нет вовсе", "Ответ: двадцать четыре", 0, false},
		{"слово без числа", "ANSWER: двадцать четыре", 0, false},
		{"пустой ответ", "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseAnswer(c.text)
			if ok != c.wantOK || got != c.want {
				t.Fatalf("получено (%d, %v), ожидалось (%d, %v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}

func TestPickKeepsDeclaredOrder(t *testing.T) {
	// The comparison table must always read A, B, C, D, E — the order of the
	// flag is the user's typing, not the order of the experiment.
	got, err := pick(methods(), "E, a ,C")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	var keys []string
	for _, m := range got {
		keys = append(keys, m.key)
	}
	if strings.Join(keys, "") != "ACE" {
		t.Fatalf("получен порядок %v, ожидался A, C, E", keys)
	}
}

func TestPickRejectsUnknownMethod(t *testing.T) {
	if _, err := pick(methods(), "A,Z"); err == nil {
		t.Fatal("неизвестный способ Z принят молча")
	}
}

func TestPickWithoutFilterReturnsEverything(t *testing.T) {
	if got, err := pick(methods(), "  "); err != nil || len(got) != len(methods()) {
		t.Fatalf("получено %d способов, ошибка %v", len(got), err)
	}
}

func TestAddUsageSumsBothCallsOfTheMetaPrompt(t *testing.T) {
	// The meta-prompt method pays twice. If only the second call were counted,
	// the cheapest-looking method in the table would be the one that costs most.
	var total llm.Usage
	first := llm.Usage{PromptTokens: 40, CompletionTokens: 300, TotalTokens: 340}
	first.CompletionDetails.ReasoningTokens = 30
	second := llm.Usage{PromptTokens: 250, CompletionTokens: 700, TotalTokens: 950}
	second.CompletionDetails.ReasoningTokens = 120

	total = addUsage(total, first)
	total = addUsage(total, second)

	if total.PromptTokens != 290 || total.CompletionTokens != 1000 || total.TotalTokens != 1290 {
		t.Fatalf("суммирование сломано: %+v", total)
	}
	if total.CompletionDetails.ReasoningTokens != 150 {
		t.Fatalf("токены рассуждения потеряны: %d", total.CompletionDetails.ReasoningTokens)
	}
}

func TestCostIsReportedOnlyForKnownModels(t *testing.T) {
	// Deliberately asymmetric: with equal counts the sum would be the same
	// whether the input and output rates were swapped, and output is the rate
	// that dominates — reasoning tokens are billed as output.
	u := llm.Usage{PromptTokens: 2_000_000, CompletionTokens: 1_000_000}
	got, known := cost("deepseek-v4-pro", u)
	want := 2*1.74 + 3.48 // 6.96
	if !known || got < want-0.01 || got > want+0.01 {
		t.Fatalf("цена pro = %.4f (known=%v), ожидалось ≈%.2f", got, known, want)
	}
	// An unknown model must not silently price at zero: a dollar column that
	// reads $0.0000 would look like a free method, not like a missing price.
	if _, known := cost("deepseek-v9-unreleased", u); known {
		t.Fatal("для неизвестной модели цена объявлена известной")
	}
}

func TestClipCountsRunesNotBytes(t *testing.T) {
	// Cyrillic costs two bytes per character, so cutting by bytes would keep
	// about half the characters asked for and would report a byte count under
	// the word "символов".
	long := strings.Repeat("рассуждение ", 200)
	got := clip(long, 100, false)

	if want := string([]rune(long)[:100]); !strings.HasPrefix(got, want) {
		t.Fatalf("сохранено %d символов вместо первых 100",
			utf8.RuneCountInString(strings.SplitN(got, "\n", 2)[0]))
	}
	if !strings.Contains(got, "ещё") {
		t.Fatal("обрезка не сказала, что текст усечён")
	}
	if kept := clip(long, 100, true); kept != long {
		t.Fatal("с -full текст должен остаться целым")
	}
}
