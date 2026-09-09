package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

func TestEstimateIsZeroOnlyForNothing(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("пустой текст оценён в %d токенов", got)
	}
	if got := EstimateTokens("а"); got < 1 {
		t.Fatalf("один символ оценён в %d токенов — счётчик округляет вниз", got)
	}
}

func TestEstimateGrowsWithText(t *testing.T) {
	short := EstimateTokens("контекст растёт")
	long := EstimateTokens(strings.Repeat("контекст растёт ", 20))
	if long <= short {
		t.Fatalf("длинный текст оценён в %d, короткий в %d", long, short)
	}
}

// The weights are the estimator's only content, and the one that matters here is not
// published by the provider: DeepSeek documents English and Chinese, never Russian.
// If Cyrillic ever stops costing more per character than Latin, the calibration in
// day-08/RESULTS.md is describing a different function than the one that runs.
func TestCyrillicIsCountedHeavierThanLatin(t *testing.T) {
	const n = 100
	ru := EstimateTokens(strings.Repeat("я", n))
	en := EstimateTokens(strings.Repeat("a", n))
	if ru <= en {
		t.Fatalf("кириллица %d, латиница %d на %d символов — вес кириллицы не выше", ru, en, n)
	}
}

func TestPreflightSplitsSystemHistoryAndInput(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: "ты счётный помощник"}, f)
	if _, err := a.Ask(context.Background(), "первый вопрос"); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	est := a.Preflight("второй вопрос")
	if est.System == 0 || est.History == 0 || est.Input == 0 || est.Overhead == 0 {
		t.Fatalf("раскладка не заполнена: %+v", est)
	}
	if est.Exchanges != 1 {
		t.Fatalf("обменов в истории %d, ожидался 1", est.Exchanges)
	}
	if sum := est.System + est.History + est.Input + est.Overhead; sum != est.Total {
		t.Fatalf("части дают %d, а Total = %d", sum, est.Total)
	}
}

// Preflight weighs a request that messagesFor builds; two independent assemblies of
// the same thing drift apart silently. The message count is what both agree on.
func TestPreflightWeighsTheSameRequestThatWillBeSent(t *testing.T) {
	f := &fakeCaller{}
	for _, system := range []string{"", "ты ассистент"} {
		a := newAgent(t, Config{SystemPrompt: system}, f)
		for i := 0; i < 2; i++ {
			if _, err := a.Ask(context.Background(), "вопрос"); err != nil {
				t.Fatalf("Ask: %v", err)
			}
		}
		est := a.Preflight("следующий")
		if got := len(a.messagesFor("следующий")); got != est.Messages {
			t.Fatalf("system=%q: в запросе %d сообщений, Preflight насчитал %d", system, got, est.Messages)
		}
	}
}

func TestOverflowRefuseDoesNotCallTheModel(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{
		SystemPrompt:     "ты ассистент",
		MaxContextTokens: 10,
		OnOverflow:       OverflowRefuse,
	}, f)

	reply, err := a.Ask(context.Background(), strings.Repeat("длинный вопрос ", 20))
	if !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("ожидалась ErrContextOverflow, получено %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("модель вызвана %d раз при отказе по потолку — отказ должен быть бесплатным", f.calls)
	}
	if reply.Estimated.Total == 0 {
		t.Fatal("в ответе нет оценки, по которой принят отказ")
	}
	if a.Totals().Calls != 0 || a.Totals().Cost != 0 {
		t.Fatalf("отказ попал в расход: %+v", a.Totals())
	}
}

