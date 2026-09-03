package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// The comparison table is the deliverable of this day. These tests hold it to
// what it promises: every attempt in the denominator, a mark when a run never
// reached an answer, and no dollar figure for a model nobody priced.

func attemptWith(correct, parsed bool, err error, out int) attempt {
	a := attempt{correct: correct, parsed: parsed, err: err, calls: 1}
	a.usage = llm.Usage{PromptTokens: 100, CompletionTokens: out}
	return a
}

func summaryOf(t *testing.T, model string, attempts ...attempt) string {
	t.Helper()
	var buf bytes.Buffer
	summary(&buf, []outcome{{method: methods()[0], attempts: attempts}}, model, 24, len(attempts))
	return buf.String()
}

func TestSummaryCountsEveryAttemptInTheDenominator(t *testing.T) {
	// Dropping the failed run would print 3/3 — a perfect score for a method
	// that answered three times out of five.
	got := summaryOf(t, "deepseek-v4-pro",
		attemptWith(true, true, nil, 500),
		attemptWith(true, true, nil, 500),
		attemptWith(true, true, nil, 500),
		attemptWith(false, false, errors.New("модель отдала только рассуждение"), 8000),
		attemptWith(false, true, nil, 500),
	)
	if !strings.Contains(got, "3/5") {
		t.Fatalf("знаменатель не по всем прогонам:\n%s", got)
	}
}

func TestSummaryMarksRunsThatNeverReachedAnAnswer(t *testing.T) {
	// The footer defines "!" as "не дошла до ответа". A run whose call succeeded
	// but produced no ANSWER line is exactly that, so it must be marked too —
	// otherwise the same failure wears two different labels depending on method.
	failed := summaryOf(t, "deepseek-v4-pro",
		attemptWith(true, true, nil, 500),
		attemptWith(false, false, errors.New("запрос не прошёл"), 0))
	if !strings.Contains(failed, "1/2!") {
		t.Fatalf("прогон с ошибкой не помечен:\n%s", failed)
	}

	silent := summaryOf(t, "deepseek-v4-pro",
		attemptWith(true, true, nil, 500),
		attemptWith(false, false, nil, 8000)) // ответ пришёл, строки ANSWER в нём нет
	if !strings.Contains(silent, "1/2!") {
		t.Fatalf("прогон без строки ANSWER не помечен:\n%s", silent)
	}

	clean := summaryOf(t, "deepseek-v4-pro",
		attemptWith(true, true, nil, 500),
		attemptWith(false, true, nil, 500)) // ответ есть, просто неверный
	if strings.Contains(clean, "1/2!") {
		t.Fatalf("неверный ответ помечен как «не дошёл до ответа»:\n%s", clean)
	}
}

func TestSummaryRefusesToPriceAnUnknownModel(t *testing.T) {
	// $0.0000 reads as "free", not as "unpriced" — the exact misreading the
	// cost() contract exists to prevent.
	got := summaryOf(t, "deepseek-v9-unreleased", attemptWith(true, true, nil, 500))
	if strings.Contains(got, "$0.0000") {
		t.Fatalf("неизвестная модель показана бесплатной:\n%s", got)
	}
	if !strings.Contains(got, "цена неизвестна") {
		t.Fatalf("не сказано, что цена неизвестна:\n%s", got)
	}
}

func TestSummaryAveragesIncludeTheRunsThatFailed(t *testing.T) {
	// A run that spent 8000 tokens on reasoning and never answered was billed.
	// Averaging it away would understate exactly the method whose failure mode
	// it is.
	var buf bytes.Buffer
	attempts := []attempt{
		attemptWith(true, true, nil, 1000),
		attemptWith(false, false, errors.New("не дошла до ответа"), 7000),
	}
	summary(&buf, []outcome{{method: methods()[0], attempts: attempts}}, "deepseek-v4-pro", 24, 2)
	if !strings.Contains(buf.String(), fmt.Sprint((1000+7000)/2)) {
		t.Fatalf("средние посчитаны без оплаченного прогона:\n%s", buf.String())
	}
}

