package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// The instrument is checked before it is used. Every criterion carries fixtures it
// must accept and fixtures it must reject, and a criterion that cannot do both is not
// a measurement — it is a constant wearing a measurement's clothes.
func TestCriteriaAreCalibrated(t *testing.T) {
	for _, c := range criteria {
		if len(c.Accept) == 0 || len(c.Reject) == 0 {
			t.Errorf("%s: нужны фикстуры и на срабатывание, и на молчание", c.Name)
		}
		for _, s := range c.Accept {
			if !c.Test(s) {
				t.Errorf("%s: должен был сработать на %q", c.Name, s)
			}
		}
		for _, s := range c.Reject {
			if c.Test(s) {
				t.Errorf("%s: должен был промолчать на %q", c.Name, s)
			}
		}
	}
	// Every criterion a probe names must exist, or a cell would be scored by nothing.
	for _, list := range [][]probe{behaviourProbes, conflictProbes, pipelineProbes} {
		for _, p := range list {
			if len(p.Criteria) == 0 {
				t.Errorf("проба %s/%s без критериев", p.Family, p.Name)
			}
			for _, name := range p.Criteria {
				if _, ok := criterionByName(name); !ok {
					t.Errorf("проба %s/%s ссылается на несуществующий критерий %q", p.Family, p.Name, name)
				}
			}
		}
	}
}

// The plan is pre-registered: its size is a fact of the design, not of whatever the
// code happens to produce today. This test is what makes a silent change of N visible.
func TestPlanHasThePreregisteredSize(t *testing.T) {
	cells := planCells(false)
	want := map[string]int{
		// 4 arms × (30 explain + 30 code + 20 control)
		familyBehaviour: 4 * 80,
		// 2 arms × 20
		familyConflict: 2 * 20,
		// 2 arms × 20
		familyPipeline: 2 * 20,
	}
	got := map[string]int{}
	for _, c := range cells {
		got[c.Family]++
	}
	for family, n := range want {
		if got[family] != n {
			t.Errorf("%s: клеток %d, предрегистрировано %d", family, got[family], n)
		}
	}
	if len(cells) != 320+40+40 {
		t.Fatalf("всего клеток %d", len(cells))
	}
	// The pilot must be small and must never be mistaken for the run.
	// One cell per probe: three behaviour probes, the conflict probe and the pipeline probe.
	if pilot := planCells(true); len(pilot) != 5 {
		t.Fatalf("пилот из %d клеток", len(pilot))
	}
}

