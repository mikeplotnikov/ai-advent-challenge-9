package main

import (
	"encoding/json"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

// Markers are opaque codes the model cannot guess, so a hit can only come from a
// layer that travelled in the request.
const (
	markerShort     = "BUG-7781"
	markerTask      = "EXP-5531"
	markerOldTask   = "EXP-0999"
	markerDecision  = "DEC-0412"
	markerKnowledge = "stg-orbita5"
	markerStyle     = "ИТОГ:"
	taskActive      = "T-SYNC"
	taskOther       = "T-OLD"
	recallUser      = "recall"
	behaviourUser   = "behaviour"
	fixtureSession  = "fixture"
	familyRecall    = "recall"
	familyBehaviour = "behaviour"
	shortTurnUser   = "Кстати, тикет в трекере — BUG-7781. Подтверди одним словом."
	shortTurnAnswer = "Принято."
	recallSystem    = "You are a developer's assistant. Answer every question with a single JSON object {\"value\": string or null}. The value is the exact value from the data available to you. If the value is not in the data available to you, return {\"value\": null} and do not guess."
	behaviourSystem = "You are a developer's assistant. Answer in Russian."
	styleProfile    = "Начинай каждый ответ строкой «ИТОГ:»"
	stackProfile    = "Код пиши только на Go и только стандартной библиотекой"
	decisionValue   = "PostgreSQL 16, решение DEC-0412"
	knowledgeValue  = "stg-orbita5.internal"
	recallMaxTokens = 80
	// webSystem is the showcase page's base prompt; the dump carries it to the JS mirror.
	webSystem       = "Ты ассистент разработчика. Отвечай кратко и по делу, на русском языке."
	behaviourTokens = 900
)

// arm is one injection setting over a fixed fixture.
type arm struct {
	Name   string              `json:"name"`
	Inject []agent.MemoryLayer `json:"inject"`
	Task   string              `json:"task"`
}

var (
	all     = []agent.MemoryLayer{agent.LayerShort, agent.LayerWorking, agent.LayerLong}
	noLayer = []agent.MemoryLayer{}

	recallArms = []arm{
		{"full", all, taskActive},
		{"no-short", []agent.MemoryLayer{agent.LayerWorking, agent.LayerLong}, taskActive},
		{"no-working", []agent.MemoryLayer{agent.LayerShort, agent.LayerLong}, taskActive},
		{"no-long", []agent.MemoryLayer{agent.LayerShort, agent.LayerWorking}, taskActive},
		{"none", noLayer, taskActive},
		{"full-other-task", all, taskOther},
	}
	behaviourArms = []arm{
		{"full", all, ""},
		{"no-long", []agent.MemoryLayer{agent.LayerShort, agent.LayerWorking}, ""},
		{"none", noLayer, ""},
	}
)

// probe is one question. Layer "" means the answer needs no memory at all.
type probe struct {
	Name     string            `json:"name"`
	Family   string            `json:"family"`
	Question string            `json:"question"`
	Layer    agent.MemoryLayer `json:"layer,omitempty"`
}

var (
	recallProbes = []probe{
		{"short", familyRecall, "Какой номер тикета я называл в этом разговоре?", agent.LayerShort},
		{"working", familyRecall, "Какой код выгрузки у текущей задачи?", agent.LayerWorking},
		{"decision", familyRecall, "Какое решение по хранилищу мы приняли? Назови его номер.", agent.LayerLong},
		{"knowledge", familyRecall, "Какой хост у staging-окружения?", agent.LayerLong},
		{"control", familyRecall, "Сколько будет 17*23? Ответь числом.", ""},
	}
	behaviourProbes = []probe{
		{"style", familyBehaviour, "Объясни, что такое dependency injection.", agent.LayerLong},
		{"stack", familyBehaviour, "Напиши минимальный HTTP-сервер с эндпоинтом /health.", agent.LayerLong},
	}
	stackForbidden = []string{"github.com/", "gin-gonic", "flask", "fastapi", "express", "require(", "import http"}
)

func injects(a arm, layer agent.MemoryLayer) bool {
	for _, l := range a.Inject {
		if l == layer {
			return true
		}
	}
	return false
}

// recallExpectation is what a correct answer contains in this arm: the marker when the
// layer that holds it travelled, nothing otherwise.
func recallExpectation(a arm, p probe) (want string, forbidden []string) {
	switch p.Name {
	case "control":
		return "391", nil
	case "working":
		mine, other := markerTask, markerOldTask
		if a.Task == taskOther {
			mine, other = markerOldTask, markerTask
		}
		if injects(a, agent.LayerWorking) {
			return mine, []string{other}
		}
		return "", []string{markerTask, markerOldTask}
	}
	marker := map[string]string{"short": markerShort, "decision": markerDecision, "knowledge": markerKnowledge}[p.Name]
	if injects(a, p.Layer) {
		return marker, nil
	}
	return "", nil
}

// Verdicts of a recall answer. hit and correct_null pass; the rest fail.
const (
	verdictHit         = "hit"
	verdictCorrectNull = "correct_null"
	verdictLost        = "lost"
	verdictWrong       = "wrong"
	verdictFabricated  = "fabricated"
	verdictLeak        = "leak"
	verdictParseError  = "parse_error"
)

type recallScore struct {
	Value   *string `json:"value"`
	Verdict string  `json:"verdict"`
	Pass    bool    `json:"pass"`
}

func scoreRecall(answer, want string, forbidden []string) recallScore {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(answer)), &envelope); err != nil {
		return recallScore{Verdict: verdictParseError}
	}
	raw, ok := envelope["value"]
	if !ok || len(envelope) != 1 {
		return recallScore{Verdict: verdictParseError}
	}
	var value *string
	if text := strings.TrimSpace(string(raw)); text != "null" {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			s = text // a bare number, e.g. 391
		}
		if s = strings.TrimSpace(s); s != "" {
			value = &s
		}
	}
	lower := strings.ToLower(answer)
	for _, marker := range forbidden {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return recallScore{Value: value, Verdict: verdictLeak}
		}
	}
	switch {
	case want == "" && value == nil:
		return recallScore{Verdict: verdictCorrectNull, Pass: true}
	case want == "":
		return recallScore{Value: value, Verdict: verdictFabricated}
	case value == nil:
		return recallScore{Verdict: verdictLost}
	case strings.Contains(strings.ToLower(*value), strings.ToLower(want)):
		return recallScore{Value: value, Verdict: verdictHit, Pass: true}
	}
	return recallScore{Value: value, Verdict: verdictWrong}
}

