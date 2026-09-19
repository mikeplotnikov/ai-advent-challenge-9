package main

// Day 13's driver. It builds a task whose state is known exactly, puts the machine on
// a chosen stage without spending a single model call, and then asks one question.
//
// The setup never talks to the provider: stages, steps and carried results are written
// through the agent's own commands, so two arms of a comparison differ in exactly the
// field the comparison is about and in nothing else.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

const (
	outcomeOK        = "ok"
	outcomeEmpty     = "empty"
	outcomeTransport = "transport"
)

// emptyShareCeiling is the share of empty answers above which an arm's numbers are not
// interpreted. Pre-registered here rather than chosen after seeing the run.
const emptyShareCeiling = 0.05

type usageRow struct {
	Prompt     int     `json:"prompt"`
	Completion int     `json:"completion"`
	Cached     int     `json:"cached"`
	Missed     int     `json:"missed"`
	Reasoning  int     `json:"reasoning"`
	Cost       float64 `json:"cost"`
	Priced     bool    `json:"priced"`
}

func (u *usageRow) add(r agent.Usage) {
	if r.PromptTokens == 0 && r.CompletionTokens == 0 {
		return
	}
	billedBefore := u.Prompt > 0 || u.Completion > 0
	u.Prompt += r.PromptTokens
	u.Completion += r.CompletionTokens
	u.Cached += r.CachedTokens
	u.Missed += r.MissedTokens
	u.Reasoning += r.ReasoningTokens
	u.Cost += r.Cost
	u.Priced = r.Priced && (u.Priced || !billedBefore)
}

type criterionRow struct {
	Name     string   `json:"name"`
	What     string   `json:"what"`
	Accept   []string `json:"accept,omitempty"`
	Reject   []string `json:"reject,omitempty"`
	FromMove bool     `json:"fromMove"`
}

type planRow struct {
	Kind              string         `json:"kind"`
	Run               string         `json:"run"`
	Started           string         `json:"started"`
	Model             string         `json:"model"`
	Commit            string         `json:"commit"`
	Seed              int64          `json:"seed"`
	Task              string         `json:"task"`
	Plan              []string       `json:"plan"`
	Scenarios         []scenario     `json:"scenarios"`
	ResumeArms        []arm          `json:"resumeArms"`
	DriveArms         []arm          `json:"driveArms"`
	CarryArms         []arm          `json:"carryArms"`
	Probes            []probe        `json:"probes"`
	Criteria          []criterionRow `json:"criteria"`
	Cells             int            `json:"cells"`
	EmptyShareCeiling float64        `json:"emptyShareCeiling"`
	Pilot             bool           `json:"pilot"`
	Notes             []string       `json:"notes"`
}

// showcaseRow is E0 and E1: slide 22 made literal, on both stage sets. Answers are kept
// whole. It is a demonstration, not a statistic.
type showcaseRow struct {
	Kind     string   `json:"kind"`
	Run      string   `json:"run"`
	StageSet string   `json:"stageSet"`
	Scenario string   `json:"scenario"`
	Stage    string   `json:"stage"`
	Step     int      `json:"step"`
	Total    int      `json:"total"`
	Question string   `json:"question"`
	Answer   string   `json:"answer"`
	Block    string   `json:"block"`
	Resumed  string   `json:"resumed"`
	Usage    usageRow `json:"usage"`
	Outcome  string   `json:"outcome"`
	Error    string   `json:"error,omitempty"`
}