func TestOverflowTrimDropsOldestAndAnswers(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: "ты ассистент"}, f)
	for _, q := range []string{"первый", "второй", "третий"} {
		if _, err := a.Ask(context.Background(), q); err != nil {
			t.Fatalf("Ask(%q): %v", q, err)
		}
	}

	// The ceiling arrives after the history did — the honest shape of the problem:
	// a conversation that fitted yesterday stops fitting today.
	a.cfg.MaxContextTokens = a.Preflight("четвёртый").Total - 5
	a.cfg.OnOverflow = OverflowTrim

	reply, err := a.Ask(context.Background(), "четвёртый")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Dropped == 0 {
		t.Fatal("обрезка не произошла, хотя запрос не помещался")
	}
	if reply.Warning == "" {
		t.Fatal("обрезка прошла молча — интерфейсу нечего показать")
	}
	sent := f.sent[len(f.sent)-1]
	for _, m := range sent {
		if m.Content == "первый" {
			t.Fatal("самый старый обмен всё ещё уехал в запрос")
		}
	}
	if a.Turns() != 4 {
		t.Fatalf("ходов %d — обрезка контекста не должна менять счёт состоявшихся ходов", a.Turns())
	}
}

// The trim is a decision about the conversation's memory. Applying it before the call
// means a failed call costs history that was never used for anything.
func TestOverflowTrimKeepsHistoryWhenTheCallFails(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: "ты ассистент"}, f)
	for _, q := range []string{"первый", "второй", "третий"} {
		if _, err := a.Ask(context.Background(), q); err != nil {
			t.Fatalf("Ask(%q): %v", q, err)
		}
	}
	before := len(a.stack)

	a.cfg.MaxContextTokens = a.Preflight("четвёртый").Total - 5
	a.cfg.OnOverflow = OverflowTrim
	f.errs = []error{nil, nil, nil, errors.New("503")}

	if _, err := a.Ask(context.Background(), "четвёртый"); err == nil {
		t.Fatal("ожидалась ошибка вызова")
	}
	if len(a.stack) != before {
		t.Fatalf("после неудачного хода в истории %d сообщений вместо %d — обрезка применилась без хода",
			len(a.stack), before)
	}
}

func TestOverflowTrimRefusesWhenEvenTheQuestionAloneIsTooBig(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{
		SystemPrompt:     "ты ассистент",
		MaxContextTokens: 5,
		OnOverflow:       OverflowTrim,
	}, f)

	_, err := a.Ask(context.Background(), strings.Repeat("вопрос ", 50))
	if !errors.Is(err, ErrInputAlone) {
		t.Fatalf("ожидалась ErrInputAlone, получено %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("модель вызвана %d раз, хотя запрос заведомо не помещается", f.calls)
	}
}

func TestOverflowWarnSendsAnyway(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{
		SystemPrompt:     "ты ассистент",
		MaxContextTokens: 10,
		OnOverflow:       OverflowWarn,
	}, f)

	reply, err := a.Ask(context.Background(), strings.Repeat("длинный вопрос ", 20))
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("вызовов модели %d, ожидался 1", f.calls)
	}
	if reply.Warning == "" {
		t.Fatal("предупреждения нет — политика warn отправила запрос молча")
	}
}

func TestNewRejectsAPolicyWithoutACeiling(t *testing.T) {
	if _, err := New(&fakeCaller{}, Config{OnOverflow: OverflowTrim}); err == nil {
		t.Fatal("политика без потолка принята — она никогда не сработает")
	}
	if _, err := New(&fakeCaller{}, Config{MaxContextTokens: 100, OnOverflow: "выбросить"}); err == nil {
		t.Fatal("неизвестная политика принята")
	}
	if _, err := New(&fakeCaller{}, Config{MaxContextTokens: -1}); err == nil {
		t.Fatal("отрицательный потолок принят")
	}
}

