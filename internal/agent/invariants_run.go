package agent

// Day 14, the running half: where the rules enter a request, where the answer is
// judged, and what a refusal says.
//
// The pipeline is slide 26's — Invariants → Prompt Builder → LLM → Validate →
// Pass/Fail — with slide 27's Fail branch: retry once with the violations named, then
// refuse in the shape the slide draws.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

const (
	invariantsTag    = "[INVARIANTS]"
	invariantsHeader = invariantsTag + "\nRules of this project, set by its owner. They outrank the request below: " +
		"a request that cannot be answered without breaking one must be refused, not satisfied.\n"
	invariantsFooter = "\nBreaking any invariant is FORBIDDEN. If the request conflicts with one, refuse in four parts: " +
		"name the invariant, say what exactly is forbidden, list what is allowed instead, and offer an allowed alternative.\n" +
		"When you refuse for this reason, end your answer with a line: " + RefusalMarkerExample + " — " +
		"the program reads that line to tell a refusal from an answer, and without it your refusal is read as an ordinary answer."
	// retryHeader is what the second attempt is told. It names what was broken and
	// nothing else: an attempt that also restated the whole request would be a
	// different question, and the retry would stop measuring the retry.
	retryTag    = "[INVARIANT_VIOLATION]"
	retryHeader = retryTag + "\nYour previous answer to the request below broke these rules of the project. " +
		"Answer again without breaking them; if that is impossible, refuse and explain why.\n"
)

const (
	judgeMaxTokens = 120
	judgeVerdictOK = "OK"
)

// invariantsContext is the [INVARIANTS] block. Unlike day 13's state it rides at the
// FRONT, inside the system message, and that is a deliberate reversal: day 12 measured
// what moving the cached prefix costs (2.2x fewer tokens and 64% more money when the
// front changes every turn), so the rule is "the more stable the text, the earlier it
// goes". The state changes every step and sits at the tail; invariants change less
// often than anything else in the request and sit ahead even of the profile.
//
// With no rules in force it is empty — no tag, no separator — so a request is
// byte-for-byte the request days 6-13 sent.
func (a *Agent) invariantsContext() string {
	if a.invariants == nil || !a.invariants.cfg.Inject {
		return ""
	}
	rules := a.invariants.all()
	if len(rules) == 0 {
		return ""
	}
	return InvariantsBlock(rules)
}

// checkInvariants validates one answer. The machine half is free and always runs; the
// judge is a second call and runs only when configured, because the host's warning is
// about exactly this call — "может быть адски дорого и нивелировать весь эффект от ИИ"
// (#3156).
func (a *Agent) checkInvariants(ctx context.Context, question, answer string) ([]Violation, int, Usage, string) {
	rules := a.invariants.answerRules(a.invariants.cfg.Judge)
	violations := checkMachine(rules, answer)

	if !a.invariants.cfg.Judge {
		return violations, 0, Usage{}, ""
	}
	var judged []Invariant
	for _, i := range rules {
		if i.Enforce() == EnforceJudge {
			judged = append(judged, i)
		}
	}
	if len(judged) == 0 {
		return violations, 0, Usage{}, ""
	}
	verdicts, usage, err := a.askJudge(ctx, judged, question, answer)
	if err != nil {
		// A judge that did not answer leaves its rules unchecked. It is reported as
		// such and never folded into a pass: silence is not a verdict.
		return violations, 1, usage, err.Error()
	}
	violations = append(violations, verdicts...)
	return violations, 1, usage, ""
}

// askJudge is the "механизм сдержек и противовесов" of #3161: a second model asked
// whether the answer broke rules a check cannot express. One call covers every judged
// rule — one call per rule would multiply exactly the cost the host warned about.
//
// It carries no guarantee (#3162), and every violation it returns is stamped
// EnforceJudge so that no report can present its opinion as a proven fact.
func (a *Agent) askJudge(ctx context.Context, rules []Invariant, question, answer string) ([]Violation, Usage, error) {
	var b strings.Builder
	b.WriteString("You are an independent reviewer. You are given rules and an assistant's answer.\n")
	b.WriteString("For each rule, decide whether the ANSWER breaks it. Judge the answer only, never the request.\n")
	b.WriteString("Reply with one line per rule, in the given order, in the form:\n")
	b.WriteString("<rule name>: OK\n<rule name>: VIOLATION — <what in the answer breaks it, in one short phrase>\n")
	b.WriteString("No other text.\n\nRULES:\n")
	for _, i := range rules {
		b.WriteString("- " + i.Name + ": " + i.Ask + "\n")
	}
	b.WriteString("\nREQUEST:\n" + question + "\n\nANSWER:\n" + answer + "\n")

	reply, err := a.client.AskWith(ctx, []llm.Message{{Role: "user", Content: b.String()}}, llm.Options{
		MaxTokens: judgeMaxTokens,
		Thinking:  "disabled",
	})
	usage := a.usage(reply)
	if err != nil {
		a.recordJudge(usage, true)
		return nil, usage, fmt.Errorf("судья недоступен: %w", err)
	}
	a.recordJudge(usage, false)
	text := strings.TrimSpace(reply.Content)
	if text == "" {
		return nil, usage, fmt.Errorf("судья вернул пустой ответ")
	}
	return parseJudgeVerdicts(rules, text), usage, nil
}

