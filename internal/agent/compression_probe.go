package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// CompressionProbeSystem makes quality measurable rather than asking a person to
// prefer one fluent answer over another. Each early exchange plants a distinct marker;
// the final questions ask for markers from the beginning, middle and newest tail.
// The model is asked for the marker alone, so exact containment is a narrow, visible
// check — not a claim about general conversational intelligence.
const CompressionProbeSystem = "Ты участвуешь в воспроизводимом тесте памяти диалога. " +
	"На сообщение с фактом отвечай «принято». На контрольный вопрос верни только точный код из факта, без пояснений."

type compressionProbeFact struct {
	Prompt string
	Code   string
}

var compressionProbeFacts = []compressionProbeFact{
	{"Факт 1: код первого договора — ЛИРА-17. Запомни его.", "ЛИРА-17"},
	{"Факт 2: код второго договора — ОРБИТА-24. Запомни его.", "ОРБИТА-24"},
	{"Факт 3: код третьего договора — КАСКАД-31. Запомни его.", "КАСКАД-31"},
	{"Факт 4: код четвёртого договора — ВЕКТОР-42. Запомни его.", "ВЕКТОР-42"},
	{"Факт 5: код пятого договора — МАЯК-58. Запомни его.", "МАЯК-58"},
	{"Факт 6: код шестого договора — ФАКЕЛ-63. Запомни его.", "ФАКЕЛ-63"},
	{"Факт 7: код седьмого договора — ПРИЗМА-76. Запомни его.", "ПРИЗМА-76"},
	{"Факт 8: код восьмого договора — МЕРИДИАН-89. Запомни его.", "МЕРИДИАН-89"},
}

type compressionProbeQuestion struct {
	Prompt string
	Code   string
}

var compressionProbeQuestions = []compressionProbeQuestion{
	{"Какой код у первого договора?", "ЛИРА-17"},
	{"Какой код у четвёртого договора?", "ВЕКТОР-42"},
	{"Какой код у восьмого договора?", "МЕРИДИАН-89"},
}

// CompressionProbeCheck is one closed quality observation. Correct says only whether
// the requested planted marker survived this fixture; it must not be read as a score
// for arbitrary conversations.
type CompressionProbeCheck struct {
	Prompt   string `json:"prompt"`
	Expected string `json:"expected"`
	Answer   string `json:"answer"`
	Correct  bool   `json:"correct"`
}

// CompressionProbeReport compares an uncompressed or compressed run on the same
// fixture. Total includes all provider calls; SummarySpend names the overhead instead
// of making a small answer request look like the whole cost of compression.
type CompressionProbeReport struct {
	Run          string                  `json:"run"`
	Mode         string                  `json:"mode"`
	Checks       []CompressionProbeCheck `json:"checks"`
	Correct      int                     `json:"correct"`
	Total        Totals                  `json:"total"`
	Context      ContextState            `json:"context"`
	SummarySpend Totals                  `json:"summarySpend"`
}

// RunCompressionProbe exercises one configured agent. The caller runs it twice with
// the same model and fixture: once with KeepLastMessages=0, then with compression.
func RunCompressionProbe(ctx context.Context, a *Agent, mode string) (CompressionProbeReport, error) {
	report := CompressionProbeReport{Mode: mode, Checks: make([]CompressionProbeCheck, 0, len(compressionProbeQuestions))}
	finish := func() CompressionProbeReport {
		report.Total = a.Totals()
		report.Context = a.ContextState()
		report.SummarySpend = report.Context.SummarySpend
		return report
	}
	for _, fact := range compressionProbeFacts {
		if _, err := a.Ask(ctx, fact.Prompt); err != nil {
			return finish(), fmt.Errorf("%s: запись факта %q: %w", mode, fact.Code, err)
		}
	}

	for _, question := range compressionProbeQuestions {
		reply, err := a.Ask(ctx, question.Prompt)
		if err != nil {
			return finish(), fmt.Errorf("%s: контроль %q: %w", mode, question.Code, err)
		}
		check := CompressionProbeCheck{
			Prompt:   question.Prompt,
			Expected: question.Code,
			Answer:   reply.Text,
			Correct:  strings.Contains(strings.ToUpper(reply.Text), strings.ToUpper(question.Code)),
		}
		if check.Correct {
			report.Correct++
		}
		report.Checks = append(report.Checks, check)
	}
	return finish(), nil
}

// WriteCompressionProbeReports persists the two raw reports as JSON Lines. JSON
// belongs inside the agent boundary for the same reason the session format does: the
// CLI demonstrates an agent, not the provider or data encoding around it.
func WriteCompressionProbeReports(w io.Writer, reports ...CompressionProbeReport) error {
	enc := json.NewEncoder(w)
	for _, report := range reports {
		if err := enc.Encode(report); err != nil {
			return err
		}
	}
	return nil
}