// Each profile must be distinguishable in the bytes of a request, or "this arm sent no
// profile" would be an assumption rather than an observation.
func TestEveryProfileMarkerIsItsOwnAndPresentInItsFixture(t *testing.T) {
	for _, f := range profileFixtures {
		found := false
		for _, group := range [][][2]string{f.Style, f.Constraints, f.Context} {
			for _, e := range group {
				if strings.Contains(e[1], f.Marker) {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("профиль %s: маркер %q не встречается ни в одной его записи", f.Name, f.Marker)
		}
	}
	// A marker of one profile must not appear in another whose preferences differ.
	for _, a := range profileFixtures {
		for _, b := range profileFixtures {
			if a.Name == b.Name || a.Marker == b.Marker {
				continue
			}
			for _, group := range [][][2]string{b.Style, b.Constraints, b.Context} {
				for _, e := range group {
					if strings.Contains(e[1], a.Marker) {
						t.Errorf("маркер профиля %s встречается в профиле %s", a.Name, b.Name)
					}
				}
			}
		}
	}
}

func TestSentViolationsFlagsBothDirections(t *testing.T) {
	senior, _ := fixtureByName(profileSenior)
	junior, _ := fixtureByName(profileJunior)
	armWith := arm{profileSenior, profileSenior, allBlocks}
	armWithout := arm{profileNone, profileSenior, noBlock}

	if got := sentViolations("system:"+senior.Marker, expectedSent(armWith)); len(got) != 0 {
		t.Fatalf("правильный запрос помечен нарушением: %v", got)
	}
	if got := sentViolations("system:нет профиля", expectedSent(armWith)); len(got) == 0 {
		t.Fatal("пропавший профиль не замечен")
	}
	if got := sentViolations("system:"+senior.Marker, expectedSent(armWithout)); len(got) == 0 {
		t.Fatal("лишний профиль в руке без профиля не замечен")
	}
	if got := sentViolations("system:"+junior.Marker, expectedSent(armWith)); len(got) == 0 {
		t.Fatal("чужой профиль не замечен")
	}
}

// An offline run over a fake provider: every cell is executed, the arms send exactly
// their own profile, and the report renders. The fake answers in a way that makes the
// expected criteria fire, so a silent wiring break shows up as a failed criterion.
func TestOfflineRunSendsExactlyTheArmsProfileAndRendersAReport(t *testing.T) {
	fixture := t.TempDir()
	if err := buildFixture(fixture); err != nil {
		t.Fatal(err)
	}
	hash, err := treeHash(fixture)
	if err != nil {
		t.Fatal(err)
	}
	cells := planCells(false)
	var criteriaRows []criterionRow
	for _, c := range criteria {
		criteriaRows = append(criteriaRows, criterionRow{Name: c.Name, What: c.What, Accept: c.Accept, Reject: c.Reject})
	}
	rows := []any{planRow{
		Kind: "plan", Run: "offline", Model: "deepseek-flash", Profiles: profileFixtures, Criteria: criteriaRows,
		BehaviourArms: behaviourArms, ConflictArms: conflictArms, PipelineArms: pipelineArms,
		BehaviourProbes: behaviourProbes, ConflictProbes: conflictProbes, PipelineProbes: pipelineProbes,
		Cells: len(cells), FixtureSHA256: hash, EmptyShareCeiling: emptyShareCeiling,
	}}
	showcase, _, err := runShowcase(context.Background(), fakeProvider{}, "deepseek-flash", fixture, "offline")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range showcase {
		if r.Outcome != outcomeOK {
			t.Fatalf("E0 %s: исход %s", r.Profile, r.Outcome)
		}
		rows = append(rows, r)
	}
	planned := 0
	for i, c := range cells {
		row := runCell(context.Background(), fakeProvider{}, "offline", fixture, "deepseek-flash", i, c)
		if row.Error != "" || len(row.SentViolations) > 0 {
			t.Fatalf("%s/%s: ошибка %q, нарушения %v", c.Arm.Name, c.Probe.Name, row.Error, row.SentViolations)
		}
		if row.Outcome != outcomeOK {
			t.Fatalf("%s/%s: исход %s", c.Arm.Name, c.Probe.Name, row.Outcome)
		}
		if len(row.Scores) != len(c.Probe.Criteria) {
			t.Fatalf("%s/%s: оценок %d, критериев %d", c.Arm.Name, c.Probe.Name, len(row.Scores), len(c.Probe.Criteria))
		}
		if row.PlanCalls > 0 {
			planned++
		}
		rows = append(rows, row)
	}
	// Exactly the plan-answer arm makes the second call, and every one of its cells does.
	if planned != 20 {
		t.Fatalf("вызовов плана %d, а рука plan-answer состоит из 20 клеток", planned)
	}
	after, _ := treeHash(fixture)
	rows = append(rows, completeRow{
		Kind: "complete", Run: "offline", ProbeRows: len(cells), FixtureSHA256After: after,
	})

	text, err := renderAny(rows, "offline.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## E0. Один вопрос — разные профили",
		"## A. Соблюдение предпочтений",
		"## C. Конфликт: просьба против ограничения",
		"## D. Конвейер профиля",
		"против none",
		"Приборы: чем именно измеряли",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в отчёте нет %q", want)
		}
	}
}

// Every refusal of the report is reachable. A gate that cannot fire is not a gate.
func TestRenderRefusesARunThatCannotCarryAConclusion(t *testing.T) {
	base := func() []any {
		return []any{
			planRow{Kind: "plan", Run: "r", Cells: 1, FixtureSHA256: "a", EmptyShareCeiling: emptyShareCeiling},
			probeRow{Kind: "probe", Run: "r", Arm: profileSenior, Family: familyBehaviour, Probe: "explain",
				Outcome: outcomeOK, Attempts: 1, Scores: map[string]bool{"brevity": true}},
			completeRow{Kind: "complete", Run: "r", ProbeRows: 1, FixtureSHA256After: "a"},
		}
	}
	if _, err := renderAny(base(), "x"); err != nil {
		t.Fatalf("здоровый прогон отвергнут: %v", err)
	}
	for name, mutate := range map[string]func([]any) []any{
		"пилот": func(r []any) []any {
			p := r[0].(planRow)
			p.Pilot = true
			r[0] = p
			return r
		},
		"другой прогон в завершении": func(r []any) []any {
			c := r[2].(completeRow)
			c.Run = "other"
			r[2] = c
			return r
		},
		"клеток меньше плана": func(r []any) []any {
			p := r[0].(planRow)
			p.Cells = 2
			r[0] = p
			return r
		},
		"ошибка вызова": func(r []any) []any {
			c := r[2].(completeRow)
			c.Errors = 1
			r[2] = c
			return r
		},
		"фикстура уехала": func(r []any) []any {
			c := r[2].(completeRow)
			c.FixtureSHA256After = "b"
			r[2] = c
			return r
		},
		"профиль разошёлся с рукой": func(r []any) []any {
			p := r[1].(probeRow)
			p.SentViolations = []string{"маркер"}
			r[1] = p
			return r
		},
		"пустых больше потолка": func(r []any) []any {
			p := r[1].(probeRow)
			p.Outcome = outcomeEmpty
			p.Scores = nil
			r[1] = p
			return r
		},
		"строка чужого прогона": func(r []any) []any {
			p := r[1].(probeRow)
			p.Run = "other"
			r[1] = p
			return r
		},
		"нет завершения": func(r []any) []any { return r[:2] },
	} {
		if _, err := renderAny(mutate(base()), "x"); err == nil {
			t.Errorf("render принял прогон со случаем %q", name)
		}
	}
}

func TestWriteRowsRoundTripsThroughReadRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rows.jsonl")
	in := []any{
		planRow{Kind: "plan", Run: "r"},
		probeRow{Kind: "probe", Run: "r", Scores: map[string]bool{"brevity": true}, SentViolations: []string{}},
		completeRow{Kind: "complete", Run: "r"},
	}
	if err := writeRows(path, in, nil); err != nil {
		t.Fatal(err)
	}
	out, err := readRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || str(out[0], "kind") != "plan" || str(out[2], "kind") != "complete" {
		t.Fatalf("round trip = %v", out)
	}
	scores, _ := out[1]["scores"].(map[string]any)
	if v, _ := scores["brevity"].(bool); !v {
		t.Fatalf("оценки не пережили запись: %v", out[1]["scores"])
	}
	// A failed run still writes what it has: a partial file is evidence, an empty one
	// is a lost run.
	if err := writeRows(path, in, errors.New("прогон упал")); err == nil {
		t.Fatal("writeRows скрыл ошибку прогона")
	}
	if out, err := readRows(path); err != nil || len(out) != 3 {
		t.Fatalf("частичный файл не записан: %v %v", out, err)
	}
}