func TestSummaryReadsThePriceRatioAgainstThePlainMethod(t *testing.T) {
	// With -only C,D the first row is C. A ratio column headed "к A" that
	// silently compared everything to C would rank the methods wrongly.
	var buf bytes.Buffer
	all := methods()
	cheap := outcome{method: all[0], attempts: []attempt{attemptWith(true, true, nil, 1000)}}  // A
	pricey := outcome{method: all[4], attempts: []attempt{attemptWith(true, true, nil, 4000)}} // E
	summary(&buf, []outcome{pricey, cheap}, "deepseek-v4-pro", 24, 1)

	out := buf.String()
	if !strings.Contains(out, "к A") {
		t.Fatalf("база сравнения не A:\n%s", out)
	}
	// "E ·" now appears twice: once in the price table and once in the
	// significance table. Only the priced row carries the ratio.
	checked := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "E ·") && strings.Contains(line, "$") {
			checked = true
			if !strings.Contains(line, "×3.") {
				t.Fatalf("цена E посчитана не относительно A: %q", line)
			}
		}
	}
	if !checked {
		t.Fatal("строка E с ценой не найдена в сводной таблице")
	}
}

func TestReportCountsEveryAttemptToo(t *testing.T) {
	var buf bytes.Buffer
	report(&buf, methods()[0], []attempt{
		attemptWith(true, true, nil, 500),
		attemptWith(false, false, errors.New("сеть"), 0),
		attemptWith(false, true, nil, 500),
	}, "deepseek-v4-pro", false, false)
	if !strings.Contains(buf.String(), "верных: 1 из 3") {
		t.Fatalf("блок способа считает не все прогоны:\n%s", buf.String())
	}
}

func TestReportSaysWhenTheAnswerHitTheTokenCeiling(t *testing.T) {
	// A truncated answer is not the same as a bad answer, and the difference is
	// invisible unless it is printed.
	a := attemptWith(false, false, nil, 8000)
	a.finish = "length"
	var buf bytes.Buffer
	report(&buf, methods()[0], []attempt{a}, "deepseek-v4-pro", false, false)
	if !strings.Contains(buf.String(), "max_tokens") {
		t.Fatalf("обрыв по потолку токенов не назван:\n%s", buf.String())
	}
}

func TestRunMethodRunsEveryRepeatAndRespectsTheWorkerCap(t *testing.T) {
	// Losing the WaitGroup would return zero-value attempts — every method would
	// read 0/N as if the model had failed, when it was our own result that was
	// dropped. Losing the semaphore would fire every repeat at the API at once.
	c := &captured{replies: []string{"ANSWER: 24"}}
	client := fakeDeepSeek(t, c)

	attempts := runMethod(context.Background(), client, methods()[0], 24, 6, 2)
	if len(attempts) != 6 {
		t.Fatalf("получено %d прогонов вместо 6", len(attempts))
	}
	for i, a := range attempts {
		if a.err != nil || !a.parsed || !a.correct || a.calls != 1 {
			t.Fatalf("прогон %d не выполнен: %+v", i+1, a)
		}
	}
	c.mu.Lock()
	peak := c.peak
	c.mu.Unlock()
	if peak > 2 {
		t.Fatalf("одновременно в воздухе было %d запросов при лимите 2", peak)
	}
}

func TestExecuteReportsAFailedCallAsAnError(t *testing.T) {
	c := &captured{replies: []string{"неважно"}, statuses: []int{http.StatusTooManyRequests}}
	client := fakeDeepSeek(t, c)

	got := execute(context.Background(), client, methods()[0], 24)
	if got.err == nil {
		t.Fatal("отказ API не стал ошибкой прогона")
	}
	if got.correct {
		t.Fatal("прогон с ошибкой засчитан верным")
	}
}

func TestFailedFirstCallDoesNotPayForTheSecond(t *testing.T) {
	// The meta-prompt's second call is only meaningful with the prompt the first
	// one wrote. Going ahead anyway would send the prompt-writer instruction as
	// the solver's system message and bill for it.
	c := &captured{replies: []string{"неважно", "ANSWER: 24"}, statuses: []int{http.StatusInternalServerError}}
	client := fakeDeepSeek(t, c)

	var meta method
	for _, m := range methods() {
		if m.key == "C" {
			meta = m
		}
	}
	got := execute(context.Background(), client, meta, 24)
	if got.err == nil {
		t.Fatal("отказ первого вызова не стал ошибкой")
	}
	if got.calls != 1 {
		t.Fatalf("сделано %d вызовов после отказа первого", got.calls)
	}
	c.mu.Lock()
	made := len(c.bodies)
	c.mu.Unlock()
	if made != 1 {
		t.Fatalf("в API ушло %d запросов вместо одного", made)
	}
}