// parseJudgeVerdicts reads the judge's lines. A rule the judge did not mention is left
// alone rather than assumed broken: an unparsed line is our failure, not the answer's.
// hasFoldPrefix is a case-insensitive prefix test that never rebuilds the string, so no
// caller can be tempted to compute an offset on a folded copy.
func hasFoldPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func parseJudgeVerdicts(rules []Invariant, text string) []Violation {
	byName := make(map[string]Invariant, len(rules))
	for _, i := range rules {
		byName[strings.ToLower(i.Name)] = i
	}
	var out []Violation
	seen := make(map[string]bool, len(rules))
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if line == "" {
			continue
		}
		name, verdict, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		rule, known := byName[strings.ToLower(strings.TrimSpace(name))]
		if !known || seen[strings.ToLower(rule.Name)] {
			continue
		}
		seen[strings.ToLower(rule.Name)] = true
		verdict = strings.TrimSpace(verdict)
		if hasFoldPrefix(verdict, judgeVerdictOK) {
			continue
		}
		if !hasFoldPrefix(verdict, "VIOLATION") {
			continue
		}
		// Sliced on the ORIGINAL string by the prefix's own length. The previous version
		// measured the offset on an upper-cased copy and sliced the untouched line with
		// it, which is correct only while case folding preserves byte length — Unicode
		// does not promise that, and a review found a character where it does not.
		detail := strings.TrimSpace(strings.TrimLeft(verdict[len("VIOLATION"):], " —-:"))
		if detail == "" {
			detail = "судья не назвал деталь"
		}
		out = append(out, Violation{
			Name: rule.Name, About: rule.About, Detail: detail,
			Scope: rule.Scope, Kind: rule.Kind, Enforce: EnforceJudge,
		})
	}
	return out
}

// InvariantsBlock renders the [INVARIANTS] block for a rule set, exactly as it travels
// in a request. Exported for the showcase: the page reimplements this in JavaScript and
// the parity test compares the two byte for byte, because the block is the thing the
// model actually sees. A mirror that differed by a word would be demonstrating a
// different agent than the one the report measured.
func InvariantsBlock(rules []Invariant) string {
	if len(rules) == 0 {
		return ""
	}
	lines := make([]string, 0, len(rules))
	for _, i := range rules {
		lines = append(lines, "- "+i.Name+" ("+string(i.Scope)+"): "+i.About)
	}
	return "\n\n" + invariantsHeader + strings.Join(lines, "\n") + invariantsFooter
}

// CheckAnswer runs the machine half of the rules over any text. It is exported for the
// measurement, and it is deliberately the SAME function the agent itself calls: an arm
// with checking switched off still has to be judged, and judging it with a second,
// look-alike implementation would compare two detectors instead of two arms.
func CheckAnswer(rules []Invariant, answer string) []Violation {
	return checkMachine(rules, answer)
}

// invariantSetFile is the shape of a hand-written set. The "comment" key the shipped
// file carries is ignored: a set of laws is documentation as much as configuration.
type invariantSetFile struct {
	Invariants []Invariant `json:"invariants"`
}

// ParseInvariantSet reads a set written by a person. It lives here rather than in the
// interface because the format belongs to the type — and because day 6's encapsulation
// test forbids the CLI from parsing anything at all, which is the same rule seen from
// the other side.
func ParseInvariantSet(raw []byte) ([]Invariant, error) {
	// Unknown top-level keys are accepted on purpose: the shipped set carries a
	// "comment" explaining where each rule comes from, and a set of laws that cannot
	// say why it exists is worse than one that tolerates a stray key.
	var file invariantSetFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("набор инвариантов: %w", err)
	}
	if len(file.Invariants) == 0 {
		return nil, errors.New("набор инвариантов: ни одного правила")
	}
	return file.Invariants, nil
}