// The committed RESULTS.md is rebuilt from the committed JSONL and compared byte for
// byte. This is what makes every number in the document a fact about the run rather
// than a claim about it.
func TestCommittedResultsAreRenderedFromTheCommittedRun(t *testing.T) {
	const jsonl, results = "profiles.jsonl", "RESULTS.md"
	if _, err := os.Stat(jsonl); errors.Is(err, os.ErrNotExist) {
		t.Skip("живого прогона ещё нет")
	}
	rows, err := readRows(jsonl)
	if err != nil {
		t.Fatal(err)
	}
	text, err := render(rows, "day-12/"+jsonl)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(results)
	if err != nil {
		t.Fatal(err)
	}
	if string(committed) != text {
		t.Fatalf("RESULTS.md разошёлся с прогоном. Пересобери: go run ./day-12 -report day-12/%s > day-12/%s", jsonl, results)
	}
}

// An empty answer is the model's outcome and is not retried; a failed call is retried
// once and its first attempt is still billed. The two must not be confused: on 15.09
// the provider was dropping content all day, and a run that called that "the model
// answered nothing" would have measured the outage instead of the profile.
func TestEmptyIsNotRetriedAndTransportIs(t *testing.T) {
	fixture := t.TempDir()
	if err := buildFixture(fixture); err != nil {
		t.Fatal(err)
	}
	c := cell{familyBehaviour, behaviourArms[2], behaviourProbes[0], 1}

	empty := runCell(context.Background(), failingProvider{err: llm.ErrEmptyContent}, "r", fixture, "m", 0, c)
	if empty.Outcome != outcomeEmpty || empty.Attempts != 1 || empty.Error != "" {
		t.Fatalf("пустой ответ: исход %q, попыток %d, ошибка %q", empty.Outcome, empty.Attempts, empty.Error)
	}
	if empty.Scores != nil {
		t.Fatal("пустой ответ не должен получать оценок критериев")
	}

	broken := runCell(context.Background(), failingProvider{err: errors.New("timeout"), usage: 7}, "r", fixture, "m", 0, c)
	if broken.Outcome != outcomeTransport || broken.Attempts != 2 {
		t.Fatalf("сбой вызова: исход %q, попыток %d", broken.Outcome, broken.Attempts)
	}
	if len(broken.RetryErrors) != 1 {
		t.Fatalf("причина первой попытки не записана: %v", broken.RetryErrors)
	}
	// Both attempts were billed, and both are in the row: a spend that omits failures
	// under-reports exactly where the day is about spend.
	if broken.Usage.Prompt != 14 {
		t.Fatalf("расход двух попыток = %d, ожидалось 14", broken.Usage.Prompt)
	}
}

