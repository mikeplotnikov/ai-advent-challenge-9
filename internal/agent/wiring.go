package agent

import (
	"fmt"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// FromEnv builds an agent together with its transport: it loads .env, reads the key
// and the model from the environment, and points the client at the model the config
// names.
//
// It lives here rather than in the interface on purpose. The task demands that the
// request-and-response logic be encapsulated in the agent, and an interface that has
// to construct an HTTP client to get an agent has not encapsulated anything — it has
// only moved one call. So day-06 asks this package for a configured agent and never
// learns that DeepSeek, a key or an endpoint exist.
//
// New stays the injection point: tests and any application that wants to own the
// transport pass their own Caller.
func FromEnv(cfg Config) (*Agent, error) {
	llm.LoadDotEnv(".env")

	client, err := llm.New()
	if err != nil {
		return nil, err
	}
	if cfg.Model != "" {
		client.Model = cfg.Model
	} else {
		// Keep the config honest about what the answer will be priced under.
		cfg.Model = client.Model
	}
	a, err := New(client, cfg)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}
	return a, nil
}

// KnownModels lists the models the price table covers, so an interface can offer a
// choice without importing the transport package.
func KnownModels() []string {
	table := llm.PricingTable()
	out := make([]string, 0, len(table))
	for name := range table {
		out = append(out, name)
	}
	return out
}
