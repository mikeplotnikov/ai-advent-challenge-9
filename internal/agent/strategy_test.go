package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

func strategyAnswer(content string, prompt int) llm.Answer {
	return llm.Answer{
		Content: content,
		Model:   "deepseek-v4-flash",
		Usage: llm.Usage{
			PromptTokens:          prompt,
			CompletionTokens:      4,
			TotalTokens:           prompt + 4,
			PromptCacheMissTokens: prompt,
		},
	}
}

func TestDay10StrategiesRejectSummaryAndAmbiguousWindows(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"unknown", Config{ContextStrategy: "magic"}},
		{"summary", Config{ContextStrategy: ContextSliding, WindowMessages: 4, KeepLastMessages: 10}},
		{"odd window", Config{ContextStrategy: ContextFacts, WindowMessages: 3}},
		{"branch window", Config{ContextStrategy: ContextBranching, WindowMessages: 4}},
		{"double trim", Config{ContextStrategy: ContextSliding, WindowMessages: 4, MaxContextTokens: 100, OnOverflow: OverflowTrim}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(&fakeCaller{}, test.cfg); err == nil {
				t.Fatal("New accepted an invalid strategy configuration")
			}
		})
	}
}

func TestSlidingWindowKeepsExactlyTheNewestRawMessages(t *testing.T) {
	f := &fakeCaller{}
	a := newAgent(t, Config{
		SystemPrompt:    "роль",
		ContextStrategy: ContextSliding,
		WindowMessages:  4,
	}, f)
	for _, input := range []string{"первый", "второй", "третий", "четвёртый"} {
		if _, err := a.Ask(context.Background(), input); err != nil {
			t.Fatalf("Ask(%q): %v", input, err)
		}
	}

	sent := f.sent[3]
	if got, want := roles(sent), "system,user,assistant,user,assistant,user"; got != want {
		t.Fatalf("roles = %s, want %s", got, want)
	}
	joined := messageText(sent)
	if strings.Contains(joined, "первый") || strings.Contains(joined, "ответ 1") {
		t.Fatalf("oldest exchange survived sliding window: %s", joined)
	}
	for _, want := range []string{"второй", "ответ 2", "третий", "ответ 3", "четвёртый"} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent stack lost %q: %s", want, joined)
		}
	}
	if got := a.StrategyState().RawMessages; got != 4 {
		t.Fatalf("stored raw messages = %d, want 4", got)
	}
}

func TestStickyFactsUpdatesBeforeAnswerAndCountsItsCall(t *testing.T) {
	f := &fakeCaller{answers: []llm.Answer{
		strategyAnswer(`{"facts":{"budget":"450000 RUB"}}`, 30),
		strategyAnswer("принято", 20),
		strategyAnswer(`{"facts":{"budget":"500000 RUB","deadline":"2026-10-21"}}`, 40),
		strategyAnswer("обновлено", 25),
	}}
	a := newAgent(t, Config{
		SystemPrompt:    "роль",
		Model:           "deepseek-v4-flash",
		ContextStrategy: ContextFacts,
		WindowMessages:  4,
	}, f)

	first, err := a.Ask(context.Background(), "Бюджет 450000 RUB")
	if err != nil {
		t.Fatal(err)
	}
	if first.FactUsage.PromptTokens != 30 {
		t.Fatalf("fact usage = %+v", first.FactUsage)
	}
	if f.opts[0].ResponseFormat != "json_object" || f.opts[0].Thinking != "disabled" {
		t.Fatalf("fact extractor options = %+v", f.opts[0])
	}
	if !strings.Contains(f.sent[1][0].Content, `"budget":"450000 RUB"`) {
		t.Fatalf("answer did not receive updated facts: %+v", f.sent[1])
	}

	if _, err := a.Ask(context.Background(), "Исправление: бюджет 500000 RUB, срок 2026-10-21"); err != nil {
		t.Fatal(err)
	}
	state := a.StrategyState()
	if state.Facts["budget"] != "500000 RUB" || state.Facts["deadline"] != "2026-10-21" {
		t.Fatalf("facts = %#v", state.Facts)
	}
	if state.FactSpend.Calls != 2 || a.Totals().Calls != 4 {
		t.Fatalf("fact spend = %+v, totals = %+v", state.FactSpend, a.Totals())
	}
	if !strings.Contains(messageText(f.sent[2]), `"budget":"450000 RUB"`) {
		t.Fatalf("second extractor did not receive previous facts: %s", messageText(f.sent[2]))
	}
}

