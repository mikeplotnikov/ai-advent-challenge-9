package agent

// Day 15 — the red path of the state machine.
//
// Day 13 built the table: which edges exist, and one gate that both the user's command
// and the model's request go through. That was the happy path, and the host says so in
// as many words: "условно в том дне мы делали happy path… А теперь красный путь"
// (chat #3303-3304). This file is the red path — "теперь жёстко контролируем переходы"
// (#3299), "добавляем контроль, что он не перепрыгивает эти состояния" (#3301), "умеет
// откатываться назад по графу" (#3302), and the property all of it is for: "не ломается,
// нельзя поломать, корректно обрабатываются сходы с маршрута" (#3314).
//
// Three things are added on top of the table, and they are deliberately separate:
//
//   1. A precondition on an EDGE. The edge exists and the state is not ready to take
//      it — "нельзя делать реализацию до утверждённого плана", "нельзя делать финал без
//      валидации" (task text, day 15). An edge that does not exist and an edge that is
//      not yet open are different findings and are reported apart; folding them into one
//      "запрещено" would lose both.
//   2. Rules for going BACK. The backward edges were already in the table on day 13, but
//      nothing said what a rollback undoes. A rollback that kept the approval it was
//      rolling back from would make the approval free.
//   3. A trail: which moves actually happened. It survives a pause, because that is the
//      question after one — where have we been.

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Control is how strictly a stage change is judged. It exists because a claim about
// what the control buys can only be measured against its absence: day 13 could report
// that the model never asked for an illegal move, but not what would have happened if
// the code had said yes. These are the arms of day 15's measurement, and the default is
// the strict one.
const (
	// ControlNone applies whatever the model asks for, consulting neither the table nor
	// the preconditions. It is antipattern 02 of slide 29 — "нет валидации переходов",
	// "LLM услужлив по природе. Без require() согласится на любой переход" — as a
	// running arm rather than a warning. The stage must still exist in the set: an
	// unknown stage would write a state file that cannot be read back.
	ControlNone = "none"
	// ControlTable is day 13: the transition table judges, nothing else.
	ControlTable = "table"
	// ControlGuards is day 15: the table judges first, then the edge's preconditions.
	ControlGuards = "guards"
)

// ControlModes lists the modes in a stable order, for flags and for the dump.
func ControlModes() []string { return []string{ControlNone, ControlTable, ControlGuards} }

var (
	// ErrPrecondition is an edge the table allows and the state is not ready for. It is
	// its own error because it is its own measurement: "рано" is not "нельзя".
	ErrPrecondition = errors.New("переход пока закрыт")
	// ErrNotPlanStage is an approval asked for where a plan is not what the machine is
	// waiting for.
	ErrNotPlanStage = errors.New("на этой стадии план не утверждают")
	// ErrNotValidationStage is a verdict recorded where nothing is being validated.
	ErrNotValidationStage = errors.New("на этой стадии нечего валидировать")
	// ErrUnknownControl is a Control value no build knows.
	ErrUnknownControl = errors.New("неизвестный режим контроля переходов")
)

// requirement is one precondition of one edge: a named question about the state that
// has to answer yes before the move is allowed.
//
// About is the rule in the words a person reads in the refusal; Detail is the evidence
// — what the state actually is right now. They are separate for the same reason an
// Invariant's About and a Violation's Detail are: a rule restated as evidence is a
// refusal that cannot be checked against the machine that issued it.
type requirement struct {
	Name   string
	About  string
	Detail func(TaskContext) string
	Met    func(TaskContext) bool
	// Clear is what entering the stage that ESTABLISHES this requirement undoes. It
	// lives on the requirement rather than in a switch inside enterStage, and that is
	// not a style choice: a switch over names is a second place to edit, and a
	// requirement added without its case there would be a condition that quietly
	// survives a rollback.
	Clear func(*TaskContext)
}

