package agent

// The probes are the day's instrument, and an instrument that quietly writes the
// wrong number into a row produces a report that looks exactly like a correct one.
// That is not hypothetical here: the first version of rowOf recorded the estimate
// made before the ceiling trimmed the request, and the error it invented — 92% where
// the truth was 24% — was caught by reading the data, not by any test.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

func rowsFrom(t *testing.T, raw []byte) []TokenRow {
	t.Helper()
	var out []TokenRow
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r TokenRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("строка замера не разбирается: %v\n%s", err, line)
		}
		out = append(out, r)
	}
	return out
}

// answer builds a scripted reply with usage, so a row has something to record.
func answer(text string, prompt, completion, cached int) llm.Answer {
	return llm.Answer{
		Content: text,
		Model:   "deepseek-v4-flash",
		Usage: llm.Usage{
			PromptTokens:          prompt,
			CompletionTokens:      completion,
			PromptCacheHitTokens:  cached,
			PromptCacheMissTokens: prompt - cached,
		},
	}
}

func TestGrowthProbeWritesOneRowPerTurnWithRunningTotals(t *testing.T) {
	f := &fakeCaller{answers: []llm.Answer{
		answer("раз", 100, 10, 0),
		answer("два", 200, 20, 64),
		answer("три", 300, 30, 128),
	}}
	a := newAgent(t, Config{SystemPrompt: GrowthSystemPrompt, Model: "deepseek-v4-flash"}, f)

	var rows bytes.Buffer
	if err := RunGrowthProbe(a, "тест", 3, io.Discard, &rows); err != nil {
		t.Fatalf("RunGrowthProbe: %v", err)
	}

	got := rowsFrom(t, rows.Bytes())
	if len(got) != 3 {
		t.Fatalf("строк %d, ожидалось 3", len(got))
	}
	for i, r := range got {
		if r.Probe != "growth" || r.Run != "тест" || r.Turn != i+1 {
			t.Errorf("строка %d: %+v — не тот замер, прогон или ход", i+1, r)
		}
		if r.Outcome != "ok" {
			t.Errorf("ход %d: итог %q", r.Turn, r.Outcome)
		}
		if r.EstTotal == 0 || r.EstSystem == 0 || r.EstInput == 0 {
			t.Errorf("ход %d: раскладка оценки не записана: %+v", r.Turn, r)
		}
		if r.Question == "" || r.Answer == "" {
			t.Errorf("ход %d: вопрос или ответ не записан", r.Turn)
		}
	}
	// The running totals are the point of the growth run: the request weighs the
	// last row, the conversation has been billed the sum of all of them.
	if got[2].CumPromptTokens != 600 || got[2].CumCompletionTokens != 60 {
		t.Errorf("накопленные токены %d/%d, ожидалось 600/60",
			got[2].CumPromptTokens, got[2].CumCompletionTokens)
	}
	if !(got[0].CumCost < got[1].CumCost && got[1].CumCost < got[2].CumCost) {
		t.Errorf("накопленная цена не растёт: %v, %v, %v", got[0].CumCost, got[1].CumCost, got[2].CumCost)
	}
	if got[1].EstHistory == 0 {
		t.Error("со второго хода история обязана весить больше нуля")
	}
}

func TestGrowthProbeRefusesToRepeatAQuestion(t *testing.T) {
	a := newAgent(t, Config{}, &fakeCaller{})
	err := RunGrowthProbe(a, "тест", len(GrowthQuestions)+1, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("замер согласился повторить вопрос — это измеряло бы кэш, а не рост")
	}
}

func TestCeilingProbeRecordsARefusalAsARefusalAndNotAsAnError(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{
		SystemPrompt:     GrowthSystemPrompt,
		Model:            "deepseek-v4-flash",
		MaxContextTokens: 130, // fits turn 1 and nothing after it
		OnOverflow:       OverflowRefuse,
	}, f)

	var rows bytes.Buffer
	if err := RunCeilingProbe(a, "тест", 3, io.Discard, &rows); err != nil {
		t.Fatalf("RunCeilingProbe: %v", err)
	}
	got := rowsFrom(t, rows.Bytes())
	if len(got) != 3 {
		t.Fatalf("строк %d, ожидалось 3", len(got))
	}
	refused := 0
	for _, r := range got {
		if r.Policy != string(OverflowRefuse) || r.Limit != 130 {
			t.Errorf("ход %d: политика %q, потолок %d — не записаны условия замера", r.Turn, r.Policy, r.Limit)
		}
		if r.Outcome == "refused" {
			refused++
			if r.PromptTokens != 0 || r.Cost != 0 {
				t.Errorf("ход %d отвергнут агентом, но записан расход %d токенов и $%f",
					r.Turn, r.PromptTokens, r.Cost)
			}
			if !strings.Contains(r.Error, "потолок") {
				t.Errorf("ход %d: причина отказа не записана: %q", r.Turn, r.Error)
			}
		}
	}
	if refused == 0 {
		t.Fatal("ни одного отказа — потолок в замере не сработал, проверять нечего")
	}
	if f.calls != 3-refused {
		t.Errorf("вызовов модели %d при %d отказах — отказ обязан быть бесплатным", f.calls, refused)
	}
}

