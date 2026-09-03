package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestLetterOracleCountsRunesNotBytes(t *testing.T) {
	// Cyrillic is two bytes per letter, so a byte index would answer a
	// different question and the whole class would be scored against a wrong
	// truth. "делопроизводство" has 16 letters; the 7th from the end is "в".
	got, ok := letterFromEnd("делопроизводство", 7)
	if !ok || got != "в" {
		t.Fatalf("получено %q (ok=%v), ожидалось \"в\"", got, ok)
	}
	// Independent count, spelled out rather than trusted to the same loop.
	letters := []rune("делопроизводство")
	if want := string(letters[len(letters)-7]); got != want {
		t.Fatalf("оракул разошёлся с прямым отсчётом: %q против %q", got, want)
	}
}

func TestLetterOracleRefusesAPositionOutsideTheWord(t *testing.T) {
	cases := []struct {
		word     string
		position int
	}{
		{"кот", 4},
		{"кот", 0},
		{"кот", -1},
		{"", 1},
	}
	for _, c := range cases {
		if got, ok := letterFromEnd(c.word, c.position); ok {
			t.Errorf("%q позиция %d: вернулось %q, ожидался отказ", c.word, c.position, got)
		}
	}
}

func TestCountingTruthMatchesAnIndependentOracle(t *testing.T) {
	// Counted here a second way — filtering the digits as a string rather than
	// with the arithmetic the task code uses.
	want := 0
	for n := 1; n <= 400; n++ {
		s := strconv.Itoa(n)
		sum := 0
		for _, ch := range s {
			sum += int(ch - '0')
		}
		if n%4 == 0 && sum%3 == 0 && !strings.ContainsRune(s, '8') {
			want++
		}
	}
	if got := len(countingTruthSet()); got != want {
		t.Fatalf("эталон %d, независимый подсчёт %d", got, want)
	}
	if want == 0 {
		t.Fatal("независимый подсчёт дал ноль — проверять нечего")
	}
}

func TestCountingTruthAppliesEveryConditionSeparately(t *testing.T) {
	set := map[int]bool{}
	for _, n := range countingTruthSet() {
		set[n] = true
	}
	// One number per rejected condition, so dropping any single check in the
	// loop breaks this test rather than shifting a total nobody reads.
	cases := []struct {
		n      int
		reason string
	}{
		{12, "делится на 4, сумма цифр 3 — обязано быть внутри"},
		{16, "сумма цифр 7 не делится на 3 — обязано быть снаружи"},
		{18, "не делится на 4 — обязано быть снаружи"},
		{48, "содержит цифру 8 — обязано быть снаружи"},
		{408, "содержит 8 и вне диапазона — обязано быть снаружи"},
	}
	for _, c := range cases {
		want := c.n == 12
		if set[c.n] != want {
			t.Errorf("%d: в эталоне=%v, ожидалось %v (%s)", c.n, set[c.n], want, c.reason)
		}
	}
}

func TestTaskWordingIsBuiltFromTheConstantsTheTruthUses(t *testing.T) {
	// Raise a bound in the text and forget it in the loop, and the model would
	// be scored against a different problem than the one it was asked.
	var counting, letter task
	for _, tk := range tasks() {
		switch tk.key {
		case "T1":
			counting = tk
		case "T3":
			letter = tk
		}
	}
	for _, want := range []string{strconv.Itoa(rangeEnd), strconv.Itoa(divisibleBy), strconv.Itoa(digitSumBy)} {
		if !strings.Contains(counting.prompt, want) {
			t.Errorf("в тексте счётной задачи нет числа %s:\n%s", want, counting.prompt)
		}
	}
	if !strings.ContainsRune(counting.prompt, forbiddenDigit) {
		t.Errorf("в тексте счётной задачи нет запрещённой цифры:\n%s", counting.prompt)
	}
	if counting.truth != strconv.Itoa(len(countingTruthSet())) {
		t.Errorf("эталон счётной задачи %q не равен перебору %d", counting.truth, len(countingTruthSet()))
	}
	if !strings.Contains(letter.prompt, letterWord) || !strings.Contains(letter.prompt, "седьмом") {
		t.Errorf("текст буквенной задачи не соответствует константам:\n%s", letter.prompt)
	}
	if want, _ := letterFromEnd(letterWord, letterPosition); letter.truth != want {
		t.Errorf("эталон буквенной задачи %q, оракул даёт %q", letter.truth, want)
	}
}

