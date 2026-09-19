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
	"regexp"
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
	if hasHTMLCode(answer) {
		out = append(out, "html-код")
	}
	return out
}

// hasHTMLCode reports a code block written as HTML. Found by the second review wave:
// <pre> and <code> are as much "a block of code" as a fence is, and the detector saw
// neither. An indented four-space block is still invisible and is NOT added here — in
// Russian prose an indented continuation line is ordinary, and a detector that read one
// as an implementation would repeat the lexical mistakes of days 12-14 in a new costume.
// That blind spot is named in the report instead.
func hasHTMLCode(answer string) bool { return htmlCodeTag.MatchString(answer) }

// htmlCodeTag matches an opening <pre> or <code> TAG, not the letters. The first
// version was a substring test inside a detector advertised as structural, and an
// external review named what it would catch: Map<Code, Token>, <prefix>. A tag is
// followed by a delimiter — '>' or whitespace before an attribute — and generics are
// not.
var htmlCodeTag = regexp.MustCompile(`(?i)<(pre|code)(\s|>|/>)`)

// hasFencedBlock reports a fenced code block. An OPENING fence is enough: an answer cut
// off by the token ceiling mid-block is still an answer that started writing code, and
// requiring the closing fence would let exactly the longest implementations through.
//
// Both fences of CommonMark count. Only backticks were checked at first, and an
// independent review walked straight through with "~~~go" — a detector that knows one
// of the two spellings is a detector with a published spelling for getting past it.
func hasFencedBlock(answer string) bool {
	for _, line := range strings.Split(answer, "\n") {
		if isFence(strings.TrimSpace(line)) {
			return true
		}
	}
	return false
}

// insideFence is the shared reader used by both marker parsers.
func insideFence(f *fenceTracker, trimmed string) (isDelimiter bool, inside bool) {
	isDelimiter = f.step(trimmed)
	return isDelimiter, f.inside()
}

// isFence is the ONE definition of a code fence in this package, and having one is the
// point. The second review wave found the detector treating "~~~" as a fence while both
// marker parsers treated it as ordinary text — so a marker shown as an example inside a
// perfectly valid CommonMark block moved the state machine. Two spellings of the same
// concept in two places is how that happens.
func isFence(trimmed string) bool {
	_, _, ok := fenceOf(trimmed)
	return ok
}

// fenceOf reads an opening or closing fence: its character and how long it is.
//
// CommonMark closes a fence only with the SAME character and at least as many of them.
// The third review wave used that: "```" inside a "````" block is content, and "~~~" does
// not close backticks at all — but a parser that only asked "is this a fence line?"
// flipped its state anyway, walked out of the block and honoured the marker inside it.
func fenceOf(trimmed string) (rune, int, bool) {
	for _, char := range []rune{'`', '~'} {
		n := 0
		for _, r := range trimmed {
			if r != char {
				break
			}
			n++
		}
		if n >= 3 {
			return char, n, true
		}
	}
	return 0, 0, false
}

// fenceTracker follows the fenced/unfenced state of a text line by line, the way
// CommonMark does. Both marker parsers and the detector share it, so "what counts as
// inside a code block" has exactly one answer in this package.
type fenceTracker struct {
	char rune
	size int
	open bool
}

// step reports whether the line is a fence delimiter, and updates the state. A line that
// is a delimiter is itself content for the caller's purposes: it is kept, never parsed.
func (f *fenceTracker) step(trimmed string) bool {
	char, size, ok := fenceOf(trimmed)
	if !ok {
		return false
	}
	if !f.open {
		f.char, f.size, f.open = char, size, true
		return true
	}
	// Closing requires the same character and at least the opening length.
	if char == f.char && size >= f.size {
		f.open = false
	}
	return true
}

func (f *fenceTracker) inside() bool { return f.open }

// hasDiffHeader reports the shape of a patch. It is checked apart from the fence
// because a diff is often pasted without one.
// hasDiffHeader reports the shape of a patch. It is checked apart from the fence
// because a diff is often pasted without one.
//
// The file markers are matched without the "a/" and "b/" prefixes: those come from git,
// and `diff -u old.go new.go` produces "--- old.go" with no prefix at all. The first
// version required them, and a review produced a perfectly ordinary unified diff that
// the detector did not see.
func hasDiffHeader(answer string) bool {
	lines := strings.Split(answer, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// "@@ -" rather than "@@ ": a hunk header always names the old range first,
		// and the narrower prefix keeps an answer that merely writes "@@ здесь" out of
		// the evidence.
		if strings.HasPrefix(trimmed, "diff --git ") || strings.HasPrefix(trimmed, "@@ -") {
			return true
		}
		// A lone "--- что-то" is an ordinary separator with a caption, and a lone "+++"
		// is decoration. A diff header is the two ADJACENT: "--- old" immediately
		// followed by "+++ new". The second review wave found "--- Минусы" and "+++
		// Плюсы" — two captions in one answer, pages apart — read as a patch.
		if strings.HasPrefix(trimmed, "--- ") && len(trimmed) > 4 && i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if strings.HasPrefix(next, "+++ ") && len(next) > 4 {
				return true
			}
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