func TestTotalsCountBilledFailuresToo(t *testing.T) {
	billed := llm.Answer{
		Model: "deepseek-v4-flash",
		Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 0, PromptCacheMissTokens: 100},
	}
	f := &fakeCaller{
		answers: []llm.Answer{
			{Content: "ответ", Model: "deepseek-v4-flash",
				Usage: llm.Usage{PromptTokens: 50, CompletionTokens: 10, PromptCacheHitTokens: 40, PromptCacheMissTokens: 10}},
			billed, // empty content: the provider billed a call that produced nothing
			billed, // and the same again, this time as a transport error
		},
		errs: []error{nil, nil, errors.New("API вернул 400")},
	}
	a := newAgent(t, Config{Model: "deepseek-v4-flash"}, f)

	if _, err := a.Ask(context.Background(), "первый"); err != nil {
		t.Fatalf("первый ход: %v", err)
	}
	if _, err := a.Ask(context.Background(), "второй"); !errors.Is(err, ErrEmptyAnswer) {
		t.Fatalf("второй ход: ожидалась ErrEmptyAnswer, получено %v", err)
	}
	if _, err := a.Ask(context.Background(), "третий"); err == nil {
		t.Fatal("третий ход: ожидалась ошибка вызова")
	}

	got := a.Totals()
	if got.Calls != 3 || got.Failed != 2 {
		t.Fatalf("вызовов %d (неудачных %d), ожидалось 3 и 2", got.Calls, got.Failed)
	}
	if got.PromptTokens != 250 || got.CompletionTokens != 10 {
		t.Fatalf("вход %d, выход %d — ожидалось 250 и 10", got.PromptTokens, got.CompletionTokens)
	}
	if got.CachedTokens != 40 || got.MissedTokens != 210 {
		t.Fatalf("кэш %d/%d, ожидалось 40 попаданий и 210 промахов", got.CachedTokens, got.MissedTokens)
	}
	if got.Cost <= 0 {
		t.Fatalf("цена беседы %f — три оплаченных вызова не могут стоить ноль", got.Cost)
	}
	if share, ok := got.CacheShare(); !ok || share <= 0 || share >= 1 {
		t.Fatalf("доля кэша %f (есть данные: %v)", share, ok)
	}
}

func TestTotalsAreUnknownRatherThanZeroForAnUnpricedModel(t *testing.T) {
	f := &fakeCaller{answers: []llm.Answer{{
		Content: "ответ", Model: "модель-которой-нет-в-прайсе",
		Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5},
	}}}
	a := newAgent(t, Config{Model: "модель-которой-нет-в-прайсе"}, f)
	if _, err := a.Ask(context.Background(), "вопрос"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := a.Totals(); got.Unpriced != 1 || got.Cost != 0 {
		t.Fatalf("%+v — вызов без цены должен считаться отдельно, а не как бесплатный", got)
	}
	if !strings.Contains(a.Totals().String(), "не меньше") {
		t.Fatalf("итог не говорит, что цена неполная: %s", a.Totals())
	}
}

func TestTotalsSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")

	answer := llm.Answer{Content: "ответ", Model: "deepseek-v4-flash",
		Usage: llm.Usage{PromptTokens: 120, CompletionTokens: 30, PromptCacheMissTokens: 120}}

	first := newAgent(t, Config{Model: "deepseek-v4-flash", Store: NewFileStore(path)},
		&fakeCaller{answers: []llm.Answer{answer}})
	if _, err := first.Ask(context.Background(), "вопрос"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	want := first.Totals()

	second := newAgent(t, Config{Model: "deepseek-v4-flash", Store: NewFileStore(path)}, &fakeCaller{})
	if got := second.Totals(); got != want {
		t.Fatalf("после перезапуска расход %+v, до перезапуска %+v", got, want)
	}
}

func TestResetClearsTheSpendWithTheConversation(t *testing.T) {
	f := &fakeCaller{answers: []llm.Answer{{Content: "ответ", Model: "deepseek-v4-flash",
		Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5}}}}
	a := newAgent(t, Config{Model: "deepseek-v4-flash"}, f)
	if _, err := a.Ask(context.Background(), "вопрос"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := a.Totals(); got != (Totals{}) {
		t.Fatalf("после сброса расход %+v — это расход другой беседы", got)
	}
}

