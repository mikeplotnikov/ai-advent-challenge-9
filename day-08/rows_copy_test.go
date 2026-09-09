package day08

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The showcase renders the day-8 page from a copy of these rows. Nothing regenerates
// that copy, so re-running a probe would leave both suites green while the page drew
// a run that no longer exists — the drift day 5 stopped hand-copying numbers to
// prevent, and the same guard day 7 put on its own rows.
func TestRowsMatchTheCopyTheShowcaseRendersFrom(t *testing.T) {
	pairs := map[string]string{
		"growth.jsonl":  "day08-growth.jsonl",
		"ceiling.jsonl": "day08-ceiling.jsonl",
		"window.jsonl":  "day08-window.jsonl",
		"output.jsonl":  "day08-output.jsonl",
	}
	const showcase = "../../uchebnik-ai-advent/challeng/test"

	for src, copyName := range pairs {
		t.Run(src, func(t *testing.T) {
			copyPath := filepath.Join(showcase, copyName)
			committed, err := os.ReadFile(copyPath)
			if err != nil {
				// Skipped rather than failed: in a clone of this repository alone the
				// showcase is not there, and a test that cannot see its subject has
				// found nothing rather than found a problem.
				t.Skipf("копии витрины нет рядом (%v)", err)
			}
			mine, err := os.ReadFile(src)
			if err != nil {
				t.Fatalf("%s: %v", src, err)
			}
			if !bytes.Equal(bytes.TrimSpace(mine), bytes.TrimSpace(committed)) {
				t.Errorf("строки замера разошлись с копией витрины.\nОбнови её: cp day-08/%s %s", src, copyPath)
			}
		})
	}
}
