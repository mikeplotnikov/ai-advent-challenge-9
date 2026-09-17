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
		"name the invariant, say what exactly is forbidden, list what is allowed instead, and offer an allowed alternative."
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
	lines := make([]string, 0, len(rules))
	for _, i := range rules {
		lines = append(lines, "- "+i.Name+" ("+string(i.Scope)+"): "+i.About)
	}
	return "\n\n" + invariantsHeader + strings.Join(lines, "\n") + invariantsFooter
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
		if strings.HasPrefix(strings.ToUpper(verdict), judgeVerdictOK) {
			continue
		}
		if !strings.HasPrefix(strings.ToUpper(verdict), "VIOLATION") {
			continue
		}
		detail := strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(strings.ToUpper(verdict), "VIOLATION"), " —-:"))
		// Recover the original casing of the detail: the prefix test upper-cased a
		// copy only, so slice the same offset out of the untouched line.
		if n := len(verdict) - len(detail); n > 0 && n <= len(verdict) {
			detail = strings.TrimSpace(verdict[n:])
		}
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

// LoadInvariants stores a whole set at once, replacing what each file held. It is how
// a project's rules arrive — from a file, not from a conversation — and it is one
// write per scope so that a half-loaded set cannot be left behind by a crash.
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
	return a.writeInvariants(ScopeTask, scoped)
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
func (a *Agent) enforceInvariants(ctx context.Context, messages []llm.Message, question, answer string) (string, InvariantReport, Usage, error) {
	report := InvariantReport{Enabled: true, Injected: a.invariants.cfg.Inject}
	if !a.invariants.cfg.Check {
		return answer, report, Usage{}, nil
	}
	report.Checked = len(a.invariants.answerRules(a.invariants.cfg.Judge))
	if report.Checked == 0 {
		return answer, report, Usage{}, nil
	}

	violations, calls, judgeUsage, judgeErr := a.checkInvariants(ctx, question, answer)
	report.JudgeCalls = calls
	report.JudgeUsage = judgeUsage
	report.JudgeError = judgeErr
	if len(violations) == 0 {
		return answer, report, Usage{}, nil
	}
	report.First = violations

	// An answer that declines is delivered as it stands. The rule the day is about is
	// "do not PROPOSE what is forbidden", and a refusal proposes nothing — it names the
	// forbidden thing in order to refuse it. What the check found is reported as a
	// warning instead, which is the fallback the host named for exactly this.
	if Declines(answer) {
		report.Warned = violations
		return answer, report, Usage{}, nil
	}

	if !a.invariants.cfg.Retry {
		report.Final = violations
		report.Refused = true
		return RefusalText(violations), report, Usage{}, nil
	}

	report.Retried = true
	retry := append(append([]llm.Message(nil), messages...), llm.Message{
		Role:    "user",
		Content: retryHeader + violationLines(violations) + "\n",
	})
	reply, err := a.client.AskWith(ctx, retry, a.options())
	retryUsage := a.usage(reply)
	if err != nil {
		a.record(retryUsage, true)
		report.Final = violations
		report.Refused = true
		return RefusalText(violations), report, retryUsage, nil
	}
	a.record(retryUsage, false)
	second := strings.TrimSpace(reply.Content)
	if second == "" {
		report.Final = violations
		report.Refused = true
		return RefusalText(violations), report, retryUsage, nil
	}

	again, calls2, judgeUsage2, judgeErr2 := a.checkInvariants(ctx, question, second)
	report.JudgeCalls += calls2
	report.JudgeUsage = sumUsage(report.JudgeUsage, judgeUsage2)
	if judgeErr2 != "" {
		report.JudgeError = judgeErr2
	}
	if len(again) == 0 {
		return second, report, retryUsage, nil
	}
	report.Final = again
	report.Refused = true
	return RefusalText(again), report, retryUsage, nil
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