func TestStickyFactsAndTheirSpendSurviveRestart(t *testing.T) {
	store := storeIn(t)
	cfg := Config{ContextStrategy: ContextFacts, WindowMessages: 4, Store: store, Model: "deepseek-v4-flash"}
	a := newAgent(t, cfg, &fakeCaller{answers: []llm.Answer{
		strategyAnswer(`{"facts":{"budget":"450000 RUB"}}`, 30),
		strategyAnswer("принято", 20),
	}})
	if _, err := a.Ask(context.Background(), "Бюджет 450000 RUB"); err != nil {
		t.Fatal(err)
	}

	restarted := newAgent(t, Config{
		ContextStrategy: ContextFacts, WindowMessages: 4,
		Store: NewFileStore(store.Path()), Model: "deepseek-v4-flash",
	}, &fakeCaller{})
	state := restarted.StrategyState()
	if state.Facts["budget"] != "450000 RUB" || state.FactSpend.Calls != 1 || restarted.Totals().Calls != 2 {
		t.Fatalf("restored facts state = %+v, totals = %+v", state, restarted.Totals())
	}
}

func TestInvalidFactUpdateDoesNotReachAnswerOrChangeHistory(t *testing.T) {
	f := &fakeCaller{answers: []llm.Answer{strategyAnswer(`{"facts":[]}`, 30)}}
	a := newAgent(t, Config{ContextStrategy: ContextFacts, WindowMessages: 4}, f)

	reply, err := a.Ask(context.Background(), "Бюджет 10")
	if !errors.Is(err, ErrFactUpdate) {
		t.Fatalf("error = %v, want ErrFactUpdate", err)
	}
	if f.calls != 1 {
		t.Fatalf("provider calls = %d, answer call must not run", f.calls)
	}
	if !reply.FactAttempted || reply.FactUsage.PromptTokens != 30 || a.Totals().Failed != 1 {
		t.Fatalf("reply = %+v totals = %+v", reply, a.Totals())
	}
	if a.Turns() != 0 || a.StrategyState().RawMessages != 0 || len(a.StrategyState().Facts) != 0 {
		t.Fatalf("failed extraction changed state: %+v", a.StrategyState())
	}
}

func TestFactUsageRemainsVisibleWhenAnswerPreflightRefuses(t *testing.T) {
	store := storeIn(t)
	f := &fakeCaller{answers: []llm.Answer{
		strategyAnswer(`{"facts":{"large":"значение"}}`, 30),
	}}
	a := newAgent(t, Config{
		ContextStrategy: ContextFacts, WindowMessages: 4, MaxContextTokens: 1,
		OnOverflow: OverflowRefuse, Store: store, Model: "deepseek-v4-flash",
	}, f)

	reply, err := a.Ask(context.Background(), "новый факт")
	if !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("error = %v, want ErrContextOverflow", err)
	}
	if reply.Estimated.Facts == 0 || !strings.Contains(err.Error(), "+ facts ") {
		t.Fatalf("refusal concealed facts contribution: estimate=%+v error=%v", reply.Estimated, err)
	}
	if f.calls != 1 || !reply.FactAttempted || reply.FactUsage.PromptTokens != 30 || a.Totals().Calls != 1 {
		t.Fatalf("hidden fact call: calls=%d reply=%+v totals=%+v", f.calls, reply, a.Totals())
	}
	restarted := newAgent(t, Config{
		ContextStrategy: ContextFacts, WindowMessages: 4, MaxContextTokens: 1,
		OnOverflow: OverflowRefuse, Store: NewFileStore(store.Path()), Model: "deepseek-v4-flash",
	}, &fakeCaller{})
	if restarted.StrategyState().Facts["large"] != "значение" || restarted.Totals().Calls != 1 {
		t.Fatalf("billed fact update did not persist: state=%+v totals=%+v", restarted.StrategyState(), restarted.Totals())
	}
}

