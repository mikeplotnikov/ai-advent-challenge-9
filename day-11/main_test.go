package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

func TestRecallScoringSeparatesEveryVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, answer, want string
		forbidden          []string
		verdict            string
		pass               bool
	}{
		{"hit", `{"value":"EXP-5531"}`, "EXP-5531", []string{"EXP-0999"}, verdictHit, true},
		{"hit inside a sentence", `{"value":"код EXP-5531"}`, "EXP-5531", nil, verdictHit, true},
		{"number", `{"value":391}`, "391", nil, verdictHit, true},
		{"correct null", `{"value":null}`, "", nil, verdictCorrectNull, true},
		{"empty string is null", `{"value":""}`, "", nil, verdictCorrectNull, true},
		{"lost", `{"value":null}`, "DEC-0412", nil, verdictLost, false},
		{"wrong", `{"value":"DEC-0413"}`, "DEC-0412", nil, verdictWrong, false},
		{"fabricated", `{"value":"BUG-1000"}`, "", nil, verdictFabricated, false},
		{"leak beats hit", `{"value":"EXP-5531 или EXP-0999"}`, "EXP-5531", []string{"EXP-0999"}, verdictLeak, false},
		{"not json", `EXP-5531`, "EXP-5531", nil, verdictParseError, false},
		{"extra field", `{"value":"EXP-5531","note":"x"}`, "EXP-5531", nil, verdictParseError, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scoreRecall(tc.answer, tc.want, tc.forbidden)
			if got.Verdict != tc.verdict || got.Pass != tc.pass {
				t.Fatalf("scoreRecall(%s) = %+v, want %s pass=%v", tc.answer, got, tc.verdict, tc.pass)
			}
		})
	}
}

func TestBehaviourScoringCanPassAndFail(t *testing.T) {
	style, stack := behaviourProbes[0], behaviourProbes[1]
	for _, tc := range []struct {
		p      probe
		answer string
		pass   bool
	}{
		{style, "ИТОГ: DI — это передача зависимостей снаружи.", true},
		{style, "**ИТОГ:** коротко", true},
		{style, "DI — это... ИТОГ: передача зависимостей", false},
		{stack, "```go\npackage main\nimport \"net/http\"\n```", true},
		{stack, "package main\nimport (\"net/http\"; \"github.com/gin-gonic/gin\")", false},
		{stack, "from flask import Flask", false},
	} {
		if got := scoreBehaviour(tc.p, tc.answer); got != tc.pass {
			t.Errorf("%s %q = %v, want %v", tc.p.Name, tc.answer, got, tc.pass)
		}
	}
}

// The detector must be able to say "violation": a wire that carries a marker the arm
// excludes is flagged, and one that lacks a marker the arm includes is flagged too.
func TestSentViolationsFlagsBothDirections(t *testing.T) {
	none, full := recallArms[4], recallArms[0]
	if v := sentViolations("system:x\nuser:BUG-7781", expectedSent(familyRecall, none)); len(v) != 1 || v[0] != markerShort {
		t.Fatalf("excluded marker on the wire: %v", v)
	}
	if v := sentViolations("system:x", expectedSent(familyRecall, full)); len(v) != 4 {
		t.Fatalf("included markers missing from the wire: %v", v)
	}
}

func TestPlanHasThePreregisteredSize(t *testing.T) {
	cells := planCells(20, 30, false)
	if got, want := len(cells), 6*5*20+3*2*30; got != want {
		t.Fatalf("cells = %d, want %d", got, want)
	}
	if got := len(planCells(20, 30, true)); got != len(recallProbes) {
		t.Fatalf("pilot cells = %d", got)
	}
}

// fakeProvider answers recall questions from what the request actually carried, so
// the offline run exercises the whole path: fixture, injection, scoring and the wire
// check.
type fakeProvider struct{}

