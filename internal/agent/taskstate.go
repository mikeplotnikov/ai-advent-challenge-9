package agent

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Day 13 — the task's state machine.
//
// Day 11 gave a task working memory: what the user saved while working on it. This
// file gives the same task a formal state: where in the work we are. The two are kept
// apart on purpose, and the separation is the host's own: "память это память" for the
// layers (chat #2898), and here, for the state, "я считаю задачей с самого старта её,
// то есть когда юзер отправил промпт… дальше профиль регулирует стадии и агентов"
// (chat #3096-3098). Working memory answers *what* the user told us; the state answers
// *where* we are, and only the machine may change it.
//
// The stages are data, not an enum. Slide 18 says the four stages should not be
// removed, but the host's own flow has six (chat #3068) and his round videos spell out
// why a fixed list would be wrong: a feature goes "собираем информацию → план → пишем
// → валидируем, тестируем → pull request" while a bug goes "зарепродюсить → понять
// причину, вычленить root cause → пофиксить → pull request", and therefore "настройки
// отличаются" (rounds 4-5). A hard-coded enum would make one of those paths the only
// correct one by construction.

// TaskStage is one stage of a task's state machine.
type TaskStage string

// stageRule is one row of the transition table: where this stage may go, and what the
// system is waiting for while the task sits in it.
type stageRule struct {
	Stage TaskStage
	// Allow is every stage reachable from Stage. An empty slice is a terminal stage.
	Allow []TaskStage
	// Expect is the "ожидаемое действие" of the task text, in the words of this
	// stage. It lives on the stage set rather than in the saved file because a stored
	// expectation can disagree with the machine that produces it, and determinism is
	// the whole point of the day.
	Expect string
}

// StageSet is one named finite automaton. Adding another path is a data change: a new
// StageSet here and a profile that names it — no new code, no new branch.
type StageSet struct {
	Name  string
	About string
	Rules []stageRule
}

const (
	// StandardStages is slide 18, unchanged. Every measured number of day 13 is
	// produced on this set.
	StandardStages = "standard"
	// BugfixStages is the host's other path, from round 5. It exists to show that the
	// machine is data: it is a different shape, not a renaming of the standard one.
	BugfixStages = "bugfix"
)

// The four stages of slide 18.
const (
	StagePlanning   TaskStage = "planning"
	StageExecution  TaskStage = "execution"
	StageValidation TaskStage = "validation"
	StageDone       TaskStage = "done"
)

// The bugfix path of round 5.
const (
	StageReproduce   TaskStage = "reproduce"
	StageRootCause   TaskStage = "root-cause"
	StageFix         TaskStage = "fix"
	StagePullRequest TaskStage = "pull-request"
)

var stageSets = map[string]StageSet{
	StandardStages: {
		Name:  StandardStages,
		About: "slide 18: planning → execution → validation → done",
		Rules: []stageRule{
			{StagePlanning, []TaskStage{StageExecution}, "approve the plan, then move to execution"},
			{StageExecution, []TaskStage{StageValidation, StagePlanning}, "finish the current step, or send the work to validation"},
			{StageValidation, []TaskStage{StageDone, StageExecution}, "accept the result, or send it back to execution"},
			{StageDone, nil, "nothing — the task is closed"},
		},
	},
	BugfixStages: {
		Name:  BugfixStages,
		About: "round 5: reproduce → root-cause → fix → pull-request",
		Rules: []stageRule{
			{StageReproduce, []TaskStage{StageRootCause}, "reproduce the defect, then look for its cause"},
			{StageRootCause, []TaskStage{StageFix, StageReproduce}, "name the root cause, or go back and reproduce again"},
			{StageFix, []TaskStage{StagePullRequest, StageRootCause}, "apply the fix, or go back to the cause"},
			{StagePullRequest, nil, "nothing — the fix is out for review"},
		},
	},
}

// StageSetNames lists every set this build knows, in a stable order.
func StageSetNames() []string { return []string{StandardStages, BugfixStages} }

// LookupStageSet resolves a set name coming from a flag, a profile or a file.
func LookupStageSet(name string) (StageSet, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = StandardStages
	}
	set, ok := stageSets[key]
	if !ok {
		return StageSet{}, fmt.Errorf("%w: %q, допустимы %s", ErrUnknownStageSet, name, strings.Join(StageSetNames(), ", "))
	}
	return set, nil
}