// LoadInvariantFile reads a set from disk and stores it. It returns how many rules
// were loaded.
func (a *Agent) LoadInvariantFile(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("набор инвариантов %s: %w", path, err)
	}
	rules, err := ParseInvariantSet(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	if err := a.LoadInvariants(rules); err != nil {
		return 0, err
	}
	return len(rules), nil
}

// LoadInvariants stores a whole set at once, replacing what each file held. It is how a
// project's rules arrive — from a file, not from a conversation.
//
// Every rule is validated BEFORE any file is touched, so a set that cannot be enforced
// is rejected without changing anything. What is NOT promised, and what an earlier
// comment here wrongly implied, is atomicity across the two files: the global half is
// written first, and an I/O failure on the task half leaves the global half already in
// force. The error says so rather than letting the caller assume nothing happened.
func (a *Agent) LoadInvariants(rules []Invariant) error {
	if a.invariants == nil {
		return ErrInvariantsOff
	}
	seen := make(map[string]bool, len(rules))
	var global, scoped []Invariant
	for _, i := range rules {
		if err := ValidateInvariant(i); err != nil {
			return fmt.Errorf("%s: %w", a.Name(), err)
		}
		key := strings.ToLower(i.Name)
		if seen[key] {
			return fmt.Errorf("%s: инвариант %q встречается дважды", a.Name(), i.Name)
		}
		seen[key] = true
		if i.Scope == ScopeGlobal {
			global = append(global, i)
			continue
		}
		if a.invariants.task == "" {
			return fmt.Errorf("%s: %w: %s", a.Name(), ErrNoTaskForInvariant, i.Name)
		}
		scoped = append(scoped, i)
	}
	if err := a.writeInvariants(ScopeGlobal, global); err != nil {
		return err
	}
	if a.invariants.task == "" {
		return nil
	}
	if err := a.writeInvariants(ScopeTask, scoped); err != nil {
		if len(global) > 0 {
			return fmt.Errorf("%w — глобальные правила (%d) уже записаны и действуют", err, len(global))
		}
		return err
	}
	return nil
}

// RefusalText is slide 27's four-part refusal, assembled by the program when the model
// would not produce one itself. It is deliberately not asked of the model: a refusal
// generated by the same model that just broke the rule is the thing under test, not
// the thing that guarantees the outcome.
func RefusalText(violations []Violation) string {
	if len(violations) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("⚠ Запрос отклонён: ответ нарушает инварианты проекта.\n")
	for _, v := range violations {
		b.WriteString("\n• Инвариант " + v.Name + " — " + v.About + "\n")
		b.WriteString("  Что нарушено: " + v.Detail + "\n")
		switch v.Enforce {
		case EnforceJudge:
			b.WriteString("  Чем доказано: внешняя модель-судья. Это мнение, а не гарантия.\n")
		default:
			b.WriteString("  Чем доказано: проверка в коде.\n")
		}
	}
	b.WriteString("\nЧто делать: переформулируйте задачу в рамках инвариантов — или снимите инвариант явной командой, " +
		"если правило больше не действует.")
	return b.String()
}

// JudgeSpend is what the outside opinion has cost this conversation, apart from the
// answers' own spend. It exists because the day's whole question is what enforcement
// costs — a total that was accumulated and never readable measured nothing, which is
// what a test review found here.
func (a *Agent) JudgeSpend() Totals { return a.judgeSpend }

func (a *Agent) recordJudge(u Usage, failed bool) {
	a.record(u, failed)
	addUsage(&a.judgeSpend, u, failed)
}

