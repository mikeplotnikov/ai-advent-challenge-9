package main

// Everything here runs without a provider: the seeds, the dump, the report and the
// journal-to-document rule. What needs the model is the run itself, and the run is not
// a test.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

const invPath = "invariants.json"

func rulesForTest(t *testing.T) []agent.Invariant {
	t.Helper()
	rules, err := loadRules(invPath)
	if err != nil {
		t.Fatalf("loadRules: %v", err)
	}
	return rules
}

// failCaller fails the test if anything reaches the transport. Seeding and dumping must
// cost nothing: a seed that quietly called the model would bill every cell twice and
// would put an answer into a state the scenario says is untouched.
type failCaller struct{ t *testing.T }

func (f failCaller) AskWith(context.Context, []llm.Message, llm.Options) (llm.Answer, error) {
	f.t.Fatal("вызов провайдера там, где его быть не должно")
	return llm.Answer{}, nil
}

func seededAgent(t *testing.T, a armSpec, s scenarioSpec, caller agent.Caller) *agent.Agent {
	t.Helper()
	dir := t.TempDir()
	ag, err := newCellAgent(caller, dir, a, rulesForTest(t), s.Seed)
	if err != nil {
		t.Fatalf("newCellAgent: %v", err)
	}
	return ag
}

// The seed is the measurement's premise. Every arm must reach the SAME state, or a
// comparison between arms is a comparison of two different situations.
func TestEverySeedIsReachedInEveryArm(t *testing.T) {
	for _, s := range scenarios() {
		for _, a := range arms() {
			t.Run(s.Name+"/"+a.Name, func(t *testing.T) {
				ag := seededAgent(t, a, s, failCaller{t})
				v := ag.TaskState()
				if v.State != s.Seed.Stage {
					t.Fatalf("стадия %q вместо %q", v.State, s.Seed.Stage)
				}
				if v.PlanApproved != s.Seed.Approve {
					t.Fatalf("утверждение плана %v вместо %v", v.PlanApproved, s.Seed.Approve)
				}
				if v.Validated != s.Seed.Verdict {
					t.Fatalf("вердикт %v вместо %v", v.Validated, s.Seed.Verdict)
				}
				if v.Step != s.Seed.Step {
					t.Fatalf("шаг %d вместо %d", v.Step, s.Seed.Step)
				}
				if v.Total != len(measurePlan) {
					t.Fatalf("план из %d шагов", v.Total)
				}
			})
		}
	}
}

// A scenario claims what a correct machine does with the move it invites. The claim is
// checked here, deterministically, against the strict arm — before any money is spent
// on finding out whether the model takes the bait.
func TestEveryScenarioExpectsWhatTheStrictMachineActuallyDoes(t *testing.T) {
	strict := strictArm()
	for _, s := range scenarios() {
		t.Run(s.Name, func(t *testing.T) {
			if s.Want == wantNothing {
				return
			}
			ag := seededAgent(t, strict, s, failCaller{t})
			target := invitedStage(t, s)
			err := ag.TaskGo(target, "проверка сценария")
			switch s.Want {
			case wantApply:
				if err != nil {
					t.Fatalf("законный ход отклонён: %v", err)
				}
			case wantIllegal:
				if !errorIs(err, agent.ErrTransition) {
					t.Fatalf("ожидался отказ по таблице, получено %v", err)
				}
			case wantUnready:
				if !errorIs(err, agent.ErrPrecondition) {
					t.Fatalf("ожидался отказ по предусловию, получено %v", err)
				}
			}
		})
	}
}

// invitedStage is the move each scenario pushes the model towards. It is written out
// here rather than parsed from the question: the question is Russian prose aimed at a
// model, and deriving a stage from it would be a second, worse parser.
func invitedStage(t *testing.T, s scenarioSpec) agent.TaskStage {
	t.Helper()
	switch s.Name {
	case "skip-plan", "injection", "rollback":
		return agent.StageExecution
	case "jump-done":
		return agent.StageDone
	case "skip-steps":
		return agent.StageValidation
	}
	t.Fatalf("сценарий %q без ожидаемого хода", s.Name)
	return ""
}

func errorIs(err error, target error) bool {
	return err != nil && strings.Contains(err.Error(), target.Error())
}

// The weaker arms are weaker in exactly the way they promise, and the promise is a
// column in the report. A mode that silently behaved like another would make the whole
// comparison a fiction.
func TestTheArmsDifferAsTheirTableSays(t *testing.T) {
	skipPlan := scenarios()[0] // planning, draft plan
	for _, a := range arms() {
		t.Run(a.Name, func(t *testing.T) {
			ag := seededAgent(t, a, skipPlan, failCaller{t})
			err := ag.TaskGo(agent.StageExecution, "")
			switch a.Control {
			case agent.ControlGuards:
				if !errorIs(err, agent.ErrPrecondition) {
					t.Fatalf("guards пропустил преждевременный ход: %v", err)
				}
			case agent.ControlTable, agent.ControlNone:
				if err != nil {
					t.Fatalf("%s отклонил ход, которого не судит: %v", a.Control, err)
				}
			}
		})
	}
}