// The three preconditions of the standard and bugfix sets. They are values, not methods
// on a stage, so that a set is still data: putting one on another edge is a line in the
// table, not a branch in the code.
var (
	// reqApprovedPlan is the task's own first example: "нельзя делать реализацию до
	// утверждённого плана". Day 13 had no such thing — planning → execution went
	// through with an empty plan, and the state the next stage worked from was a plan
	// of zero steps.
	reqApprovedPlan = requirement{
		Name:  "approved-plan",
		About: "план задачи должен быть утверждён: /plan пишет черновик, /approve утверждает",
		Detail: func(c TaskContext) string {
			if c.Total() == 0 {
				return "плана нет ни одного шага"
			}
			return fmt.Sprintf("план из %d шагов не утверждён", c.Total())
		},
		Met:   func(c TaskContext) bool { return c.Total() > 0 && c.PlanApproved },
		Clear: func(c *TaskContext) { c.PlanApproved = false },
	}
	// reqPlanExhausted is "перепрыгнуть этап" at the level the table cannot see: the
	// stage is right, the work is not done. Reaching the last step is what counts as
	// finishing it — the machine closes a step when it moves off it, which is day 13's
	// shape and is left alone.
	reqPlanExhausted = requirement{
		Name:  "plan-exhausted",
		About: "все шаги плана должны быть пройдены: /step done закрывает шаг",
		Detail: func(c TaskContext) string {
			if c.Total() == 0 {
				return "плана нет"
			}
			return fmt.Sprintf("машина на шаге %d из %d", c.Step, c.Total())
		},
		Met: func(c TaskContext) bool { return c.Total() > 0 && c.Step == c.Total() },
		// Nothing to clear: the steps already taken are work, and entering a stage
		// does not undo work. This is the requirement whose absence of a Clear is
		// deliberate, and saying so here is cheaper than wondering later.
		Clear: nil,
	}
	// reqValidationVerdict is the task's second example: "нельзя делать финал без
	// валидации". The table already routes done through validation; without a verdict
	// that route can be walked in one turn, and then the rule is decoration.
	reqValidationVerdict = requirement{
		Name:   "validation-verdict",
		About:  "результат должен быть провалидирован: /validate ok или /validate fail",
		Detail: func(TaskContext) string { return "вердикт валидации не записан" },
		Met:    func(c TaskContext) bool { return c.Validated },
		Clear:  func(c *TaskContext) { c.Validated = false },
	}
)

// requirementsOn is the preconditions of one edge, or nil when the edge is open.
func (s StageSet) requirementsOn(from, to TaskStage) []requirement {
	r, ok := s.rule(from)
	if !ok {
		return nil
	}
	return r.Guards[to]
}

// requirementsFrom is every precondition on every edge leaving a stage. It is what a
// stage ESTABLISHES: a stage whose outgoing edge needs an approved plan is the stage in
// which a plan is approved, and that is how entering it knows what to clear.
func (s StageSet) requirementsFrom(stage TaskStage) []requirement {
	r, ok := s.rule(stage)
	if !ok {
		return nil
	}
	var out []requirement
	for _, to := range r.Allow {
		out = append(out, r.Guards[to]...)
	}
	return out
}

// index is a stage's position in the set's own order. It is what tells a rollback from
// a step forward, and it is the set's order rather than a stored direction flag because
// the order is the graph the host means by "откатываться назад по графу" (#3302).
func (s StageSet) index(stage TaskStage) int {
	for i, r := range s.Rules {
		if r.Stage == stage {
			return i
		}
	}
	return -1
}

// IsRollback reports whether an edge goes back. Exported for the showcase, which draws
// the two kinds of edge differently and must not decide that for itself.
func (s StageSet) IsRollback(from, to TaskStage) bool {
	i, j := s.index(from), s.index(to)
	return i >= 0 && j >= 0 && j < i
}

// BlockedTransition is an edge the table allows and a precondition currently closes.
// The view carries these so a person — and the model, through the state block — can see
// not only what is allowed but what is merely not yet allowed, and why.
type BlockedTransition struct {
	To          TaskStage `json:"to"`
	Requirement string    `json:"requirement"`
	About       string    `json:"about"`
	Detail      string    `json:"detail"`
}

// blockedTransitions lists every allowed edge whose preconditions are not met, in the
// table's own order. The order is the table's and not sorted by name: the state block
// lists them, and a block whose lines reshuffled between two turns would be a different
// prompt for no reason.
func blockedTransitions(set StageSet, c TaskContext) []BlockedTransition {
	rule, ok := set.rule(c.State)
	if !ok {
		return nil
	}
	var out []BlockedTransition
	for _, to := range rule.Allow {
		for _, req := range rule.Guards[to] {
			if req.Met(c) {
				continue
			}
			out = append(out, BlockedTransition{
				To: to, Requirement: req.Name, About: req.About, Detail: req.Detail(c),
			})
		}
	}
	return out
}

// checkPreconditions returns the first unmet precondition of an edge. First, not all:
// the refusal names one concrete thing to do next, and a state that fails two is fixed
// by fixing them in order anyway.
func checkPreconditions(set StageSet, c TaskContext, to TaskStage) (requirement, bool) {
	for _, req := range set.requirementsOn(c.State, to) {
		if !req.Met(c) {
			return req, false
		}
	}
	return requirement{}, true
}