// enforceInvariants is the Validate → Pass/Fail → retry → refuse branch of slides 25
// and 27. It returns the text to hand to the user, the report, and whether the answer
// was refused.
//
// The violating answer never reaches the conversation stack. An answer that broke a
// rule and was then stored would be re-sent as history on the next turn, and the model
// would read its own violation as an accepted precedent.
func (a *Agent) enforceInvariants(ctx context.Context, messages []llm.Message, question, answer string) (enforced, error) {
	out := enforced{text: answer}
	out.report = InvariantReport{Enabled: true, Injected: a.invariants.cfg.Inject}

	// The marker is read and stripped BEFORE any switch is consulted. It is the
	// model's, not ours, and like day 13's markers it must never reach the person or
	// the history — including in an arm where nothing is checked. The first run with
	// this design leaked it in exactly that arm, and the leak was visible only in the
	// journal, which is why the journal exists.
	clean, declared, rule := ParseRefusalMarker(answer)
	if clean == "" {
		// An answer that is nothing but the marker refuses nothing and answers nothing.
		// It cannot be delivered, because the marker must never reach the person, and
		// it cannot be checked, because there is no answer to check. So it is what it
		// is: an empty answer.
		//
		// The comment this replaces said the caller's empty-answer handling would see
		// it. It would not: that check runs before enforcement, on the day-13 markers
		// only. A second review wave reproduced the leak — marker delivered verbatim
		// and stored in history — which is the same defect class this design was
		// introduced to close, in the very marker it introduced.
		return out, ErrEmptyAnswer
	}
	out.text = clean
	out.report.Declared = declared
	out.report.DeclaredRule = rule

	if !a.invariants.cfg.Check {
		return out, nil
	}
	out.report.Checked = len(a.invariants.answerRules(a.invariants.cfg.Judge))
	if out.report.Checked == 0 {
		return out, nil
	}

	violations, calls, judgeUsage, judgeErr := a.checkInvariants(ctx, question, out.text)
	out.report.JudgeCalls = calls
	out.report.JudgeUsage = judgeUsage
	out.report.JudgeError = judgeErr
	if len(violations) == 0 {
		return out, nil
	}
	out.report.First = violations

	// A DECLARED refusal is delivered as it stands. The rule the day is about is "do
	// not PROPOSE what is forbidden", and a refusal proposes nothing — it names the
	// forbidden thing in order to refuse it. What the check found is reported as a
	// warning, which is the fallback the host named for exactly this case.
	//
	// The declaration is the model's, not our inference: the previous design guessed
	// the speech act from substrings and a review broke it with ordinary phrasing.
	if out.report.Declared {
		out.report.Warned = violations
		return out, nil
	}

	if !a.invariants.cfg.Retry {
		return a.refuse(out, violations), nil
	}

	out.report.Retried = true
	retry := append(append([]llm.Message(nil), messages...), llm.Message{
		Role:    "user",
		Content: retryHeader + violationLines(violations) + "\n",
	})
	reply, err := a.client.AskWith(ctx, retry, a.options())
	out.retryUsage = a.usage(reply)
	if err != nil {
		a.record(out.retryUsage, true)
		return a.refuse(out, violations), nil
	}
	a.record(out.retryUsage, false)
	second := strings.TrimSpace(reply.Content)
	if second == "" {
		return a.refuse(out, violations), nil
	}

	// The retried answer is the one that will be delivered, so it goes through exactly
	// the same marker stripping the first one did. Without this its [[NEXT_STEP]] and
	// [[TRANSITION: …]] reached the person and the history verbatim — 23 of the 600
	// cells of the first run carried one — and the state machine moved on the FIRST
	// answer's intent while the person read the second.
	secondClean, secondStep, secondStage := parseControlMarkers(second)
	if secondClean == "" {
		return a.refuse(out, violations), nil
	}
	second, out.step, out.stage = secondClean, secondStep, secondStage
	out.moved = true
	second, declared2, rule2 := ParseRefusalMarker(second)
	if second == "" {
		return a.refuse(out, violations), nil
	}
	if declared2 {
		out.report.Declared = true
		out.report.DeclaredRule = rule2
	}

	again, calls2, judgeUsage2, judgeErr2 := a.checkInvariants(ctx, question, second)
	out.report.JudgeCalls += calls2
	out.report.JudgeUsage = sumUsage(out.report.JudgeUsage, judgeUsage2)
	if judgeErr2 != "" {
		out.report.JudgeError = judgeErr2
	}
	out.text = second
	if len(again) == 0 || declared2 {
		if declared2 && len(again) > 0 {
			out.report.Warned = again
		}
		return out, nil
	}
	return a.refuse(out, again), nil
}

// enforced is what one enforcement pass produced: the text to deliver, what the rules
// said about it, what the second call cost, and — when a retry produced the delivered
// answer — the control markers THAT answer carried. The markers travel with the text
// they came from, because the alternative is what the first run shipped: a state
// machine moving on an answer the person never saw.
type enforced struct {
	text       string
	report     InvariantReport
	retryUsage Usage
	// moved is set when step/stage come from the retried answer and must replace the
	// first answer's.
	moved bool
	step  bool
	stage TaskStage
}

// refuse replaces the answer with the program's own four-part refusal. Nothing of the
// model's text is delivered, so no marker of its can reach the person or the history.
func (a *Agent) refuse(out enforced, violations []Violation) enforced {
	out.report.Final = violations
	out.report.Refused = true
	out.text = RefusalText(violations)
	out.moved = true
	out.step, out.stage = false, ""
	return out
}

func violationLines(violations []Violation) string {
	lines := make([]string, 0, len(violations))
	for _, v := range violations {
		lines = append(lines, "- "+v.Name+": "+v.About+" (нарушено: "+v.Detail+")")
	}
	return strings.Join(lines, "\n")
}