func (fakeProvider) AskWith(_ context.Context, messages []llm.Message, _ llm.Options) (llm.Answer, error) {
	var wire strings.Builder
	for _, m := range messages {
		wire.WriteString(m.Content)
	}
	text := wire.String()
	question := messages[len(messages)-1].Content
	value := "null"
	for q, marker := range map[string][]string{
		"тикета": {markerShort}, "выгрузки": {markerTask, markerOldTask}, "хранилищу": {markerDecision},
		"staging": {markerKnowledge}, "17*23": {"391"},
	} {
		if strings.Contains(question, q) {
			for _, m := range marker {
				if m == "391" || strings.Contains(text, "export_code: "+m) || (m != markerTask && m != markerOldTask && strings.Contains(text, m)) {
					value = fmt.Sprintf("%q", m)
				}
			}
		}
	}
	answer := fmt.Sprintf(`{"value":%s}`, value)
	if !strings.Contains(text, `{"value"`) {
		answer = "обычный ответ"
		if strings.Contains(text, markerStyle) {
			answer = "ИТОГ: ответ"
		}
	}
	return llm.Answer{Content: answer, Model: "deepseek-flash"}, nil
}

func TestOfflineRunSendsExactlyTheArmsLayersAndRendersAReport(t *testing.T) {
	fixture := t.TempDir()
	if err := buildFixture(fixture); err != nil {
		t.Fatal(err)
	}
	hash, err := treeHash(fixture)
	if err != nil {
		t.Fatal(err)
	}
	cells := planCells(2, 2, false)
	r := rows{plan: planRow{Kind: "plan", Run: "offline", Model: "deepseek-flash", RecallRepeats: 2, BehaviourRepeats: 2,
		RecallArms: recallArms, BehaviourArms: behaviourArms, RecallProbes: recallProbes, BehaviourProbes: behaviourProbes,
		Cells: len(cells), FixtureSHA256: hash}}
	for i, c := range cells {
		row := runCell(context.Background(), fakeProvider{}, "offline", fixture, "deepseek-flash", i, c)
		if row.Error != "" || len(row.SentViolations) > 0 {
			t.Fatalf("%s/%s: error %q, violations %v", c.Arm.Name, c.Probe.Name, row.Error, row.SentViolations)
		}
		if c.Family == familyRecall && !row.Pass {
			t.Fatalf("%s/%s: the honest fake failed: %+v", c.Arm.Name, c.Probe.Name, row)
		}
		r.probes = append(r.probes, row)
	}
	lifecycle, _, err := runLifecycle(context.Background(), fakeProvider{}, "deepseek-flash", "offline")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lifecycle {
		if !l.OK {
			t.Fatalf("E0 %s: found %v, expected %v", l.Checkpoint, l.Found, l.Expected)
		}
	}
	r.lifecycle = lifecycle
	after, _ := treeHash(fixture)
	r.complete = completeRow{Kind: "complete", Run: "offline", ProbeRows: len(cells), FixtureSHA256After: after}
	text, err := render(r, "offline.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| full | short | BUG-7781 |", "| none | short | null |", "full против no-long", "Повторных попыток не было"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report lacks %q:\n%s", want, text)
		}
	}

	retried := r
	retried.probes = append([]probeRow(nil), r.probes...)
	retried.probes[0].Attempts, retried.probes[0].RetryErrors = 2, []string{"вызов модели не удался: timeout"}
	text, err = render(retried, "offline.jsonl")
	if err != nil || !strings.Contains(text, "со второй попытки: 1") || !strings.Contains(text, "timeout") {
		t.Fatalf("a retried cell is not disclosed (%v):\n%s", err, text)
	}

	// Each refusal is reachable.
	for name, mutate := range map[string]func(*rows){
		"violation":               func(x *rows) { x.probes[0].SentViolations = []string{markerShort} },
		"pilot":                   func(x *rows) { x.plan.Pilot = true },
		"errors":                  func(x *rows) { x.complete.Errors = 1 },
		"fixture":                 func(x *rows) { x.complete.FixtureSHA256After = "other" },
		"short":                   func(x *rows) { x.probes = x.probes[1:] },
		"no plan":                 func(x *rows) { x.plan.Kind = "" },
		"no complete":             func(x *rows) { x.complete.Kind = "" },
		"complete of another run": func(x *rows) { x.complete.Run = "other" },
		"complete count":          func(x *rows) { x.complete.ProbeRows-- },
		"lifecycle missing":       func(x *rows) { x.lifecycle = x.lifecycle[1:] },
		"probe of another run":    func(x *rows) { x.probes[3].Run = "other" },
	} {
		copyRows := r
		copyRows.probes = append([]probeRow(nil), r.probes...)
		mutate(&copyRows)
		if _, err := render(copyRows, "x"); err == nil {
			t.Errorf("render accepted a run with %s", name)
		}
	}
}

func TestWriteRowsRoundTripsThroughReadRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rows.jsonl")
	value := "EXP-5531"
	in := []any{planRow{Kind: "plan", Run: "r"}, probeRow{Kind: "probe", Run: "r", Value: &value, SentViolations: []string{}}, completeRow{Kind: "complete", Run: "r"}}
	if err := writeRows(path, in, nil); err != nil {
		t.Fatal(err)
	}
	got, err := readRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.plan.Run != "r" || len(got.probes) != 1 || *got.probes[0].Value != value || got.complete.Kind != "complete" {
		t.Fatalf("round trip = %+v", got)
	}
	raw, _ := os.ReadFile(path)
	var first map[string]any
	if json.Unmarshal([]byte(strings.SplitN(string(raw), "\n", 2)[0]), &first) != nil || first["kind"] != "plan" {
		t.Fatal("first line is not the plan")
	}
}

// Once a live run is committed, RESULTS.md must be exactly what -report renders.
func TestCommittedResultsAreRenderedFromTheCommittedRun(t *testing.T) {
	want, err := os.ReadFile("RESULTS.md")
	if os.IsNotExist(err) {
		t.Skip("живого прогона ещё нет")
	}
	if err != nil {
		t.Fatal(err)
	}
	r, err := readRows("layers.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got, err := render(r, "day-11/layers.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatal("RESULTS.md не совпадает с тем, что строится из layers.jsonl — пересобери: go run ./day-11 -report day-11/layers.jsonl > day-11/RESULTS.md")
	}
}

var _ = agent.LayerShort

// emptyProvider answers every call with no text, as DeepSeek did on the arithmetic
// control in json_object mode.
type emptyProvider struct{ calls int }

func (e *emptyProvider) AskWith(context.Context, []llm.Message, llm.Options) (llm.Answer, error) {
	e.calls++
	return llm.Answer{Model: "deepseek-flash", FinishReason: "stop"}, fmt.Errorf("%w (finish_reason=stop)", llm.ErrEmptyContent)
}

type flakyProvider struct{ calls int }

func (f *flakyProvider) AskWith(ctx context.Context, m []llm.Message, o llm.Options) (llm.Answer, error) {
	f.calls++
	if f.calls == 1 {
		return llm.Answer{}, fmt.Errorf("сеть оборвалась")
	}
	return fakeProvider{}.AskWith(ctx, m, o)
}

// An empty answer is the model's outcome and is scored; only a failed call is retried.
func TestEmptyAnswersAreScoredNotRetriedAndFailedCallsAreRetriedOnce(t *testing.T) {
	fixture := t.TempDir()
	if err := buildFixture(fixture); err != nil {
		t.Fatal(err)
	}
	control := cell{familyRecall, recallArms[0], recallProbes[4], 1}
	empty := &emptyProvider{}
	row := runCell(context.Background(), empty, "r", fixture, "deepseek-flash", 0, control)
	if empty.calls != 1 || row.Attempts != 1 || row.Verdict != verdictEmpty || row.Pass || row.Error != "" {
		t.Fatalf("empty answer: calls %d, row %+v", empty.calls, row)
	}
	flaky := &flakyProvider{}
	row = runCell(context.Background(), flaky, "r", fixture, "deepseek-flash", 0, control)
	if flaky.calls != 2 || row.Attempts != 2 || len(row.RetryErrors) != 1 || row.Error != "" || !row.Pass {
		t.Fatalf("failed call: calls %d, row %+v", flaky.calls, row)
	}
}