// scoreBehaviour checks the two profile instructions on free text.
func scoreBehaviour(p probe, answer string) bool {
	switch p.Name {
	case "style":
		trimmed := strings.TrimLeft(answer, " \t\r\n*#_>`")
		return strings.HasPrefix(trimmed, markerStyle)
	case "stack":
		lower := strings.ToLower(answer)
		if !strings.Contains(answer, "package main") || !strings.Contains(answer, "net/http") {
			return false
		}
		for _, bad := range stackForbidden {
			if strings.Contains(lower, bad) {
				return false
			}
		}
		return true
	}
	return false
}

// expectedSent is which markers must and must not be in the bytes of a request made
// in this arm. It is checked on what the transport received, between assembly and
// the provider — the one place where "the layer was left out" can be observed.
func expectedSent(family string, a arm) map[string]bool {
	if family == familyBehaviour {
		return map[string]bool{markerStyle: injects(a, agent.LayerLong)}
	}
	return map[string]bool{
		markerShort:     injects(a, agent.LayerShort),
		markerTask:      injects(a, agent.LayerWorking) && a.Task == taskActive,
		markerOldTask:   injects(a, agent.LayerWorking) && a.Task == taskOther,
		markerDecision:  injects(a, agent.LayerLong),
		markerKnowledge: injects(a, agent.LayerLong),
	}
}

// sentViolations lists markers whose presence in the request differs from the arm.
func sentViolations(wire string, expected map[string]bool) []string {
	var out []string
	for _, marker := range sortedKeys(expected) {
		if strings.Contains(wire, marker) != expected[marker] {
			out = append(out, marker)
		}
	}
	return out
}
