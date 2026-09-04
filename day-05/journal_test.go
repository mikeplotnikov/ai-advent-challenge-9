package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

func tempJournal(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestJournalCountsWhatEachCellAlreadyHolds(t *testing.T) {
	path := tempJournal(t,
		`{"task":"T1","rung":"qwen3:1.7b","correct":true,"parsed":true}`,
		`{"task":"T1","rung":"qwen3:1.7b","correct":false,"parsed":true}`,
		`{"task":"T1","rung":"deepseek-v4-pro","correct":true,"parsed":true}`,
		``, // a blank line must not count as a call
	)
	j, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()

	if got := j.done("T1", "qwen3:1.7b"); got != 2 {
		t.Errorf("в клетке T1/1.7b учтено %d вызовов, ожидалось 2", got)
	}
	if got := j.done("T1", "deepseek-v4-pro"); got != 1 {
		t.Errorf("в клетке T1/pro учтено %d, ожидалось 1", got)
	}
	if got := j.done("T2", "qwen3:1.7b"); got != 0 {
		t.Errorf("в пустой клетке учтено %d, ожидалось 0", got)
	}
}

func TestJournalRefusesToStartFreshOnACorruptFile(t *testing.T) {
	// The whole point of the journal is that four hours of measurement survive an
	// interruption. Treating an unreadable journal as "no journal" would throw them
	// away silently, which is the failure it exists to prevent.
	path := tempJournal(t,
		`{"task":"T1","rung":"qwen3:1.7b","correct":true}`,
		`{это не json`,
	)
	if _, err := openJournal(path); err == nil {
		t.Fatal("битый журнал принят молча")
	} else if !strings.Contains(err.Error(), "строка 2") {
		t.Errorf("ошибка не называет строку: %v", err)
	}
}

func TestJournalCreatesTheFileWhenThereIsNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(path)
	if err != nil {
		t.Fatalf("новый журнал не открылся: %v", err)
	}
	defer j.close()
	if got := j.done("T1", "qwen3:1.7b"); got != 0 {
		t.Errorf("в новом журнале уже %d вызовов", got)
	}
}

func TestAppendedCallSurvivesAndIsCountedOnReopen(t *testing.T) {
	// Written and flushed per call, because what has to survive is a kill, not a
	// clean exit.
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 4, 20, 0, 0, 0, time.UTC)
	o := outcome{
		at: at, elapsed: 2500 * time.Millisecond,
		usage:   llm.Usage{PromptTokens: 40, PromptCacheMissTokens: 40, CompletionTokens: 120},
		correct: true, parsed: true, raw: "ANSWER: 24",
	}
	if err := j.append("T1", "qwen3:1.7b", o); err != nil {
		t.Fatal(err)
	}
	// Deliberately not closed: a paused run is killed, it does not close its files.
	reopened, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	if got := reopened.done("T1", "qwen3:1.7b"); got != 1 {
		t.Fatalf("после перезапуска учтено %d вызовов, ожидался 1", got)
	}

	run, warnings, err := loadRun(path, tasks(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("неожиданные предупреждения: %v", warnings)
	}
	rung, _ := rungByID("qwen3:1.7b")
	task := tasks()[1]
	restored := run.cell(task, rung)
	if len(restored.outcomes) != 1 {
		t.Fatalf("восстановлено %d вызовов", len(restored.outcomes))
	}
	got := restored.outcomes[0]
	if !got.correct || !got.parsed || got.raw != "ANSWER: 24" {
		t.Errorf("вызов восстановлен искажённым: %+v", got)
	}
	if got.elapsed != 2500*time.Millisecond {
		t.Errorf("время восстановлено как %v, ожидалось 2.5s", got.elapsed)
	}
	if !got.at.Equal(at) {
		t.Errorf("момент вызова восстановлен как %v, ожидался %v", got.at, at)
	}
	// The instant matters beyond bookkeeping: it is what prices the call, because the
	// provider's rates change by the hour. Pricing a local rung at all needs the day
	// to have declared it free first — an ordering main() satisfies before any branch,
	// and which this reproduces rather than assumes.
	registerFreePrices()
	if _, ok := llm.CostAt(rung.id, got.usage, got.at); !ok {
		t.Error("восстановленный вызов не оценивается")
	}
}

func TestLoadRunDropsEntriesTheGridNoLongerDefines(t *testing.T) {
	// A journal written before the grid changed describes a different question.
	// Mixing the two would compare answers to different prompts, so those entries
	// are dropped — and said out loud, because a silent drop looks like a short run.
	path := tempJournal(t,
		`{"task":"T1","rung":"qwen3:1.7b","correct":true,"parsed":true}`,
		`{"task":"T9","rung":"qwen3:1.7b","correct":true,"parsed":true}`,
		`{"task":"T1","rung":"llama-imaginary","correct":true,"parsed":true}`,
	)
	run, warnings, err := loadRun(path, tasks(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if got := countOutcomes(run); got != 1 {
		t.Errorf("восстановлено %d вызовов, ожидался 1", got)
	}
	if len(warnings) != 2 {
		t.Fatalf("предупреждений %d, ожидалось 2: %v", len(warnings), warnings)
	}
	for _, w := range warnings {
		if !strings.Contains(w, "отброшено") {
			t.Errorf("предупреждение не говорит об отбрасывании: %q", w)
		}
	}
}

func TestLoadRunKeepsFailedCallsAsMeasurements(t *testing.T) {
	// A call that failed is data: it is counted as a failure and its usage is still
	// billed. Dropping it on resume would make the run look cleaner than it was and
	// would re-call something the provider already charged for.
	path := tempJournal(t,
		`{"task":"T1","rung":"deepseek-v4-pro","error":"API вернул 402","usage":{"prompt_tokens":40,"completion_tokens":4000}}`,
	)
	run, _, err := loadRun(path, tasks(), 30)
	if err != nil {
		t.Fatal(err)
	}
	rung, _ := rungByID("deepseek-v4-pro")
	c := run.cell(tasks()[1], rung)
	_, _, _, _, failed := c.counts()
	if failed != 1 {
		t.Errorf("провалов учтено %d, ожидался 1", failed)
	}
	if c.measured() {
		t.Error("клетка с одним провалившимся вызовом объявлена измеренной")
	}
	if cost, known := c.cost(); !known || cost <= 0 {
		t.Errorf("провалившийся вызов не оценён: %f (known=%v)", cost, known)
	}
}

func TestLoadRunRecoversTheRunWindowFromTheEntries(t *testing.T) {
	path := tempJournal(t,
		`{"task":"T1","rung":"qwen3:1.7b","at":"2026-09-04T20:10:00Z","correct":true,"parsed":true}`,
		`{"task":"T1","rung":"qwen3:1.7b","at":"2026-09-04T19:00:00Z","correct":true,"parsed":true}`,
		`{"task":"T1","rung":"qwen3:1.7b","at":"2026-09-04T21:30:00Z","correct":true,"parsed":true}`,
	)
	run, _, err := loadRun(path, tasks(), 30)
	if err != nil {
		t.Fatal(err)
	}
	// Entries are not ordered by time: a resumed run appends after an earlier one,
	// and taking the first and last lines would report the window backwards.
	if want := time.Date(2026, 9, 4, 19, 0, 0, 0, time.UTC); !run.startedAt.Equal(want) {
		t.Errorf("начало прогона %v, ожидалось %v", run.startedAt, want)
	}
	if want := time.Date(2026, 9, 4, 21, 30, 0, 0, time.UTC); !run.finishedAt.Equal(want) {
		t.Errorf("конец прогона %v, ожидался %v", run.finishedAt, want)
	}
}