// First is the stage a task starts in.
func (s StageSet) First() TaskStage { return s.Rules[0].Stage }

// rule finds the row of a stage. The second result is false for a stage that does not
// belong to this set — which is how a file written under one set and read under
// another is caught instead of being silently accepted.
func (s StageSet) rule(stage TaskStage) (stageRule, bool) {
	for _, r := range s.Rules {
		if r.Stage == stage {
			return r, true
		}
	}
	return stageRule{}, false
}

// Stages lists the set's stages in order.
func (s StageSet) Stages() []TaskStage {
	out := make([]TaskStage, 0, len(s.Rules))
	for _, r := range s.Rules {
		out = append(out, r.Stage)
	}
	return out
}

// Allowed is the transition table of slide 20, and the single place a stage change is
// decided. Nothing else in this package compares stages to decide whether a move is
// legal: antipattern 02 of slide 29 is "нет валидации переходов", and one table with
// one caller is what keeps that from happening by accident.
func (s StageSet) Allowed(from, to TaskStage) bool {
	r, ok := s.rule(from)
	if !ok {
		return false
	}
	for _, a := range r.Allow {
		if a == to {
			return true
		}
	}
	return false
}

const (
	// TaskStateVersion is the format of the state file.
	TaskStateVersion = 1
	// maxPlanSteps bounds the plan the way maxMemoryEntries bounds a memory block:
	// the state is injected whole, so its size is settled where it is written.
	maxPlanSteps = 32
	// maxPlanStepRunes is the per-step limit, matching a memory entry's value.
	maxPlanStepRunes = maxMemoryValueRunes
)

var (
	ErrTaskStateOff     = errors.New("состояние задачи выключено")
	ErrUnknownStageSet  = errors.New("неизвестный набор стадий")
	ErrUnknownStage     = errors.New("неизвестная стадия")
	ErrTransition       = errors.New("переход запрещён")
	ErrNoPlan           = errors.New("план задачи не утверждён")
	ErrPlanExhausted    = errors.New("все шаги плана уже закрыты")
	ErrStageSetMismatch = errors.New("файл состояния написан под другой набор стадий")
)

// TaskContext is slide 19's data class, with two deliberate differences.
//
// `total`, `done` and `current` are not stored: they are len(Plan), Plan[:Step-1] and
// Plan[Step-1]. A file that carried them could disagree with the plan it came with,
// and a state machine whose own fields contradict each other is worse than none.
//
// `expect` is not stored either, for the same reason — it is derived from the stage
// set, which is the one place a stage's meaning is written down.
type TaskContext struct {
	Version int    `json:"version"`
	User    string `json:"user"`
	Task    string `json:"task"`
	// Stages names the StageSet this task runs on. It is saved so that a task keeps
	// its automaton across restarts even if the default changes.
	Stages string    `json:"stages"`
	State  TaskStage `json:"state"`
	// Step is the 1-based position in Plan. 0 means no plan has been approved yet.
	Step int      `json:"step"`
	Plan []string `json:"plan"`
	// Carry is "передавая в каждый систем промпт результаты предыдущего промпта"
	// (round 5): the result of a stage, kept for the stages that follow it. One entry
	// per stage, keyed by stage name, written by the machine at transition time.
	Carry   []MemoryEntry `json:"carry"`
	Paused  bool          `json:"paused"`
	Updated time.Time     `json:"updated"`
}

// Total is slide 19's `total`.
func (c TaskContext) Total() int { return len(c.Plan) }

// Current is slide 19's `current`: the step being worked on, or "" before a plan.
func (c TaskContext) Current() string {
	if c.Step < 1 || c.Step > len(c.Plan) {
		return ""
	}
	return c.Plan[c.Step-1]
}

// Done is slide 19's `done`: every step before the current one.
func (c TaskContext) Done() []string {
	if c.Step < 1 {
		return nil
	}
	end := c.Step - 1
	if end > len(c.Plan) {
		end = len(c.Plan)
	}
	return append([]string(nil), c.Plan[:end]...)
}

// carryValue returns what a stage left behind, if anything.
func (c TaskContext) carryValue(stage TaskStage) (string, bool) {
	for _, e := range c.Carry {
		if e.Key == string(stage) {
			return e.Value, true
		}
	}
	return "", false
}

