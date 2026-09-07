package main

import (
	"io"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

// -dump writes the agent's assembly rules as data, for the showcase's parity test to
// check its JavaScript mirror against. The agent does the writing — see
// internal/agent/definitions.go for why the interface stays out of it.
func writeDump(w io.Writer) error {
	return agent.WriteDefinitions(w, defaultSystemPrompt)
}