// enterStage clears whatever the stage being entered is itself responsible for
// establishing. Every visit to a stage has to earn its own conditions again: a task
// rolled back to planning has no approved plan until it is approved anew, and a task
// sent back to validation has no verdict until one is recorded anew.
//
// It is derived from the guard table rather than written per stage, so a new stage set
// gets the behaviour by declaring its edges and nothing else.
func enterStage(set StageSet, next *TaskContext, to TaskStage) {
	for _, req := range set.requirementsFrom(to) {
		if req.Clear != nil {
			req.Clear(next)
		}
	}
}

// TrailEntry is one move that actually happened.
//
// Refusals are NOT here, and that is the day's own property rather than an omission: a
// refused attempt must leave the state byte for byte as it was, and writing the refusal
// into the state would be the state changing. They live in a session-scoped log instead,
// printed by /trail and reported in the reply.
type TrailEntry struct {
	From TaskStage `json:"from"`
	To   TaskStage `json:"to"`
	// Actor is ActorUser or ActorModelMove: who asked for the move.
	Actor string `json:"actor"`
	// Back marks a rollback, so that reading the trail does not require re-deriving the
	// set's order to see that the task went backwards.
	Back   bool      `json:"back"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

const (
	// ActorUser is a move the person asked for with a command. Day 14's transition bans
	// treat that command as the user's consent (#3152), and the trail records it as such.
	ActorUser = "user"
	// ActorModelMove is a move the model asked for with a marker.
	ActorModelMove = "model"
)

const (
	// maxTrailEntries bounds the trail the way the plan and the memory blocks are
	// bounded: the state file is read on every turn, so its size is settled where it is
	// written. The oldest entries are dropped first.
	maxTrailEntries = 12
	// maxTrailReasonRunes keeps one reason to a readable line.
	maxTrailReasonRunes = 200
)

// appendTrail adds a move and drops the oldest when the trail is full.
func appendTrail(trail []TrailEntry, e TrailEntry) []TrailEntry {
	out := append(append([]TrailEntry(nil), trail...), e)
	if len(out) > maxTrailEntries {
		out = out[len(out)-maxTrailEntries:]
	}
	return out
}

// trailReason sanitises the text that travels with a move. A reason can come from the
// model's own answer, and although the trail is not injected into a request, it is
// printed to a person and stored in the state file that validateTaskContext reads back
// — so it goes through the same boundary check every stored, model-written string does.
func trailReason(s string) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if runes := []rune(s); len(runes) > maxTrailReasonRunes {
		s = strings.TrimSpace(string(runes[:maxTrailReasonRunes-1])) + "…"
	}
	if forgesBlockBoundary(s) {
		return ""
	}
	return s
}

func validateTrail(trail []TrailEntry) error {
	if len(trail) > maxTrailEntries {
		return fmt.Errorf("в журнале переходов %d записей, предел %d", len(trail), maxTrailEntries)
	}
	for i, e := range trail {
		if e.From == "" || e.To == "" {
			return fmt.Errorf("запись %d журнала переходов без стадии", i+1)
		}
		if e.Actor != ActorUser && e.Actor != ActorModelMove {
			return fmt.Errorf("запись %d журнала переходов: неизвестный автор %q", i+1, e.Actor)
		}
		if !singleLine(e.Reason) || forgesBlockBoundary(e.Reason) {
			return fmt.Errorf("запись %d журнала переходов: причина подделывает границу блока", i+1)
		}
		if len([]rune(e.Reason)) > maxTrailReasonRunes {
			return fmt.Errorf("запись %d журнала переходов: причина длиннее %d символов", i+1, maxTrailReasonRunes)
		}
	}
	return nil
}

// RefusedMove is an attempt the machine turned down, kept for the current session only.
type RefusedMove struct {
	From   TaskStage
	To     TaskStage
	Actor  string
	Reason string
	// Kind is why it was refused: "illegal" (no such edge), "unready" (edge closed by a
	// precondition), "blocked" (an invariant of day 14), "unknown" (no such stage).
	Kind string
	At   time.Time
}

// The kinds of refusal, kept as constants because the measurement counts them by name
// and a typo in a journal column is a column that silently reads zero.
const (
	RefusedIllegal = "illegal"
	RefusedUnready = "unready"
	RefusedBlocked = "blocked"
	RefusedUnknown = "unknown"
)

// maxRefusalLog bounds the session log. It is not persisted, so this only keeps a long
// conversation from growing one list forever.
const maxRefusalLog = 32

func (a *Agent) recordRefusal(m RefusedMove) {
	if a.task == nil {
		return
	}
	m.At = time.Now().UTC()
	a.task.refused = append(a.task.refused, m)
	if len(a.task.refused) > maxRefusalLog {
		a.task.refused = a.task.refused[len(a.task.refused)-maxRefusalLog:]
	}
}

// RefusedMoves is what this session tried and was not allowed to do.
func (a *Agent) RefusedMoves() []RefusedMove {
	if a.task == nil {
		return nil
	}
	return append([]RefusedMove(nil), a.task.refused...)
}

// ApprovePlan is the act the day's first example needs to exist at all. On day 13 there
// was no difference between writing a plan and approving it — /plan did both, and its
// own message said "план утверждён". Day 15 splits them, because "утверждённого плана"
// is a state the machine must be able to check, and a plan that exists is not a plan
// somebody agreed to.
func (a *Agent) ApprovePlan() error {
	s, err := a.requireTask()
	if err != nil {
		return err
	}
	if !stageEstablishes(s.set, s.ctx.State, reqApprovedPlan.Name) {
		return fmt.Errorf("%s: %w: %s", a.Name(), ErrNotPlanStage, s.ctx.State)
	}
	if s.ctx.Total() == 0 {
		return fmt.Errorf("%s: %w", a.Name(), ErrNoPlan)
	}
	if s.ctx.PlanApproved {
		return nil
	}
	next := s.ctx.clone()
	next.PlanApproved = true
	return a.commitTaskState(s, next)
}

// RecordVerdict writes the validation verdict the last example needs: "нельзя делать
// финал без валидации". A passing verdict opens the edge to the terminal stage; a
// failing one leaves it closed and is the reason to go back, which is the graph's own
// answer to a failure.
//
// The note travels forward as the stage's carried result, so the verdict is not just a
// flag: the stages after it read what the validation actually said.
func (a *Agent) RecordVerdict(pass bool, note string) error {
	s, err := a.requireTask()
	if err != nil {
		return err
	}
	if !stageEstablishes(s.set, s.ctx.State, reqValidationVerdict.Name) {
		return fmt.Errorf("%s: %w: %s", a.Name(), ErrNotValidationStage, s.ctx.State)
	}
	next := s.ctx.clone()
	next.Validated = pass
	if summary := summariseForCarry(verdictNote(pass, note)); summary != "" {
		entries, err := upsertEntry(next.Carry, MemoryEntry{
			Key: string(s.ctx.State), Value: summary, Source: SourceCommand, Updated: time.Now().UTC(),
		})
		if err == nil {
			next.Carry = entries
		}
	}
	return a.commitTaskState(s, next)
}

func verdictNote(pass bool, note string) string {
	head := "валидация не пройдена"
	if pass {
		head = "валидация пройдена"
	}
	if strings.TrimSpace(note) == "" {
		return head
	}
	return head + ": " + note
}

// stageEstablishes reports whether a named precondition is one this stage is responsible
// for satisfying — that is, whether it guards an edge leaving this stage.
func stageEstablishes(set StageSet, stage TaskStage, name string) bool {
	for _, req := range set.requirementsFrom(stage) {
		if req.Name == name {
			return true
		}
	}
	return false
}

// validateControl checks a configured control mode.
func validateControl(mode string) error {
	switch strings.TrimSpace(mode) {
	case "", ControlNone, ControlTable, ControlGuards:
		return nil
	default:
		return fmt.Errorf("%w: %q, допустимы %s", ErrUnknownControl, mode, strings.Join(ControlModes(), ", "))
	}
}

// controlMode is the mode in force, with the default filled in.
func (s *taskStateState) controlMode() string {
	mode := strings.TrimSpace(s.cfg.Control)
	if mode == "" {
		return ControlGuards
	}
	return mode
}

// StateBlock is the [TASK_STATE] block as it would travel in the next request. It is
// exported for two readers that must not guess at it: the measurement, which compares
// the block a resumed agent builds against the one the live agent built, and the
// showcase, whose JavaScript mirror is checked against these bytes.
func (a *Agent) StateBlock() string { return a.taskStateBlock() }

// RequirementInfo is one precondition as the outside world may read it: its name and
// what it says. The predicate itself is not exported — a mirror that reimplemented it
// from a description would be a second, quieter judge.
type RequirementInfo struct {
	Name  string `json:"name"`
	About string `json:"about"`
}

// Requirements lists the preconditions of one edge, in the order they are checked.
// Exported for the dump the showcase is verified against: the page draws an edge as
// closed, and it must draw it from the same table the agent refuses from.
func (s StageSet) Requirements(from, to TaskStage) []RequirementInfo {
	reqs := s.requirementsOn(from, to)
	if len(reqs) == 0 {
		return nil
	}
	out := make([]RequirementInfo, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, RequirementInfo{Name: r.Name, About: r.About})
	}
	return out
}
