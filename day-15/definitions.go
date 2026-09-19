package main

// The dump the showcase checks itself against. The page runs on JavaScript and the
// agent runs on Go, and "the two agree" has to be a machine-checked fact rather than a
// promise kept by whoever edited last: since day 5 the JS side asserts against this
// output, and a test here fails when the dump goes stale.
//
// Everything in it is PRODUCED by running the code, never transcribed: the block comes
// out of a real assembled request, and every refusal string comes out of a real refused
// attempt. A dump written by hand would let the mirror agree with a description of the
// agent instead of with the agent.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

type definitions struct {
	Task string   `json:"task"`
	Plan []string `json:"plan"`
	// Controls are the strictness modes, in the order the page offers them.
	Controls  []string       `json:"controls"`
	StageSets []stageSetDump `json:"stageSets"`
	Arms      []armSpec      `json:"arms"`
	Scenarios []scenarioSpec `json:"scenarios"`
	// Rules is the stage-scope rule set the run carries, as it travels.
	Rules []agent.Invariant `json:"rules"`
	// InvariantsBlock is that set rendered exactly as the model sees it.
	InvariantsBlock string `json:"invariantsBlock"`
	// BlockOrder is the assembly order of a request, which the showcase reproduces.
	BlockOrder []string `json:"blockOrder"`
	// Example is one real request's state block, with the state that produced it.
	Example exampleDump `json:"example"`
	// Refusals are the exact words of every kind of refusal, produced by refusing.
	// A mirror that paraphrased one would be showing the visitor a different agent.
	Refusals []refusalDump `json:"refusals"`
	// Detector is the structural check's own worked cases: text and stage in,
	// verdict out. These are the cases where two implementations can quietly
	// disagree, not the ones that are obviously right.
	Detector []detectorCase `json:"detector"`
}

type stageSetDump struct {
	Name        string              `json:"name"`
	About       string              `json:"about"`
	Stages      []string            `json:"stages"`
	Transitions map[string][]string `json:"transitions"`
	Expect      map[string]string   `json:"expect"`
	// Guards are the preconditions per edge, keyed "from→to".
	Guards map[string][]agent.RequirementInfo `json:"guards"`
	// Rollbacks are the edges that go back, keyed the same way.
	Rollbacks []string `json:"rollbacks"`
}