type probeRow struct {
	Kind     string          `json:"kind"`
	Run      string          `json:"run"`
	Order    int             `json:"order"`
	Family   string          `json:"family"`
	Arm      string          `json:"arm"`
	Probe    string          `json:"probe"`
	Scenario string          `json:"scenario"`
	Stage    string          `json:"stage"`
	Step     int             `json:"step"`
	Repeat   int             `json:"repeat"`
	Question string          `json:"question"`
	Answer   string          `json:"answer"`
	Scores   map[string]bool `json:"scores"`
	Outcome  string          `json:"outcome"`

	// What the model asked of the machine, and what the machine answered.
	MoveStepAsked    bool   `json:"moveStepAsked"`
	MoveStepApplied  bool   `json:"moveStepApplied"`
	MoveStageAsked   string `json:"moveStageAsked"`
	MoveStageApplied bool   `json:"moveStageApplied"`
	MoveIllegal      bool   `json:"moveIllegal"`
	// StateAfter is where the machine actually stood once the turn was over. It is
	// recorded so a report never has to assume the refusal held.
	StateAfter string `json:"stateAfter"`
	StepAfter  int    `json:"stepAfter"`

	StateBlock     string   `json:"stateBlock"`
	StateTokens    int      `json:"stateTokens"`
	SentViolations []string `json:"sentViolations,omitempty"`
	EstimateTotal  int      `json:"estimateTotal"`
	Truncated      bool     `json:"truncated"`
	Usage          usageRow `json:"usage"`
	RequestedModel string   `json:"requestedModel"`
	ServedModel    string   `json:"servedModel"`
	Peak           bool     `json:"peak"`
	At             string   `json:"at"`
	ElapsedMs      int64    `json:"elapsedMs"`
	Attempts       int      `json:"attempts"`
	RetryErrors    []string `json:"retryErrors,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type completeRow struct {
	Kind      string       `json:"kind"`
	Run       string       `json:"run"`
	Finished  string       `json:"finished"`
	ProbeRows int          `json:"probeRows"`
	Errors    int          `json:"errors"`
	Empty     int          `json:"empty"`
	Spend     agent.Totals `json:"spend"`
}

func main() {
	out := flag.String("out", "", "куда записать JSONL прогона")
	report := flag.String("report", "", "построить RESULTS.md из этого JSONL и напечатать в stdout")
	model := flag.String("model", "deepseek-flash", "модель; в строках пишется и ответившая модель")
	workers := flag.Int("workers", 4, "параллельных вызовов")
	seed := flag.Int64("seed", 0, "seed перемешивания порядка; 0 — от времени, записывается в план")
	pilot := flag.Bool("pilot", false, "пилот: по одной клетке на пробу, без демонстраций и без статистики")
	dump := flag.Bool("dump", false, "выгрузить набор стадий, критерии и сборку запроса для сверки витрины и выйти")
	rescoreIn := flag.String("rescore", "", "пересчитать вердикты в этом JSONL сегодняшними критериями (нужен -out)")
	flag.Parse()

	if *rescoreIn != "" {
		if strings.TrimSpace(*out) == "" {
			fail(errors.New("-rescore требует -out: исходная выгрузка не переписывается на месте"))
		}
		rows, err := readRows(*rescoreIn)
		if err != nil {
			fail(err)
		}
		rescored, err := rescore(rows)
		if err != nil {
			fail(err)
		}
		scored := make([]any, 0, len(rows))
		for _, r := range rescored {
			scored = append(scored, r)
		}
		if err := writeRows(*out, scored, nil); err != nil {
			fail(err)
		}
		fmt.Fprintf(os.Stderr, "пересчитано строк: %d → %s\n", len(scored), *out)
		return
	}

	if *dump {
		if err := writeDefinitions(os.Stdout); err != nil {
			fail(err)
		}
		return
	}
	if *report != "" {
		rows, err := readRows(*report)
		if err != nil {
			fail(err)
		}
		text, err := render(rows, *report)
		if err != nil {
			fail(err)
		}
		fmt.Print(text)
		return
	}
	if strings.TrimSpace(*out) == "" {
		fail(errors.New("-out обязателен, например: -out day-13/state.jsonl"))
	}
	if *workers < 1 {
		fail(errors.New("число потоков должно быть положительным"))
	}
	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	if err := runAll(context.Background(), *out, *model, *workers, *seed, *pilot); err != nil {
		fail(err)
	}
}

// recorder keeps the bytes of the last request: the evidence of what actually travelled.
type recorder struct {
	client agent.Caller
	mu     sync.Mutex
	wire   string
}

func (r *recorder) AskWith(ctx context.Context, messages []llm.Message, opts llm.Options) (llm.Answer, error) {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(m.Role)
		b.WriteString(":")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	r.mu.Lock()
	r.wire = b.String()
	r.mu.Unlock()
	return r.client.AskWith(ctx, messages, opts)
}

func (r *recorder) lastWire() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wire
}

// scriptedCaller answers the seeding turns from a fixed list and refuses anything else.
// Seeding must not reach the provider: a silent call here would be a billed surprise
// inside a function that looks local, and it would also make the two arms differ.
type scriptedCaller struct {
	answers []string
	n       int
}

func (s *scriptedCaller) AskWith(context.Context, []llm.Message, llm.Options) (llm.Answer, error) {
	if s.n >= len(s.answers) {
		return llm.Answer{}, errors.New("сценарий исчерпан: подготовка не должна ходить в модель")
	}
	a := llm.Answer{Content: s.answers[s.n], Model: "scripted"}
	s.n++
	return a, nil
}

// noCaller refuses every call outright.
type noCaller struct{}

func (noCaller) AskWith(context.Context, []llm.Message, llm.Options) (llm.Answer, error) {
	return llm.Answer{}, errors.New("подготовка клетки не ходит в модель")
}

// memoryCfg points the layers at this cell's own tree. The task name has to be named
// here: which task is active is a property of the run, not something the conversation
// file remembers, so an agent built without it would wake up with no task and therefore
// with no state — which is exactly the bug this comment exists to prevent returning.
func memoryCfg(dir, task string) *agent.MemoryConfig {
	return &agent.MemoryConfig{Dir: dir, User: measureUser, Session: "s1", Task: task}
}

func storeOf(dir string) agent.Store {
	return agent.NewFileStore(agent.MemorySessionPath(dir, measureUser, "s1"))
}

// seed builds one cell's world: the task, the approved plan, the stage the person comes
// back to, and — for the arms that have it — yesterday's conversation.
//
// Every step of it runs against a local caller, so the fixture is identical in every arm
// by construction rather than by hope.
func seed(dir string, sc scenario, set string, withHistory, withCarry bool) error {
	cfg := agent.Config{
		Name: "day-13", SystemPrompt: measureSystem,
		Memory: memoryCfg(dir, ""),
		// Day 15 made "guards" the default strictness. Day 13's driver pins the
		// mode it measured — the table alone — so that re-running it reproduces day
		// 13 and not a later day's agent wearing day 13's name.
		Task: &agent.TaskConfig{Inject: true, Stages: set, Control: agent.ControlTable},
	}
	if withHistory {
		var answers []string
		for _, h := range measureHistory {
			answers = append(answers, h[1])
		}
		cfg.Store = storeOf(dir)
		a, err := agent.New(&scriptedCaller{answers: answers}, cfg)
		if err != nil {
			return err
		}
		if err := prepareTask(a, sc, withCarry); err != nil {
			return err
		}
		for _, h := range measureHistory {
			if _, err := a.Ask(context.Background(), h[0]); err != nil {
				return fmt.Errorf("история %q: %w", h[0], err)
			}
		}
		return nil
	}
	// The "none" arm gets no conversation and no task at all: it is the control that
	// shows what the question alone can produce.
	a, err := agent.New(noCaller{}, agent.Config{
		Name: "day-13", SystemPrompt: measureSystem, Memory: memoryCfg(dir, ""),
	})
	if err != nil {
		return err
	}
	_ = a
	return nil
}

// prepareTask walks the machine to the scenario's stage through the transition table,
// so a scenario that the table does not allow cannot be measured by mistake.
func prepareTask(a *agent.Agent, sc scenario, withCarry bool) error {
	if err := a.StartTask(measureTask); err != nil {
		return err
	}
	if err := a.PlanTask(measurePlan); err != nil {
		return err
	}
	view := a.TaskState()
	stages := view.Stages
	for i, stage := range stages {
		if stage == sc.Stage {
			break
		}
		carry := ""
		if withCarry {
			switch i {
			case 0:
				carry = planningCarry
			case 1:
				carry = executionCarry
			}
		}
		next := stages[i+1]
		if err := a.TaskGo(next, carry); err != nil {
			return fmt.Errorf("подготовка: %w", err)
		}
	}
	for a.TaskState().Step < sc.Step {
		if err := a.StepDone(); err != nil {
			return fmt.Errorf("подготовка шага: %w", err)
		}
	}
	return nil
}

type cell struct {
	Family string
	Arm    arm
	Probe  probe
	Repeat int
}

func planCells(pilot bool) []cell {
	var cells []cell
	add := func(arms []arm, probes []probe, repeats func(probe) int) {
		for _, a := range arms {
			for _, p := range probes {
				for r := 1; r <= repeats(p); r++ {
					cells = append(cells, cell{p.Family, a, p, r})
				}
			}
		}
	}
	if pilot {
		one := func(probe) int { return 1 }
		add(resumeArms[:1], resumeProbes[1:2], one)
		add(driveArms, driveProbes, one)
		add(carryArms[:1], carryProbes, one)
		return cells
	}
	full := func(p probe) int { return p.Repeats }
	add(resumeArms, resumeProbes, full)
	add(driveArms, driveProbes, full)
	add(carryArms, carryProbes, full)
	return cells
}

func runAll(ctx context.Context, out, model string, workers int, seedValue int64, pilot bool) error {
	llm.LoadDotEnv(".env")
	client, err := llm.New()
	if err != nil {
		return err
	}
	client.Model = model
	run := fmt.Sprintf("day13-%d", time.Now().UnixNano())

	cells := planCells(pilot)
	rand.New(rand.NewSource(seedValue)).Shuffle(len(cells), func(i, j int) { cells[i], cells[j] = cells[j], cells[i] })

	plan := planRow{
		Kind: "plan", Run: run, Started: time.Now().UTC().Format(time.RFC3339), Model: model,
		Commit: gitCommit(), Seed: seedValue, Task: measureTask, Plan: measurePlan,
		Scenarios:  append(append([]scenario{}, resumeScenarios...), bugfixScenarios...),
		ResumeArms: resumeArms, DriveArms: driveArms, CarryArms: carryArms,
		Probes: allProbes(), Criteria: criteriaRows(), Cells: len(cells),
		EmptyShareCeiling: emptyShareCeiling, Pilot: pilot,
		Notes: []string{
			"порядок клеток перемешан по seed",
			"temperature не отправляется, thinking выключен",
			"стадия и шаг выставляются командами агента, без единого вызова модели",
			"история одинакова во всех руках с историей и обрывается на шаге 1 — в этом и смысл слайда 22",
			"пустое содержимое (outcome=empty) и сбой вызова (outcome=transport) различаются: первое не повторяется, второе повторяется один раз",
			"доля empty выше потолка делает числа руки непригодными для интерпретации",
			"критерии откалиброваны на фикстурах до прогона; asked_* читаются из решения машины, а не из текста",
		},
	}
	rows := []any{plan}

	var spend agent.Totals
	if !pilot {
		shows, showSpend, err := runShowcase(ctx, client, model, run)
		for _, r := range shows {
			rows = append(rows, r)
		}
		spend = addTotals(spend, showSpend)
		if err != nil {
			return writeRows(out+".partial", rows, fmt.Errorf("демонстрация: %w", err))
		}
	}

	results := make([]probeRow, len(cells))
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				results[i] = runCell(ctx, client, run, model, i, cells[i])
				if (i+1)%50 == 0 {
					fmt.Fprintf(os.Stderr, "клеток готово: около %d из %d\n", i+1, len(cells))
				}
			}
		}()
	}
	for i := range cells {
		next <- i
	}
	close(next)
	wg.Wait()

	for _, r := range results {
		rows = append(rows, r)
	}
	spend, errorsCount, emptyCount := aggregate(spend, results)
	rows = append(rows, completeRow{
		Kind: "complete", Run: run, Finished: time.Now().UTC().Format(time.RFC3339),
		ProbeRows: len(results), Errors: errorsCount, Empty: emptyCount, Spend: spend,
	})
	fmt.Fprintf(os.Stderr, "готово: клеток %d, ошибок %d, пустых %d, %s\n",
		len(results), errorsCount, emptyCount, spend)
	return writeRows(out, rows, nil)
}

func allProbes() []probe {
	out := append([]probe{}, resumeProbes...)
	out = append(out, driveProbes...)
	return append(out, carryProbes...)
}

func criteriaRows() []criterionRow {
	var rows []criterionRow
	for _, c := range criteria {
		rows = append(rows, criterionRow{Name: c.Name, What: c.What, Accept: c.Accept, Reject: c.Reject})
	}
	for _, c := range moveCriteria {
		rows = append(rows, criterionRow{Name: c.Name, What: c.What, FromMove: true})
	}
	return rows
}

// runShowcase is E0 and E1: the slide-22 scenario played once on every pause point of
// both stage sets, with the answers and the injected block kept whole.
func runShowcase(ctx context.Context, client agent.Caller, model, run string) ([]showcaseRow, agent.Totals, error) {
	var rows []showcaseRow
	var spend agent.Totals
	for _, set := range []struct {
		name      string
		scenarios []scenario
	}{
		{agent.StandardStages, resumeScenarios},
		{agent.BugfixStages, bugfixScenarios},
	} {
		for _, sc := range set.scenarios {
			dir, err := os.MkdirTemp("", "day13-show-")
			if err != nil {
				return rows, spend, err
			}
			row := showcaseRow{
				Kind: "showcase", Run: run, StageSet: set.name, Scenario: sc.Name,
				Stage: string(sc.Stage), Step: sc.Step, Total: len(measurePlan),
				Question: resumeQuestion,
			}
			if err := seed(dir, sc, set.name, true, sc.Carry); err != nil {
				os.RemoveAll(dir)
				return rows, spend, fmt.Errorf("%s/%s: %w", set.name, sc.Name, err)
			}
			rec := &recorder{client: client}
			a, err := agent.New(rec, measureConfig(dir, set.name, true, true))
			if err != nil {
				os.RemoveAll(dir)
				return rows, spend, err
			}
			// The pause is a real one: the machine is put down and picked back up
			// through the same commands a person would use.
			if err := a.PauseTask(); err != nil {
				os.RemoveAll(dir)
				return rows, spend, err
			}
			view, err := a.ResumeTask()
			if err != nil {
				os.RemoveAll(dir)
				return rows, spend, err
			}
			row.Resumed = fmt.Sprintf("State: %s, шаг %d/%d. %s", view.State, view.Step, view.Total, view.Current)
			reply, err := a.Ask(ctx, resumeQuestion)
			row.Usage.add(reply.Usage)
			spend = addTotals(spend, totalsOf(reply.Usage))
			row.Block = stateBlockOf(rec.lastWire())
			switch {
			case errors.Is(err, llm.ErrEmptyContent):
				row.Outcome = outcomeEmpty
			case err != nil:
				row.Outcome, row.Error = outcomeTransport, err.Error()
			default:
				row.Outcome, row.Answer = outcomeOK, reply.Text
			}
			os.RemoveAll(dir)
			rows = append(rows, row)
			if row.Outcome == outcomeTransport {
				return rows, spend, fmt.Errorf("%s/%s: %v", set.name, sc.Name, err)
			}
		}
	}
	return rows, spend, nil
}

// measureConfig is the agent that asks the measured question. The arm without history
// gets no conversation file and no task: it is the control showing what the question
// alone produces, so it must not inherit either by accident.
func measureConfig(dir, set string, withHistory, injectState bool) agent.Config {
	task := ""
	if withHistory {
		task = measureTask
	}
	cfg := agent.Config{
		Name: "day-13", SystemPrompt: measureSystem, Thinking: "disabled", MaxTokens: answerTokens,
		Memory: memoryCfg(dir, task),
		Task:   &agent.TaskConfig{Inject: injectState, Stages: set, Control: agent.ControlTable},
	}
	if withHistory {
		cfg.Store = storeOf(dir)
	}
	return cfg
}

func runCell(ctx context.Context, client agent.Caller, run, model string, order int, c cell) probeRow {
	sc, ok := scenarioByName(c.Probe.Scenario)
	if !ok {
		return probeRow{Kind: "probe", Run: run, Order: order, Error: "неизвестный сценарий " + c.Probe.Scenario}
	}
	row := probeRow{
		Kind: "probe", Run: run, Order: order, Family: c.Family, Arm: c.Arm.Name,
		Probe: c.Probe.Name, Scenario: sc.Name, Stage: string(sc.Stage), Step: sc.Step,
		Repeat: c.Repeat, Question: c.Probe.Question, RequestedModel: model,
	}

	dir, err := os.MkdirTemp("", "day13-cell-")
	if err != nil {
		row.Outcome, row.Error = outcomeTransport, err.Error()
		return row
	}
	defer os.RemoveAll(dir)
	if err := seed(dir, sc, c.Probe.StageSet, c.Arm.History, c.Arm.Carry); err != nil {
		row.Outcome, row.Error = outcomeTransport, "подготовка: "+err.Error()
		return row
	}

	rec := &recorder{client: client}
	cfg := measureConfig(dir, c.Probe.StageSet, c.Arm.History, c.Arm.State)

	var reply agent.Reply
	var a *agent.Agent
	for attempt := 1; attempt <= 2; attempt++ {
		row.Attempts = attempt
		a, err = agent.New(rec, cfg)
		if err != nil {
			break
		}
		started := time.Now()
		reply, err = a.Ask(ctx, c.Probe.Question)
		row.At = started.UTC().Format(time.RFC3339Nano)
		row.Peak = llm.IsPeak(started)
		row.ElapsedMs = time.Since(started).Milliseconds()
		row.Usage.add(reply.Usage)
		// An empty answer is the model's outcome and is never retried; a failed call is
		// the transport's and is retried once.
		if err == nil || errors.Is(err, llm.ErrEmptyContent) {
			break
		}
		if attempt < 2 {
			row.RetryErrors = append(row.RetryErrors, err.Error())
		}
	}
	row.ServedModel = reply.Model
	row.EstimateTotal = reply.Estimated.Total
	row.Truncated = reply.Truncated
	row.StateBlock = stateBlockOf(rec.lastWire())
	row.MoveStepAsked = reply.Move.StepAsked
	row.MoveStepApplied = reply.Move.StepApplied
	row.MoveStageAsked = string(reply.Move.StageAsked)
	row.MoveStageApplied = reply.Move.StageApplied
	row.MoveIllegal = reply.Move.Illegal
	if a != nil {
		view := a.TaskState()
		row.StateAfter, row.StepAfter, row.StateTokens = string(view.State), view.Step, view.Tokens
	}
	if v := sentViolations(rec.lastWire(), c.Arm, sc); len(v) > 0 {
		row.SentViolations = v
	}

	switch {
	case errors.Is(err, llm.ErrEmptyContent):
		row.Outcome = outcomeEmpty
		return row
	case err != nil:
		row.Outcome, row.Error = outcomeTransport, err.Error()
		return row
	}
	row.Outcome = outcomeOK
	row.Answer = reply.Text
	row.Scores = map[string]bool{}
	for _, name := range c.Probe.Criteria {
		if isMoveCriterion(name) {
			row.Scores[name] = scoreMove(name, reply.Move)
			continue
		}
		crit, ok := criterionByName(name)
		if !ok {
			row.Error = "неизвестный критерий " + name
			return row
		}
		row.Scores[name] = crit.Test(reply.Text, sc.ctx())
	}
	return row
}

// stateBlockOf extracts the [TASK_STATE] block from the recorded request, so a row
// carries the evidence of what actually travelled rather than what was configured.
func stateBlockOf(wire string) string {
	start := strings.Index(wire, "[TASK_STATE]")
	if start < 0 {
		return ""
	}
	rest := wire[start:]
	if end := strings.Index(rest, "[USER_MESSAGE]"); end > 0 {
		return strings.TrimSpace(rest[:end])
	}
	return strings.TrimSpace(rest)
}

// sentViolations checks the request against what the arm promised. An arm whose block
// did not travel, or travelled when it should not have, invalidates its own cell, and
// finding that out from the report instead of from the bytes is finding it out too late.
func sentViolations(wire string, a arm, sc scenario) []string {
	var out []string
	hasBlock := strings.Contains(wire, "[TASK_STATE]")
	if a.State != hasBlock {
		out = append(out, fmt.Sprintf("блок состояния: ожидался %v, в запросе %v", a.State, hasBlock))
	}
	if a.State {
		if want := "stage: " + string(sc.Stage); !strings.Contains(wire, want) {
			out = append(out, "в блоке нет "+want)
		}
		hasCarry := strings.Contains(wire, "result.")
		wantCarry := a.Carry && sc.Carry
		if wantCarry != hasCarry {
			out = append(out, fmt.Sprintf("перенос стадии: ожидался %v, в запросе %v", wantCarry, hasCarry))
		}
	}
	if !a.History && strings.Contains(wire, measureHistory[0][1]) {
		out = append(out, "в руке без истории оказалась история")
	}
	return out
}

func aggregate(spend agent.Totals, results []probeRow) (agent.Totals, int, int) {
	var errorsCount, emptyCount int
	for _, r := range results {
		if r.Error != "" {
			errorsCount++
		}
		if r.Outcome == outcomeEmpty {
			emptyCount++
		}
		spend.Calls += r.Attempts
		u := r.Usage
		spend.PromptTokens += u.Prompt
		spend.CompletionTokens += u.Completion
		spend.CachedTokens += u.Cached
		spend.MissedTokens += u.Missed
		spend.ReasoningTokens += u.Reasoning
		spend.Cost += u.Cost
		if (u.Prompt > 0 || u.Completion > 0) && !u.Priced {
			spend.Unpriced++
		}
	}
	return spend, errorsCount, emptyCount
}

func totalsOf(u agent.Usage) agent.Totals {
	t := agent.Totals{Calls: 1, PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
		CachedTokens: u.CachedTokens, MissedTokens: u.MissedTokens, ReasoningTokens: u.ReasoningTokens, Cost: u.Cost}
	if (u.PromptTokens > 0 || u.CompletionTokens > 0) && !u.Priced {
		t.Unpriced = 1
	}
	return t
}

func addTotals(a, b agent.Totals) agent.Totals {
	a.Calls += b.Calls
	a.Failed += b.Failed
	a.PromptTokens += b.PromptTokens
	a.CompletionTokens += b.CompletionTokens
	a.ReasoningTokens += b.ReasoningTokens
	a.CachedTokens += b.CachedTokens
	a.MissedTokens += b.MissedTokens
	a.Cost += b.Cost
	a.Unpriced += b.Unpriced
	return a
}

func treeHash(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var normalized map[string]any
		if json.Unmarshal(raw, &normalized) == nil {
			delete(normalized, "updated")
			if raw2, err := json.Marshal(normalized); err == nil {
				raw = raw2
			}
		}
		h.Write([]byte(rel))
		h.Write(raw)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func writeRows(path string, rows []any, runErr error) error {
	f, err := os.Create(path)
	if err != nil {
		return errors.Join(runErr, err)
	}
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			f.Close()
			return errors.Join(runErr, err)
		}
	}
	if err := f.Close(); err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
}

func readRows(path string) ([]map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ошибка:", err)
	os.Exit(1)
}

// probeByName finds a probe's pre-registration.
func probeByName(name string) (probe, bool) {
	for _, p := range allProbes() {
		if p.Name == name {
			return p, true
		}
	}
	return probe{}, false
}

// rescore recomputes every text verdict from the answers already recorded, using today's
// criteria. It exists so that fixing an instrument never means paying for the run again,
// and never means leaving a number that the instrument no longer supports.
//
// It touches only `scores`: the answers, the usage and the machine's own decisions stay
// exactly as the run wrote them. Verdicts read from the machine (`asked_*`) are taken
// from the recorded move fields rather than recomputed from text.
func rescore(rows []map[string]any) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		copied := map[string]any{}
		for k, v := range row {
			copied[k] = v
		}
		if str(copied, "kind") == "probe" && str(copied, "outcome") == outcomeOK {
			// A row naming a probe or scenario this build no longer has cannot be
			// rescored, and silently keeping its old verdicts would present stale,
			// pre-fix numbers as freshly recomputed ones. Refuse instead: the whole
			// point of the command is that every verdict in the output came from
			// today's criteria.
			p, okProbe := probeByName(str(copied, "probe"))
			sc, okScenario := scenarioByName(str(copied, "scenario"))
			if !okProbe {
				return nil, fmt.Errorf("строка %v: пробы %q нет в предрегистрации — пересчитать её нечем",
					copied["order"], str(copied, "probe"))
			}
			if !okScenario {
				return nil, fmt.Errorf("строка %v: сценария %q нет в предрегистрации — пересчитать её нечем",
					copied["order"], str(copied, "scenario"))
			}
			{
				{
					answer := str(copied, "answer")
					move := agent.TaskMove{
						StepAsked:    boolOf(copied, "moveStepAsked"),
						StepApplied:  boolOf(copied, "moveStepApplied"),
						StageAsked:   agent.TaskStage(str(copied, "moveStageAsked")),
						StageApplied: boolOf(copied, "moveStageApplied"),
						Illegal:      boolOf(copied, "moveIllegal"),
					}
					scores := map[string]any{}
					for _, name := range p.Criteria {
						if isMoveCriterion(name) {
							scores[name] = scoreMove(name, move)
							continue
						}
						if crit, ok := criterionByName(name); ok {
							scores[name] = crit.Test(answer, sc.ctx())
						}
					}
					copied["scores"] = scores
				}
			}
		}
		out = append(out, copied)
	}
	return out, nil
}
