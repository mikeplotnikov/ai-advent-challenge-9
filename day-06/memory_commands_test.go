package main

import (
	"errors"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

func TestParseRememberKeepsTheValueWhole(t *testing.T) {
	target, key, value, err := parseRemember("/remember decision storage = PostgreSQL 16 = решение DEC-0412")
	if err != nil || target != agent.TargetDecision || key != "storage" || value != "PostgreSQL 16 = решение DEC-0412" {
		t.Fatalf("parseRemember = %q %q %q %v", target, key, value, err)
	}
	for _, line := range []string{"/remember", "/remember task", "/remember task export_code EXP-1"} {
		if _, _, _, err := parseRemember(line); err == nil {
			t.Errorf("%q accepted", line)
		}
	}
}

func TestParseInjectRejectsUnknownLayers(t *testing.T) {
	got, blocks, err := parseInject(" short, long ")
	if err != nil || len(got) != 2 || got[0] != agent.LayerShort || got[1] != agent.LayerLong {
		t.Fatalf("parseInject = %v %v", got, err)
	}
	// A flag that names only layers must switch the profile off, not leave it nil and
	// therefore fully injected.
	if blocks == nil || len(blocks) != 0 {
		t.Fatalf("-inject without profile blocks must send none, got %v", blocks)
	}
	if got, blocks, err := parseInject(""); err != nil || got == nil || len(got) != 0 || blocks == nil || len(blocks) != 0 {
		t.Fatalf("empty -inject must mean nothing travels, got %v %v %v", got, blocks, err)
	}
	if _, _, err := parseInject("short,vector"); err == nil {
		t.Fatal("unknown layer accepted")
	}
}

func TestParseInjectReadsProfileBlocks(t *testing.T) {
	// `profile` is shorthand for the three blocks, and it must not duplicate a block
	// that was also named on its own.
	layers, blocks, err := parseInject("short,profile,style")
	if err != nil || len(layers) != 1 {
		t.Fatalf("parseInject = %v %v %v", layers, blocks, err)
	}
	if len(blocks) != 3 || blocks[0] != agent.BlockStyle || blocks[1] != agent.BlockConstraints || blocks[2] != agent.BlockContext {
		t.Fatalf("blocks = %v", blocks)
	}
	if _, blocks, err := parseInject("constraints"); err != nil || len(blocks) != 1 || blocks[0] != agent.BlockConstraints {
		t.Fatalf("single block: %v %v", blocks, err)
	}
}

func commandAgent(t *testing.T, memory *agent.MemoryConfig) *agent.Agent {
	t.Helper()
	t.Setenv("DEEPSEEK_API_KEY", "test-no-network")
	a, err := agent.FromEnv(agent.Config{Memory: memory})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMemoryCommandsRefuseWhenLayersAreOff(t *testing.T) {
	a := commandAgent(t, nil)
	for _, line := range []string{"/memory", "/remember profile k = v", "/drop profile k", "/task new T"} {
		handled, err := handleMemoryCommand(a, line)
		if !handled || !errors.Is(err, agent.ErrMemoryOff) {
			t.Errorf("%q: handled %v, err %v", line, handled, err)
		}
	}
	if handled, _ := handleMemoryCommand(a, "обычный вопрос"); handled {
		t.Fatal("a question was taken for a memory command")
	}
}

func TestMemoryCommandsRouteAndRefuse(t *testing.T) {
	a := commandAgent(t, &agent.MemoryConfig{Dir: t.TempDir(), User: "u", Session: "s"})
	for _, tc := range []struct {
		line string
		ok   bool
	}{
		{"/remember task export_code = EXP-5531", false}, // no active task
		{"/remember secret k = v", false},
		{"/remember profile k", false},
		{"/drop profile", false},
		{"/task rename T", false},
		{"/task new", false},
		{"/task new T-SYNC", true},
		{"/remember task export_code = EXP-5531", true},
		{"/remember knowledge vps = KRASNODAR-2", true},
		{"/drop knowledge vps", true},
		{"/drop knowledge vps", false},
		{"/task", true},
		{"/memory", true},
		{"/task done", true},
		{"/task done", false},
	} {
		handled, err := handleMemoryCommand(a, tc.line)
		if !handled || (err == nil) != tc.ok {
			t.Errorf("%q: handled %v, err %v, want ok=%v", tc.line, handled, err, tc.ok)
		}
	}
	if state := a.MemoryState(); state.Task != "" || len(state.Knowledge) != 0 {
		t.Fatalf("commands left state %+v", state)
	}
}
