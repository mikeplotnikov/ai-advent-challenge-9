package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

type compressionProbeCaller struct {
	calls int
}

func (c *compressionProbeCaller) AskWith(_ context.Context, messages []llm.Message, _ llm.Options) (llm.Answer, error) {
	c.calls++
	if len(messages) == 2 && messages[0].Content == summarySystem {
		// The fake is deliberately context-bound: it produces a summary only from
		// markers actually present in the previous summary or raw exchanges. A
		// fake that always returned all markers would make a broken compression
		// pipeline look like it preserved quality.
		return compressedAnswer(probeCodesIn(messages), 8, 8), nil
	}
	question := messages[len(messages)-1].Content
	for _, check := range compressionProbeQuestions {
		if question == check.Prompt {
			if !strings.Contains(probeCodesIn(messages[:len(messages)-1]), check.Code) {
				return compressedAnswer("код не найден в переданном контексте", 8, 3), nil
			}
			return compressedAnswer(check.Code, 8, 3), nil
		}
	}
	return compressedAnswer("принято", 8, 2), nil
}

func probeCodesIn(messages []llm.Message) string {
	all := make([]string, 0, len(messages))
	for _, message := range messages {
		all = append(all, message.Content)
	}
	context := strings.Join(all, "\n")
	codes := make([]string, 0, len(compressionProbeFacts))
	for _, fact := range compressionProbeFacts {
		if strings.Contains(context, fact.Code) {
			codes = append(codes, fact.Code)
		}
	}
	return strings.Join(codes, "; ")
}

func compressedAnswer(text string, prompt, completion int) llm.Answer {
	return llm.Answer{
		Content: text,
		Model:   "deepseek-flash",
		Usage: llm.Usage{
			PromptTokens:          prompt,
			CompletionTokens:      completion,
			TotalTokens:           prompt + completion,
			PromptCacheMissTokens: prompt,
		},
	}
}

func TestCompressionSendsSummaryAndKeepsOnlyTheRawTail(t *testing.T) {
	f := &fakeCaller{answers: []llm.Answer{
		compressedAnswer("ответ один", 10, 4),
		compressedAnswer("ответ два", 15, 4),
		compressedAnswer("Михаил выбрал вариант А.", 12, 6),
		compressedAnswer("ответ три", 20, 4),
		compressedAnswer("Михаил выбрал вариант А; сроки не менялись.", 14, 7),
	}}
	a := newAgent(t, Config{SystemPrompt: "роль агента", KeepLastMessages: 2}, f)

	for _, question := range []string{"первый вопрос", "второй вопрос", "третий вопрос"} {
		if _, err := a.Ask(context.Background(), question); err != nil {
			t.Fatalf("Ask(%q): %v", question, err)
		}
	}

	if f.calls != 5 {
		t.Fatalf("вызовов %d, ожидалось 5: три ответа и две суммаризации", f.calls)
	}
	thirdQuestion := f.sent[3]
	if got, want := roles(thirdQuestion), "system,user,assistant,user"; got != want {
		t.Fatalf("третий вопрос ушёл с ролями %q, ожидалось %q", got, want)
	}
	if !strings.Contains(thirdQuestion[0].Content, summaryHeader) || !strings.Contains(thirdQuestion[0].Content, "Михаил выбрал вариант А.") {
		t.Fatalf("в следующий запрос не подставлено отдельное summary:\n%s", thirdQuestion[0].Content)
	}
	if thirdQuestion[1].Content != "второй вопрос" || thirdQuestion[2].Content != "ответ два" {
		t.Fatalf("последний сырой обмен изменён: %+v", thirdQuestion[1:3])
	}
	for _, m := range thirdQuestion {
		if m.Content == "первый вопрос" || m.Content == "ответ один" {
			t.Fatalf("старый обмен отправлен дословно вместе с summary: %+v", thirdQuestion)
		}
	}
	if f.opts[2].MaxTokens != summaryMaxTokens || f.opts[2].Thinking != "disabled" {
		t.Fatalf("суммаризация ушла с настройками %+v, ожидались max_tokens=%d и thinking=disabled", f.opts[2], summaryMaxTokens)
	}
	firstSummary := f.sent[2]
	if got := roles(firstSummary); got != "system,user" ||
		!strings.Contains(firstSummary[1].Content, "(резюме ещё нет)") ||
		!strings.Contains(firstSummary[1].Content, "первый вопрос") ||
		!strings.Contains(firstSummary[1].Content, "ответ один") {
		t.Fatalf("первая суммаризация не получила первый полный обмен: %+v", firstSummary)
	}
	secondSummary := f.sent[4]
	if !strings.Contains(secondSummary[1].Content, "Михаил выбрал вариант А.") ||
		!strings.Contains(secondSummary[1].Content, "второй вопрос") ||
		!strings.Contains(secondSummary[1].Content, "ответ два") {
		t.Fatalf("повторная суммаризация не получила прошлое summary и новый старый обмен: %+v", secondSummary)
	}
	if strings.Contains(secondSummary[1].Content, "первый вопрос") || strings.Contains(secondSummary[1].Content, "ответ один") {
		t.Fatalf("повторная суммаризация отправила уже сжатый сырой обмен вместо прошлого summary: %+v", secondSummary)
	}

	state := a.ContextState()
	if state.RawMessages != 2 || state.CompressedMessages != 4 || state.SummaryTokens == 0 {
		t.Fatalf("слои контекста %+v, ожидались 2 сырого, 4 сжатого и непустое summary", state)
	}
	if got := a.Preflight("четвёртый вопрос"); got.Summary == 0 || got.Total != got.System+got.Summary+got.History+got.Input+got.Overhead {
		t.Fatalf("предполётная оценка не учитывает summary честно: %+v", got)
	}
}

