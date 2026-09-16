package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

// The instruments are calibrated before the first live call. A threshold that cannot see
// its own object prints a plausible wrong number, and that number then travels into a
// report as a fact.
func TestCriteriaAreCalibrated(t *testing.T) {
	for _, c := range criteria {
		t.Run(c.Name, func(t *testing.T) {
			if len(c.Accept) == 0 || len(c.Reject) == 0 {
				t.Fatal("у критерия должны быть фикстуры и «обязан сработать», и «обязан промолчать»")
			}
			for _, fixture := range c.Accept {
				if !c.Test(fixture, c.Ctx) {
					t.Errorf("обязан сработать, но промолчал:\n%s", fixture)
				}
			}
			for _, fixture := range c.Reject {
				if c.Test(fixture, c.Ctx) {
					t.Errorf("обязан промолчать, но сработал:\n%s", fixture)
				}
			}
		})
	}
}

func TestStepsMentionedReadsTheShapesAnswersActuallyUse(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   []int
	}{
		{"Шаг 2/4: Token validation", []int{2}},
		{"Продолжаю второй шаг", []int{2}},
		{"Step 3 из 4", []int{3}},
		{"Приступаю к шагу 1, потом к шагу 2", []int{1, 2}},
		{"Во-вторых, проверим токены", nil},
		{"Продолжаю работу", nil},
		{"2 из 4 готово", []int{2}},
	} {
		got := stepsMentioned(tc.answer)
		want := map[int]bool{}
		for _, n := range tc.want {
			want[n] = true
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q → %v, ожидалось %v", tc.answer, got, want)
		}
	}
}

// The carried decision is a duration, and the criterion must score the decision rather
// than the digit.
func TestUsesPlanScoresTheDurationNotTheDigit(t *testing.T) {
	for _, yes := range []string{"7 минут", "TTL 7 мин", "не старше 7 минут", "7 minutes"} {
		if !mentionsSevenMinutes(yes) {
			t.Errorf("не засчитано: %q", yes)
		}
	}
	for _, no := range []string{"7 дней", "7 часов", "7 секунд", "токен 7", "порт 7000", "17 минут"} {
		if mentionsSevenMinutes(no) {
			t.Errorf("засчитано ошибочно: %q", no)
		}
	}
}

// The decision the carry hands forward must not be reachable any other way, or family D
// measures nothing. This is checked against the actual strings, not by reading them.
func TestTheCarriedDecisionLeaksNowhereElse(t *testing.T) {
	for _, step := range measurePlan {
		if mentionsSevenMinutes(step) {
			t.Fatalf("решение просочилось в шаг плана: %q", step)
		}
	}
	for _, h := range measureHistory {
		if mentionsSevenMinutes(h[0]) || mentionsSevenMinutes(h[1]) {
			t.Fatalf("решение просочилось в историю: %v", h)
		}
	}
	for _, p := range allProbes() {
		if mentionsSevenMinutes(p.Question) {
			t.Fatalf("решение просочилось в вопрос пробы %q", p.Name)
		}
	}
	if !mentionsSevenMinutes(planningCarry) {
		t.Fatal("решение отсутствует в переносе — тогда рука carry ничем не отличается")
	}
}

func TestPlanCellsMatchesThePreRegistration(t *testing.T) {
	cells := planCells(false)
	// 3 arms × 4 pause points × 20 + 1 arm × 2 probes × 20 + 2 arms × 1 probe × 20.
	const want = 3*4*20 + 1*2*20 + 2*1*20
	if len(cells) != want {
		t.Fatalf("клеток %d, предрегистрация обещала %d", len(cells), want)
	}
	if pilot := planCells(true); len(pilot) != 4 {
		t.Fatalf("пилот из %d клеток, ожидалось 4", len(pilot))
	}
	// Every probe names criteria that exist, or the run scores nothing and says so
	// only after it has been paid for.
	for _, p := range allProbes() {
		for _, name := range p.Criteria {
			if isMoveCriterion(name) {
				continue
			}
			if _, ok := criterionByName(name); !ok {
				t.Errorf("проба %s ссылается на несуществующий критерий %s", p.Name, name)
			}
		}
		if _, ok := scenarioByName(p.Scenario); !ok {
			t.Errorf("проба %s ссылается на несуществующий сценарий %s", p.Name, p.Scenario)
		}
	}
}

