// Package day07 holds no program: day 7 grew internal/agent and day-06 instead of
// copying an interface into a new folder. What it does hold is the measurement data,
// and the showcase repository keeps a copy of that data to render its table from.
//
// Nothing regenerates that copy. Without this test, re-running the probe would leave
// both suites green while the page showed numbers from a run that no longer exists —
// the same drift day 5 stopped hand-copying numbers to prevent.
package day07

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeDataMatchesTheCopyTheShowcaseRendersFrom(t *testing.T) {
	pairs := map[string]string{
		"context-probe-split.jsonl":   "day07-probe-split.jsonl",
		"context-probe-control.jsonl": "day07-probe-control.jsonl",
		"context-probe-split-2.jsonl": "day07-probe-split-2.jsonl",
	}
	const showcase = "../../uchebnik-ai-advent/challeng/test"

	for src, copyName := range pairs {
		t.Run(src, func(t *testing.T) {
			copyPath := filepath.Join(showcase, copyName)
			committed, err := os.ReadFile(copyPath)
			if err != nil {
				// Skipped rather than failed: in a clone of this repository alone the
				// showcase is not there, and a test that cannot see its subject has
				// found nothing rather than found a problem. On the machine that
				// publishes the showcase — the only one where the drift can be
				// introduced — it runs.
				t.Skipf("копии витрины нет рядом (%v)", err)
			}
			mine, err := os.ReadFile(src)
			if err != nil {
				t.Fatalf("%s: %v", src, err)
			}
			if !bytes.Equal(bytes.TrimSpace(mine), bytes.TrimSpace(committed)) {
				t.Errorf("данные замера разошлись с копией витрины.\nОбнови её: cp day-07/%s %s", src, copyPath)
			}
		})
	}
}
