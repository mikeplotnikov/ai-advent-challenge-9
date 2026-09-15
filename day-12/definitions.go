package main

// The showcase page rebuilds the personalized request in JavaScript. What has to match
// is the exact request, so these are worked examples recorded from the real agent in a
// temporary directory — not a description of what the agent is believed to send.

import (
	"context"
	"fmt"
	"os"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

func buildDefinitions() (definitions, error) {
	defs := definitions{
		System: webSystem,
		Rules: []string{
			"профиль идёт в единственном system-сообщении сразу после базового промпта, до долговременного слоя и до summary",
			"блок, выключенный в inject, хранится, но не отправляется; пустой профиль не даёт ни байта",
			"писать в профиль можно только командой пользователя; ответы модели туда не попадают",
			"профиль — это не память: слои дня 11 живут отдельно и в этих примерах не участвуют",
			"конвейер plan-answer делает два вызова: план, затем ответ с планом перед вопросом",
		},
	}
	defs.Profiles = profileFixtures
	for _, c := range criteria {
		defs.Criteria = append(defs.Criteria, criterionRow{Name: c.Name, What: c.What, Accept: c.Accept, Reject: c.Reject})
	}

	dir, err := os.MkdirTemp("", "day12-dump-")
	if err != nil {
		return defs, err
	}
	defer os.RemoveAll(dir)
	if err := buildFixture(dir); err != nil {
		return defs, err
	}

	// One example per arm of the behaviour family, plus one ablation showing that a
	// stored profile with no injected block costs nothing.
	examples := append([]arm(nil), behaviourArms...)
	examples = append(examples, arm{profileSenior + "-style-only", profileSenior, []agent.ProfileBlock{agent.BlockStyle}})
	for _, a := range examples {
		sent, err := recordProfiled(dir, a)
		if err != nil {
			return defs, fmt.Errorf("agent: пример %q: %w", a.Name, err)
		}
		inject := []string{}
		for _, b := range a.Inject {
			inject = append(inject, string(b))
		}
		defs.Examples = append(defs.Examples, dumpedExample{Profile: a.Name, Inject: inject, Sent: sent})
	}
	return defs, nil
}

// recorderOnly answers every call with a fixed text and keeps the messages: the dump
// needs the request, not an answer, and must not spend anything to get it.
type recorderOnly struct{ sent []llm.Message }

func (r *recorderOnly) AskWith(_ context.Context, messages []llm.Message, _ llm.Options) (llm.Answer, error) {
	r.sent = append([]llm.Message(nil), messages...)
	return llm.Answer{Content: "ответ", Model: llm.DefaultModel}, nil
}

func recordProfiled(dir string, a arm) ([]dumpedMessage, error) {
	rec := &recorderOnly{}
	ag, err := agent.New(rec, agent.Config{
		SystemPrompt: webSystem,
		Profile:      &agent.ProfileConfig{Dir: dir, User: measureUser, Name: a.Profile, Inject: a.Inject},
	})
	if err != nil {
		return nil, err
	}
	if _, err := ag.Ask(context.Background(), questionExplain); err != nil {
		return nil, err
	}
	var out []dumpedMessage
	for _, m := range rec.sent {
		out = append(out, dumpedMessage{Role: m.Role, Content: m.Content})
	}
	return out, nil
}