func TestCompressionSurvivesARestartWithoutRestoringOldRawMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	first := newAgent(t, Config{KeepLastMessages: 2, Store: NewFileStore(path)}, &fakeCaller{answers: []llm.Answer{
		compressedAnswer("ответ один", 10, 4),
		compressedAnswer("ответ два", 14, 4),
		compressedAnswer("в старом обмене назван пароль-образец МАЯК-17.", 11, 5),
	}})
	for _, q := range []string{"запомни: пароль-образец МАЯК-17", "это второй обмен"} {
		if _, err := first.Ask(context.Background(), q); err != nil {
			t.Fatalf("первый процесс Ask(%q): %v", q, err)
		}
	}

	secondCaller := &fakeCaller{answers: []llm.Answer{
		compressedAnswer("МАЯК-17", 20, 4),
		compressedAnswer("в старом обмене назван пароль-образец МАЯК-17; это второй обмен.", 12, 5),
	}}
	second := newAgent(t, Config{KeepLastMessages: 2, Store: NewFileStore(path)}, secondCaller)
	if state := second.ContextState(); state.RawMessages != 2 || state.CompressedMessages != 2 {
		t.Fatalf("после перезапуска слои не восстановлены: %+v", state)
	}
	if _, err := second.Ask(context.Background(), "какой пароль-образец?"); err != nil {
		t.Fatalf("второй процесс Ask: %v", err)
	}
	request := secondCaller.sent[0]
	if !strings.Contains(request[0].Content, "МАЯК-17") {
		t.Fatalf("после перезапуска summary не отправлен: %+v", request)
	}
	for _, m := range request[1:] {
		if strings.Contains(m.Content, "запомни: пароль-образец") {
			t.Fatalf("старый обмен вернулся в сырой хвост: %+v", request)
		}
	}
}

