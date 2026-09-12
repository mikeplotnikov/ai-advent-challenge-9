package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

func TestScenarioHasFifteenUniqueUserMessages(t *testing.T) {
	unique := len(common)
	for _, branch := range branches {
		unique += len(branch.Facts) + 2 // two identical final checks per branch
	}
	if unique != 15 {
		t.Fatalf("unique scenario messages = %d, want 15", unique)
	}
	if len(common) != 5 || len(branches) != 2 {
		t.Fatalf("scenario shape changed: common=%d branches=%d", len(common), len(branches))
	}
}

func TestScoreChecksRequiredAndForbiddenDetails(t *testing.T) {
	good := `{"product":"ATLAS-17","deadline":"2026-10-21","budget":"450000 RUB","roles":["employee","admin"],"integration":"Google Calendar","auth":"SSO","notifications":"Telegram","rollout":"20 employees"}`
	got := score(branches[0], 1, good)
	if !got.Correct || got.Passed != got.Total || got.Total != 12 {
		t.Fatalf("good answer = %+v", got)
	}

	bad := strings.Replace(good, `"SSO"`, `"email/password"`, 1)
	got = score(branches[0], 2, bad)
	if got.Correct || got.Passed >= got.Total {
		t.Fatalf("cross-branch leak passed: %+v", got)
	}

	missingRole := strings.Replace(good, `["employee","admin"]`, `["admin"]`, 1)
	got = score(branches[0], 2, missingRole)
	if got.Correct || got.Passed != got.Total-1 {
		t.Fatalf("employee in rollout masked a missing role: %+v", got)
	}

	extraRole := strings.Replace(good, `["employee","admin"]`, `["employee","admin","superadmin"]`, 1)
	got = score(branches[0], 2, extraRole)
	if got.Correct || got.Passed >= got.Total {
		t.Fatalf("invented role received a perfect score: %+v", got)
	}

	got = score(branches[0], 2, "not json")
	if got.Passed != 0 || got.ParseError == "" {
		t.Fatalf("invalid JSON received quality credit: %+v", got)
	}
}

func TestWriteReportsIsAtomicJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "strategies.jsonl")
	want := []report{
		{Run: "one", Strategy: agent.ContextSliding, Window: 10},
		{Run: "one", Strategy: agent.ContextFacts, Window: 10},
		{Run: "one", Strategy: agent.ContextBranching},
	}
	if err := writeReports(path, want); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 3 {
		t.Fatalf("JSONL lines = %d, want 3", len(lines))
	}
	for i, line := range lines {
		var got report
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		if got.Strategy != want[i].Strategy {
			t.Fatalf("line %d strategy = %q, want %q", i+1, got.Strategy, want[i].Strategy)
		}
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".day10-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v, err=%v", matches, err)
	}
}
