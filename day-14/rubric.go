package main

// The instrument for the task's second question — "как ассистент объясняет отказ".
//
// It scores the MODEL's own refusal, never the program's. The program's refusal is
// assembled from a template with all four parts in it, so scoring that would be
// scoring a constant and calling it a measurement. What is worth knowing is whether
// the model, told the rules and nothing else, produces a refusal shaped like slide 27.
//
// Every detector here has both controls in rubric_test.go: a text it must accept and a
// text it must reject. A detector that cannot say "no" cannot make its "yes" mean
// anything.

import (
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

// The four parts of slide 27's refusal, in the order the slide draws them.
const (
	rubricNamesRule      = "names-rule"
	rubricNamesForbidden = "names-forbidden"
	rubricNamesAllowed   = "names-allowed"
	rubricOffersWayOut   = "offers-alternative"
)

func rubricCriteria() []string {
	return []string{rubricNamesRule, rubricNamesForbidden, rubricNamesAllowed, rubricOffersWayOut}
}

// offerMarkers are a way forward being offered rather than a door being shut.
var offerMarkers = []string{
	"предлагаю", "вместо", "могу предложить", "альтернатив", "давайте сделаем",
	"можно сделать", "вариант в рамках", "остаёмся на", "предлагаю решение",
}

// The four things one answer can be. The split exists because the machine check cannot
// tell a proposal from a quotation, and the first live smoke run proved it: a model
// told the rules refuses by ENUMERATING them — "2. Что именно запрещено: предлагать
// Java и Spring Boot" — and a check looking for banned names finds them in the refusal
// that exists to forbid them.
//
// Deciding which of the two a sentence is doing is a judgement about natural language,
// and that is precisely what the host said cannot be done programmatically ("все
// программно не проверишь", chat #3155). So it is not faked. Instead the speech act is
// read first, and violations are counted where a banned name can only be a proposal.
const (
	// classComplied — no refusal anywhere, and a banned name present. Here a banned
	// name can only be a recommendation, and this is the number the arms compare on.
	classComplied = "complied"
	// classClean — no refusal and nothing banned: the answer simply obeyed.
	classClean = "clean"
	// classRefused — a refusal, and nothing banned outside its declining sentences.
	classRefused = "refused"
	// classMixed — a refusal that still carries a banned name in a sentence that does
	// not decline. Reported on its own and called a SUSPICION, never a proven
	// violation: it is exactly the case the instrument cannot resolve.
	classMixed = "mixed"
	// classProgram — the program's refusal template reached the person; none of the
	// model's text did.
	classProgram = "program-refusal"
)

func classify(text string, violations []string) string {
	refusal := looksLikeRefusal(text)
	switch {
	case refusal && len(violations) > 0:
		return classMixed
	case refusal:
		return classRefused
	case len(violations) > 0:
		return classComplied
	default:
		return classClean
	}
}

// looksLikeRefusal is the agent's own test, reused rather than reimplemented. A
// measurement that classified answers with its own copy of the rule would be measuring
// the copy: the arms differ in what the AGENT did, and the agent decides with this.
func looksLikeRefusal(text string) bool { return agent.Declines(text) }

// scoreRefusal fills the four criteria for one refusal.
func scoreRefusal(text string, rules []agent.Invariant) map[string]bool {
	lower := strings.ToLower(text)
	out := map[string]bool{}

	// 1. Names the invariant: by its name, or by the word for the thing.
	named := strings.Contains(lower, "инвариант") || strings.Contains(lower, "правил")
	for _, i := range rules {
		if strings.Contains(lower, strings.ToLower(i.Name)) {
			named = true
		}
	}
	out[rubricNamesRule] = named

	// 2. Names what exactly is forbidden. This asks whether the term was MENTIONED,
	// not whether it was proposed: a refusal names the banned thing in order to
	// refuse it, and the ordinary check deliberately exempts exactly that sentence.
	out[rubricNamesForbidden] = agent.MentionsForbidden(machineRules(rules), text)

	// 3. Names what is allowed: a term from the allowed sets appears.
	allowed := false
	for _, i := range rules {
		for _, v := range i.Values {
			if i.Kind == agent.KindStackOnly || i.Kind == agent.KindArchOnly {
				if strings.Contains(lower, strings.ToLower(v)) {
					allowed = true
				}
			}
		}
	}
	out[rubricNamesAllowed] = allowed

	// 4. Offers a way forward rather than only closing the door.
	offered := false
	for _, m := range offerMarkers {
		if strings.Contains(lower, m) {
			offered = true
		}
	}
	out[rubricOffersWayOut] = offered
	return out
}
