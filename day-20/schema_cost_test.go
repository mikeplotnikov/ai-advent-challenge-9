package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResultsSectionsSurviveBenchAndSchemaCostInBothOrders(t *testing.T) {
	bench := BenchReport{Revision: "fixture", Started: "2026-09-26T12:00:00+03:00", Today: benchToday,
		Runs: []BenchRun{{Verdict: BenchVerdict{}}}}
	oldSchema := SchemaCostReport{Variants: []SchemaCostVariant{{
		Name: "old-schema", SchemaBytes: 10, PromptTokens: []int{1, 2, 3}, Delta: []int{0, 0, 0},
	}}}
	newSchema := SchemaCostReport{Variants: []SchemaCostVariant{{
		Name: "new-schema", SchemaBytes: 20, PromptTokens: []int{4, 5, 6}, Delta: []int{1, 2, 3},
	}}}
	for _, schemaFirst := range []bool{false, true} {
		name := "bench_then_schema_then_bench"
		if schemaFirst {
			name = "schema_then_bench"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "RESULTS.md")
			metricsBefore := ""
			if schemaFirst {
				if err := writeSchemaCostSection(path, oldSchema); err != nil {
					t.Fatal(err)
				}
				if err := writeBenchResults(path, bench); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := writeBenchResults(path, bench); err != nil {
					t.Fatal(err)
				}
				benchOnly := readResults(t, path)
				metricsBefore = betweenMarkers(benchOnly, "<!-- metrics:start -->", "<!-- metrics:end -->")
				if err := writeSchemaCostSection(path, oldSchema); err != nil {
					t.Fatal(err)
				}
				afterAddition := readResults(t, path)
				assertResultsSections(t, afterAddition, "old-schema")
				if !strings.HasPrefix(afterAddition, benchOnly) {
					t.Fatalf("first schema-cost addition changed existing RESULTS content\nbefore:\n%s\nafter:\n%s",
						benchOnly, afterAddition)
				}
				if metricsAfterAddition := betweenMarkers(afterAddition, "<!-- metrics:start -->", "<!-- metrics:end -->"); metricsAfterAddition != metricsBefore {
					t.Fatalf("first schema-cost addition changed the bench metrics\nbefore:\n%s\nafter:\n%s",
						metricsBefore, metricsAfterAddition)
				}
				if err := writeBenchResults(path, bench); err != nil {
					t.Fatal(err)
				}
			}
			before := readResults(t, path)
			assertResultsSections(t, before, "old-schema")
			if metricsBefore == "" {
				metricsBefore = betweenMarkers(before, "<!-- metrics:start -->", "<!-- metrics:end -->")
			}

			if err := writeSchemaCostSection(path, newSchema); err != nil {
				t.Fatal(err)
			}
			after := readResults(t, path)
			assertResultsSections(t, after, "new-schema")
			if strings.Contains(after, "old-schema") {
				t.Fatalf("schema-cost replacement retained the old section:\n%s", after)
			}
			if metricsAfter := betweenMarkers(after, "<!-- metrics:start -->", "<!-- metrics:end -->"); metricsAfter != metricsBefore {
				t.Fatalf("schema-cost changed the bench metrics\nbefore:\n%s\nafter:\n%s", metricsBefore, metricsAfter)
			}
		})
	}
}

func readResults(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertResultsSections(t *testing.T, results, schemaName string) {
	t.Helper()
	for _, marker := range []string{
		"<!-- metrics:start -->", "<!-- metrics:end -->",
		"<!-- schema-cost:start -->", "<!-- schema-cost:end -->",
	} {
		if strings.Count(results, marker) != 1 {
			t.Fatalf("marker %q count=%d:\n%s", marker, strings.Count(results, marker), results)
		}
	}
	if !strings.Contains(results, schemaName) || !strings.Contains(results, benchPromptDisclosure) {
		t.Fatalf("RESULTS lost schema or disclosure:\n%s", results)
	}
}