func sumUsage(a, b Usage) Usage {
	a.PromptTokens += b.PromptTokens
	a.CompletionTokens += b.CompletionTokens
	a.TotalTokens += b.TotalTokens
	a.CachedTokens += b.CachedTokens
	a.MissedTokens += b.MissedTokens
	a.ReasoningTokens += b.ReasoningTokens
	a.Cost += b.Cost
	a.Priced = a.Priced || b.Priced
	return a
}

// syncInvariantTask points the scoped half at whatever task the memory layer considers
// active, exactly as syncTaskState does for the state machine.
func (a *Agent) syncInvariantTask() {
	if a.invariants == nil || a.cfg.Memory == nil {
		return
	}
	task := strings.TrimSpace(a.cfg.Memory.Task)
	if a.memory != nil {
		task = a.memory.task
	}
	if a.invariants.task == task {
		return
	}
	a.invariants.setTask(task)
}

// InvariantsEnabled reports whether day 14 is on.
func (a *Agent) InvariantsEnabled() bool { return a.invariants != nil }

// InvariantState is what an interface may show.
func (a *Agent) InvariantState() InvariantState {
	if a.invariants == nil {
		return InvariantState{}
	}
	s := a.invariants
	st := InvariantState{
		Enabled:    true,
		User:       s.cfg.User,
		Task:       s.task,
		GlobalPath: s.globalFile.path,
		TaskPath:   s.taskFile.path,
		Inject:     s.cfg.Inject,
		Check:      s.cfg.Check,
		Retry:      s.cfg.Retry,
		Judge:      s.cfg.Judge,
		Invariants: s.all(),
		Tokens:     EstimateTokens(a.invariantsContext()),
	}
	return st
}

// AddInvariant stores a rule in the file its scope belongs to. A rule that cannot be
// enforced is refused here rather than at the next answer.
func (a *Agent) AddInvariant(i Invariant) error {
	if a.invariants == nil {
		return ErrInvariantsOff
	}
	if err := ValidateInvariant(i); err != nil {
		return fmt.Errorf("%s: %w", a.Name(), err)
	}
	if err := a.invariants.reload(); err != nil {
		return fmt.Errorf("%s: %w", a.Name(), err)
	}
	if i.Scope != ScopeGlobal && a.invariants.task == "" {
		return fmt.Errorf("%s: %w: %s", a.Name(), ErrNoTaskForInvariant, i.Name)
	}
	for _, have := range a.invariants.all() {
		if strings.EqualFold(have.Name, i.Name) {
			return fmt.Errorf("%s: инвариант %q уже есть", a.Name(), i.Name)
		}
	}
	return a.writeInvariants(i.Scope, append(a.invariants.listFor(i.Scope), i))
}

// RemoveInvariant deletes a rule by name. Lifting a rule is an explicit act of the
// owner, which is what makes a refusal honest: the way past an invariant is to change
// it, not to argue with the agent.
func (a *Agent) RemoveInvariant(name string) error {
	if a.invariants == nil {
		return ErrInvariantsOff
	}
	if err := a.invariants.reload(); err != nil {
		return fmt.Errorf("%s: %w", a.Name(), err)
	}
	for _, scope := range []InvariantScope{ScopeGlobal, ScopeTask} {
		list := a.invariants.listFor(scope)
		for idx, have := range list {
			if strings.EqualFold(have.Name, name) {
				next := append(append([]Invariant(nil), list[:idx]...), list[idx+1:]...)
				return a.writeInvariants(scope, next)
			}
		}
	}
	return fmt.Errorf("%s: инварианта %q нет", a.Name(), name)
}

// listFor is every stored rule of the file that scope lives in — the task file holds
// both task-scoped and transition-scoped rules.
func (s *invariantState) listFor(scope InvariantScope) []Invariant {
	if scope == ScopeGlobal {
		return append([]Invariant(nil), s.global...)
	}
	return append([]Invariant(nil), s.scoped...)
}

func (a *Agent) writeInvariants(scope InvariantScope, list []Invariant) error {
	s := a.invariants
	file := &s.globalFile
	task := ""
	if scope != ScopeGlobal {
		file = &s.taskFile
		task = s.task
	}
	payload := InvariantFile{
		Version: InvariantsVersion, User: s.cfg.User, Task: task,
		Invariants: list, Updated: time.Now().UTC(),
	}
	if err := file.write(payload); err != nil {
		return fmt.Errorf("%s: %w", a.Name(), err)
	}
	return s.reload()
}
