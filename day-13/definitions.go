package main

// The dump the showcase checks itself against. The showcase runs on JavaScript and this
// runs on Go, and "the two agree" has to be a machine-checked fact rather than a promise
// kept by whoever edited last: from day 5 on, the JS side asserts against this output,
// and a test here fails when the dump goes stale.

import (
	"context"
	"encoding/json"
	"os"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

func buildDefinitions() (definitions, error) {
	defs := definitions{
		Task:     measureTask,
		Plan:     measurePlan,
		Criteria: criteriaRows(),
		// The assembly order of a request, which the showcase reproduces.
		BlockOrder: []string{
			"system: базовый промпт",
			"system: [PROFILE]",
			"system: [LONG_TERM_MEMORY]",
			"history: краткосрочный слой",
			"user: [TASK_STATE]",
			"user: [WORKING_MEMORY]",
			"user: [PLAN]",
			"user: [USER_MESSAGE]",
		},
		Markers: map[string]string{
			"nextStep":   "[[NEXT_STEP]]",
			"transition": "[[TRANSITION: <stage>]]",
		},
	}
	for _, name := range agent.StageSetNames() {
		set, err := agent.LookupStageSet(name)
		if err != nil {
			return defs, err
		}
		dump := stageSetDump{
			Name: set.Name, About: set.About,
			Transitions: map[string][]string{}, Expect: map[string]string{},
		}
		for _, stage := range set.Stages() {
			dump.Stages = append(dump.Stages, string(stage))
			allowed := []string{}
			for _, to := range set.Stages() {
				if set.Allowed(stage, to) {
					allowed = append(allowed, string(to))
				}
			}
			dump.Transitions[string(stage)] = allowed
			dump.Expect[string(stage)] = set.Expect(stage)
		}
		defs.StageSets = append(defs.StageSets, dump)
	}

	block, err := recordBlock(resumeScenarios[1]) // execution, step 2/4, with a carried result
	if err != nil {
		return defs, err
	}
	defs.Example = exampleDump{Scenario: resumeScenarios[1].Name, Block: block}
	return defs, nil
}

// recorderOnly captures the request and never answers, so producing the dump costs
// nothing and cannot accidentally reach the provider.
type recorderOnly struct{ wire string }

func (r *recorderOnly) AskWith(_ context.Context, messages []llm.Message, _ llm.Options) (llm.Answer, error) {
	for _, m := range messages {
		r.wire += m.Role + ":" + m.Content + "\n"
	}
	return llm.Answer{}, errEnough
}

var errEnough = errorString("запрос записан")

type errorString string

func (e errorString) Error() string { return string(e) }

// recordBlock builds one real request through the agent and returns the state block it
// carried. The dump therefore shows what the code sends, not what a comment says it
// sends — the same arrangement day 5 introduced and every day since has relied on.
func recordBlock(sc scenario) (string, error) {
	dir, err := os.MkdirTemp("", "day13-dump-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err := seed(dir, sc, agent.StandardStages, true, sc.Carry); err != nil {
		return "", err
	}
	rec := &recorderOnly{}
	a, err := agent.New(rec, measureConfig(dir, agent.StandardStages, true, true))
	if err != nil {
		return "", err
	}
	// The call fails on purpose; the request is what is wanted.
	_, _ = a.Ask(context.Background(), resumeQuestion)
	return stateBlockOf(rec.wire), nil
}

// readDump parses a previously written dump, for the test that keeps it from going
// stale against the code.
func readDump(path string) (definitions, error) {
	var defs definitions
	raw, err := os.ReadFile(path)
	if err != nil {
		return defs, err
	}
	return defs, json.Unmarshal(raw, &defs)
}
