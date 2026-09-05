package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// The grid takes about four hours, almost all of it local models holding the GPU at
// 100%. It has now been interrupted twice — once for a defect found mid-run, once
// because the fans were keeping the owner awake — and both times every measurement
// taken so far was lost, because results lived in memory until the report was
// written at the end.
//
// So every call is appended to a journal as it completes. A run started against an
// existing journal continues from where it stopped instead of beginning again, and a
// report can be rendered from a journal alone. Stopping the grid becomes pausing it.

// journalEntry is one call, flat and self-describing: a journal has to be readable
// by something that does not share this program's types.
type journalEntry struct {
	Task      string    `json:"task"`
	Rung      string    `json:"rung"`
	At        time.Time `json:"at"`
	ElapsedMs int64     `json:"elapsedMs"`
	Usage     llm.Usage `json:"usage"`
	Correct   bool      `json:"correct"`
	Parsed    bool      `json:"parsed"`
	Truncated bool      `json:"truncated"`
	Reasoned  bool      `json:"reasoned"`
	Error     string    `json:"error,omitempty"`
	Raw       string    `json:"raw"`
}

type journal struct {
	path string
	file *os.File
	// entries is how many calls the file already held when it was opened — used only
	// to decide whether this is a resumed run. What each cell still owes is worked
	// out by planCalls from the run loadRun rebuilds, not counted here: two counters
	// over the same file can disagree, and the one that decides would not be the one
	// under test.
	entries int
}

// openJournal loads whatever is already on disk and opens the file for appending.
// A journal that cannot be parsed is a hard error, not a warning: silently starting
// from zero is how four hours get thrown away twice.
func openJournal(path string) (*journal, error) {
	j := &journal{path: path}

	existing, err := os.Open(path)
	switch {
	case err == nil:
		defer existing.Close()
		scanner := bufio.NewScanner(existing)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		line := 0
		for scanner.Scan() {
			line++
			if len(scanner.Bytes()) == 0 {
				continue
			}
			var e journalEntry
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				return nil, fmt.Errorf("%s, строка %d: %w", path, line, err)
			}
			j.entries++
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return nil, err
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	j.file = file
	return j, nil
}

func (j *journal) close() error {
	if j == nil || j.file == nil {
		return nil
	}
	return j.file.Close()
}

// append writes one call and flushes it. Flushing every line costs a syscall per
// call — against calls that take seconds to minutes, that is free, and it is what
// makes the journal survive a kill rather than a clean exit.
func (j *journal) append(taskKey, rungID string, o outcome) error {
	if j == nil || j.file == nil {
		return nil
	}
	e := journalEntry{
		Task: taskKey, Rung: rungID, At: o.at, ElapsedMs: o.elapsed.Milliseconds(),
		Usage: o.usage, Correct: o.correct, Parsed: o.parsed,
		Truncated: o.truncated, Reasoned: o.reasoned, Raw: o.raw,
	}
	if o.err != nil {
		e.Error = o.err.Error()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := j.file.Write(append(line, '\n')); err != nil {
		return err
	}
	return j.file.Sync()
}

// loadRun rebuilds a gridRun from a journal so a report can be written without
// calling anything. An entry whose class or rung is no longer defined is dropped
// with a warning rather than silently: it means the journal predates a change to
// the grid, and mixing the two would compare answers to different questions.
func loadRun(path string, chosen []task, repeat int) (gridRun, []string, error) {
	file, err := os.Open(path)
	if err != nil {
		return gridRun{}, nil, err
	}
	defer file.Close()

	byKey := map[string]task{}
	for _, t := range chosen {
		byKey[t.key] = t
	}

	run := gridRun{repeat: repeat, tasks: chosen, rungs: ladder}
	var warnings []string
	dropped := map[string]int{}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var e journalEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return gridRun{}, nil, fmt.Errorf("%s, строка %d: %w", path, line, err)
		}
		t, okTask := byKey[e.Task]
		r, okRung := rungByID(e.Rung)
		if !okTask || !okRung {
			dropped[e.Task+"/"+e.Rung]++
			continue
		}
		o := outcome{
			at: e.At, elapsed: time.Duration(e.ElapsedMs) * time.Millisecond,
			usage: e.Usage, correct: e.Correct, parsed: e.Parsed,
			truncated: e.Truncated, reasoned: e.Reasoned, raw: e.Raw,
		}
		if e.Error != "" {
			o.err = fmt.Errorf("%s", e.Error)
		}
		run.record(t, r, o)
		if run.startedAt.IsZero() || e.At.Before(run.startedAt) {
			run.startedAt = e.At
		}
		if e.At.After(run.finishedAt) {
			run.finishedAt = e.At
		}
	}
	if err := scanner.Err(); err != nil {
		return gridRun{}, nil, fmt.Errorf("%s: %w", path, err)
	}
	for what, n := range dropped {
		warnings = append(warnings, fmt.Sprintf("отброшено %d записей на %s: такого класса или ступени в сетке больше нет", n, what))
	}
	return run, warnings, nil
}
