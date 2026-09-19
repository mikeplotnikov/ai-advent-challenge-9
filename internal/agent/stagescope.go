package agent

// Day 15, the half the transition table cannot see.
//
// The table judges a DECLARED move: the model ends its answer with a marker and the
// code says yes or no. Day 13 measured that and found the model never asked for an
// illegal move — but it was never pushed, and a model that does not ask is not a model
// that stays inside the stage. The other way to skip a stage is to skip it silently:
// stay in planning and deliver the implementation. No transition is requested, the
// state never moves, and the rule is broken anyway.
//
// That is what this file checks, and it is the second half of the double defence slide
// 29 asks for: the prompt says "do not produce the work of a later stage", and the code
// looks at what came back.
//
// The detector is STRUCTURAL, not lexical. Days 12-14 spent three review waves on
// substring detectors that read ordinary speech as evidence — "вместо" as a refusal,
// "уточнили" as a re-ask, "второстепенный" as a step. A fenced code block is a property
// of the markup, not of the words, so it cannot be tripped by phrasing. What it cannot
// see is stated plainly in the report rather than papered over: prose that describes an
// implementation without showing one, and a single inline `identifier`, are invisible to
// it.

import (
	"strings"
)

// KindStageScope forbids the work of a later stage in the stages it names. Values are
// the stages on which the answer may not carry implementation.
const KindStageScope InvariantKind = "stage-scope"

// checkEnv is what a machine check may know about the situation, beyond the answer
// itself. It exists because a stage-scope rule is a question about the answer AND the
// stage, and reaching for the agent's state from inside a pure check would make the
// check untestable in isolation.
//
// The zero value means "no task, no stage" — and a stage-scope rule then matches
// nothing at all. That is the honest reading: outside a task there is no stage whose
// scope could be exceeded.
type checkEnv struct {
	Stage TaskStage
}

// implementationMarkers is what the detector actually found, in a stable order. It
// returns the evidence rather than a boolean so that a violation can quote what it saw
// — a verdict a person cannot check against the text is a verdict they have to trust.
func implementationMarkers(answer string) []string {
	var out []string
	if hasFencedBlock(answer) {
		out = append(out, "блок кода")
	}
	if hasDiffHeader(answer) {
		out = append(out, "дифф")
	}
	return out
}

// hasFencedBlock reports a fenced code block. An OPENING fence is enough: an answer cut
// off by the token ceiling mid-block is still an answer that started writing code, and
// requiring the closing fence would let exactly the longest implementations through.
func hasFencedBlock(answer string) bool {
	for _, line := range strings.Split(answer, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			return true
		}
	}
	return false
}

// hasDiffHeader reports the shape of a patch. It is checked apart from the fence
// because a diff is often pasted without one.
func hasDiffHeader(answer string) bool {
	for _, line := range strings.Split(answer, "\n") {
		trimmed := strings.TrimSpace(line)
		// "@@ -" rather than "@@ ": a hunk header always names the old range first,
		// and the narrower prefix keeps an answer that merely writes "@@ здесь" out of
		// the evidence.
		switch {
		case strings.HasPrefix(trimmed, "diff --git "),
			strings.HasPrefix(trimmed, "--- a/"),
			strings.HasPrefix(trimmed, "+++ b/"),
			strings.HasPrefix(trimmed, "@@ -"):
			return true
		}
	}
	return false
}

// stageScopeViolation judges one stage-scope rule against one answer.
func stageScopeViolation(i Invariant, answer string, env checkEnv) (string, bool) {
	if env.Stage == "" {
		return "", false
	}
	if !namesStage(i.Values, env.Stage) {
		return "", false
	}
	found := implementationMarkers(answer)
	if len(found) == 0 {
		return "", false
	}
	return "на стадии " + string(env.Stage) + " ответ содержит " + strings.Join(found, ", "), true
}

// namesStage reports whether a rule's value list contains a stage, case-insensitively
// and ignoring surrounding space — the same tolerance every other stage name in this
// package is read with.
func namesStage(values []string, stage TaskStage) bool {
	for _, v := range values {
		if TaskStage(strings.ToLower(strings.TrimSpace(v))) == stage {
			return true
		}
	}
	return false
}

// CheckAnswerInStage runs the machine rules over an answer produced in a known stage.
// It is the stage-aware form of CheckAnswer, which stays as it was: day 14's driver
// calls that one, its report is built from its output, and a signature change there
// would quietly re-score a measurement that is already published.
func CheckAnswerInStage(rules []Invariant, answer string, stage TaskStage) []Violation {
	return checkMachine(rules, answer, checkEnv{Stage: stage})
}

// checkEnv is the agent's current situation, for the checks that need one. It is a
// method rather than a field so that it cannot go stale: the stage it reports is the
// stage the state machine is in at the moment the answer is judged.
func (a *Agent) checkEnv() checkEnv {
	if a.task == nil || a.task.task == "" {
		return checkEnv{}
	}
	return checkEnv{Stage: a.task.ctx.State}
}

// stageExistsInAnySet reports whether a name is a stage of some automaton this build
// knows. A rule may legitimately name a stage of a set the current task does not use —
// a project with both a feature path and a bug path writes one rule for both.
func stageExistsInAnySet(name string) bool {
	stage := TaskStage(strings.ToLower(strings.TrimSpace(name)))
	for _, setName := range StageSetNames() {
		set, err := LookupStageSet(setName)
		if err != nil {
			continue
		}
		if _, ok := set.rule(stage); ok {
			return true
		}
	}
	return false
}