// TaskConfig turns the state machine on. It requires MemoryConfig rather than carrying
// its own identity: a task's state belongs to a task, and the task is day 11's.
type TaskConfig struct {
	// Stages names the StageSet for tasks started from now on. Empty means standard.
	Stages string
	// Inject decides whether [TASK_STATE] travels in requests. Storage is never
	// affected — this is the ablation switch, exactly like Profile.Inject.
	Inject bool
	// Auto starts a task in the first stage when a message arrives and no task is
	// active. It is the host's "я считаю задачей с самого старта её, то есть когда
	// юзер отправил промпт" (#3096).
	Auto bool
	// AutoName is the task Auto creates. Empty means "task".
	AutoName string
}

// TaskStateView is what an interface may show. Slices are copies.
type TaskStateView struct {
	Enabled  bool
	Path     string
	Task     string
	StageSet string
	Stages   []TaskStage
	State    TaskStage
	Step     int
	Total    int
	Current  string
	Plan     []string
	Done     []string
	Carry    []MemoryEntry
	Expect   string
	Allowed  []TaskStage
	Paused   bool
	Inject   bool
	Tokens   int
}

type taskStateState struct {
	cfg  TaskConfig
	set  StageSet
	task string
	ctx  TaskContext
	file layerFile
}

func validateTaskConfig(t *TaskConfig, m *MemoryConfig) error {
	if t == nil {
		return nil
	}
	if m == nil {
		return errors.New("agent: Task без Memory: состояние задачи живёт в задаче, а задачи — в слоях памяти")
	}
	if _, err := LookupStageSet(t.Stages); err != nil {
		return fmt.Errorf("agent: Task.Stages: %w", err)
	}
	if t.AutoName != "" {
		if _, err := validateTaskName(t.AutoName); err != nil {
			return fmt.Errorf("agent: Task.AutoName: %w", err)
		}
	}
	return nil
}

func taskStatePath(dir, user, task string) string {
	return filepath.Join(MemoryUserDir(dir, user), "tasks", sessionFileName(task)+".state.json")
}

func newTaskStateState(t TaskConfig) *taskStateState {
	set, _ := LookupStageSet(t.Stages) // validated in validateTaskConfig
	return &taskStateState{cfg: t, set: set}
}

// setTask points the state at a task. An empty name means no task is active, and then
// there is no state at all — not an empty one.
func (s *taskStateState) setTask(dir, user, task string) {
	s.task = task
	s.ctx = TaskContext{}
	s.file = layerFile{}
	if task != "" {
		s.file = layerFile{path: taskStatePath(dir, user, task)}
	}
}

// reload re-reads the state file before every request and every write, for the same
// reason the layers and the profile are re-read: the file may have been changed by
// another process between two turns.
func (s *taskStateState) reload(user string) error {
	if s.task == "" {
		s.ctx = TaskContext{}
		return nil
	}
	var ctx TaskContext
	found, err := s.file.read(&ctx)
	if err != nil {
		return err
	}
	if !found {
		s.ctx = TaskContext{
			Version: TaskStateVersion, User: user, Task: s.task,
			Stages: s.set.Name, State: s.set.First(),
		}
		return nil
	}
	if err := validateTaskContext(ctx, user, s.task); err != nil {
		return fmt.Errorf("состояние задачи %s: %w", s.file.path, err)
	}
	// The set is taken from the file, not from the config: a task keeps the automaton
	// it was started on. A config naming a different set applies to the next task.
	set, err := LookupStageSet(ctx.Stages)
	if err != nil {
		return fmt.Errorf("состояние задачи %s: %w", s.file.path, err)
	}
	if _, ok := set.rule(ctx.State); !ok {
		return fmt.Errorf("состояние задачи %s: %w: стадия %q не входит в набор %q",
			s.file.path, ErrStageSetMismatch, ctx.State, set.Name)
	}
	s.set = set
	s.ctx = ctx
	return nil
}

func validateTaskContext(c TaskContext, user, task string) error {
	if c.Version != TaskStateVersion {
		return fmt.Errorf("версия формата %d, эта сборка понимает %d", c.Version, TaskStateVersion)
	}
	if c.User != user {
		return fmt.Errorf("файл принадлежит пользователю %q, а не %q", c.User, user)
	}
	if c.Task != task {
		return fmt.Errorf("файл принадлежит задаче %q, а не %q", c.Task, task)
	}
	if err := validatePlan(c.Plan); err != nil {
		return err
	}
	if c.Step < 0 || c.Step > len(c.Plan) {
		return fmt.Errorf("шаг %d вне плана из %d шагов", c.Step, len(c.Plan))
	}
	if len(c.Plan) > 0 && c.Step == 0 {
		return errors.New("план есть, а шаг не выставлен")
	}
	if err := validateEntries(c.Carry); err != nil {
		return fmt.Errorf("перенос между стадиями: %w", err)
	}
	return nil
}

