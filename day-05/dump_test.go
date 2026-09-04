package main

import (
	"bytes"
	"os"
	"testing"
)

// The showcase's parity test compares its JS mirror against a committed copy of
// this dump, in a different repository. Nothing regenerated that copy, so any edit
// to a prompt, the output rule, the price table or the token cap would leave both
// test suites green while the showcase asked a different question than the recorded
// run — the exact drift the dump exists to prevent.
//
// This closes it from the side that can: the dump is compared against the committed
// copy every time the challenge repo's tests run, which is constantly.
func TestDumpMatchesTheCopyTheShowcaseChecksAgainst(t *testing.T) {
	const copyPath = "../../uchebnik-ai-advent/challeng/test/day05-definitions.json"

	committed, err := os.ReadFile(copyPath)
	if err != nil {
		// Skipped rather than failed: in a fresh clone of this repo alone the
		// showcase is not there, and a test that cannot see its subject has found
		// nothing rather than found a problem. On the machine that publishes the
		// showcase — the only one where the drift can be introduced — it runs.
		t.Skipf("копии витрины нет рядом (%v); проверка идёт только там, где оба репозитория лежат вместе", err)
	}

	// The local rungs are priced as free by the day that owns them, and the dump is
	// produced after that has happened in main(). Without this the table would be
	// missing four models and the comparison would fail for the wrong reason.
	registerFreePrices()

	var fresh bytes.Buffer
	if err := writeDump(&fresh); err != nil {
		t.Fatalf("выгрузка не собралась: %v", err)
	}

	if fresh.String() != string(committed) {
		t.Fatalf("выгрузка разошлась с копией, по которой сверяется витрина.\n"+
			"Пересобрать: go run ./day-05 -dump > %s\n"+
			"свежая: %d байт, копия: %d байт", copyPath, fresh.Len(), len(committed))
	}
}