// Seeding is what makes the arms comparable. If it ever stops producing exactly the
// state an arm promises, every number of the run describes something else.
func TestSeedProducesExactlyTheStateEachArmPromises(t *testing.T) {
	for _, sc := range resumeScenarios {
		t.Run(sc.Name, func(t *testing.T) {
			dir := t.TempDir()
			if err := seed(dir, sc, agent.StandardStages, true, sc.Carry); err != nil {
				t.Fatal(err)
			}
			a, err := agent.New(noCaller{}, measureConfig(dir, agent.StandardStages, true, true))
			if err != nil {
				t.Fatal(err)
			}
			v := a.TaskState()
			if v.State != sc.Stage {
				t.Fatalf("стадия %q, сценарий обещал %q", v.State, sc.Stage)
			}
			if v.Step != sc.Step {
				t.Fatalf("шаг %d, сценарий обещал %d", v.Step, sc.Step)
			}
			if v.Total != len(measurePlan) {
				t.Fatalf("план из %d шагов, ожидалось %d", v.Total, len(measurePlan))
			}
			hasCarry := len(v.Carry) > 0
			if hasCarry != sc.Carry {
				t.Fatalf("перенос %v, сценарий обещал %v", hasCarry, sc.Carry)
			}
		})
	}
}

func TestSeedingNeverCallsTheProvider(t *testing.T) {
	// scriptedCaller has exactly as many answers as there are history turns; one extra
	// call fails it. That is the guard: a change that made setup ask the model would
	// turn a local fixture into a billed one, silently.
	dir := t.TempDir()
	if err := seed(dir, resumeScenarios[2], agent.StandardStages, true, true); err != nil {
		t.Fatal(err)
	}
	// And the arm without history builds no task at all.
	bare := t.TempDir()
	if err := seed(bare, resumeScenarios[1], agent.StandardStages, false, false); err != nil {
		t.Fatal(err)
	}
	a, err := agent.New(noCaller{}, measureConfig(bare, agent.StandardStages, false, false))
	if err != nil {
		t.Fatal(err)
	}
	if v := a.TaskState(); v.Task != "" {
		t.Fatalf("в руке без истории оказалась задача %q", v.Task)
	}
}

func TestSentViolationsCatchesAnArmThatDidNotTravel(t *testing.T) {
	sc := resumeScenarios[1] // execution, with carry
	full := "user:[TASK_STATE]\nstage: execution\nresult.planning: x\n[USER_MESSAGE]Продолжай\n"
	stateArm := arm{Name: "state", History: true, State: true, Carry: true}
	if v := sentViolations(full, stateArm, sc); len(v) > 0 {
		t.Fatalf("исправный запрос отмечен как нарушение: %v", v)
	}
	// The block promised and absent.
	if v := sentViolations("user:[USER_MESSAGE]Продолжай\n", stateArm, sc); len(v) == 0 {
		t.Fatal("пропавший блок состояния не замечен")
	}
	// The block not promised and present.
	offArm := arm{Name: "no-state", History: true, State: false, Carry: true}
	if v := sentViolations(full, offArm, sc); len(v) == 0 {
		t.Fatal("лишний блок состояния не замечен")
	}
	// The carry promised and absent.
	noCarryWire := "user:[TASK_STATE]\nstage: execution\n[USER_MESSAGE]Продолжай\n"
	if v := sentViolations(noCarryWire, stateArm, sc); len(v) == 0 {
		t.Fatal("пропавший перенос стадии не замечен")
	}
	// History leaking into the arm that must not have it.
	noneArm := arm{Name: "none", History: false, State: false, Carry: false}
	leaked := "user:" + measureHistory[0][1] + "\n[USER_MESSAGE]Продолжай\n"
	if v := sentViolations(leaked, noneArm, sc); len(v) == 0 {
		t.Fatal("протёкшая история не замечена")
	}
}

