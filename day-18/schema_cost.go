package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SchemaUsage struct {
	WithTools    llm.Usage `json:"with_tools"`
	WithoutTools llm.Usage `json:"without_tools"`
}
type SchemaCost struct {
	At          string        `json:"at"`
	Model       string        `json:"model"`
	ToolCount   int           `json:"tool_count"`
	Pairs       []SchemaUsage `json:"pairs"`
	Differences []int         `json:"differences"`
	Mismatch    bool          `json:"mismatch"`
	Cost        float64       `json:"cost"`
	CostKnown   bool          `json:"cost_known"`
}

type firstTurnLLM struct{ inner toolagent.LLM }

func (f firstTurnLLM) Converse(ctx context.Context, messages []llm.ToolMessage, opts llm.Options) (llm.Answer, error) {
	answer, err := f.inner.Converse(ctx, messages, opts)
	answer.ToolCalls = nil
	return answer, err
}

func runSchemaCostMode(opts cliOptions, stdout, stderr io.Writer) int {
	model, err := newModel(stderr)
	if err != nil {
		return 1
	}
	commandForStore := func(store string) string {
		return schemaCommand(opts.command, store)
	}
	return executeSchemaCost(opts.timeout, stdout, stderr, model, commandForStore, "day-18/schema-cost.json", time.Now())
}

func schemaCommand(command, store string) string {
	if command == "" {
		return "go run ./day-18/mcp-server -store " + store
	}
	// The temporary store is a mode invariant. Appending the flag also
	// overrides an earlier -store in a custom Go flag-based server command.
	return command + " -store " + store
}

func executeSchemaCost(timeout time.Duration, stdout, stderr io.Writer, model toolagent.LLM, commandForStore func(string) string, output string, now time.Time) int {
	stderr = synchronizeWriter(stderr)
	temp, err := os.MkdirTemp("", "day18-schema-")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer os.RemoveAll(temp)
	command := commandForStore(filepath.Join(temp, "store.json"))
	transport, err := mcpclient.NewTransport("", command)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if cmd, ok := transport.MCP.(*mcp.CommandTransport); ok {
		cmd.Command.Stderr = stderr
		cmd.Command.Env = withoutProviderSecrets(os.Environ())
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	session, err := mcpclient.NewNamed("ai-advent-day-18-schema-cost", "1").Open(ctx, transport)
	if err != nil {
		fmt.Fprintln(stderr, "MCP:", err)
		return 1
	}
	defer session.Close()
	result, err := measureSchemaCost(ctx, session, model, now)
	if err != nil {
		fmt.Fprintln(stderr, safeMultiline(err.Error()))
		return 1
	}
	if err := writeJSONAtomic(output, result); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "токенов схем: %v\n", result.Differences)
	if result.CostKnown {
		fmt.Fprintf(stderr, "[schema-cost: 6 обращений · $%.6f]\n", result.Cost)
	} else {
		fmt.Fprintln(stderr, "[schema-cost: 6 обращений · цена неизвестна]")
	}
	return 0
}

func measureSchemaCost(ctx context.Context, session toolagent.Session, model toolagent.LLM, now time.Time) (SchemaCost, error) {
	result := SchemaCost{At: now.UTC().Format(time.RFC3339), Pairs: []SchemaUsage{}, Differences: []int{}, CostKnown: true}
	question := "Составь сводку по всем активным наблюдениям за последние 24 ч"
	for i := 0; i < 3; i++ {
		with, err := toolagent.Run(ctx, firstTurnLLM{model}, session, toolagent.Input{SystemPrompt: promptNow(now), Question: question})
		if err != nil {
			return result, err
		}
		without, err := toolagent.Run(ctx, firstTurnLLM{model}, session, toolagent.Input{SystemPrompt: promptNow(now), Question: question, NoTools: true})
		if err != nil {
			return result, err
		}
		if len(with.ModelCalls) != 1 || len(without.ModelCalls) != 1 {
			return result, fmt.Errorf("измерение схем должно делать ровно один вызов в каждой ветке")
		}
		if i == 0 {
			result.ToolCount = len(with.Tools)
		}
		result.Model = with.ModelCalls[0].Model
		wu, nu := with.ModelCalls[0].Usage, without.ModelCalls[0].Usage
		result.Pairs = append(result.Pairs, SchemaUsage{wu, nu})
		result.Differences = append(result.Differences, wu.PromptTokens-nu.PromptTokens)
		if !with.Totals.CostKnown || !without.Totals.CostKnown {
			result.CostKnown = false
		}
		result.Cost += with.Totals.Cost + without.Totals.Cost
	}
	for _, difference := range result.Differences[1:] {
		if difference != result.Differences[0] {
			result.Mismatch = true
		}
	}
	return result, nil
}

func writeJSONAtomic(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