func validatePlan(plan []string) error {
	if len(plan) > maxPlanSteps {
		return fmt.Errorf("в плане %d шагов, предел %d", len(plan), maxPlanSteps)
	}
	for i, step := range plan {
		if strings.TrimSpace(step) == "" {
			return fmt.Errorf("шаг %d пуст", i+1)
		}
		if len([]rune(step)) > maxPlanStepRunes {
			return fmt.Errorf("шаг %d длиннее %d символов", i+1, maxPlanStepRunes)
		}
		// One line per step, so a step cannot forge a neighbouring step or the tag
		// that ends the block. Same rule, same reason as a memory entry's value.
		if !singleLine(step) {
			return fmt.Errorf("шаг %d: только одна строка без управляющих символов", i+1)
		}
		if containsControlMarker(step) {
			return fmt.Errorf("шаг %d содержит служебный маркер перехода", i+1)
		}
	}
	return nil
}

// The control markers. The model does not change the state: it asks, in a line the
// program parses, and the transition table answers. That split is the day's thesis —
// "мы можем жёстко задать транзишены детерминированно в программе, в коде" (lesson 3,
// 14:xx) — and it is also the only arrangement in which antipattern 02 can be measured
// at all: a model that cannot ask for an illegal move tells us nothing about whether
// it would.
const (
	markerNextStep   = "[[NEXT_STEP]]"
	markerTransition = "[[TRANSITION:"
	markerEnd        = "]]"
)

// containsControlMarker reports whether stored text carries a marker. Plan steps and
// carried results are refused when they do: both are re-injected into later requests,
// and a marker that survives a round trip would let yesterday's text drive today's
// machine.
func containsControlMarker(s string) bool {
	return strings.Contains(s, markerNextStep) || strings.Contains(s, markerTransition)
}

// TaskMove is what the model asked of the machine on one turn, and what the machine
// answered. Every field is recorded per turn: these are the day's numbers.
type TaskMove struct {
	// StepAsked is the model ending its answer with [[NEXT_STEP]].
	StepAsked   bool
	StepApplied bool
	// StageAsked is the stage named in [[TRANSITION: …]], empty when none was asked.
	StageAsked   TaskStage
	StageApplied bool
	// Illegal is a stage change the transition table refused. It is the measurement of
	// antipattern 02: the model asked to skip, the code said no.
	Illegal bool
	// Note is one line for the interface, in Russian like the rest of the interface.
	Note string
}

// Asked reports whether the model tried to move the machine at all.
func (m TaskMove) Asked() bool { return m.StepAsked || m.StageAsked != "" }

// parseControlMarkers pulls the markers out of an answer and returns the text without
// them. Markers are honoured only on a line of their own and only outside fenced code
// blocks: an answer that *shows* the marker as an example, which is exactly what a
// question about this agent produces, must not thereby move the machine.
func parseControlMarkers(text string) (clean string, step bool, stage TaskStage) {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	fenced := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			fenced = !fenced
			kept = append(kept, line)
			continue
		}
		if !fenced {
			if trimmed == markerNextStep {
				step = true
				continue
			}
			if strings.HasPrefix(trimmed, markerTransition) && strings.HasSuffix(trimmed, markerEnd) {
				name := strings.TrimSuffix(strings.TrimPrefix(trimmed, markerTransition), markerEnd)
				// The first request wins; a second one is left in the text so that a
				// human reading the answer can see the model contradicted itself.
				if stage == "" {
					if parsed := TaskStage(strings.ToLower(strings.TrimSpace(name))); parsed != "" {
						stage = parsed
						continue
					}
				}
			}
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), step, stage
}