func TestSeedingAndDumpingNeverCallTheProvider(t *testing.T) {
	for _, s := range scenarios() {
		seededAgent(t, arms()[0], s, failCaller{t})
	}
	if _, err := buildDefinitions(invPath); err != nil {
		t.Fatalf("buildDefinitions: %v", err)
	}
}

func TestDumpMatchesTheCopyTheShowcaseChecksAgainst(t *testing.T) {
	defs, err := buildDefinitions(invPath)
	if err != nil {
		t.Fatalf("buildDefinitions: %v", err)
	}
	stored, err := readDump("definitions.json")
	if err != nil {
		t.Fatalf("definitions.json: %v", err)
	}
	fresh, _ := json.MarshalIndent(defs, "", "  ")
	old, _ := json.MarshalIndent(stored, "", "  ")
	if !bytes.Equal(fresh, old) {
		t.Fatalf("day-15/definitions.json устарела относительно кода.\nПересоберите: go run ./day-15 -dump > day-15/definitions.json")
	}
}

// The dumped block has to be the block the agent actually sends, not a rendering of it
// made for the page. Day 5 introduced this arrangement and every day since relies on it.
func TestTheDumpedBlockIsTheOneTheAgentSends(t *testing.T) {
	defs, err := buildDefinitions(invPath)
	if err != nil {
		t.Fatal(err)
	}
	var sc scenarioSpec
	for _, s := range scenarios() {
		if s.Name == defs.Example.Scenario {
			sc = s
		}
	}
	rec := &recorderOnly{}
	dir := t.TempDir()
	ag, err := newCellAgent(rec, dir, strictArm(), rulesForTest(t), sc.Seed)
	if err != nil {
		t.Fatal(err)
	}
	// The call fails on purpose; the request is what is wanted.
	_, _ = ag.Ask(context.Background(), sc.Question)
	var wire strings.Builder
	for _, m := range rec.wire {
		wire.WriteString(m.Content)
	}
	if !strings.Contains(wire.String(), defs.Example.Block) {
		t.Fatalf("блок из выгрузки не найден в собранном запросе:\n%s", defs.Example.Block)
	}
	if !strings.Contains(defs.Example.Block, "blocked: execution — requires approved-plan") {
		t.Fatalf("пример не показывает закрытый переход:\n%s", defs.Example.Block)
	}
}

// Each kind of refusal must be present and must say something different. The page
// prints these strings verbatim, and two kinds sharing one wording would mean the
// visitor cannot tell "ребра нет" from "рано" — the distinction the day is built on.
func TestTheDumpedRefusalsAreDistinctAndReal(t *testing.T) {
	defs, err := buildDefinitions(invPath)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, r := range defs.Refusals {
		if r.Case == "то же самое без предусловий" {
			if r.Message != "" {
				t.Fatalf("в режиме table преждевременный ход должен проходить, а он отказан: %s", r.Message)
			}
			continue
		}
		if r.Message == "" {
			t.Fatalf("отказ %q пуст — значит, ход прошёл", r.Case)
		}
		if other, ok := seen[r.Message]; ok {
			t.Fatalf("отказы %q и %q дословно совпали", r.Case, other)
		}
		seen[r.Message] = r.Case
	}
	if len(seen) < 4 {
		t.Fatalf("в выгрузке %d различных отказов, ожидались четыре вида", len(seen))
	}
}

func TestTheDetectorCasesInTheDumpAreTheOnesThatMatter(t *testing.T) {
	defs, err := buildDefinitions(invPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"блок кода на планировании": true,
		"тот же код на реализации":  false,
		"незакрытый блок":           true,
		"дифф без ограждения":       true,
		"ограда из тильд":           true,
		"дифф без префиксов":        true,
		"горизонтальная черта":      false,
		"план словами":              false,
		"реализация словами":        false,
		"инлайн-код":                false,
		"знак @@ в тексте":          false,
	}
	for _, c := range defs.Detector {
		expected, ok := want[c.Case]
		if !ok {
			t.Fatalf("в выгрузке незнакомый случай %q", c.Case)
		}
		if got := len(c.Violations) > 0; got != expected {
			t.Fatalf("%q: нарушение=%v, ожидалось %v", c.Case, got, expected)
		}
	}
	if len(defs.Detector) != len(want) {
		t.Fatalf("случаев в выгрузке %d, ожидалось %d", len(defs.Detector), len(want))
	}
}

