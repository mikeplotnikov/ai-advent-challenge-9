package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
		// The inflected forms the first review wave demonstrated were being missed.
		{"Перехожу ко второму шагу.", []int{2}},
		{"Работаю над вторым шагом.", []int{2}},
		{"Второго шага пока не закончил.", []int{2}},
		{"Шаг четвёртый закрыт.", []int{4}},
		// And the trap that made the narrow version narrow in the first place: a bare
		// ordinal enumerating arguments is not a step reference.
		{"Во-вторых, шаг нужно согласовать", nil},
		{"Во-первых, это удобно. Во-вторых, быстро.", nil},
		// The second review wave's trap: ordinary Russian words that merely share a
		// stem with an ordinal, all of them at home in this task's own register.
		{"это второстепенный шаг", nil},
		{"шаг вторичен по важности", nil},
		{"шаг первично обработан, доработаю позже", nil},
		{"потратил четверть шага на анализ", nil},
		{"шагнул второй раз", nil},
		{"Шаг третий в работе", []int{3}},
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

// The two criteria that read prose for what an answer is DOING, checked on phrasings no
// fixture contained. Both were demonstrated wrong by the first review wave: `no_restart`
// fired on an ordinary recap of finished work, and `no_reask` fired on a past-tense
// sentence that merely shares a stem with a marker word.
func TestRestartAndReaskScoreBehaviourNotSubstrings(t *testing.T) {
	restart, _ := criterionByName("no_restart")
	reask, _ := criterionByName("no_reask")
	ctx := scoreCtx{Stage: agent.StageExecution, Step: 2, Total: 4}

	for _, ok := range []string{
		"Шаг 1: JWT module — готово. Шаг 2: Token validation, продолжаю проверку подписи.",
		"Шаг 1: сделан. Перехожу к шагу 2.",
	} {
		if !restart.Test(ok, ctx) {
			t.Errorf("пересказ сделанного засчитан как перезапуск плана: %q", ok)
		}
	}
	for _, bad := range []string{
		"Давайте составим план работ.",
		"Начнём с плана: шаг 1: определить требования.",
		"Сначала определим требования к сервису.",
	} {
		if restart.Test(bad, ctx) {
			t.Errorf("перезапуск плана не замечен: %q", bad)
		}
	}

	for _, ok := range []string{
		"Мы не уточнили формат токена ранее, поэтому исхожу из HMAC-SHA256, TTL 7 минут.",
		"Напомню: шаг 2 из 4.",
		"Требования уточнили на планировании, продолжаю.",
	} {
		if !reask.Test(ok, ctx) {
			t.Errorf("обычная фраза засчитана как переспрос: %q", ok)
		}
	}
	for _, bad := range []string{
		"Уточните, на каком шаге мы остановились.",
		"Напомни, пожалуйста, что за задача?",
		"Уточни, что именно продолжить.",
		"У меня нет контекста предыдущего разговора.",
	} {
		if reask.Test(bad, ctx) {
			t.Errorf("переспрос не замечен: %q", bad)
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
				if _, has := scores["right_step"]; has {
					scores["right_step"] = false
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
	// The label has to be the one the committed file was generated with: it is printed
	// into the header, so a different string here would report a hand edit that is not
	// there and hide one that is.
	text, err := render(rows, reportSource)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != text {
		t.Fatal("RESULTS.md разошёлся с тем, что строится из JSONL — значит, его правили руками; " +
			"пересоберите: go run ./day-13 -report day-13/state.jsonl > day-13/RESULTS.md")
	}
	fmt.Fprintln(os.Stderr, "RESULTS.md совпадает с пересборкой из JSONL")
}

// A zero needs a positive control. `names_stage` came back 0/20 at the planning stage,
// and that reading is only allowed if the criterion can fire there at all — so every
// stage gets its own must-fire fixture, and every stage gets the domain sentence that
// must not fire.
func TestNamesStageFiresForEveryStageAndNotOnDomainWords(t *testing.T) {
	crit, ok := criterionByName("names_stage")
	if !ok {
		t.Fatal("критерия names_stage нет")
	}
	for _, tc := range []struct {
		stage  agent.TaskStage
		fires  []string
		silent []string
	}{
		{agent.StagePlanning,
			[]string{"Стадия planning, шаг 1/4.", "Мы на этапе планирования.", "State: PLANNING."},
			[]string{"Вот план: 1) JWT module 2) Token validation.", "Приступаю к шагу 1 — JWT module."}},
		{agent.StageExecution,
			[]string{"Стадия execution, шаг 2/4.", "Сейчас этап выполнения.", "State: EXECUTION."},
			[]string{"Реализую выпуск токенов.", "Переходим к реализации JWT module."}},
		{agent.StageValidation,
			[]string{"Стадия validation.", "Мы на этапе проверки.", "State: VALIDATION, шаг 4/4."},
			// The trap the first run walked into: this task's own second step is
			// called "Token validation", so the domain says these words constantly.
			[]string{"Продолжаю валидацию токена, проверяю подпись.",
				"Проверка срока жизни и подписи access-токена.",
				"Пишу тесты на валидацию."}},
		{agent.StageDone,
			[]string{"Стадия done — задача закрыта.", "Этап завершён.", "State: DONE."},
			[]string{"Готово, токен выпускается.", "Шаг 4/4 закрыт, пишу revocation list."}},
	} {
		t.Run(string(tc.stage), func(t *testing.T) {
			ctx := scoreCtx{Stage: tc.stage, Step: 1, Total: len(measurePlan)}
			for _, f := range tc.fires {
				if !crit.Test(f, ctx) {
					t.Errorf("обязан сработать на %s, но промолчал: %q", tc.stage, f)
				}
			}
			for _, f := range tc.silent {
				if crit.Test(f, ctx) {
					t.Errorf("обязан промолчать на %s, но сработал: %q", tc.stage, f)
				}
			}
		})
	}
}

// Re-scoring the recorded answers must be a pure function of the criteria: the same
// JSONL, today's instruments, and no model call. Without it, fixing an instrument would
// mean paying for the run again — or, worse, leaving the wrong number published.
func TestRescoringIsAPureFunctionOfTheRecordedAnswers(t *testing.T) {
	rows, err := readRows("state.jsonl")
	if os.IsNotExist(err) {
		t.Skip("прогона ещё не было")
	}
	if err != nil {
		t.Fatal(err)
	}
	once, err := rescore(rows)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := rescore(once)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(once)
	b, _ := json.Marshal(twice)
	if string(a) != string(b) {
		t.Fatal("пересчёт не идемпотентен")
	}
	// Every cell that has an answer is re-scored, and the count matches what the
	// probes promised. A guard that only checks "at least one" is one a cleanup can
	// walk straight past — day 12 learned that the hard way.
	promised, got := 0, 0
	for _, r := range once {
		if str(r, "kind") != "probe" || str(r, "outcome") != outcomeOK {
			continue
		}
		p, ok := probeByName(str(r, "probe"))
		if !ok {
			t.Fatalf("в выгрузке проба %q, которой нет в предрегистрации", str(r, "probe"))
		}
		promised += len(p.Criteria)
		scores, _ := r["scores"].(map[string]any)
		got += len(scores)
	}
	if promised == 0 || promised != got {
		t.Fatalf("оценок %d, пробы обещали %d", got, promised)
	}
}

// The -rescore CLI path, not just the pure function inside it. RESULTS.md's own
// regeneration instructions send a person through this command, and it is the sanctioned
// way to fix a miscalibrated instrument without paying for the run again — which this
// day has now done twice. A test that only calls rescore() leaves the wiring unguarded.
func TestRescoreCommandRefusesToOverwriteItsSourceAndWritesAFullFile(t *testing.T) {
	rows, err := readRows("state.jsonl")
	if os.IsNotExist(err) {
		t.Skip("прогона ещё не было")
	}
	if err != nil {
		t.Fatal(err)
	}

	// The guard that keeps the raw run from being rewritten in place: -rescore without
	// -out must refuse, because the answers are the only thing a rescore cannot rebuild.
	bin := filepath.Join(t.TempDir(), "day13")
	if out, err := exec.Command("go", "build", "-o", bin,
		"github.com/mikeplotnikov/ai-advent-challenge-9/day-13").CombinedOutput(); err != nil {
		t.Fatalf("сборка: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "-rescore", "state.jsonl")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("-rescore без -out не отказал: исходная выгрузка переписывается на месте")
	}
	if !strings.Contains(string(out), "-out") {
		t.Fatalf("отказ не объясняет, чего не хватает:\n%s", out)
	}

	// And with -out it writes every row, not only the scored ones.
	target := filepath.Join(t.TempDir(), "rescored.jsonl")
	if out, err := exec.Command(bin, "-rescore", "state.jsonl", "-out", target).CombinedOutput(); err != nil {
		t.Fatalf("-rescore: %v\n%s", err, out)
	}
	got, err := readRows(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(rows) {
		t.Fatalf("строк в пересчитанной выгрузке %d, в исходной %d", len(got), len(rows))
	}
	// The answers and the machine's own decisions are untouched: a rescore may only
	// change verdicts, or it is not a rescore.
	for i := range rows {
		if str(rows[i], "answer") != str(got[i], "answer") {
			t.Fatalf("строка %d: пересчёт изменил ответ модели", i)
		}
		if boolOf(rows[i], "moveIllegal") != boolOf(got[i], "moveIllegal") {
			t.Fatalf("строка %d: пересчёт изменил решение машины", i)
		}
	}
	// The report builds from the rescored file, which is the point of the command.
	if _, err := render(got, reportSource); err != nil {
		t.Fatalf("отчёт из пересчитанной выгрузки не строится: %v", err)
	}
}

// A row naming a probe or scenario this build no longer has must stop the rescore, not
// slip through with its old verdicts. The second review wave demonstrated the silent
// path: a renamed probe kept a poisoned score byte-for-byte, and the output file looked
// like a freshly rescored one.
func TestRescoreRefusesARowItCannotRescore(t *testing.T) {
	rows, err := readRows("state.jsonl")
	if os.IsNotExist(err) {
		t.Skip("прогона ещё не было")
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"probe", "scenario"} {
		t.Run(field, func(t *testing.T) {
			broken := make([]map[string]any, 0, len(rows))
			renamed := false
			for _, r := range rows {
				copied := map[string]any{}
				for k, v := range r {
					copied[k] = v
				}
				if !renamed && str(copied, "kind") == "probe" && str(copied, "outcome") == outcomeOK {
					copied[field] = "снято-из-предрегистрации"
					renamed = true
				}
				broken = append(broken, copied)
			}
			if !renamed {
				t.Fatal("в выгрузке нет ни одной клетки с ответом")
			}
			if _, err := rescore(broken); err == nil {
				t.Fatalf("пересчёт принял строку с неизвестным полем %s и сохранил её старые вердикты", field)
			}
		})
	}
}

// The distance bound inside namesStage is load-bearing: it is what keeps a stage word in
// one sentence from being licensed by a marker word in another. The wave-2 test review
// showed the bound could be removed entirely without any test noticing.
func TestNamesStageWillNotCrossASentence(t *testing.T) {
	crit, _ := criterionByName("names_stage")
	ctx := scoreCtx{Stage: agent.StageValidation, Step: 4, Total: 4}
	for _, silent := range []string{
		"Сейчас стадия исполнения. При валидации проверять подпись и срок.",
		"Форма задачи: stage → done. Продолжаю валидацию токена.",
		"Этап понятен.\nПроверка подписи идёт.",
		"Стадия ясна — двигаемся. Тестирование будет позже, отдельным шагом работы.",
	} {
		if crit.Test(silent, ctx) {
			t.Errorf("маркер из одного предложения узаконил доменное слово из другого: %q", silent)
		}
	}
	// And the construction it exists to catch still fires.
	if !crit.Test("Стадия validation, шаг 4/4.", ctx) {
		t.Error("настоящее называние стадии перестало засчитываться")
	}
}
