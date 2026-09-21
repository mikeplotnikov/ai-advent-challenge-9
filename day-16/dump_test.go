package main

import (
	"bytes"
	"os"
	"testing"
)

func TestDumpMatchesTheCopyTheShowcaseChecksAgainst(t *testing.T) {
	const copyPath = "../../uchebnik-ai-advent/challeng/test/day16-definitions.json"
	committed, err := os.ReadFile(copyPath)
	if err != nil {
		t.Skipf("копии витрины нет рядом (%v); проверка идёт только там, где оба репозитория лежат вместе", err)
	}
	var got bytes.Buffer
	if err := writeDump(&got); err != nil {
		t.Fatalf("writeDump: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(committed), bytes.TrimSpace(got.Bytes())) {
		t.Errorf("выгрузка разошлась с копией витрины. Обнови её: go run ./day-16 -dump > %s", copyPath)
	}
}