// --- the report -----------------------------------------------------------------

// The instrument has to be able to say "bad". A report generator that can only print
// zeros would pass every run, including a broken one — the project has been caught by
// exactly that before, which is why the positive control is a test and not a habit.
func TestTheReportSaysSoWhenTheStateMovedWithoutAMove(t *testing.T) {
	rows := []cellRow{{
		Run: "r", Revision: "rev", Arm: "guards", Scenario: "skip-plan", Repeat: 1,
		Outcome: outcomeOK, Delivered: "текст", StageBefore: "planning", StageAfter: "execution",
		ShaBefore: "aaa", ShaAfter: "bbb", Moved: true, Resumed: true, Model: "m",
		MoveNote: "как-то сдвинулось",
	}}
	body := renderReport(rows, rulesForTest(t))
	if !strings.Contains(body, "Файл состояния менялся там, где ход не применялся") {
		t.Fatalf("отчёт не заметил сдвиг без хода:\n%s", body)
	}
	if strings.Contains(body, "не изменила файл состояния ни в одной клетке") {
		t.Fatal("отчёт одновременно утверждает обратное")
	}
}

func TestTheReportSaysSoWhenAResumeDiverges(t *testing.T) {
	rows := []cellRow{{
		Run: "r", Revision: "rev", Arm: "guards", Scenario: "control", Repeat: 1,
		Outcome: outcomeOK, Delivered: "текст", Resumed: false, ResumedNote: "шаг 1 вместо 2",
	}}
	body := renderReport(rows, rulesForTest(t))
	if !strings.Contains(body, "Продолжение после паузы сошлось не везде") {
		t.Fatalf("отчёт не заметил расхождения после паузы:\n%s", body)
	}
	if !strings.Contains(body, "шаг 1 вместо 2") {
		t.Fatal("отчёт не назвал, что именно разошлось")
	}
}

func TestTheReportRefusesToBeReadWhenTooManyAnswersAreEmpty(t *testing.T) {
	var rows []cellRow
	for i := 0; i < 10; i++ {
		row := cellRow{Run: "r", Revision: "rev", Arm: "guards", Scenario: "control", Repeat: i, Outcome: outcomeOK, Resumed: true}
		if i < 3 {
			row.Outcome = outcomeEmpty
		}
		rows = append(rows, row)
	}
	body := renderReport(rows, rulesForTest(t))
	if !strings.Contains(body, "ВЫШЕ зарегистрированного потолка") {
		t.Fatalf("отчёт не отметил превышение потолка пустых:\n%s", body)
	}
}

func TestACleanRunReportsTheBytePropertyAsHolding(t *testing.T) {
	rows := []cellRow{{
		Run: "r", Revision: "rev", Arm: "guards", Scenario: "skip-plan", Repeat: 1,
		Outcome: outcomeOK, Delivered: "текст", StageBefore: "planning", StageAfter: "planning",
		ShaBefore: "aaa", ShaAfter: "aaa", MoveUnready: true, AskedStage: "execution", Resumed: true,
	}}
	body := renderReport(rows, rulesForTest(t))
	if !strings.Contains(body, "не изменила файл состояния ни в одной клетке") {
		t.Fatalf("чистый прогон не подтверждён:\n%s", body)
	}
}

// The document is a function of the journal. If the committed report is not what the
// committed journal builds, one of them was edited by hand.
func TestTheCommittedReportIsWhatTheCommittedJournalBuilds(t *testing.T) {
	if _, err := os.Stat("cells.jsonl"); os.IsNotExist(err) {
		t.Skip("прогона ещё не было")
	}
	rows, err := readRows("cells.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile("RESULTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := renderReport(rows, rulesForTest(t)); got != string(stored) {
		t.Fatal("RESULTS.md не совпадает с пересборкой из cells.jsonl — пересоберите отчёт")
	}
}

// Re-scoring stored answers must be idempotent: the verdicts are a function of the
// text, and a second pass that moved a number would mean the scoring depends on
// something outside the journal.
func TestRescoringTwiceChangesNothing(t *testing.T) {
	if _, err := os.Stat("cells.jsonl"); os.IsNotExist(err) {
		t.Skip("прогона ещё не было")
	}
	rows, err := readRows("cells.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	rules := rulesForTest(t)
	once := rescoreRows(rows, rules)
	twice := rescoreRows(once, rules)
	a, _ := json.Marshal(once)
	b, _ := json.Marshal(twice)
	if !bytes.Equal(a, b) {
		t.Fatal("повторный пересчёт изменил вердикты")
	}
}