func TestBranchingForksOneCheckpointWithoutCrossBranchLeakage(t *testing.T) {
	store := storeIn(t)
	cfg := Config{ContextStrategy: ContextBranching, Store: store, Model: "deepseek-v4-flash"}
	f := &fakeCaller{}
	a := newAgent(t, cfg, f)
	if _, err := a.Ask(context.Background(), "общий срок 21 октября"); err != nil {
		t.Fatal(err)
	}
	if err := a.Checkpoint("base"); err != nil {
		t.Fatal(err)
	}
	if err := a.Fork("variant-a", "base"); err != nil {
		t.Fatal(err)
	}
	if err := a.Fork("variant-b", "base"); err != nil {
		t.Fatal(err)
	}
	if err := a.Switch("variant-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ask(context.Background(), "SSO и Telegram"); err != nil {
		t.Fatal(err)
	}
	if err := a.Switch("variant-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ask(context.Background(), "пароль и email"); err != nil {
		t.Fatal(err)
	}
	if err := a.Switch("variant-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ask(context.Background(), "перечисли решения этой ветки"); err != nil {
		t.Fatal(err)
	}
	last := messageText(f.sent[len(f.sent)-1])
	if !strings.Contains(last, "общий срок") || !strings.Contains(last, "SSO и Telegram") {
		t.Fatalf("branch A lost its context: %s", last)
	}
	if strings.Contains(last, "пароль и email") {
		t.Fatalf("branch B leaked into branch A: %s", last)
	}

	// A new process over the same JSON must restore the active branch and both
	// inactive branches, not flatten them into one conversation.
	restarted := newAgent(t, cfg, &fakeCaller{})
	state := restarted.StrategyState()
	if state.ActiveBranch != "variant-a" || strings.Join(state.Branches, ",") != "main,variant-a,variant-b" {
		t.Fatalf("restored branching state = %+v", state)
	}
	if err := restarted.Switch("variant-b"); err != nil {
		t.Fatal(err)
	}
	check := &fakeCaller{}
	restarted.client = check
	if _, err := restarted.Ask(context.Background(), "что выбрано здесь?"); err != nil {
		t.Fatal(err)
	}
	joined := messageText(check.sent[0])
	if !strings.Contains(joined, "пароль и email") || strings.Contains(joined, "SSO и Telegram") {
		t.Fatalf("restored branch B is not isolated: %s", joined)
	}
	if err := restarted.Fork("variant-c", "base"); err != nil {
		t.Fatalf("restored checkpoint is not reusable: %v", err)
	}
	if err := restarted.Switch("variant-c"); err != nil {
		t.Fatal(err)
	}
	checkpointCall := &fakeCaller{}
	restarted.client = checkpointCall
	if _, err := restarted.Ask(context.Background(), "что было общим?"); err != nil {
		t.Fatal(err)
	}
	checkpointContext := messageText(checkpointCall.sent[0])
	if !strings.Contains(checkpointContext, "общий срок") || strings.Contains(checkpointContext, "SSO и Telegram") || strings.Contains(checkpointContext, "пароль и email") {
		t.Fatalf("restored checkpoint leaked a branch: %s", checkpointContext)
	}
}

func TestPersistedStrategyCannotBeSilentlyReinterpreted(t *testing.T) {
	store := storeIn(t)
	a := newAgent(t, Config{ContextStrategy: ContextSliding, WindowMessages: 4, Store: store}, &fakeCaller{})
	if _, err := a.Ask(context.Background(), "первый"); err != nil {
		t.Fatal(err)
	}
	if _, err := New(&fakeCaller{}, Config{ContextStrategy: ContextFacts, WindowMessages: 4, Store: NewFileStore(store.Path())}); err == nil {
		t.Fatal("facts mode accepted a sliding snapshot")
	}
}

func TestFactsParserRejectsTrailingData(t *testing.T) {
	if _, err := parseFacts(`{"facts":{"x":"y"}} {"facts":{}}`); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}

func TestBranchingSnapshotRejectsInvalidActiveName(t *testing.T) {
	snap := Snapshot{
		Version: SnapshotVersion, Turns: 0, Strategy: ContextBranching,
		ActiveBranch: strings.Repeat("x", maxBranchNameRunes+1),
	}
	if err := validate(snap); err == nil {
		t.Fatal("invalid active branch name was accepted")
	}
}

func messageText(messages []llm.Message) string {
	var b strings.Builder
	for _, message := range messages {
		b.WriteString(message.Content)
		b.WriteByte('\n')
	}
	return b.String()
}