// Version 1 files were written before the spend existed. Refusing them would strand
// every conversation started before day 8.
func TestVersionOneFileLoadsWithNoRecordedSpend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.json")
	old := map[string]any{
		"version": 1,
		"agent":   "агент",
		"model":   "deepseek-v4-flash",
		"system":  "ты ассистент",
		"turns":   1,
		"updated": "2026-09-07T10:00:00Z",
		"messages": []map[string]string{
			{"role": "user", "content": "вопрос"},
			{"role": "assistant", "content": "ответ"},
		},
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	a := newAgent(t, Config{SystemPrompt: "ты ассистент", Model: "deepseek-v4-flash",
		Store: NewFileStore(path)}, &fakeCaller{})
	if a.Turns() != 1 {
		t.Fatalf("ходов загружено %d, ожидался 1", a.Turns())
	}
	if got := a.Totals(); got != (Totals{}) {
		t.Fatalf("у файла версии 1 расход %+v, а записан он не был", got)
	}
}

func TestBrokenSpendIsRefusedRatherThanBelieved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	raw := []byte(`{"version":2,"agent":"агент","model":"deepseek-v4-flash","system":"ты ассистент",` +
		`"turns":1,"updated":"2026-09-09T10:00:00Z","spend":{"calls":1,"failed":3},` +
		`"messages":[{"role":"user","content":"вопрос"},{"role":"assistant","content":"ответ"}]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := New(&fakeCaller{}, Config{Store: NewFileStore(path)}); err == nil {
		t.Fatal("файл с невозможным расходом принят")
	}
}

func TestTruncatedAnswerIsReportedAsSuch(t *testing.T) {
	f := &fakeCaller{answers: []llm.Answer{{
		Content:      `{"город": "Сара`,
		Model:        "deepseek-v4-flash",
		FinishReason: "length",
		Usage:        llm.Usage{PromptTokens: 20, CompletionTokens: 8},
	}}}
	a := newAgent(t, Config{Model: "deepseek-v4-flash"}, f)
	reply, err := a.Ask(context.Background(), "назови город одним объектом JSON")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !reply.Truncated {
		t.Fatal("ответ оборван по потолку генерации, а Reply об этом молчит")
	}
	if json.Valid([]byte(reply.Text)) {
		t.Fatal("тест собран неверно: оборванный ответ обязан быть невалидным JSON")
	}
}

func TestEstimateTravelsWithEveryReply(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: "ты ассистент"}, f)
	reply, err := a.Ask(context.Background(), "вопрос")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Estimated.Total == 0 {
		t.Fatal("в ответе нет предполётной оценки — измерять ошибку счётчика нечем")
	}
}

// The estimate that travels back with a reply must describe the request that was
// actually sent. On a trimmed turn those are two different requests, and a row built
// from the wrong one measures the counter against tokens it never weighed.
func TestReplyEstimateDescribesWhatWasSentAfterTrimming(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: "ты ассистент"}, f)
	for _, q := range []string{"первый", "второй", "третий"} {
		if _, err := a.Ask(context.Background(), q); err != nil {
			t.Fatalf("Ask(%q): %v", q, err)
		}
	}
	full := a.Preflight("четвёртый")
	a.cfg.MaxContextTokens = full.Total - 5
	a.cfg.OnOverflow = OverflowTrim

	reply, err := a.Ask(context.Background(), "четвёртый")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Dropped == 0 {
		t.Fatal("тест собран неверно: обрезки не было")
	}
	if reply.Estimated.Total > a.cfg.MaxContextTokens {
		t.Fatalf("оценка в ответе %d выше потолка %d — это вес необрезанного запроса",
			reply.Estimated.Total, a.cfg.MaxContextTokens)
	}
	if reply.Estimated.History >= full.History {
		t.Fatalf("история в оценке %d не уменьшилась после отбрасывания %d обменов (было %d)",
			reply.Estimated.History, reply.Dropped, full.History)
	}
	// And the estimate must match the request the transport actually received.
	sent := f.sent[len(f.sent)-1]
	if reply.Estimated.Messages != len(sent) {
		t.Fatalf("в оценке %d сообщений, отправлено %d", reply.Estimated.Messages, len(sent))
	}
}

