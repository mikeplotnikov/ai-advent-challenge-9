package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedReportReproduces(t *testing.T) {
	requireFile(t, "run.json")
	requireFile(t, "RESULTS.md")
	requireFile(t, "showcase.json")
	dir := t.TempDir()
	results := filepath.Join(dir, "RESULTS.md")
	showcase := filepath.Join(dir, "showcase.json")
	if err := writeReport("run.json", "eval/questions.json", results, showcase); err != nil {
		t.Fatal(err)
	}
	assertSameFile(t, "RESULTS.md", results)
	assertSameFile(t, "showcase.json", showcase)
}

func TestCommittedREADMECarriesResults(t *testing.T) {
	requireFile(t, "RESULTS.md")
	results, err := os.ReadFile("RESULTS.md")
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(results), "## Успех по вопросам")
	end := strings.Index(string(results), "## Ограничения")
	if start < 0 || end <= start {
		t.Fatal("RESULTS.md has no report tables")
	}
	if !strings.Contains(string(readme), string(results[start:end])) {
		t.Fatal("README.md does not carry the RESULTS.md tables verbatim")
	}
}

func TestCommittedShowcaseCopy(t *testing.T) {
	ub := filepath.Clean("../../uchebnik-ai-advent")
	if _, err := os.Stat(ub); os.IsNotExist(err) {
		t.Skip("sibling uchebnik-ai-advent repository is absent")
	}
	requireFile(t, "showcase.json")
	copyPath := filepath.Join(ub, "challeng/public/day-22/showcase.json")
	requireFile(t, copyPath)
	assertSameFile(t, "showcase.json", copyPath)
}

func TestCommittedEvidenceRanksMatchDay21(t *testing.T) {
	requireFile(t, "run.json")
	var run Run
	readJSONFile(t, "run.json", &run)
	var old struct {
		Questions []struct {
			ID        string `json:"id"`
			Structure struct {
				FirstHit int `json:"first_hit"`
			} `json:"structure"`
		} `json:"questions"`
	}
	readJSONFile(t, "../day-21/showcase.json", &old)
	want := map[string]int{}
	for _, q := range old.Questions {
		want[q.ID] = q.Structure.FirstHit
	}
	seen := map[string]bool{}
	if len(run.Questions) != 10 {
		t.Fatalf("run has %d questions, want 10", len(run.Questions))
	}
	for _, q := range run.Questions {
		if len(q.NoRAG) != 3 || len(q.RAG) != 3 {
			t.Fatalf("%s has %d/%d calls, want 3/3", q.Question.ID, len(q.NoRAG), len(q.RAG))
		}
		seen[q.Question.ID] = true
		if q.Question.Kind != "in_base" {
			continue
		}
		rank := 0
		for _, chunk := range q.Chunks {
			if rank == 0 && chunk.EvidenceHit {
				rank = chunk.Rank
			}
		}
		if rank != want[q.Question.ID] {
			t.Fatalf("%s rank=%d want=%d", q.Question.ID, rank, want[q.Question.ID])
		}
	}
	for _, id := range []string{"q05", "q10", "q15", "q20", "q25", "q30", "q35", "g01", "x01", "x02"} {
		if !seen[id] {
			t.Fatalf("run is missing %s", id)
		}
	}
}

func requireFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("required committed artefact %s: %v", path, err)
	}
}
func assertSameFile(t *testing.T, a, b string) {
	t.Helper()
	left, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	right, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatalf("%s and %s differ", a, b)
	}
}
func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}
