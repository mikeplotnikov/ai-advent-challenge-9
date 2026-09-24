package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/watch"
)

type schemaModel struct {
	calls       atomic.Int32
	differences []int
}

func (m *schemaModel) Converse(_ context.Context, _ []llm.ToolMessage, opts llm.Options) (llm.Answer, error) {
	call := int(m.calls.Add(1)) - 1
	pair := call / 2
	prompt := 100
	if len(opts.Tools) > 0 {
		prompt += m.differences[pair]
	}
	return llm.Answer{Content: "ответ", Model: "deepseek-flash", FinishReason: "stop", Usage: llm.Usage{PromptTokens: prompt, CompletionTokens: 2}, ToolCalls: []llm.ToolCall{{ID: "ignored", Function: llm.ToolCallFunction{Name: "get_watch_summary", Arguments: `{}`}}}}, nil
}

func TestSchemaCostSixFirstTurnsAndMismatch(t *testing.T) {
	session := &fakeSession{}
	model := &schemaModel{differences: []int{400, 400, 400}}
	result, err := measureSchemaCost(context.Background(), session, model, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if model.calls.Load() != 6 || len(result.Differences) != 3 || result.Differences[0] != 400 || result.Mismatch || session.calls != 0 {
		t.Fatalf("calls=%d result=%+v toolcalls=%d", model.calls.Load(), result, session.calls)
	}
	mismatch, err := measureSchemaCost(context.Background(), &fakeSession{}, &schemaModel{differences: []int{400, 401, 400}}, time.Now())
	if err != nil || !mismatch.Mismatch {
		t.Fatalf("result=%+v err=%v", mismatch, err)
	}
}

func TestSchemaCommandForcesTemporaryStore(t *testing.T) {
	temp := "/tmp/day18-schema/store.json"
	if got := schemaCommand("day18-mcp -store working.json", temp); got != "day18-mcp -store working.json -store "+temp {
		t.Fatalf("command=%q", got)
	}
	if got := schemaCommand("", temp); got != "go run ./day-18/mcp-server -store "+temp {
		t.Fatalf("default command=%q", got)
	}
}

func TestSchemaCostModeUsesTemporaryStoreAndWritesResult(t *testing.T) {
	binary := buildMCPServer(t)
	dir := t.TempDir()
	working := filepath.Join(dir, "working-store.json")
	if err := os.WriteFile(working, []byte(`{"sentinel":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(working)
	output := filepath.Join(dir, "schema-cost.json")
	model := &schemaModel{differences: []int{400, 400, 400}}
	usedStore := ""
	commandForStore := func(store string) string { usedStore = store; return binary + " -store " + store }
	var stdout, stderr bytes.Buffer
	code := executeSchemaCost(10*time.Second, &stdout, &stderr, model, commandForStore, output, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC))
	after, _ := os.ReadFile(working)
	if code != 0 || model.calls.Load() != 6 || usedStore == "" || usedStore == working || !bytes.Equal(before, after) {
		t.Fatalf("code=%d calls=%d store=%q stderr=%s", code, model.calls.Load(), usedStore, stderr.String())
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var result SchemaCost
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Pairs) != 3 || result.Differences[0] != 400 || result.ToolCount != 4 {
		t.Fatalf("%+v", result)
	}
	if _, err := os.Stat(usedStore); !os.IsNotExist(err) {
		t.Fatalf("empty temporary store was unexpectedly written: %v", err)
	}
}

func TestReportCoverageSchemaAndReproducibility(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")
	digestsPath := filepath.Join(dir, "digests.json")
	schemaPath := filepath.Join(dir, "schema-cost.json")
	store := watch.NewStore(storePath)
	first := time.Date(2026, 9, 24, 0, 59, 0, 0, time.UTC)
	poll := watch.Poll{At: first.Format(time.RFC3339), OK: true, RatesDate: "2026-09-24", Rates: map[string]float64{"USD": 80}}
	created, err := store.Create([]string{"USD"}, 60, first, poll)
	if err != nil {
		t.Fatal(err)
	}
	failureAt := first.Add(time.Hour)
	_, _, err = store.AppendPollIfDue(created.ID, failureAt, watch.Poll{At: failureAt.Format(time.RFC3339), OK: false, Error: "ЦБ недоступен"})
	if err != nil {
		t.Fatal(err)
	}
	secondPollAt := first.Add(2 * time.Hour)
	_, _, err = store.AppendPollIfDue(created.ID, secondPollAt, watch.Poll{At: secondPollAt.Format(time.RFC3339), OK: true, RatesDate: "2026-09-25", Rates: map[string]float64{"USD": 81}})
	if err != nil {
		t.Fatal(err)
	}
	digests := []Digest{
		{At: first.Format(time.RFC3339), DigestEvery: "3h0m0s", ModelCalls: 2, Tokens: DigestTokens{Prompt: 100, Cached: 20, Output: 10, PerCall: []TokenCall{{60, 10, 2}, {40, 10, 8}}}, Cost: .001, CostKnown: true, Checks: DigestChecks{CalledSummary: true, QuotesLastRates: true}},
		{At: first.Add(3 * time.Hour).Format(time.RFC3339), DigestEvery: "6h0m0s", ModelCalls: 1, Error: "модель недоступна", Tokens: DigestTokens{Prompt: 50, Output: 5, PerCall: []TokenCall{{50, 0, 5}}}, Cost: .002, CostKnown: true},
	}
	if err := writeDigests(digestsPath, digests); err != nil {
		t.Fatal(err)
	}
	until := time.Date(2026, 9, 24, 4, 29, 0, 0, time.UTC)
	firstReport := filepath.Join(dir, "one.md")
	secondReport := filepath.Join(dir, "two.md")
	if err := writeReport(firstReport, storePath, digestsPath, schemaPath, until); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(firstReport)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"## Покрытие 24/7", "2 / 4", "| 1 | 2h0m0s |", "2026-09-24", "2026-09-25", "Всего: 2; с ошибкой: 1", "Успешно вызван `get_watch_summary`: 1/2", "Текст цитирует последние курсы: 1/2", "Среднее на сводку: 75.0 prompt, 10.0 cached, 7.5 output", "Ход 1: 9.1%", "Ход 2: 25.0%", "Средняя известная цена: $0.001500; суммарная: $0.003000", "Цена суток для периода 3h0m0s: $0.008000", "Цена суток для периода 6h0m0s: $0.008000", "## Сводки", "## Экономика", "Цена схем не измерена", until.UTC().Format(time.RFC3339), "store sha256:", "digests sha256:"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	if err := writeJSONAtomic(schemaPath, SchemaCost{Differences: []int{400, 400, 400}}); err != nil {
		t.Fatal(err)
	}
	if err := writeReport(firstReport, storePath, digestsPath, schemaPath, until); err != nil {
		t.Fatal(err)
	}
	if err := writeReport(secondReport, storePath, digestsPath, schemaPath, until); err != nil {
		t.Fatal(err)
	}
	one, _ := os.ReadFile(firstReport)
	two, _ := os.ReadFile(secondReport)
	if !bytes.Equal(one, two) || !bytes.Contains(one, []byte("Токенов схем на ход: 400")) || !bytes.Contains(one, []byte("1200 токенов")) {
		t.Fatalf("not reproducible/schema missing:\n%s", one)
	}
	if bytes.Contains(one, []byte("расходятся")) {
		t.Fatalf("agreeing measurements reported as a mismatch:\n%s", one)
	}
	// Three disagreeing schema measurements must be reported as such, not shown as a stable
	// number (test review, wave 2: the warning branch was never exercised).
	if err := writeJSONAtomic(schemaPath, SchemaCost{Differences: []int{400, 401, 400}, Mismatch: true}); err != nil {
		t.Fatal(err)
	}
	if err := writeReport(firstReport, storePath, digestsPath, schemaPath, until); err != nil {
		t.Fatal(err)
	}
	if mismatch, _ := os.ReadFile(firstReport); !bytes.Contains(mismatch, []byte("Три измерения расходятся")) {
		t.Fatalf("mismatch not reported:\n%s", mismatch)
	}
	currentPoll := time.Date(2026, 9, 24, 0, 10, 0, 0, time.UTC)
	short := coverage(watch.Watch{ID: "current", EveryMinutes: 60, Polls: []watch.Poll{{At: currentPoll.Format(time.RFC3339), OK: true}}}, currentPoll.Add(10*time.Minute))
	if short.Expected != 0 {
		t.Fatalf("current slot counted: %+v", short)
	}
	stoppedAt := time.Date(2026, 9, 24, 2, 30, 0, 0, time.UTC)
	stopped := coverage(watch.Watch{ID: "stopped", EveryMinutes: 60, StoppedAt: stoppedAt.Format(time.RFC3339), Polls: []watch.Poll{{At: time.Date(2026, 9, 24, 0, 10, 0, 0, time.UTC).Format(time.RFC3339), OK: true}, {At: time.Date(2026, 9, 24, 1, 10, 0, 0, time.UTC).Format(time.RFC3339), OK: true}}}, until)
	if stopped.Expected != 2 || stopped.Covered != 2 {
		t.Fatalf("stopped boundary: %+v", stopped)
	}
}

func TestDigestCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "digests.json")
	items := make([]Digest, 200)
	for i := range items {
		items[i] = Digest{At: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * 3 * time.Hour).Format(time.RFC3339)}
	}
	if err := writeDigests(path, items); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	result := issueDigest(context.Background(), &fakeSession{}, &digestLLM{}, path, 3*time.Hour, 1, time.Now, &stdout, &stderr)
	if result.err != nil || result.busy || result.already {
		t.Fatalf("result=%+v stderr=%s", result, stderr.String())
	}
	loaded, _ := readDigests(path)
	if len(loaded) != 200 || loaded[0].At != items[1].At {
		t.Fatalf("len=%d first=%s", len(loaded), loaded[0].At)
	}
}

func TestFailedDigestOccupiesItsSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "digests.json")
	now := time.Date(2026, 9, 24, 1, 0, 0, 0, time.UTC)
	model := &digestLLM{fail: true}
	var stdout, stderr bytes.Buffer
	first := issueDigest(context.Background(), &fakeSession{}, model, path, 3*time.Hour, 1, func() time.Time { return now }, &stdout, &stderr)
	if first.err == nil {
		t.Fatalf("first=%+v", first)
	}
	calls := model.calls.Load()
	stdout.Reset()
	stderr.Reset()
	second := issueDigest(context.Background(), &fakeSession{}, model, path, 3*time.Hour, 1, func() time.Time { return now.Add(time.Hour) }, &stdout, &stderr)
	if !second.already || second.err != nil || model.calls.Load() != calls {
		t.Fatalf("second=%+v calls=%d/%d", second, model.calls.Load(), calls)
	}
	digests, err := readDigests(path)
	if err != nil || len(digests) != 1 || digests[0].Error == "" {
		t.Fatalf("digests=%+v err=%v", digests, err)
	}
}

func TestSampleIsDeterministic(t *testing.T) {
	first, second := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	if err := writeSampleData(first, sampleOptions(t)); err != nil {
		t.Fatal(err)
	}
	if err := writeSampleData(second, sampleOptions(t)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"store.json", "digests.json", "tools.json", "questions.json"} {
		one, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatalf("first %s: %v", name, err)
		}
		two, err := os.ReadFile(filepath.Join(second, name))
		if err != nil {
			t.Fatalf("second %s: %v", name, err)
		}
		if !bytes.Equal(one, two) {
			t.Errorf("%s differs", name)
		}
	}
}

func TestSampleContainsGuestToolsAndQuestionOutcomes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sample")
	if err := writeSampleData(dir, sampleOptions(t)); err != nil {
		t.Fatal(err)
	}
	state, err := watch.NewStore(filepath.Join(dir, "store.json")).Read()
	if err != nil || len(state.Watches) != 1 || !state.Watches[0].Guest || state.Watches[0].ExpiresAt == "" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	created, _ := time.Parse(time.RFC3339, state.Watches[0].CreatedAt)
	expires, _ := time.Parse(time.RFC3339, state.Watches[0].ExpiresAt)
	if expires.Sub(created) != 24*time.Hour {
		t.Fatalf("created=%s expires=%s", created, expires)
	}
	digests, err := readDigests(filepath.Join(dir, "digests.json"))
	if err != nil || len(digests) != 1 {
		t.Fatalf("digests=%+v err=%v", digests, err)
	}
	toolsRaw, err := os.ReadFile(filepath.Join(dir, "tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tools ToolsSnapshot
	if err := json.Unmarshal(toolsRaw, &tools); err != nil || tools.Server.Name != "cbr-watch" || len(tools.Tools) != 4 {
		t.Fatalf("tools=%+v err=%v", tools, err)
	}
	records, err := readQuestions(filepath.Join(dir, "questions.json"))
	if err != nil || len(records) != 2 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	if records[0].Answer == "" || records[0].Error != "" || len(records[0].ToolCalls) == 0 || len(records[0].Tokens.PerCall) == 0 {
		t.Fatalf("successful record=%+v", records[0])
	}
	if records[1].Error == "" || records[1].Answer != "" {
		t.Fatalf("error record=%+v", records[1])
	}
}

func TestSampleMatchesTheShowcaseCopy(t *testing.T) {
	generated := filepath.Join(t.TempDir(), "sample")
	if err := writeSampleData(generated, sampleOptions(t)); err != nil {
		t.Fatal(err)
	}
	// Relative to the package directory, as in day 17: the showcase repository sits next to this one.
	const copyDir = "../../uchebnik-ai-advent/challeng/test/day18-sample"
	if _, err := os.Stat(copyDir); err != nil {
		t.Skipf("копии витрины нет рядом (%v)", err)
	}
	for _, name := range []string{"store.json", "digests.json", "tools.json", "questions.json"} {
		got, _ := os.ReadFile(filepath.Join(generated, name))
		want, err := os.ReadFile(filepath.Join(copyDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s разошёлся с копией витрины", name)
		}
	}
}

func sampleOptions(t *testing.T) cbr.Options {
	t.Helper()
	first, err := ratesmcp.Fixtures.ReadFile("fixtures/2026-09-01.xml")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ratesmcp.Fixtures.ReadFile("fixtures/2026-09-20-weekend.xml")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	return cbr.Options{Fetcher: cbr.FetchFunc(func(context.Context, string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	})}
}

type errorModel struct{}

func (errorModel) Converse(context.Context, []llm.ToolMessage, llm.Options) (llm.Answer, error) {
	return llm.Answer{}, errors.New("unused")
}

var _ = json.Valid