func TestCompressionCompactsLegacyRawHistoryBeforeTheFirstNewRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	legacy := newAgent(t, Config{Store: NewFileStore(path)}, &fakeCaller{answers: []llm.Answer{
		compressedAnswer("старый ответ один", 10, 4),
		compressedAnswer("старый ответ два", 14, 4),
	}})
	for _, q := range []string{"старый вопрос один", "старый вопрос два"} {
		if _, err := legacy.Ask(context.Background(), q); err != nil {
			t.Fatalf("подготовка legacy %q: %v", q, err)
		}
	}

	caller := &fakeCaller{answers: []llm.Answer{
		compressedAnswer("в старом разговоре был первый обмен.", 11, 5),
		compressedAnswer("новый ответ", 18, 4),
		compressedAnswer("в старом разговоре был первый обмен; сохранён второй.", 12, 5),
	}}
	upgraded := newAgent(t, Config{KeepLastMessages: 2, Store: NewFileStore(path)}, caller)
	if _, err := upgraded.Ask(context.Background(), "новый вопрос"); err != nil {
		t.Fatalf("первый ход после обновления: %v", err)
	}
	if caller.calls != 3 {
		t.Fatalf("вызовов %d, ожидались summary до запроса, ответ и summary после него", caller.calls)
	}
	request := caller.sent[1]
	if !strings.Contains(request[0].Content, "старом разговоре") {
		t.Fatalf("первый запрос после обновления отправил полную историю вместо summary: %+v", request)
	}
	for _, m := range request[1:] {
		if m.Content == "старый вопрос один" || m.Content == "старый ответ один" {
			t.Fatalf("самый старый обмен ушёл дословно: %+v", request)
		}
	}
}

func TestUnusableSummaryKeepsAndPersistsRawHistory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		summary llm.Answer
		errs    []error
	}{
		{
			name:    "transport failure with billed usage",
			summary: compressedAnswer("", 9, 2),
			errs:    []error{nil, nil, errors.New("503")},
		},
		{
			name:    "empty successful response",
			summary: compressedAnswer(" \n\t", 9, 2),
		},
		{
			name: "generation cap truncates summary",
			summary: llm.Answer{
				Content:      "оборванное резюме",
				Model:        "deepseek-flash",
				FinishReason: finishLength,
				Usage:        llm.Usage{PromptTokens: 9, CompletionTokens: 2, TotalTokens: 11, PromptCacheMissTokens: 9},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.json")
			f := &fakeCaller{
				answers: []llm.Answer{
					compressedAnswer("ответ один", 10, 4),
					compressedAnswer("ответ два", 14, 4),
					tc.summary,
				},
				errs: tc.errs,
			}
			a := newAgent(t, Config{KeepLastMessages: 2, Store: NewFileStore(path)}, f)
			if _, err := a.Ask(context.Background(), "первый"); err != nil {
				t.Fatalf("первый ход: %v", err)
			}
			reply, err := a.Ask(context.Background(), "второй")
			if !errors.Is(err, ErrNotCompressed) {
				t.Fatalf("ошибка %v, ожидалась ErrNotCompressed", err)
			}
			if reply.Text != "ответ два" {
				t.Fatalf("успешный ответ потерян вместе с ошибкой сжатия: %+v", reply)
			}
			if state := a.ContextState(); state.RawMessages != 4 || state.CompressedMessages != 0 {
				t.Fatalf("неудачная суммаризация уничтожила или пометила сырой контекст: %+v", state)
			}

			restored := newAgent(t, Config{KeepLastMessages: 2, Store: NewFileStore(path)}, &fakeCaller{})
			if state := restored.ContextState(); state.RawMessages != 4 || state.CompressedMessages != 0 {
				t.Fatalf("неудачный ход не пережил перезапуск как сырой контекст: %+v", state)
			}
			if got := restored.Totals(); got.Calls != 3 || got.Failed != 1 || got.PromptTokens != 33 ||
				got.CompletionTokens != 10 || got.MissedTokens != 33 || got.Cost <= 0 || got.Unpriced != 0 {
				t.Fatalf("расход неудачной суммаризации скрыт или неполон: %+v", got)
			}
			if got := restored.ContextState().SummarySpend; got.Calls != 1 || got.Failed != 1 ||
				got.PromptTokens != 9 || got.CompletionTokens != 2 || got.MissedTokens != 9 || got.Cost <= 0 || got.Unpriced != 0 {
				t.Fatalf("расход суммаризации не отделён или неполон: %+v", got)
			}
		})
	}
}

