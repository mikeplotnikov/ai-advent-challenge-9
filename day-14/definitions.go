package main

// The dump the showcase checks itself against. The showcase runs on JavaScript and the
// agent runs on Go, and "the two agree" has to be a machine-checked fact rather than a
// promise kept by whoever edited last: since day 5 the JS side asserts against this
// output, and a test here fails when the dump goes stale.

import (
	"encoding/json"
	"io"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

type definitions struct {
	Task       string            `json:"task"`
	Invariants []agent.Invariant `json:"invariants"`
	Arms       []armSpec         `json:"arms"`
	Scenarios  []scenarioSpec    `json:"scenarios"`
	// BlockOrder is the assembly order of a request, which the showcase reproduces.
	// Day 14 adds one line at the very top and changes nothing else.
	BlockOrder []string `json:"blockOrder"`
	// Rubric is the four parts of slide 27's refusal, in the slide's order.
	Rubric []string `json:"rubric"`
	// Vocabularies are what each kind of check can see at all. The showcase mirrors
	// the matcher, and a mirror built against a different word list agrees with Go
	// right up until the one answer where it does not.
	Vocabularies map[string][]string `json:"vocabularies"`
	// Examples are worked verdicts: text in, violation names out. They exist because
	// the JS mirror reimplements the matcher, and the two can quietly disagree about
	// exactly the cases a hand-written test would never think to try.
	Examples []verdictExample `json:"examples"`
}

type verdictExample struct {
	Case       string   `json:"case"`
	Text       string   `json:"text"`
	Violations []string `json:"violations"`
	Mentions   bool     `json:"mentions"`
	Refusal    bool     `json:"refusal"`
	// StackTerms and LibraryTerms are the matcher's own output, apart from any rule.
	// A mirror can agree on every verdict while disagreeing about what it matched —
	// "JavaScript" counted as "Java" cancels out whenever both are forbidden.
	StackTerms   []string `json:"stackTerms"`
	LibraryTerms []string `json:"libraryTerms"`
}

// verdictCases are the ones where two implementations can quietly disagree, not the
// ones that are obviously right. Day 12's review found a detector that counted "Kotlin
// хороший выбор, вот CI:" as Kotlin being delivered; day 14's own build found a check
// that read a correct refusal as a violation. Both are here.
func verdictCases() []struct {
	name, text string
} {
	return []struct{ name, text string }{
		{"javascript is not java", "Возьмём JavaScript и Node.js для прототипа."},
		// A declared refusal: the check still finds the banned name it quotes, and the
		// MARKER — not the absence of a finding — is what tells a refusal from a
		// proposal. The mirror has to reproduce both halves.
		{"a declared refusal", "Java использовать нельзя: разрешены только Kotlin и Ktor. Предлагаю решение на Ktor.\n" + "[[REFUSED: stack]]"},
		{"refusing words without a declaration", "Я не могу предложить Java. Вот пример на Java со Spring Boot:"},
		{"an ordinary connective is not a refusal", "Вместо долгих раздумий сразу возьмём Java и Spring Boot."},
		{"one bad item in a list", "Что нужно:\n- Ktor для HTTP\n- Spring Security для входа"},
		{"within the rules", "Берём Kotlin и Ktor, больше ничего не нужно."},
		{"over the dependency ceiling", "Нужны Ktor, PostgreSQL, Redis, Kafka и Keycloak."},
		{"cyrillic and declined", "Перешли с котлина на питон."},
	}
}

func buildDefinitions(invPath string) (definitions, error) {
	rules, err := loadRules(invPath)
	if err != nil {
		return definitions{}, err
	}
	defs := definitions{
		Task:       measureTask,
		Invariants: rules,
		Arms:       arms(),
		Scenarios:  scenarios(),
		BlockOrder: []string{
			"system: [INVARIANTS]",
			"system: базовый промпт",
			"system: [PROFILE]",
			"system: [LONG_TERM_MEMORY]",
			"history: краткосрочный слой",
			"user: [TASK_STATE]",
			"user: [WORKING_MEMORY]",
			"user: [PLAN]",
			"user: [USER_MESSAGE]",
		},
		Rubric: rubricCriteria(),
		Vocabularies: map[string][]string{
			string(agent.KindStackOnly): agent.VocabularyTerms(agent.KindStackOnly),
			string(agent.KindMaxDeps):   agent.VocabularyTerms(agent.KindMaxDeps),
			string(agent.KindArchOnly):  agent.VocabularyTerms(agent.KindArchOnly),
		},
	}
	machine := machineRules(rules)
	for _, c := range verdictCases() {
		v := names(agent.CheckAnswer(machine, c.text))
		if v == nil {
			v = []string{}
		}
		defs.Examples = append(defs.Examples, verdictExample{
			Case: c.name, Text: c.text, Violations: v,
			Mentions:     len(agent.CheckAnswer(machine, c.text)) > 0,
			Refusal:      looksLikeRefusal(c.text),
			StackTerms:   orEmpty(agent.FindTerms(agent.KindStackOnly, c.text)),
			LibraryTerms: orEmpty(agent.FindTerms(agent.KindMaxDeps, c.text)),
		})
	}
	return defs, nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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
