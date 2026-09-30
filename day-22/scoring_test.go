package main

import (
	"math"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

func TestScoreAnswerOutcomes(t *testing.T) {
	base := Question{Kind: "in_base", Sources: []string{"a.md"}, Facts: []Fact{{Name: "one", Patterns: []string{"alpha"}}, {Name: "two", Patterns: []string{"beta"}}}}
	out := Question{Kind: "out_of_base", Neighbours: []string{"bge"}}
	chunks := []FoundChunk{{Source: "a.md"}}
	tests := []struct {
		name        string
		q           Question
		text        string
		empty       bool
		outcome     string
		markerFacts bool
	}{
		{"correct", base, "alpha beta [источник: a.md]", false, "correct", false},
		{"partial", base, "alpha", false, "partial", false},
		{"unknown", base, noDataMarker, false, "unknown", false},
		{"wrong", base, "не знаю", false, "wrong", false},
		{"declined", out, noDataMarker, false, "declined", false},
		{"fabricated", out, "используется bge", false, "fabricated", false},
		{"marker with fabrication", out, noDataMarker + " используется bge", false, "fabricated", false},
		{"empty", base, "", true, "empty", false},
		{"marker with facts", base, noDataMarker + " alpha", false, "partial", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := scoreAnswer(test.q, test.text, chunks, test.empty)
			if got.Outcome != test.outcome || got.MarkerWithFacts != test.markerFacts {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestScoreAnswerCitationChecks(t *testing.T) {
	q := Question{Kind: "in_base", Sources: []string{"expected.md"}, Facts: []Fact{{Name: "fact", Patterns: []string{"alpha"}}}}
	got := scoreAnswer(q, "alpha [источник: missing.md]", []FoundChunk{{Source: "found.md"}}, false)
	if !got.Uncited && len(got.Citations) == 0 {
		t.Fatal("citation was not parsed")
	}
	if got.AllCitationsFound || got.ExpectedCitation {
		t.Fatalf("bad citation accepted: %+v", got)
	}
	uncited := scoreAnswer(q, "alpha", nil, false)
	if !uncited.Uncited {
		t.Fatal("facts without citations were not marked uncited")
	}
	valid := scoreAnswer(q, "alpha [источник:  expected.md  ]", []FoundChunk{{Source: "expected.md"}}, false)
	if len(valid.Citations) != 1 || !valid.Citations[0].Found || !valid.Citations[0].Expected || !valid.AllCitationsFound || !valid.ExpectedCitation {
		t.Fatalf("valid citation was not recognized: %+v", valid)
	}
}

func TestMajorityAndMcNemar(t *testing.T) {
	calls := []Call{{Score: Score{Success: true}}, {Score: Score{Success: false}}, {Score: Score{Success: true}}}
	if !majority(calls) {
		t.Fatal("2/3 must be a majority")
	}
	if got := stats.McNemarExact(6, 0); math.Abs(got-0.03125) > 1e-12 {
		t.Fatalf("p=%v", got)
	}
	if got := neededDiscordance(5, 0, 10); got != "6:0 (ещё 1 в пользу RAG)" {
		t.Fatalf("needed=%s", got)
	}
	if got := neededDiscordance(7, 1, 10); got != "8:1 (ещё 1 в пользу RAG)" {
		t.Fatalf("needed=%s", got)
	}
	run := Run{}
	for i := 0; i < 10; i++ {
		noSuccess, ragSuccess := true, true
		if i < 6 {
			noSuccess = false
		}
		run.Questions = append(run.Questions, QuestionRun{
			NoRAG: []Call{{Score: Score{Success: noSuccess}}, {Score: Score{Success: noSuccess}}, {Score: Score{Success: noSuccess}}},
			RAG:   []Call{{Score: Score{Success: ragSuccess}}, {Score: Score{Success: ragSuccess}}, {Score: Score{Success: ragSuccess}}},
		})
	}
	row := buildReportData(run).Sections["Макнемар"][1]
	if row[1] != "6" || row[2] != "0" || row[3] != "0.0313" {
		t.Fatalf("McNemar row=%v", row)
	}
}
