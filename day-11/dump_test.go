package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

// The showcase checks its JS mirror against a committed copy of this dump in another
// repository; this keeps that copy from silently going stale.
func TestDumpMatchesTheCopyTheShowcaseChecksAgainst(t *testing.T) {
	const copyPath = "../../uchebnik-ai-advent/challeng/test/day11-definitions.json"
	committed, err := os.ReadFile(copyPath)
	if err != nil {
		t.Skipf("копии витрины нет рядом (%v); проверка идёт только там, где оба репозитория лежат вместе", err)
	}
	var got bytes.Buffer
	if err := agent.WriteMemoryDefinitions(&got, webSystem); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(committed), bytes.TrimSpace(got.Bytes())) {
		t.Errorf("выгрузка разошлась с копией витрины.\nОбнови её: go run ./day-11 -dump > %s", copyPath)
	}
}