// A run that is not fit to be interpreted must not become a report. Each gate is checked
// by breaking exactly one thing in an otherwise valid run: a gate that cannot fire is
// decoration.
func TestTheReportRefusesAnUnfitRun(t *testing.T) {
	good := syntheticRun()
	if _, err := render(good, "test.jsonl"); err != nil {
		t.Fatalf("исправный прогон не отрендерился: %v", err)
	}

	for _, tc := range []struct {
		name   string
		break_ func(rows []map[string]any) []map[string]any
		want   string
	}{
		{"нет строки complete", func(rows []map[string]any) []map[string]any {
			return filter(rows, func(r map[string]any) bool { return str(r, "kind") != "complete" })
		}, "не завершён"},
		{"клеток меньше, чем обещал план", func(rows []map[string]any) []map[string]any {
			// One probe row goes missing while the run still reports itself complete —
			// a partially written JSONL, not a truncated one.
			dropped := false
			var out []map[string]any
			for _, r := range rows {
				if !dropped && str(r, "kind") == "probe" {
					dropped = true
					continue
				}
				out = append(out, r)
			}
			return out
		}, "план обещал"},
		{"в прогоне есть сбой вызова", func(rows []map[string]any) []map[string]any {
			for _, r := range rows {
				if str(r, "kind") == "complete" {
					r["errors"] = 1.0
				}
			}
			return rows
		}, "сбоем вызова"},
		{"в запрос ушло не то, что обещала рука", func(rows []map[string]any) []map[string]any {
			for _, r := range rows {
				if str(r, "kind") == "probe" {
					r["sentViolations"] = []any{"блок состояния: ожидался true, в запросе false"}
					break
				}
			}
			return rows
		}, "не то, что обещала рука"},
		{"отклонённый переход всё же случился", func(rows []map[string]any) []map[string]any {
			for _, r := range rows {
				if str(r, "kind") == "probe" {
					r["moveIllegal"] = true
					r["stateAfter"] = "done"
					r["stage"] = "planning"
					break
				}
			}
			return rows
		}, "стадия всё же сменилась"},
		{"пустых ответов больше потолка", func(rows []map[string]any) []map[string]any {
			n := 0
			for _, r := range rows {
				if str(r, "kind") == "probe" && str(r, "arm") == "state" && n < 10 {
					r["outcome"] = outcomeEmpty
					n++
				}
			}
			return rows
		}, "непригодны"},
		{"это пилот", func(rows []map[string]any) []map[string]any {
			for _, r := range rows {
				if str(r, "kind") == "plan" {
					r["pilot"] = true
				}
			}
			return rows
		}, "пилот"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := tc.break_(syntheticRun())
			_, err := render(rows, "test.jsonl")
			if err == nil {
				t.Fatal("непригодный прогон превратился в отчёт")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("отказ не объясняет причину: %v", err)
			}
		})
	}
}

// A positive control that came back zero must be printed as a failed control, not
// quietly passed over on the way to "различий нет".
func TestAFailedPositiveControlIsAnnounced(t *testing.T) {
	rows := syntheticRun()
	for _, r := range rows {
		if str(r, "kind") == "probe" && str(r, "arm") == "state" {
			if scores, ok := r["scores"].(map[string]any); ok {
				if _, has := scores["names_stage"]; has {
					scores["names_stage"] = false
				}
			}
		}
	}
	text, err := render(rows, "test.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Контроль провален") {
		t.Fatal("нулевой положительный контроль не объявлен — тогда любой ноль ниже читается как факт")
	}
}

// syntheticRun is a complete, self-consistent run: enough to exercise every gate by
// breaking one thing at a time.
func syntheticRun() []map[string]any {
	plan := map[string]any{
		"kind": "plan", "run": "test", "commit": "deadbeef", "model": "deepseek-flash",
		"seed": 1.0, "task": measureTask, "pilot": false, "emptyShareCeiling": emptyShareCeiling,
		"plan": []any{"JWT module", "Token validation"},
	}
	var rows []map[string]any
	rows = append(rows, plan)

	order := 0
	add := func(family, arm, probe, scenario, stage string, step int, scores map[string]any) {
		rows = append(rows, map[string]any{
			"kind": "probe", "run": "test", "order": float64(order), "family": family,
			"arm": arm, "probe": probe, "scenario": scenario, "stage": stage,
			"step": float64(step), "outcome": outcomeOK, "answer": "ответ",
			"scores": scores, "servedModel": "deepseek-flash", "stateAfter": stage,
			"stepAfter": float64(step), "stateTokens": 120.0, "estimateTotal": 400.0,
			"usage": map[string]any{"prompt": 400.0, "completion": 60.0, "cached": 320.0, "cost": 0.0002},
		})
		order++
	}
	for i := 0; i < 20; i++ {
		for _, a := range []string{"state", "no-state", "none"} {
			pass := a == "state"
			add(familyResume, a, "resume-execution", "execution", "execution", 2, map[string]any{
				"names_stage": pass, "right_step": pass, "no_reask": true, "no_restart": true,
			})
		}
		add(familyDrive, "state", "drive-next-step", "execution", "execution", 2, map[string]any{
			"asked_step": i%4 == 0, "asked_nothing": i%4 != 0,
		})
		add(familyDrive, "state", "drive-skip", "planning", "planning", 1, map[string]any{
			"asked_illegal": i%5 == 0, "asked_nothing": i%5 != 0,
		})
		add(familyCarry, "carry", "carry-ttl", "execution", "execution", 2, map[string]any{
			"uses_plan": true, "no_reask": true,
		})
		add(familyCarry, "no-carry", "carry-ttl", "execution", "execution", 2, map[string]any{
			"uses_plan": false, "no_reask": true,
		})
	}
	plan["cells"] = float64(order)
	rows = append(rows, map[string]any{
		"kind": "complete", "run": "test", "probeRows": float64(order), "errors": 0.0, "empty": 0.0,
		"spend": map[string]any{"calls": float64(order), "cost": 0.05},
	})
	return rows
}