type exampleDump struct {
	Scenario string                    `json:"scenario"`
	Context  agent.TaskContext         `json:"context"`
	Block    string                    `json:"block"`
	Blocked  []agent.BlockedTransition `json:"blocked"`
	// Request is the whole assembled request of that scenario's question: the system
	// message with the rules already in it, and the user message with the state block
	// in front of the question. The showcase sends its own version of this, and a
	// mirror that agreed on every block while assembling them differently would be
	// showing the visitor a request the agent never sends.
	Request []wireMessage `json:"request"`
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type refusalDump struct {
	Case    string `json:"case"`
	From    string `json:"from"`
	To      string `json:"to"`
	Control string `json:"control"`
	Message string `json:"message"`
}

type detectorCase struct {
	Case       string   `json:"case"`
	Stage      string   `json:"stage"`
	Text       string   `json:"text"`
	Violations []string `json:"violations"`
}

// detectorCases are the ones a second implementation gets wrong: an unclosed fence, a
// diff with no fence at all, prose that talks about code without showing any, and the
// same code one stage later where it is the work of the stage rather than a jump.
func detectorCases() []struct{ name, stage, text string } {
	return []struct{ name, stage, text string }{
		{"блок кода на планировании", "planning", "Начну сразу:\n```kotlin\nfun main() {}\n```"},
		{"тот же код на реализации", "execution", "Начну сразу:\n```kotlin\nfun main() {}\n```"},
		{"незакрытый блок", "planning", "```kotlin\nfun main() {"},
		{"дифф без ограждения", "planning", "--- a/main.kt\n+++ b/main.kt\n@@ -1,2 +1,3 @@\n+val x = 1"},
		{"план словами", "planning", "План: 1) модуль JWT, 2) проверка токена, 3) отзыв."},
		{"реализация словами", "planning", "Сделай класс TokenService с методом validate, он парсит заголовок."},
		{"инлайн-код", "planning", "Шаг 2 закрывает функция `validate`, но пишем её позже."},
		{"знак @@ в тексте", "planning", "Пометил спорные места как @@ спорно."},
	}
}

func buildDefinitions(invPath string) (definitions, error) {
	rules, err := loadRules(invPath)
	if err != nil {
		return definitions{}, err
	}
	defs := definitions{
		Task:            measureTask,
		Plan:            measurePlan,
		Controls:        agent.ControlModes(),
		Arms:            arms(),
		Scenarios:       scenarios(),
		Rules:           rules,
		InvariantsBlock: agent.InvariantsBlock(rules),
		BlockOrder: []string{
			"system: базовый промпт",
			"system: [INVARIANTS]",
			"system: [PROFILE]",
			"system: [LONG_TERM_MEMORY]",
			"history: краткосрочный слой",
			"user: [TASK_STATE]",
			"user: [WORKING_MEMORY]",
			"user: [PLAN]",
			"user: [USER_MESSAGE]",
		},
	}

	for _, name := range agent.StageSetNames() {
		set, err := agent.LookupStageSet(name)
		if err != nil {
			return defs, err
		}
		dump := stageSetDump{
			Name: set.Name, About: set.About,
			Transitions: map[string][]string{}, Expect: map[string]string{},
			Guards: map[string][]agent.RequirementInfo{},
		}
		for _, stage := range set.Stages() {
			dump.Stages = append(dump.Stages, string(stage))
			// In the rule's own order, which is the order the prompt prints — not
			// sorted, or the showcase would match a table the model never saw.
			allowed := []string{}
			for _, to := range set.Allow(stage) {
				allowed = append(allowed, string(to))
				edge := string(stage) + "→" + string(to)
				if reqs := set.Requirements(stage, to); len(reqs) > 0 {
					dump.Guards[edge] = reqs
				}
				if set.IsRollback(stage, to) {
					dump.Rollbacks = append(dump.Rollbacks, edge)
				}
			}
			dump.Transitions[string(stage)] = allowed
			dump.Expect[string(stage)] = set.Expect(stage)
		}
		defs.StageSets = append(defs.StageSets, dump)
	}

	for _, c := range detectorCases() {
		stage := agent.TaskStage(c.stage)
		defs.Detector = append(defs.Detector, detectorCase{
			Case: c.name, Stage: c.stage, Text: c.text,
			Violations: names(agent.CheckAnswerInStage(rules, c.text, stage)),
		})
	}

	// The example is the scenario that shows the most: a draft plan, so the block
	// carries a closed edge and the reason it is closed.
	block, ctx, blocked, request, err := recordBlock(scenarios()[0], rules)
	if err != nil {
		return defs, err
	}
	defs.Example = exampleDump{
		Scenario: scenarios()[0].Name, Context: ctx, Block: block,
		Blocked: blocked, Request: request,
	}

	refusals, err := recordRefusals(rules)
	if err != nil {
		return defs, err
	}
	defs.Refusals = refusals
	return defs, nil
}

// recorderOnly captures the request and never answers, so producing the dump costs
// nothing and cannot accidentally reach the provider.
type recorderOnly struct{ wire []llm.Message }

func (r *recorderOnly) AskWith(_ context.Context, messages []llm.Message, _ llm.Options) (llm.Answer, error) {
	r.wire = append(r.wire, messages...)
	return llm.Answer{}, errEnough
}

var errEnough = errorString("запрос записан")

type errorString string

func (e errorString) Error() string { return string(e) }

// recordBlock seeds one scenario and returns the state block the agent would send,
// the state that produced it and the edges that state closes.
func recordBlock(sc scenarioSpec, rules []agent.Invariant) (string, agent.TaskContext, []agent.BlockedTransition, []wireMessage, error) {
	var ctx agent.TaskContext
	dir, err := os.MkdirTemp("", "day15-dump-")
	if err != nil {
		return "", ctx, nil, nil, err
	}
	defer os.RemoveAll(dir)

	rec := &recorderOnly{}
	a, err := newCellAgent(rec, dir, arms()[len(arms())-1], rules, sc.Seed)
	if err != nil {
		return "", ctx, nil, nil, err
	}
	block := a.StateBlock()
	// The call fails on purpose: what is wanted is the request it assembled.
	_, _ = a.Ask(context.Background(), sc.Question)
	request := make([]wireMessage, 0, len(rec.wire))
	for _, m := range rec.wire {
		request = append(request, wireMessage{Role: m.Role, Content: m.Content})
	}
	raw, err := os.ReadFile(a.TaskState().Path)
	if err != nil {
		return "", ctx, nil, nil, err
	}
	if err := json.Unmarshal(raw, &ctx); err != nil {
		return "", ctx, nil, nil, err
	}
	// The timestamps change on every generation and say nothing the showcase needs.
	// Leaving them in would make the dump differ from itself on every run, and a
	// parity test that always fails is a parity test nobody reads.
	ctx.Updated = time.Time{}
	for i := range ctx.Carry {
		ctx.Carry[i].Updated = time.Time{}
	}
	for i := range ctx.Trail {
		ctx.Trail[i].At = time.Time{}
	}
	return block, ctx, a.TaskState().Blocked, request, nil
}

// recordRefusals produces every kind of refusal by actually being refused. The three
// kinds are the day's whole taxonomy — no such edge, the edge is closed, and the edge
// is open — and the page prints these strings verbatim.
func recordRefusals(rules []agent.Invariant) ([]refusalDump, error) {
	type attempt struct {
		name    string
		seed    seedSpec
		to      agent.TaskStage
		control string
	}
	attempts := []attempt{
		{"ребра нет", seedSpec{Stage: agent.StageExecution, Approve: true, Step: 1}, agent.StageDone, agent.ControlGuards},
		{"план не утверждён", seedSpec{Stage: agent.StagePlanning, Step: 1}, agent.StageExecution, agent.ControlGuards},
		{"план не пройден", seedSpec{Stage: agent.StageExecution, Approve: true, Step: 1}, agent.StageValidation, agent.ControlGuards},
		{"нет вердикта", seedSpec{Stage: agent.StageValidation, Approve: true, Step: len(measurePlan)}, agent.StageDone, agent.ControlGuards},
		{"стадии нет в наборе", seedSpec{Stage: agent.StagePlanning, Step: 1}, agent.StageFix, agent.ControlGuards},
		// The same premature move in the weaker mode, which lets it through: the page
		// shows both, and the difference is the day.
		{"то же самое без предусловий", seedSpec{Stage: agent.StagePlanning, Step: 1}, agent.StageExecution, agent.ControlTable},
	}
	var out []refusalDump
	for _, at := range attempts {
		dir, err := os.MkdirTemp("", "day15-refusal-")
		if err != nil {
			return nil, err
		}
		arm := armSpec{Name: "dump", Control: at.control}
		a, err := newCellAgent(&recorderOnly{}, dir, arm, rules, at.seed)
		if err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
		from := a.TaskState().State
		message := ""
		if err := a.TaskGo(at.to, ""); err != nil {
			message = err.Error()
		}
		out = append(out, refusalDump{
			Case: at.name, From: string(from), To: string(at.to),
			Control: at.control, Message: message,
		})
		os.RemoveAll(dir)
	}
	return out, nil
}

func writeDump(w io.Writer, invPath string) error {
	defs, err := buildDefinitions(invPath)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(defs)
}

// readDump parses a previously written dump, for the test that keeps it from going
// stale against the code.
func readDump(path string) (definitions, error) {
	var defs definitions
	raw, err := os.ReadFile(path)
	if err != nil {
		return defs, err
	}
	return defs, json.Unmarshal(raw, &defs)
}
