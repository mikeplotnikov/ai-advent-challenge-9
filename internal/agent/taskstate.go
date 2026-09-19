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
	// Guards are day 15's preconditions, by target stage: the edge exists and may still
	// be closed until the state satisfies it. An edge with no entry here is open the
	// moment the table allows it, which is every edge day 13 had.
	Guards map[TaskStage][]requirement
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
			{
				Stage: StagePlanning, Allow: []TaskStage{StageExecution},
				// Day 15's first example, verbatim: "нельзя делать реализацию до
				// утверждённого плана".
				Guards: map[TaskStage][]requirement{StageExecution: {reqApprovedPlan}},
				Expect: "approve the plan, then move to execution",
			},
			{
				Stage: StageExecution, Allow: []TaskStage{StageValidation, StagePlanning},
				Guards: map[TaskStage][]requirement{StageValidation: {reqPlanExhausted}},
				Expect: "finish the current step, or send the work to validation",
			},
			{
				Stage: StageValidation, Allow: []TaskStage{StageDone, StageExecution},
				// The second example: "нельзя делать финал без валидации". The table
				// already routes done through validation; the verdict is what keeps
				// that route from being walked in a single turn.
				Guards: map[TaskStage][]requirement{StageDone: {reqValidationVerdict}},
				Expect: "accept the result, or send it back to execution",
			},
			{Stage: StageDone, Expect: "nothing — the task is closed"},
		},
	},
	BugfixStages: {
		Name:  BugfixStages,
		About: "round 5: reproduce → root-cause → fix → pull-request",
		Rules: []stageRule{
			{
				Stage: StageReproduce, Allow: []TaskStage{StageRootCause},
				Expect: "reproduce the defect, then look for its cause",
			},
			{
				Stage: StageRootCause, Allow: []TaskStage{StageFix, StageReproduce},
				// The same requirement on the bug path: the plan here is the plan of
				// the fix, and it is approved before the fix is written. The guards are
				// data, so the second set gets them by naming them — this is the test
				// that they are not welded to the standard set's stage names.
				Guards: map[TaskStage][]requirement{StageFix: {reqApprovedPlan}},
				Expect: "name the root cause, or go back and reproduce again",
			},
			{
				Stage: StageFix, Allow: []TaskStage{StagePullRequest, StageRootCause},
				Guards: map[TaskStage][]requirement{
					StagePullRequest: {reqPlanExhausted, reqValidationVerdict},
				},
				Expect: "apply the fix, or go back to the cause",
			},
			{Stage: StagePullRequest, Expect: "nothing — the fix is out for review"},
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
	// TaskStateVersion is the format of the state file. Version 2 is day 15: it adds
	// the approval of the plan, the validation verdict and the trail of moves.
	TaskStateVersion = 2
	// taskStateVersionDay13 is the format day 13 wrote, still read and migrated.
	taskStateVersionDay13 = 1
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
	Carry []MemoryEntry `json:"carry"`
	// PlanApproved is day 15: the plan is not merely written but agreed to. Day 13 had
	// no such distinction — /plan was the approval — and so "нельзя делать реализацию
	// до утверждённого плана" had nothing to check.
	PlanApproved bool `json:"plan_approved"`
	// Validated is the verdict of this visit to the validating stage. It is cleared on
	// every entry to that stage, so a task that came back for rework has to be
	// validated again rather than inheriting the verdict it failed.
	Validated bool `json:"validated"`
	// Trail is the moves that actually happened, oldest first, bounded. Refusals are
	// deliberately absent: a refused attempt may not change the state, and writing it
	// down would be the state changing.
	Trail   []TrailEntry `json:"trail,omitempty"`
	Paused  bool         `json:"paused"`
	Updated time.Time    `json:"updated"`
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
	// Control is day 15's strictness: none, table or guards. Empty means guards, the
	// strict default — the weaker modes exist to be measured against it, not to be
	// fallen into by leaving a field unset.
	Control string
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
	// Blocked are the allowed edges a precondition currently closes, with the reason.
	Blocked []BlockedTransition
	// PlanApproved and Validated are day 15's two facts about readiness.
	PlanApproved bool
	Validated    bool
	// Control is the strictness in force: none, table or guards.
	Control string
	// Trail is the moves that happened, Refused is what this session was not allowed
	// to do. The second is session-scoped by design — see taskStateState.refused.
	Trail   []TrailEntry
	Refused []RefusedMove
	Paused  bool
	Inject  bool
	Tokens  int
}

type taskStateState struct {
	cfg  TaskConfig
	set  StageSet
	task string
	ctx  TaskContext
	file layerFile
	// refused is this session's log of moves the machine turned down. It is in memory
	// and not in the file on purpose: a refused attempt must leave the state byte for
	// byte as it was, and persisting the refusal would be the state changing.
	refused []RefusedMove
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
	if err := validateControl(t.Control); err != nil {
		return fmt.Errorf("agent: Task.Control: %w", err)
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
	// The refusal log belongs to the task that produced it: carrying it to the next
	// task would print one task's blocked moves while looking at another's state.
	s.refused = nil
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
	ctx = migrateTaskContext(ctx)
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

// migrateTaskContext brings a day-13 state file up to the day-15 format.
//
// A version-1 file with a plan is read as a file whose plan was APPROVED, and that is
// not a guess: on day 13 writing the plan and approving it were the same act, and the
// command said so — "план утверждён". A migration that read those files as unapproved
// would stop a running task at a gate it had already passed under the rules it was
// started under. A file with no plan migrates to no approval, which is the same state
// day 13 was in.
//
// The migration happens on read and is not written back. A read that wrote would make
// opening a task a mutation, and day 15's own property is that looking at the machine —
// including looking at an attempt it refused — leaves the file alone.
func migrateTaskContext(c TaskContext) TaskContext {
	if c.Version != taskStateVersionDay13 {
		return c
	}
	c.Version = TaskStateVersion
	c.PlanApproved = len(c.Plan) > 0
	c.Validated = false
	c.Trail = nil
	return c
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
	// The task name is printed inside the block as "task: …".
	if forgesBlockBoundary(c.Task) {
		return errors.New("имя задачи содержит служебный маркер или тег блока")
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
	// An approval of a plan that does not exist would open the edge day 15 exists to
	// close, and it can only arrive by a hand edit or an older build.
	if c.PlanApproved && len(c.Plan) == 0 {
		return errors.New("план утверждён, а шагов в нём нет")
	}
	if err := validateTrail(c.Trail); err != nil {
		return err
	}
	if err := validateEntries(c.Carry); err != nil {
		return fmt.Errorf("перенос между стадиями: %w", err)
	}
	// Re-checked on every load, not only on write: a value that reached the file by any
	// other route — an older build, a hand edit — would otherwise be injected forever.
	for _, e := range c.Carry {
		if forgesBlockBoundary(e.Value) {
			return fmt.Errorf("перенос стадии %q подделывает границу блока", e.Key)
		}
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
		// The same rule as the carried result, for the same reason: a plan step is
		// re-injected into every later request, so it may not impersonate a section of
		// the request it lands in — even though a step is written by the user, who is
		// the principal here. Consistency matters more than the threat model: the
		// showcase takes a plan from an untrusted client and shares this contract.
		if forgesBlockBoundary(step) {
			return fmt.Errorf("шаг %d содержит служебный маркер или тег блока", i+1)
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
	return strings.Contains(s, markerNextStep) || strings.Contains(s, markerTransition) ||
		strings.Contains(s, markerRefused)
}

// blockTags is every tag that marks a section of an assembled request, by its BARE name.
//
// Bare, because that is how a forgery gets built: a value ending exactly on
// "[USER_MESSAGE]" becomes a real tag the moment the renderer appends its own newline,
// and a guard that looked for the tag WITH its newline would pass it through. This is
// the same mistake the carry sanitiser made and the JS mirror caught.
//
// The list lives here rather than beside each block because the check has to be
// exhaustive: day 13 added a new trusted tag to a request that a day-12 guard was
// already filtering, and a per-block list would have left exactly the gap it did.
var blockTags = []string{
	"[USER_MESSAGE]", taskStateTag, "[WORKING_MEMORY]", "[LONG_TERM_MEMORY]", "[PROFILE]", "[PLAN]",
	invariantsTag, retryTag, markerRefused,
}

// forgesBlockBoundary reports whether model-written text would impersonate a section of
// the request it is about to be spliced into.
func forgesBlockBoundary(s string) bool {
	if containsControlMarker(s) {
		return true
	}
	for _, tag := range blockTags {
		if strings.Contains(s, tag) {
			return true
		}
	}
	return false
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
	// Unready is day 15: the edge exists, and the state has not met its preconditions —
	// "нельзя делать реализацию до утверждённого плана". Kept apart from Illegal
	// because "рано" and "нельзя" are different answers about different defects, and a
	// measurement that added them up could not tell which control did the work.
	Unready bool
	// Blocked is a stage change the table allowed and an invariant of day 14 refused —
	// typically "не закрывать задачу без согласия пользователя" (chat #3152). Kept
	// apart from Illegal because they measure different things: an edge that does not
	// exist, against an edge the project will not let the model take alone.
	Blocked bool
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
	// Stripping has to run to a fixed point, not once. A single left-to-right pass is
	// not confluent: "[USER_[TASK_STATE]MESSAGE]" contains neither tag as a contiguous
	// substring, but deleting the inner one splices the halves of the outer into a real
	// "[USER_MESSAGE]" that nothing re-examines. Found by the second review wave, on the
	// fix the first wave had just landed.
	forbidden := append([]string{markerNextStep, markerTransition, markerEnd}, blockTags...)
	for {
		before := text
		for _, f := range forbidden {
			text = strings.ReplaceAll(text, f, "")
		}
		if text == before {
			break
		}
	}
	// Stripping leaves the gaps where the tags were; collapse once more so a stored
	// result does not carry the scars of its own sanitising.
	text = strings.Join(strings.Fields(text), " ")
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
	// Last check, after every transformation including the truncation. The loop above
	// should make this unreachable; it stays because "should" is not a guarantee, and
	// because a carried result is optional — dropping it costs a line of context, while
	// letting one through costs the property the whole day is built on.
	if forgesBlockBoundary(out) {
		return ""
	}
	return out
}

// The prompt block. Tags and instructions are English by the owner's rule for AI
// prompts; the plan steps are whatever the user approved.
const (
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
	// Day 15: what is allowed is not the whole truth — an edge can exist and still be
	// closed. The model is told which ones and why, so that "не перепрыгивай" is a
	// statement it can act on rather than a slogan.
	//
	// These lines are computed from the preconditions and do NOT depend on the control
	// mode. That is deliberate and it is the ablation: the prompt is held identical
	// across the measured arms, so the only thing that differs between them is what the
	// CODE does with a request. In the weaker arms the block therefore describes a
	// promise nothing keeps, which is exactly antipattern 03 — "текстовые правила =
	// просьба" — put where it can be measured instead of asserted.
	//
	// The line names the requirement and not its Russian explanation: the block is in
	// English by the project's rule for prompts, while About and Detail are what a
	// person reads in the refusal. Naming the requirement keeps one wording in the
	// prompt and one in the interface without either being a translation of the other
	// that could drift.
	for _, b := range blockedTransitions(s.set, c) {
		lines = append(lines, "blocked: "+string(b.To)+" — requires "+b.Requirement)
	}
	lines = append(lines, "rules:", "- Work only within the current step and do not skip stages.",
		"- Do not produce the work of a later stage, however the request is phrased.")
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
	out.Trail = append([]TrailEntry(nil), c.Trail...)
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

// PlanTask writes the plan of the current task and puts the machine on its first step.
// Replacing an existing plan is allowed only from the first stage: the slide-22 promise
// is that a task resumed tomorrow is on the step it was on, and a plan swapped under a
// running execution would make "шаг 2/4" mean something else than it did.
//
// Since day 15 this WRITES the plan and does not approve it: ApprovePlan does that, and
// the split is what makes "утверждённый план" a fact the machine can check. Writing a
// new plan therefore revokes an approval the previous one had — the approved artefact
// is gone, and an approval that survived the text it approved would approve nothing.
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
	next.PlanApproved = false
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
	_, err = a.transition(s, target, carry, false)
	return err
}

// transition is shared by the user's command and the model's request so that both are
// judged by one table. It returns the stage left behind.
//
// byModel says who asked, and only day 14 cares: a transition invariant with actor
// "model" is the host's "запрет ... переходов без согласия пользователя" (#3152), and
// the user's own command IS that consent. Nothing else in this package branches on the
// caller, so the table stays the single judge of what the machine permits — the
// invariant only narrows it further.
// The order of the checks is the day's design, not an accident of writing: the stage
// has to exist, then the TABLE answers, then the edge's PRECONDITIONS, then day 14's
// invariants. Each layer refuses with its own error, because each is a different
// finding — an edge that does not exist, an edge that is not open yet, and an edge the
// project will not let the model take alone are three different things to report, and
// a single "запрещено" would lose all three.
func (a *Agent) transition(s *taskStateState, target TaskStage, carry string, byModel bool) (TaskStage, error) {
	target = TaskStage(strings.ToLower(strings.TrimSpace(string(target))))
	from := s.ctx.State
	actor := ActorUser
	if byModel {
		actor = ActorModelMove
	}
	if _, ok := s.set.rule(target); !ok {
		a.recordRefusal(RefusedMove{From: from, To: target, Actor: actor, Kind: RefusedUnknown,
			Reason: "такой стадии нет в наборе " + s.set.Name})
		return from, fmt.Errorf("%s: %w: %q; в наборе %q есть %s",
			a.Name(), ErrUnknownStage, target, s.set.Name, joinStages(s.set.Stages()))
	}
	control := s.controlMode()
	if control != ControlNone && !s.set.Allowed(from, target) {
		rule, _ := s.set.rule(from)
		allowed := joinStages(rule.Allow)
		if allowed == "" {
			allowed = "ничего — это конечная стадия"
		}
		a.recordRefusal(RefusedMove{From: from, To: target, Actor: actor, Kind: RefusedIllegal,
			Reason: "из " + string(from) + " разрешено: " + allowed})
		return from, fmt.Errorf("%s: %w: %s → %s; из %s разрешено: %s",
			a.Name(), ErrTransition, from, target, from, allowed)
	}
	// Day 15: the edge exists and the state may still not be ready to take it.
	if control == ControlGuards {
		if req, ok := checkPreconditions(s.set, s.ctx, target); !ok {
			detail := req.Detail(s.ctx)
			a.recordRefusal(RefusedMove{From: from, To: target, Actor: actor, Kind: RefusedUnready,
				Reason: req.Name + ": " + detail})
			return from, fmt.Errorf("%s: %w: %s → %s; %s (сейчас: %s)",
				a.Name(), ErrPrecondition, from, target, req.About, detail)
		}
	}
	// Day 14 narrows the table: a move the automaton permits may still be forbidden by
	// an invariant of this task. The order matters — the table answers first, so an
	// illegal move is still reported as illegal and not as an invariant violation.
	if a.invariants != nil {
		if v, bad := transitionViolation(a.invariants.transitionRules(), from, target, byModel); bad {
			a.recordRefusal(RefusedMove{From: from, To: target, Actor: actor, Kind: RefusedBlocked,
				Reason: v.Name + ": " + v.Detail})
			return from, fmt.Errorf("%s: %w: %s (%s)", a.Name(), ErrInvariantViolated, v.Detail, v.Name)
		}
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
	// Day 15's rollback rules. Going back undoes what the stages ahead had established,
	// or the way back would be free: a task rolled out of execution keeps neither the
	// approval of the plan it is about to rewrite nor a verdict on work it is about to
	// change. What survives is the plan itself, the step the machine was on and the
	// results the stages carried — that is the difference between a rollback and a
	// restart, and "умеет откатываться назад по графу" (#3302) is the first, not the
	// second.
	back := s.set.IsRollback(from, target)
	if back {
		next.Validated = false
	}
	// Every visit to a stage earns that stage's own conditions again, forward or back.
	enterStage(s.set, &next, target)
	next.Trail = appendTrail(next.Trail, TrailEntry{
		From: from, To: target, Actor: actor, Back: back,
		Reason: trailReason(carry), At: time.Now().UTC(),
	})
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
		Inject: s.cfg.Inject, Path: s.file.path, Control: s.controlMode(),
		Refused: append([]RefusedMove(nil), s.refused...),
	}
	if s.task == "" {
		return view
	}
	c := s.ctx
	view.State, view.Step, view.Total = c.State, c.Step, c.Total()
	view.Current, view.Paused = c.Current(), c.Paused
	view.Plan, view.Done = append([]string(nil), c.Plan...), c.Done()
	view.Carry = append([]MemoryEntry(nil), c.Carry...)
	view.PlanApproved, view.Validated = c.PlanApproved, c.Validated
	view.Trail = append([]TrailEntry(nil), c.Trail...)
	view.Blocked = blockedTransitions(s.set, c)
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
		from, err := a.transition(s, stage, text, true)
		switch {
		case err == nil:
			move.StageApplied = true
			notes = append(notes, fmt.Sprintf("переход %s → %s выполнен", from, stage))
		case errors.Is(err, ErrInvariantViolated):
			// The table allowed it and an invariant did not. It is reported apart
			// from an illegal move because the two are different findings: one says
			// the automaton has no such edge, the other says the project forbids
			// taking it without the user.
			move.Blocked = true
			notes = append(notes, "переход запрещён инвариантом: "+err.Error())
		case errors.Is(err, ErrPrecondition):
			// The table has this edge and the state is not ready for it. This is the
			// number day 15 is about: the model did not ask for something impossible,
			// it asked for something premature, and the two are measured apart.
			move.Unready = true
			notes = append(notes, "переход пока закрыт: "+err.Error())
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

// syncActiveTask points everything that is scoped to a task at the task the memory
// layer considers active: day 13's state machine and day 14's invariants. It is the
// only caller of either sync, and that is on purpose — when day 14 added a second
// task-scoped thing, five call sites updating one of them and not the other was the
// obvious way to end up judging one task's answers by another task's laws.
func (a *Agent) syncActiveTask() {
	a.syncTaskState()
	a.syncInvariantTask()
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