// The showcase reads the stage sets, the transitions and the criteria from this dump. A
// dump that has drifted from the code would let the JS side agree with a Go build that
// no longer exists.
func TestDumpMatchesTheCopyTheShowcaseChecksAgainst(t *testing.T) {
	fresh, err := buildDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	committed, err := readDump(filepath.Join(".", "definitions.json"))
	if err != nil {
		t.Fatalf("выгрузка для витрины не читается: %v — пересоберите её: go run ./day-13 -dump > day-13/definitions.json", err)
	}
	freshJSON, _ := json.MarshalIndent(fresh, "", "  ")
	committedJSON, _ := json.MarshalIndent(committed, "", "  ")
	if string(freshJSON) != string(committedJSON) {
		t.Fatalf("day-13/definitions.json устарела относительно кода.\nПересоберите: go run ./day-13 -dump > day-13/definitions.json\n\nсейчас в коде:\n%s", freshJSON)
	}
}

// The example block in the dump has to be a real one, produced by the agent, or the
// showcase is checked against a hand-written copy of a promise.
func TestTheDumpedBlockIsTheOneTheAgentSends(t *testing.T) {
	defs, err := buildDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	block := defs.Example.Block
	for _, want := range []string{
		"[TASK_STATE]", "task: " + measureTask, "stage: execution",
		"step: 2/4 — Token validation", "result.planning:", "expect: ", "[[NEXT_STEP]]",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("в выгруженном блоке нет %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "[USER_MESSAGE]") {
		t.Error("в блок состояния затесался тег вопроса")
	}
}

func TestEveryStageSetIsReachableEndToEnd(t *testing.T) {
	for _, set := range []struct {
		name      string
		scenarios []scenario
	}{
		{agent.StandardStages, resumeScenarios},
		{agent.BugfixStages, bugfixScenarios},
	} {
		for _, sc := range set.scenarios {
			dir := t.TempDir()
			if err := seed(dir, sc, set.name, true, sc.Carry); err != nil {
				t.Fatalf("%s/%s: %v", set.name, sc.Name, err)
			}
			a, err := agent.New(noCaller{}, measureConfig(dir, set.name, true, true))
			if err != nil {
				t.Fatalf("%s/%s: %v", set.name, sc.Name, err)
			}
			if v := a.TaskState(); v.State != sc.Stage || v.StageSet != set.name {
				t.Fatalf("%s/%s: получили %s/%s", set.name, sc.Name, v.StageSet, v.State)
			}
		}
	}
}

func TestReportIsWrittenNowhereByHand(t *testing.T) {
	raw, err := os.ReadFile("RESULTS.md")
	if os.IsNotExist(err) {
		t.Skip("прогона ещё не было")
	}
	if err != nil {
		t.Fatal(err)
	}
	rows, err := readRows("state.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	text, err := render(rows, "state.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != text {
		t.Fatal("RESULTS.md разошёлся с тем, что строится из JSONL — значит, его правили руками; " +
			"пересоберите: go run ./day-13 -report day-13/state.jsonl > day-13/RESULTS.md")
	}
	fmt.Fprintln(os.Stderr, "RESULTS.md совпадает с пересборкой из JSONL")
}