func TestCompressionSpendIsIncludedInTheConversationTotal(t *testing.T) {
	f := &fakeCaller{answers: []llm.Answer{
		compressedAnswer("ответ один", 10, 4),
		compressedAnswer("ответ два", 15, 5),
		compressedAnswer("резюме", 7, 3),
	}}
	a := newAgent(t, Config{Model: "deepseek-flash", KeepLastMessages: 2}, f)
	for _, q := range []string{"первый", "второй"} {
		if _, err := a.Ask(context.Background(), q); err != nil {
			t.Fatalf("Ask(%q): %v", q, err)
		}
	}
	if got := a.Totals(); got.Calls != 3 || got.PromptTokens != 32 || got.CompletionTokens != 12 {
		t.Fatalf("общий расход не включает вызов summary: %+v", got)
	}
	if got := a.ContextState().SummarySpend; got.Calls != 1 || got.PromptTokens != 7 || got.CompletionTokens != 3 {
		t.Fatalf("подытог summary неверный: %+v", got)
	}
}

func TestCompressionConfigRejectsAnIncompleteExchangeAndTrimMix(t *testing.T) {
	for _, cfg := range []Config{
		{KeepLastMessages: 1},
		{KeepLastMessages: 3},
		{KeepLastMessages: -2},
		{KeepLastMessages: 2, MaxTurns: 1},
		{KeepLastMessages: 2, MaxContextTokens: 100, OnOverflow: OverflowTrim},
	} {
		if _, err := New(&fakeCaller{}, cfg); err == nil {
			t.Fatalf("недопустимый конфиг %+v принят", cfg)
		}
	}
}

func TestCompressedSessionCannotPretendToBeAnUncompressedControl(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	compressed := newAgent(t, Config{KeepLastMessages: 2, Store: NewFileStore(path)}, &fakeCaller{answers: []llm.Answer{
		compressedAnswer("первый ответ", 10, 4),
		compressedAnswer("второй ответ", 14, 4),
		compressedAnswer("резюме первых сообщений", 9, 2),
	}})
	for _, question := range []string{"первый", "второй"} {
		if _, err := compressed.Ask(context.Background(), question); err != nil {
			t.Fatalf("подготовка сжатой беседы %q: %v", question, err)
		}
	}
	if _, err := New(&fakeCaller{}, Config{KeepLastMessages: 0, Store: NewFileStore(path)}); err == nil ||
		!strings.Contains(err.Error(), "не может восстановить") {
		t.Fatalf("сжатая беседа была выдана за полный контроль: %v", err)
	}
}