func TestCeilingProbeRecordsTheTrimmedRequestNotTheOneItPlanned(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: GrowthSystemPrompt, Model: "deepseek-v4-flash"}, f)
	// Two turns without a ceiling, then a ceiling that forces a trim on the third.
	for i := 0; i < 2; i++ {
		if _, err := a.Ask(context.Background(), GrowthQuestions[i]); err != nil {
			t.Fatalf("подготовка: %v", err)
		}
	}
	a.cfg.MaxContextTokens = a.Preflight(GrowthQuestions[2]).Total - 20
	a.cfg.OnOverflow = OverflowTrim

	var rows bytes.Buffer
	if err := RunCeilingProbe(a, "тест", 1, io.Discard, &rows); err != nil {
		t.Fatalf("RunCeilingProbe: %v", err)
	}
	got := rowsFrom(t, rows.Bytes())[0]
	if got.Dropped == 0 {
		t.Fatal("тест собран неверно: обрезки не было")
	}
	if got.EstTotal > got.Limit {
		t.Fatalf("в строке записана оценка %d при потолке %d — это вес запроса ДО обрезки, который никуда не уезжал",
			got.EstTotal, got.Limit)
	}
	if got.Warning == "" {
		t.Error("обрезка не попала в строку замера")
	}
}

func TestWindowProbeKeepsEachRungACleanConversation(t *testing.T) {
	f := &fakeCaller{
		answers: []llm.Answer{answer("первый", 500, 5, 0), {}},
		errs:    []error{nil, errors.New("API вернул 400 Bad Request: context length")},
	}
	a := newAgent(t, Config{SystemPrompt: "ты ассистент", Model: "deepseek-v4-flash"}, f)

	var rows bytes.Buffer
	if err := RunWindowProbe(a, "тест", []int{200, 400}, io.Discard, &rows); err != nil {
		t.Fatalf("RunWindowProbe: %v", err)
	}
	got := rowsFrom(t, rows.Bytes())
	if len(got) != 2 {
		t.Fatalf("ступеней записано %d, ожидалось 2", len(got))
	}
	if got[0].Outcome != "ok" || got[0].PromptTokens != 500 {
		t.Errorf("контрольная ступень: %+v", got[0])
	}
	if got[1].Outcome != "error" || !strings.Contains(got[1].Error, "400") {
		t.Errorf("отвергнутая ступень записана как %q, ошибка %q", got[1].Outcome, got[1].Error)
	}
	// The second rung must not carry the first one's exchange: each rung is its own
	// conversation, or the ladder measures the history rather than the request.
	second := f.sent[1]
	if len(second) != 2 {
		t.Fatalf("на второй ступени отправлено %d сообщений — предыдущая ступень осталась в контексте", len(second))
	}
	if got[1].Answer != "" {
		t.Errorf("у отвергнутой ступени записан ответ %q", got[1].Answer)
	}
}

func TestWindowProbeInsistsOnAControlRung(t *testing.T) {
	a := newAgent(t, Config{}, &fakeCaller{})
	if err := RunWindowProbe(a, "тест", []int{1000}, io.Discard, io.Discard); err == nil {
		t.Fatal("замер окна согласился на одну ступень — отказ без контроля ничего не доказывает")
	}
}

func TestOutputProbeRecordsWhetherTheAnswerStillParses(t *testing.T) {
	whole := &fakeCaller{answers: []llm.Answer{{
		Content: `{"city":"Саратов"}`, Model: "deepseek-v4-flash", FinishReason: "stop",
		Usage: llm.Usage{PromptTokens: 20, CompletionTokens: 12},
	}}}
	cut := &fakeCaller{answers: []llm.Answer{{
		Content: `{"city":"Сара`, Model: "deepseek-v4-flash", FinishReason: "length",
		Usage: llm.Usage{PromptTokens: 20, CompletionTokens: 8},
	}}}
	control := newAgent(t, Config{Model: "deepseek-v4-flash"}, whole)
	capped := newAgent(t, Config{Model: "deepseek-v4-flash"}, cut)

	var rows bytes.Buffer
	if err := RunOutputProbe(control, capped, "тест", "верни JSON", io.Discard, &rows); err != nil {
		t.Fatalf("RunOutputProbe: %v", err)
	}
	got := rowsFrom(t, rows.Bytes())
	if len(got) != 2 {
		t.Fatalf("строк %d, ожидалось 2", len(got))
	}
	if got[0].ValidJSON == nil || !*got[0].ValidJSON || got[0].Truncated {
		t.Errorf("контрольный ответ записан как оборванный или неразбираемый: %+v", got[0])
	}
	if got[1].ValidJSON == nil || *got[1].ValidJSON || !got[1].Truncated {
		t.Errorf("оборванный ответ записан как целый или разбираемый: %+v", got[1])
	}
}

// The window ladder is only as good as the blob it sends: a rung that lands under the
// limit it meant to exceed measures nothing at all.
func TestFillerIsNeverLighterThanAsked(t *testing.T) {
	for _, want := range []int{1, 50, 1000, 25000} {
		got := EstimateTokens(Filler(want))
		if got < want {
			t.Errorf("заказано %d токенов, получено %d — ступень окажется легче задуманной", want, got)
		}
		if want > 100 && got > want*2 {
			t.Errorf("заказано %d токенов, получено %d — перелёт вдвое", want, got)
		}
	}
}

func TestEstimateErrorSaysNothingWhenNothingWasBilled(t *testing.T) {
	if _, ok := (TokenRow{EstTotal: 500}).EstimateError(); ok {
		t.Error("ошибка счётчика посчитана против вызова, которого не было")
	}
	delta, ok := TokenRow{EstTotal: 120, PromptTokens: 100}.EstimateError()
	if !ok || delta != 20 {
		t.Errorf("ошибка счётчика %d (есть данные: %v), ожидалось +20", delta, ok)
	}
}