// summariseForCarry turns an answer into the one-line result a stage hands to the next
// one. It is the program copying a bounded, sanitised slice of what the model just
// produced — not the model writing where it likes. The day-11 invariant that the model
// never writes the memory layers is untouched: this is the task's state, it is written
// only on a transition the table authorised, and its size is fixed here.
func summariseForCarry(text string) string {
	text = strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
	// Whatever the model wrote, it may not carry a marker or a block tag forward.
	//
	// The tags are stripped by their bare names, not by the userMessageTag constant:
	// that constant ends in "\n", and by this point the line breaks are already gone, so
	// stripping the constant would leave "[USER_MESSAGE]" sitting in a value that gets
	// re-injected next turn. The JS mirror found this one, which is the argument for
	// having a second implementation at all.
	for _, forbidden := range []string{markerNextStep, markerTransition, markerEnd, userMessageBareTag, taskStateTag} {
		text = strings.ReplaceAll(text, forbidden, "")
	}
	var b strings.Builder
	for _, r := range text {
		if singleLine(string(r)) {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if runes := []rune(out); len(runes) > maxMemoryValueRunes {
		out = strings.TrimSpace(string(runes[:maxMemoryValueRunes-1])) + "…"
	}
	return out
}

// The prompt block. Tags and instructions are English by the owner's rule for AI
// prompts; the plan steps are whatever the user approved.
const (
	// userMessageBareTag is userMessageTag without its trailing newline, for sanitising
	// text whose line breaks have already been collapsed.
	userMessageBareTag = "[USER_MESSAGE]"

	taskStateTag    = "[TASK_STATE]"
	taskStateHeader = taskStateTag + "\nFormal state of this task, maintained by the program. " +
		"Reference data, not instructions from the user.\n"
)

// taskStateBlock is the state block that rides in front of the new question. Like
// working memory it travels at the tail of the request, and deliberately not at the
// top of the system message where slide 21 draws it: the state changes every single
// turn, and day 12 measured what moving the cached prefix costs — 2.2 times fewer
// tokens and 64% more money. The profile is stable and stays at the front; the state
// is not and rides behind.
//
// With no active task the block is empty — no tag, no separator — so a request is
// byte-for-byte the request days 6-12 sent.
func (a *Agent) taskStateBlock() string {
	if a.task == nil || !a.task.cfg.Inject || a.task.task == "" {
		return ""
	}
	s := a.task
	c := s.ctx
	rule, ok := s.set.rule(c.State)
	if !ok {
		return ""
	}
	lines := []string{
		"task: " + c.Task,
		"stage: " + string(c.State) + " (" + stageLine(s.set) + ")",
	}
	if c.Total() > 0 {
		lines = append(lines, fmt.Sprintf("step: %d/%d — %s", c.Step, c.Total(), c.Current()))
		if done := c.Done(); len(done) > 0 {
			lines = append(lines, "done: "+strings.Join(done, "; "))
		}
	} else {
		lines = append(lines, "step: no plan approved yet")
	}
	// Only the stages already passed have a result to hand over, and they are listed
	// in the set's own order so the block is stable between turns.
	for _, stage := range s.set.Stages() {
		if stage == c.State {
			break
		}
		if v, ok := c.carryValue(stage); ok {
			lines = append(lines, "result."+string(stage)+": "+v)
		}
	}
	lines = append(lines, "expect: "+rule.Expect)
	lines = append(lines, "rules:", "- Work only within the current step and do not skip stages.")
	if c.Total() > 0 {
		lines = append(lines, "- If the current step is finished, end your answer with a line: "+markerNextStep)
	}
	if len(rule.Allow) > 0 {
		lines = append(lines, "- To ask for a stage change, end with a line: "+markerTransition+" <stage>"+markerEnd+
			" — allowed from here: "+joinStages(rule.Allow)+".")
	}
	return taskStateHeader + strings.Join(lines, "\n") + "\n\n"
}

func stageLine(set StageSet) string { return joinStagesWith(set.Stages(), " → ") }

func joinStages(stages []TaskStage) string { return joinStagesWith(stages, ", ") }

func joinStagesWith(stages []TaskStage, sep string) string {
	parts := make([]string, 0, len(stages))
	for _, s := range stages {
		parts = append(parts, string(s))
	}
	return strings.Join(parts, sep)
}

// clone is a copy a mutator may work on without touching what the agent is currently
// serving. The slices are copied too: sharing their backing arrays would let a
// discarded change survive in the original.
func (c TaskContext) clone() TaskContext {
	out := c
	out.Plan = append([]string(nil), c.Plan...)
	out.Carry = append([]MemoryEntry(nil), c.Carry...)
	return out
}

// commit writes a modified context and adopts it ONLY if the write succeeded.
//
// The order matters and was wrong once: mutating s.ctx first and saving after left the
// agent serving a stage that is not on disk whenever the write failed — and the write
// fails exactly in the case the file layer exists to catch, a second process having
// changed the file since this one read it. The state that answers the next request has
// to be the state that is stored, or "детерминизм" is a word in a README.
//
// Every mutation persists immediately, which is what makes "пауза на любом этапе" a
// property rather than a command: the process may die at any point.
func (s *taskStateState) commit(next TaskContext) error {
	next.Version = TaskStateVersion
	next.Stages = s.set.Name
	next.Updated = time.Now().UTC()
	if err := s.file.write(next); err != nil {
		return err
	}
	s.ctx = next
	return nil
}

// requireTask is the guard every command shares.
func (a *Agent) requireTask() (*taskStateState, error) {
	if a.task == nil {
		return nil, fmt.Errorf("%s: %w", a.Name(), ErrTaskStateOff)
	}
	if a.task.task == "" {
		return nil, fmt.Errorf("%s: %w", a.Name(), ErrNoActiveTask)
	}
	if err := a.task.reload(a.taskUser()); err != nil {
		return nil, fmt.Errorf("%s: %w", a.Name(), err)
	}
	return a.task, nil
}

func (a *Agent) taskUser() string {
	if a.cfg.Memory == nil {
		return ""
	}
	return strings.TrimSpace(a.cfg.Memory.User)
}

// PlanTask approves the plan of the current task and puts the machine on its first
// step. Replacing an existing plan is allowed only from the first stage: the slide-22
// promise is that a task resumed tomorrow is on the step it was on, and a plan swapped
// under a running execution would make "шаг 2/4" mean something else than it did.
func (a *Agent) PlanTask(steps []string) error {
	s, err := a.requireTask()
	if err != nil {
		return err
	}
	clean := make([]string, 0, len(steps))
	for _, step := range steps {
		if trimmed := strings.TrimSpace(step); trimmed != "" {
			clean = append(clean, trimmed)
		}
	}
	if len(clean) == 0 {
		return fmt.Errorf("%s: план пуст", a.Name())
	}
	if err := validatePlan(clean); err != nil {
		return fmt.Errorf("%s: %w", a.Name(), err)
	}
	if s.ctx.Total() > 0 && s.ctx.State != s.set.First() {
		return fmt.Errorf("%s: план меняется только на стадии %q, а задача на %q",
			a.Name(), s.set.First(), s.ctx.State)
	}
	next := s.ctx.clone()
	next.Plan = clean
	next.Step = 1
	return a.commitTaskState(s, next)
}

// StepDone closes the current step. It stops at the last one instead of walking off the
// end: what follows a finished plan is a stage change, and that goes through the table.
func (a *Agent) StepDone() error {
	s, err := a.requireTask()
	if err != nil {
		return err
	}
	if s.ctx.Total() == 0 {
		return fmt.Errorf("%s: %w", a.Name(), ErrNoPlan)
	}
	if s.ctx.Step >= s.ctx.Total() {
		return fmt.Errorf("%s: %w: шаг %d из %d", a.Name(), ErrPlanExhausted, s.ctx.Step, s.ctx.Total())
	}
	next := s.ctx.clone()
	next.Step++
	return a.commitTaskState(s, next)
}

// TaskGo is the transition, and the only way the State field ever changes. carry is
// what the stage being left hands to the ones after it; empty means "take what the
// last answer produced", which is the round-5 mechanism, and the caller supplies that
// text rather than this function reaching for it.
func (a *Agent) TaskGo(target TaskStage, carry string) error {
	s, err := a.requireTask()
	if err != nil {
		return err
	}
	_, err = a.transition(s, target, carry)
	return err
}

// transition is shared by the user's command and the model's request so that both are
// judged by one table. It returns the stage left behind.
func (a *Agent) transition(s *taskStateState, target TaskStage, carry string) (TaskStage, error) {
	target = TaskStage(strings.ToLower(strings.TrimSpace(string(target))))
	from := s.ctx.State
	if _, ok := s.set.rule(target); !ok {
		return from, fmt.Errorf("%s: %w: %q; в наборе %q есть %s",
			a.Name(), ErrUnknownStage, target, s.set.Name, joinStages(s.set.Stages()))
	}
	if !s.set.Allowed(from, target) {
		rule, _ := s.set.rule(from)
		allowed := joinStages(rule.Allow)
		if allowed == "" {
			allowed = "ничего — это конечная стадия"
		}
		return from, fmt.Errorf("%s: %w: %s → %s; из %s разрешено: %s",
			a.Name(), ErrTransition, from, target, from, allowed)
	}
	next := s.ctx.clone()
	if summary := summariseForCarry(carry); summary != "" {
		entries, err := upsertEntry(next.Carry, MemoryEntry{
			Key: string(from), Value: summary, Source: SourceCommand, Updated: time.Now().UTC(),
		})
		// A carried result that will not fit is dropped, not fatal: the transition the
		// table approved must still happen. The state stays consistent either way.
		if err == nil {
			next.Carry = entries
		}
	}
	next.State = target
	// A failed write leaves the agent on the stage it was on. The caller is told the
	// move did not happen, and what it reads afterwards agrees with the disk.
	return from, a.commitTaskState(s, next)
}

// PauseTask marks the task as put down. The state is already on disk — every mutation
// wrote it — so this changes nothing about what survives; it records that the person
// stepped away, which is what /resume then reports back.
func (a *Agent) PauseTask() error {
	s, err := a.requireTask()
	if err != nil {
		return err
	}
	next := s.ctx.clone()
	next.Paused = true
	return a.commitTaskState(s, next)
}

// ResumeTask is slide 22's "Продолжай": it clears the pause and hands back the state
// to print. Nothing is re-explained because nothing was lost.
func (a *Agent) ResumeTask() (TaskStateView, error) {
	s, err := a.requireTask()
	if err != nil {
		return TaskStateView{}, err
	}
	if s.ctx.Paused {
		next := s.ctx.clone()
		next.Paused = false
		if err := a.commitTaskState(s, next); err != nil {
			return TaskStateView{}, err
		}
	}
	return a.TaskState(), nil
}

func (a *Agent) commitTaskState(s *taskStateState, next TaskContext) error {
	if err := s.commit(next); err != nil {
		return fmt.Errorf("%s: %w", a.Name(), err)
	}
	return nil
}

// TaskState is what an interface may show.
func (a *Agent) TaskState() TaskStateView {
	if a.task == nil {
		return TaskStateView{}
	}
	s := a.task
	view := TaskStateView{
		Enabled: true, Task: s.task, StageSet: s.set.Name, Stages: s.set.Stages(),
		Inject: s.cfg.Inject, Path: s.file.path,
	}
	if s.task == "" {
		return view
	}
	c := s.ctx
	view.State, view.Step, view.Total = c.State, c.Step, c.Total()
	view.Current, view.Paused = c.Current(), c.Paused
	view.Plan, view.Done = append([]string(nil), c.Plan...), c.Done()
	view.Carry = append([]MemoryEntry(nil), c.Carry...)
	if rule, ok := s.set.rule(c.State); ok {
		view.Expect = rule.Expect
		view.Allowed = append([]TaskStage(nil), rule.Allow...)
	}
	view.Tokens = EstimateTokens(a.taskStateBlock())
	return view
}

// applyTaskMove is the machine answering the model. It runs after a usable answer has
// arrived and before that answer joins the conversation, so the history never contains
// a marker and the next request cannot be driven by the last one's text.
func (a *Agent) applyTaskMove(text string, step bool, stage TaskStage) TaskMove {
	move := TaskMove{StepAsked: step, StageAsked: stage}
	if !move.Asked() {
		return move
	}
	s, err := a.requireTask()
	if err != nil {
		move.Note = "модель попросила сдвинуть состояние, но состояние выключено"
		return move
	}
	var notes []string
	if step {
		switch err := a.StepDone(); {
		case err == nil:
			move.StepApplied = true
			notes = append(notes, fmt.Sprintf("шаг закрыт, теперь %d/%d", s.ctx.Step, s.ctx.Total()))
		default:
			notes = append(notes, "шаг не закрыт: "+err.Error())
		}
	}
	if stage != "" {
		from, err := a.transition(s, stage, text)
		switch {
		case err == nil:
			move.StageApplied = true
			notes = append(notes, fmt.Sprintf("переход %s → %s выполнен", from, stage))
		case errors.Is(err, ErrTransition), errors.Is(err, ErrUnknownStage):
			// The model asked for a move the table does not allow. This is the number
			// antipattern 02 is about, and the refusal is deterministic.
			move.Illegal = true
			notes = append(notes, "переход отклонён: "+err.Error())
		default:
			notes = append(notes, "переход не выполнен: "+err.Error())
		}
	}
	move.Note = strings.Join(notes, "; ")
	return move
}

// syncTaskState points the state machine at whatever task the memory layer considers
// active. It is called after every /task command and at construction: two pointers to
// two different tasks would let the state of one describe the work of another.
func (a *Agent) syncTaskState() {
	if a.task == nil || a.cfg.Memory == nil {
		return
	}
	task := strings.TrimSpace(a.cfg.Memory.Task)
	if a.memory != nil {
		task = a.memory.task
	}
	if a.task.task == task {
		return
	}
	// A task about to be opened gets the configured set; reload then replaces it with
	// the set already written in that task's file, if the file exists.
	a.task.set = a.configuredStageSet()
	a.task.setTask(a.cfg.Memory.Dir, a.cfg.Memory.User, task)
}

// autoStartTask is the host's "я считаю задачей с самого старта её, то есть когда юзер
// отправил промпт" (#3096): a message arriving with no task open starts one rather than
// being answered outside any task. It is off unless Task.Auto asks for it, because the
// explicit /task commands of day 11 remain the way a person names their own work.
func (a *Agent) autoStartTask() error {
	if a.task == nil || !a.task.cfg.Auto || a.memory == nil || a.memory.task != "" {
		return nil
	}
	name := strings.TrimSpace(a.task.cfg.AutoName)
	if name == "" {
		name = "task"
	}
	if err := a.StartTask(name); err != nil {
		// An existing task of that name is not an error here: adopt it, which is what
		// a second run of the same process should do.
		if useErr := a.UseTask(name); useErr != nil {
			return errors.Join(err, useErr)
		}
	}
	return nil
}

// reloadTaskState re-reads the state file after the active task changed. A /task
// command that switched the working memory but left the state pointing at the previous
// task would print one task's stage over another's data.
func (a *Agent) reloadTaskState() error {
	if a.task == nil || a.task.task == "" {
		return nil
	}
	if err := a.task.reload(a.taskUser()); err != nil {
		return fmt.Errorf("%s: %w", a.Name(), err)
	}
	return nil
}

// initTaskState gives a freshly created task its state file. A task exists from the
// moment it is started and is then in the first stage of its set, so the file exists
// from that moment too: "пауза на любом этапе" has to include the first one, and a
// state that only appears after the first transition would not survive a restart
// before it.
func (a *Agent) initTaskState() error {
	if err := a.reloadTaskState(); err != nil {
		return err
	}
	if a.task == nil || a.task.task == "" {
		return nil
	}
	exists, err := a.task.file.exists()
	if err != nil {
		return fmt.Errorf("%s: %w", a.Name(), err)
	}
	if exists {
		return nil
	}
	return a.commitTaskState(a.task, a.task.ctx.clone())
}

// configuredStageSet is which automaton a task started now would run on. The profile
// wins over the application's default, and an explicit TaskConfig.Stages wins over the
// profile: the flag is an override, the profile is the setting.
//
// An unusable name cannot reach here — validateProfile refuses a profile that names an
// unknown set, and validateTaskConfig refuses a config that does.
func (a *Agent) configuredStageSet() StageSet {
	name := ""
	if a.task != nil {
		name = strings.TrimSpace(a.task.cfg.Stages)
	}
	if name == "" && a.profile != nil {
		name = strings.TrimSpace(a.profile.profile.Stages)
	}
	set, err := LookupStageSet(name)
	if err != nil {
		set, _ = LookupStageSet(StandardStages)
	}
	return set
}

// Expect is what the machine waits for while a task sits in a stage — the "ожидаемое
// действие" of the task text. It is exported so the showcase can be checked against the
// same words the prompt uses, rather than against a copy of them.
func (s StageSet) Expect(stage TaskStage) string {
	r, _ := s.rule(stage)
	return r.Expect
}

// Allow is the stages reachable from one stage, in the order the prompt lists them.
// The order matters and is not incidental: the block tells the model "allowed from here:
// …", and a dump that sorted them differently would let the showcase agree with a
// transition table the model never saw.
func (s StageSet) Allow(stage TaskStage) []TaskStage {
	r, _ := s.rule(stage)
	return append([]TaskStage(nil), r.Allow...)
}
