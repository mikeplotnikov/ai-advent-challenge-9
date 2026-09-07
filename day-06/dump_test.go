package main

import (
	"bytes"
	"os"
	"testing"
)

// The showcase's parity test compares its JS mirror against a committed copy of this
// dump, in a different repository. Nothing regenerates that copy, so an edit to the
// agent's assembly would leave both suites green while the showcase built a
// different request than the Go agent — the exact drift the dump exists to prevent.
//
// This closes it from the side that can: the committed copy is checked every time
// this repo's tests run.
func TestDumpMatchesTheCopyTheShowcaseChecksAgainst(t *testing.T) {
	const copyPath = "../../uchebnik-ai-advent/challeng/test/day06-definitions.json"

	committed, err := os.ReadFile(copyPath)
	if err != nil {
		// Skipped rather than failed: in a fresh clone of this repo alone the
		// showcase is not there, and a test that cannot see its subject has found
		// nothing rather than found a problem. On the machine that publishes the
		// showcase — the only one where the drift can be introduced — it runs.
		t.Skipf("копии витрины нет рядом (%v); проверка идёт только там, где оба репозитория лежат вместе", err)
	}

	var got bytes.Buffer
	if err := writeDump(&got); err != nil {
		t.Fatalf("writeDump: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(committed), bytes.TrimSpace(got.Bytes())) {
		t.Errorf("выгрузка разошлась с копией витрины.\nОбнови её: go run ./day-06 -dump > %s", copyPath)
	}
}