func TestOpenTaskCarriesNoTruthAndNoAccuracy(t *testing.T) {
	for _, tk := range tasks() {
		if tk.kind == open && tk.truth != "" {
			t.Errorf("%s объявлен без эталона, но эталон задан: %q", tk.key, tk.truth)
		}
		if tk.kind == exact && tk.truth == "" {
			t.Errorf("%s объявлен с эталоном, но эталон пуст", tk.key)
		}
	}
}

func TestExactTasksAskForTheAnswerLineAndOpenOneDoesNot(t *testing.T) {
	// Where the answer is written down says nothing about temperature, so it is
	// identical in both exact classes — and absent in the open one, where the
	// whole reply is the answer.
	for _, tk := range tasks() {
		hasRule := strings.Contains(tk.system, "ANSWER")
		if (tk.kind == exact) != hasRule {
			t.Errorf("%s: kind=%v, а правило вывода %v", tk.key, tk.kind, hasRule)
		}
	}
}

func TestParseAnswerSurvivesMarkdownAndTakesTheLastLine(t *testing.T) {
	numbers := []struct {
		text string
		want string
		ok   bool
	}{
		{"ANSWER: 24", "24", true},
		{"**ANSWER:** 24", "24", true},
		{"ANSWER: **24**", "24", true},
		{"answer:24", "24", true},
		{"ANSWER: 25\nПересчитал.\nANSWER: 24", "24", true},
		{"Ответ где-то тут", "", false},
	}
	for _, c := range numbers {
		got, ok := parseNumber(c.text)
		if ok != c.ok || got != c.want {
			t.Errorf("parseNumber(%q) = %q,%v; ожидалось %q,%v", c.text, got, ok, c.want, c.ok)
		}
	}
	letters := []struct {
		text string
		want string
		ok   bool
	}{
		{"ANSWER: в", "в", true},
		{"**ANSWER:** «В»", "в", true},
		{"ANSWER: з\nОй, нет.\nANSWER: в", "в", true},
		{"ANSWER: 7", "", false},
		{"ANSWER: v", "", false},
	}
	for _, c := range letters {
		got, ok := parseLetter(c.text)
		if ok != c.ok || got != c.want {
			t.Errorf("parseLetter(%q) = %q,%v; ожидалось %q,%v", c.text, got, ok, c.want, c.ok)
		}
	}
}

func TestOpenAnswerNormalisationDropsFormattingButNotWords(t *testing.T) {
	// Counting "Депо Кофе" and "депо кофе." as two variants would inflate
	// diversity with punctuation, which is not a creative choice.
	same := []string{"Депо Кофе", "депо кофе.", "  «Депо   Кофе»  ", "ДЕПО КОФЕ!"}
	first, ok := parseWholeAnswer(same[0])
	if !ok {
		t.Fatal("первый вариант не разобран")
	}
	for _, s := range same[1:] {
		got, ok := parseWholeAnswer(s)
		if !ok || got != first {
			t.Errorf("parseWholeAnswer(%q) = %q, ожидалось %q", s, got, first)
		}
	}
	if got, _ := parseWholeAnswer("Рельсы и Кофе"); got == first {
		t.Error("разные названия свелись к одному — нормализация съела слова")
	}
	if _, ok := parseWholeAnswer("   \n  "); ok {
		t.Error("пустой ответ признан ответом")
	}
}

func TestPickTasksKeepsOrderAndRejectsUnknown(t *testing.T) {
	all := tasks()
	got, err := pickTasks(all, "t3,t1")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if len(got) != 2 || got[0].key != "T1" || got[1].key != "T3" {
		t.Fatalf("порядок объявления не сохранён: %v", keysOf(got))
	}
	if _, err := pickTasks(all, "T1,T9"); err == nil {
		t.Fatal("неизвестный класс задачи принят")
	}
	if got, _ := pickTasks(all, ""); len(got) != len(all) {
		t.Fatalf("без фильтра вернулось %d из %d", len(got), len(all))
	}
}

func keysOf(ts []task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.key
	}
	return out
}