// The spend was first written with Go's own capitalised field names, before the json
// tags were added. Files from that build exist on the machine that ran the day-8
// measurements, and continuing to read them is not an accident of encoding/json's
// case-insensitive matching that anyone may quietly break — it is the behaviour a
// restart depends on.
func TestSpendWrittenWithTheOlderCapitalisedKeysStillLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old-keys.json")
	raw := []byte(`{"version":2,"agent":"агент","model":"deepseek-v4-flash","system":"ты ассистент",` +
		`"turns":1,"updated":"2026-09-09T10:00:00Z",` +
		`"spend":{"Calls":3,"Failed":1,"PromptTokens":250,"CompletionTokens":10,"Cost":0.0001},` +
		`"messages":[{"role":"user","content":"вопрос"},{"role":"assistant","content":"ответ"}]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	a := newAgent(t, Config{SystemPrompt: "ты ассистент", Model: "deepseek-v4-flash",
		Store: NewFileStore(path)}, &fakeCaller{})
	got := a.Totals()
	if got.Calls != 3 || got.Failed != 1 || got.PromptTokens != 250 {
		t.Fatalf("расход из файла со старыми ключами прочитан как %+v", got)
	}
}

// Trimming buys the answer with memory, so it must spend as little memory as it can.
// A loop that drops two exchanges where one would do forgets a whole turn more than
// the ceiling asked for — and every trim test passed with exactly that mutation until
// this one existed.
func TestOverflowTrimDropsTheFewestExchangesThatFit(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: "ты ассистент"}, f)
	for _, q := range []string{"первый", "второй", "третий", "четвёртый"} {
		if _, err := a.Ask(context.Background(), q); err != nil {
			t.Fatalf("Ask(%q): %v", q, err)
		}
	}

	// A ceiling that one dropped exchange clears and none does not.
	full := a.Preflight("пятый")
	oldest := EstimateTokens(a.stack[0].Content) + EstimateTokens(a.stack[1].Content) + 2*tokensPerMessage
	a.cfg.MaxContextTokens = full.Total - oldest
	a.cfg.OnOverflow = OverflowTrim

	reply, err := a.Ask(context.Background(), "пятый")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Dropped != 1 {
		t.Fatalf("отброшено обменов %d, хватало одного — обрезка забывает больше, чем просил потолок", reply.Dropped)
	}
	sent := f.sent[len(f.sent)-1]
	var carried []string
	for _, m := range sent {
		carried = append(carried, m.Content)
	}
	joined := strings.Join(carried, "|")
	if strings.Contains(joined, "первый") {
		t.Errorf("самый старый обмен не отброшен: %s", joined)
	}
	for _, keep := range []string{"второй", "третий", "четвёртый"} {
		if !strings.Contains(joined, keep) {
			t.Errorf("обмен %q отброшен без нужды: %s", keep, joined)
		}
	}
}

// The ceiling is a limit, not a threshold: a request weighing exactly the ceiling
// fits. Both an off-by-one in either direction and a spurious refusal on the exact
// boundary are invisible to every other test.
func TestARequestWeighingExactlyTheCeilingIsSent(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: "ты ассистент"}, f)
	exact := a.Preflight("вопрос").Total
	a.cfg.MaxContextTokens = exact
	a.cfg.OnOverflow = OverflowRefuse

	reply, err := a.Ask(context.Background(), "вопрос")
	if err != nil {
		t.Fatalf("запрос ровно в потолок (%d) отвергнут: %v", exact, err)
	}
	if f.calls != 1 {
		t.Fatalf("вызовов модели %d, ожидался 1", f.calls)
	}
	if reply.Warning != "" || reply.Dropped != 0 {
		t.Fatalf("запрос ровно в потолок вызвал реакцию политики: dropped=%d warning=%q", reply.Dropped, reply.Warning)
	}
}

// Trimming has to be able to empty the history completely and still send: the
// question plus the system prompt may fit even when nothing else does. A loop that
// refuses to drop the last remaining exchange fails exactly here, and reports it as
// "the question alone does not fit" — which would be false.
func TestOverflowTrimCanEmptyTheHistoryAndStillAnswer(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{SystemPrompt: "ты ассистент"}, f)
	if _, err := a.Ask(context.Background(), "первый"); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	// Fits the system prompt and the question, not the single stored exchange.
	bare := a.estimate("второй", nil)
	a.cfg.MaxContextTokens = bare.Total
	a.cfg.OnOverflow = OverflowTrim

	reply, err := a.Ask(context.Background(), "второй")
	if err != nil {
		t.Fatalf("обрезка до пустой истории не сработала: %v", err)
	}
	if reply.Dropped != 1 {
		t.Fatalf("отброшено обменов %d, ожидался 1", reply.Dropped)
	}
	if got := len(f.sent[len(f.sent)-1]); got != 2 {
		t.Fatalf("отправлено сообщений %d, ожидались только системный промпт и вопрос", got)
	}
}

// validateSpend has ten independent guards and one of them was tested. A file whose
// token counts are negative would otherwise be accepted and poison every total the
// conversation reports from then on.
func TestEveryImpossibleSpendFieldIsRefused(t *testing.T) {
	cases := map[string]string{
		"отрицательные вызовы":       `{"calls":-1}`,
		"отрицательные неудачи":      `{"failed":-1}`,
		"отрицательный вход":         `{"promptTokens":-5}`,
		"отрицательный выход":        `{"completionTokens":-5}`,
		"отрицательное рассуждение":  `{"reasoningTokens":-5}`,
		"отрицательный кэш":          `{"cachedTokens":-5}`,
		"отрицательные промахи":      `{"missedTokens":-5}`,
		"отрицательные без цены":     `{"unpriced":-1}`,
		"отрицательная цена":         `{"cost":-0.5}`,
		"неудач больше, чем вызовов": `{"calls":1,"failed":2}`,
	}
	for name, spend := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "broken.json")
			raw := []byte(`{"version":2,"agent":"агент","model":"deepseek-v4-flash","system":"ты ассистент",` +
				`"turns":1,"updated":"2026-09-09T10:00:00Z","spend":` + spend + `,` +
				`"messages":[{"role":"user","content":"вопрос"},{"role":"assistant","content":"ответ"}]}`)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := New(&fakeCaller{}, Config{Store: NewFileStore(path)}); err == nil {
				t.Fatalf("файл с расходом %s принят", spend)
			}
		})
	}
}

// The provider charges for asking for JSON, and the counter did not know it. These
// are the numbers that found it: the same request, sent plain and with
// response_format, came back 20 tokens heavier (2026-09-09, deepseek-v4-flash).
func TestAskingForJSONIsCountedBeforeItIsBilled(t *testing.T) {
	const system = "Ты ассистент, работающий через официальный API DeepSeek. " +
		"Отвечай кратко и по делу, на русском языке."
	const question = "Верни один объект JSON с полями city, country и population про Саратов."

	plain := newAgent(t, Config{SystemPrompt: system}, &fakeCaller{})
	asJSON := newAgent(t, Config{SystemPrompt: system, ResponseFormat: "json_object"}, &fakeCaller{})

	before, after := plain.Preflight(question), asJSON.Preflight(question)
	if after.Total-before.Total != tokensForResponseFormat {
		t.Fatalf("json-режим прибавил %d токенов, ожидалось %d",
			after.Total-before.Total, tokensForResponseFormat)
	}
	// The measured facts this constant exists for: 56 input tokens plain, 76 with
	// response_format. The counter must not fall below either.
	if before.Total < 56 {
		t.Errorf("оценка обычного запроса %d при факте 56 — занижение", before.Total)
	}
	if after.Total < 76 {
		t.Errorf("оценка запроса в json-режиме %d при факте 76 — занижение, ровно то, что нашлось на демо", after.Total)
	}
}