func TestEveryCallCarriesTheTaskItself(t *testing.T) {
	// The truth is computed for taskText. A method asked something else — or
	// nothing — would be graded against a problem it never saw.
	c := &captured{replies: []string{"Вот промпт.", "ANSWER: 24"}}
	client := fakeDeepSeek(t, c)

	var meta method
	for _, m := range methods() {
		if m.key == "C" {
			meta = m
		}
	}
	execute(context.Background(), client, meta, 24)

	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.bodies {
		messages, _ := c.bodies[i]["messages"].([]any)
		found := false
		for _, m := range messages {
			msg, _ := m.(map[string]any)
			if msg["role"] == "user" && strings.Contains(msg["content"].(string), taskText) {
				found = true
			}
		}
		if !found {
			t.Fatalf("вызов %d ушёл без условия задачи", i+1)
		}
	}
}

func TestFailedAttemptStillReportsWhatItSpent(t *testing.T) {
	// Reasoning that ate the whole budget was billed even though no answer came.
	c := &captured{replies: []string{""}, reasons: []string{"думал долго и не успел"}}
	client := fakeDeepSeek(t, c)

	got := execute(context.Background(), client, methods()[4], 24)
	if got.err == nil {
		t.Fatal("ответ без текста не стал ошибкой")
	}
	if got.usage.CompletionTokens == 0 {
		t.Fatal("оплаченные токены неудавшегося прогона потеряны")
	}
}

func summaryOfPair(t *testing.T, aCorrect, aTotal, xCorrect, xTotal int) string {
	t.Helper()
	build := func(m method, correct, total int) outcome {
		var attempts []attempt
		for i := 0; i < total; i++ {
			attempts = append(attempts, attemptWith(i < correct, true, nil, 500))
		}
		return outcome{method: m, attempts: attempts}
	}
	all := methods()
	var buf bytes.Buffer
	summary(&buf, []outcome{build(all[0], aCorrect, aTotal), build(all[1], xCorrect, xTotal)},
		"deepseek-v4-pro", 24, aTotal)
	return buf.String()
}

func TestSummaryRefusesToCallNoiseADifference(t *testing.T) {
	// This is the mistake the day 3 submission actually made: 6/10 against 5/10
	// was reported as "хуже", and Fisher's exact test on that pair gives p=1.0.
	out := summaryOfPair(t, 6, 10, 5, 10)
	if !strings.Contains(out, "ОТЛИЧИМА ЛИ РАЗНИЦА") {
		t.Fatalf("таблицы значимости нет вовсе:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "B ·") && strings.Contains(line, "1.000") {
			if !strings.Contains(line, "не отличить") {
				t.Fatalf("разница в один прогон объявлена реальной: %q", line)
			}
			return
		}
	}
	t.Fatalf("строка со сравнением не найдена:\n%s", out)
}

func TestSummaryConfirmsARealDifference(t *testing.T) {
	// The same 60% against 90%, but on thirty runs instead of ten: p=0.015.
	out := summaryOfPair(t, 18, 30, 27, 30)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "B ·") && strings.Contains(line, "0.015") {
			if !strings.Contains(line, "разница подтверждена") {
				t.Fatalf("подтверждённая разница названа шумом: %q", line)
			}
			return
		}
	}
	t.Fatalf("строка со сравнением не найдена:\n%s", out)
}

func TestSummaryShowsThatMoreRunsNarrowTheInterval(t *testing.T) {
	small := summaryOfPair(t, 6, 10, 6, 10)
	big := summaryOfPair(t, 18, 30, 18, 30)
	if !strings.Contains(small, "[31%, 83%]") {
		t.Fatalf("интервал для 6/10 не тот:\n%s", small)
	}
	if !strings.Contains(big, "[42%, 75%]") {
		t.Fatalf("интервал для 18/30 не тот:\n%s", big)
	}
}