// fakeProvider answers like a model that obeys whatever profile it was given, so the
// offline run exercises the wiring rather than the model's goodwill.
type fakeProvider struct{}

func (fakeProvider) AskWith(_ context.Context, messages []llm.Message, _ llm.Options) (llm.Answer, error) {
	var system, question string
	if len(messages) > 0 {
		system = messages[0].Content
		question = messages[len(messages)-1].Content
	}
	if strings.Contains(system, "planning step") {
		return llm.Answer{Content: "1. Ответить коротко\n2. Дать пример", Model: "deepseek-flash"}, nil
	}
	switch {
	case strings.Contains(question, "17*23"):
		return llm.Answer{Content: "391", Model: "deepseek-flash"}, nil
	case strings.Contains(question, "Java"):
		return llm.Answer{Content: "Вот пример на Java со Spring:\n```java\nclass A {}\n```", Model: "deepseek-flash"}, nil
	}
	analyst, _ := fixtureByName(profileAnalyst)
	if strings.Contains(system, analyst.Marker) {
		return llm.Answer{Content: "SUMMARY: Dependency injection passes collaborators from outside.", Model: "deepseek-flash"}, nil
	}
	return llm.Answer{
		Content: "Коротко: зависимости приходят снаружи. При дедлайне 2 недели бери Koin.\n```kotlin\nclass A(val b: B)\n```",
		Model:   "deepseek-flash",
	}, nil
}

// failingProvider fails every call the same way and reports a usage for each attempt,
// so a test can see whether a failed attempt was billed and counted.
type failingProvider struct {
	err   error
	usage int
}

func (p failingProvider) AskWith(context.Context, []llm.Message, llm.Options) (llm.Answer, error) {
	return llm.Answer{Usage: llm.Usage{PromptTokens: p.usage}, Model: "deepseek-flash"}, p.err
}

// renderAny writes typed rows out and reads them back the way -report does, so a test
// renders through exactly the path the command uses.
func renderAny(rows []any, source string) (string, error) {
	var lines []string
	for _, r := range rows {
		raw, err := json.Marshal(r)
		if err != nil {
			return "", err
		}
		lines = append(lines, string(raw))
	}
	var parsed []map[string]any
	for _, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return "", err
		}
		parsed = append(parsed, row)
	}
	return render(parsed, source)
}

var _ = agent.Config{}
var _ = fmt.Sprint
