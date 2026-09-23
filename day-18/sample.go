package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/watch"
)

func runSample(dir string, stderr io.Writer) int {
	if err := writeSample(dir); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// writeSample feeds the fixtures through a fetch function, not a local HTTP server: the
// staleness test builds the same sample the same way, so there is one path to the bytes.
func writeSample(dir string) error {
	first, err := ratesmcp.Fixtures.ReadFile("fixtures/2026-09-01.xml")
	if err != nil {
		return err
	}
	second, err := ratesmcp.Fixtures.ReadFile("fixtures/2026-09-20-weekend.xml")
	if err != nil {
		return err
	}
	var calls atomic.Int32
	return writeSampleData(dir, cbr.Options{Fetcher: cbr.FetchFunc(func(context.Context, string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	})})
}

func writeSampleData(dir string, options cbr.Options) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	storePath, digestsPath := filepath.Join(dir, "store.json"), filepath.Join(dir, "digests.json")
	_ = os.Remove(storePath)
	_ = os.Remove(storePath + ".lock")
	_ = os.Remove(digestsPath)
	_ = os.Remove(digestsPath + ".lock")
	store := watch.NewStore(storePath)
	start := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	codes := []string{"USD", "EUR", "CNY"}
	poll, _, err := watch.FetchPoll(context.Background(), options, codes, start)
	if err != nil {
		return err
	}
	created, err := store.Create(codes, 60, start, poll)
	if err != nil {
		return err
	}
	later := start.Add(time.Hour)
	poll, _, _ = watch.FetchPoll(context.Background(), options, codes, later)
	if _, _, err := store.AppendPollIfDue(created.ID, later, poll); err != nil {
		return err
	}
	state, err := store.Read()
	if err != nil {
		return err
	}
	summary, err := watch.Summaries(state, "", 24, later)
	if err != nil {
		return err
	}
	text := "Фиксированная сводка по официальным курсам ЦБ."
	for _, item := range summary.Watches {
		for _, currency := range item.Currencies {
			text += fmt.Sprintf(" %s %.4f.", currency.Code, currency.Last.UnitRate)
		}
	}
	digest := Digest{At: later.UTC().Format(time.RFC3339), SlotStart: watch.SlotStart(later, 3*time.Hour).Format(time.RFC3339), DigestEvery: "3h0m0s", WindowHours: 24, Text: text, Model: "sample-fixed", ModelCalls: 2, ToolCalls: []DigestToolCall{{Name: "get_watch_summary", Arguments: map[string]any{"hours": 24}, IsError: false}}, Summary: &summary, Tokens: DigestTokens{Prompt: 120, Cached: 40, Output: 30, PerCall: []TokenCall{{70, 20, 5}, {50, 20, 25}}}, Cost: 0.000123, CostKnown: true, Checks: DigestChecks{CalledSummary: true, QuotesLastRates: true}}
	return writeDigests(digestsPath, []Digest{digest})
}