func TestPrecompressedStateAndBilledFailedAnswerPersistAcrossRestart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     Config
		answer  llm.Answer
		callErr error
		wantErr error
	}{
		{
			name:    "transport",
			answer:  compressedAnswer("", 13, 1),
			callErr: errors.New("503"),
		},
		{
			name:    "empty output",
			answer:  compressedAnswer(" \n", 13, 1),
			wantErr: ErrEmptyAnswer,
		},
		{
			name:   "output policy",
			cfg:    Config{Validate: func(string) error { return errors.New("неверный формат") }},
			answer: compressedAnswer("неподходящий ответ", 13, 1),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.json")
			legacy := newAgent(t, Config{Store: NewFileStore(path)}, &fakeCaller{answers: []llm.Answer{
				compressedAnswer("старый ответ один", 10, 4),
				compressedAnswer("старый ответ два", 14, 4),
			}})
			for _, question := range []string{"старый вопрос один", "старый вопрос два"} {
				if _, err := legacy.Ask(context.Background(), question); err != nil {
					t.Fatalf("подготовка legacy %q: %v", question, err)
				}
			}

			cfg := tc.cfg
			cfg.KeepLastMessages = 2
			cfg.Store = NewFileStore(path)
			upgraded := newAgent(t, cfg, &fakeCaller{
				answers: []llm.Answer{
					compressedAnswer("резюме старой беседы", 9, 2),
					tc.answer,
				},
				errs: []error{nil, tc.callErr},
			})
			if _, err := upgraded.Ask(context.Background(), "новый вопрос"); err == nil ||
				(tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("неудачный ответ должен вернуть исходную ошибку, получено: %v", err)
			}

			restored := newAgent(t, Config{KeepLastMessages: 2, Store: NewFileStore(path)}, &fakeCaller{})
			if state := restored.ContextState(); state.RawMessages != 2 || state.CompressedMessages != 2 || state.SummaryTokens == 0 {
				t.Fatalf("сжатие до неудачного ответа не пережило перезапуск: %+v", state)
			}
			if got := restored.Totals(); got.Calls != 4 || got.Failed != 1 || got.PromptTokens != 46 || got.CompletionTokens != 11 {
				t.Fatalf("оплаченная неудачная попытка не сохранена: %+v", got)
			}
			if got := restored.ContextState().SummarySpend; got.Calls != 1 || got.Failed != 0 || got.PromptTokens != 9 || got.CompletionTokens != 2 {
				t.Fatalf("расход успешного pre-compression не сохранён: %+v", got)
			}
		})
	}
}

func TestSnapshotRefusesSummarySpendOutsideTheConversationTotal(t *testing.T) {
	snap := Snapshot{
		Version:            SnapshotVersion,
		Spend:              Totals{Calls: 1, PromptTokens: 10},
		Summary:            "резюме",
		CompressedMessages: 2,
		SummarySpend:       Totals{Calls: 2, PromptTokens: 11},
	}
	if err := validate(snap); err == nil {
		t.Fatal("снимок с расходом summary больше общего принят")
	}
}

func TestCompressionProbeCountsSummaryCallsAndWritesBothComparableReports(t *testing.T) {
	fullCaller := &compressionProbeCaller{}
	full, err := New(fullCaller, Config{KeepLastMessages: 0})
	if err != nil {
		t.Fatalf("полный агент: %v", err)
	}
	whole, err := RunCompressionProbe(context.Background(), full, "full")
	if err != nil {
		t.Fatalf("полный прогон: %v", err)
	}
	if whole.Correct != len(compressionProbeQuestions) || whole.SummarySpend.Calls != 0 || whole.Total.Calls != 11 {
		t.Fatalf("полный отчёт %+v", whole)
	}

	compactCaller := &compressionProbeCaller{}
	compact, err := New(compactCaller, Config{KeepLastMessages: 2})
	if err != nil {
		t.Fatalf("сжатый агент: %v", err)
	}
	compressed, err := RunCompressionProbe(context.Background(), compact, "compressed")
	if err != nil {
		t.Fatalf("сжатый прогон: %v", err)
	}
	if compressed.Correct != len(compressionProbeQuestions) || compressed.SummarySpend.Calls == 0 {
		t.Fatalf("сжатый отчёт не показывает качество или цену summary: %+v", compressed)
	}
	if compressed.Total.Calls != 11+compressed.SummarySpend.Calls {
		t.Fatalf("общий вызовы %d не включают summary %d", compressed.Total.Calls, compressed.SummarySpend.Calls)
	}

	var raw bytes.Buffer
	if err := WriteCompressionProbeReports(&raw, whole, compressed); err != nil {
		t.Fatalf("запись отчётов: %v", err)
	}
	dec := json.NewDecoder(&raw)
	for i := 0; i < 2; i++ {
		var got CompressionProbeReport
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("строка %d не JSON: %v", i+1, err)
		}
	}
}
