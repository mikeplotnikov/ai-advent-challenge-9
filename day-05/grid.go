package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// gridRun is the whole measurement: every class on every rung, and when each call
// happened. The report reads only this.
type gridRun struct {
	repeat                int
	tasks                 []task
	rungs                 []rung
	cells                 map[string]map[string]cell // task key -> rung id
	startedAt, finishedAt time.Time
}

func (g gridRun) cell(t task, r rung) cell { return g.cells[t.key][r.id] }

// countOutcomes is how far a run has got, journalled calls included.
func countOutcomes(g gridRun) int {
	n := 0
	for _, byRung := range g.cells {
		for _, c := range byRung {
			n += len(c.outcomes)
		}
	}
	return n
}

func (g *gridRun) record(t task, r rung, o outcome) {
	if g.cells == nil {
		g.cells = map[string]map[string]cell{}
	}
	if g.cells[t.key] == nil {
		g.cells[t.key] = map[string]cell{}
	}
	c := g.cells[t.key][r.id]
	c.rung, c.task = r, t
	c.outcomes = append(c.outcomes, o)
	g.cells[t.key][r.id] = c
}

// crossedPeakBoundary reports whether the run straddled one of the provider's
// rate changes. If it did, two rungs measured on either side of it were billed
// differently, and the cost column has to say so rather than average it away.
func (g gridRun) crossedPeakBoundary() bool {
	seen := map[bool]bool{}
	for _, byRung := range g.cells {
		for _, c := range byRung {
			for _, o := range c.outcomes {
				seen[llm.IsPeak(o.at)] = true
			}
		}
	}
	return len(seen) > 1
}

// plannedCall is one call a run still owes: which class, on which rung.
type plannedCall struct {
	task task
	rung rung
}

// splitLadder groups the ladder the way the run has to execute it.
//
// Three tiers are what day 5 asks for; the extra local sizes are a curve we chose to
// add. So the assignment finishes first — an interruption after the cloud phase leaves
// a complete weak/medium/strong comparison, where the earlier ordering would have left
// three local models and nothing to compare them against.
func splitLadder() (requiredLocal, bonusLocal, cloud []rung) {
	for _, r := range ladder {
		switch {
		case !r.local:
			cloud = append(cloud, r)
		case r.tier == "слабая":
			requiredLocal = append(requiredLocal, r)
		default:
			bonusLocal = append(bonusLocal, r)
		}
	}
	return
}

// planCalls is what a run still has to do, in the order it has to do it. It is the
// whole of the resume decision, in one place and with no I/O, because that decision
// is the reason the journal exists: a resumed run must call the remainder of each
// cell and not redo it. It used to live as two loop conditions written two different
// ways, which no test could reach.
//
// Local rungs are planned in blocks, one model at a time: all four weigh 20.6 GB
// resident against 11.8 GiB of GPU memory, so they cannot be held at once, and
// interleaving them would reload weights between calls until every second measured
// was load time. Cloud rungs interleave call by call — they have no residency to
// protect, and blocking them would attribute a network slowdown to whichever rung
// happened to be running through it.
func planCalls(run gridRun, chosen []task, repeat int) []plannedCall {
	requiredLocal, bonusLocal, cloud := splitLadder()

	var plan []plannedCall
	blocks := func(rungs []rung) {
		for _, r := range rungs {
			for _, t := range chosen {
				for done := len(run.cell(t, r).outcomes); done < repeat; done++ {
					plan = append(plan, plannedCall{task: t, rung: r})
				}
			}
		}
	}

	blocks(requiredLocal)
	for cycle := 0; cycle < repeat; cycle++ {
		for _, t := range chosen {
			for _, r := range cloud {
				// One call per cycle per cell, skipping the cycles the journal already
				// covers. A cell holding five calls joins from cycle five, which leaves
				// exactly the remainder and keeps the interleaving intact.
				if cycle >= len(run.cell(t, r).outcomes) {
					plan = append(plan, plannedCall{task: t, rung: r})
				}
			}
		}
	}
	blocks(bonusLocal)
	return plan
}

// runGrid measures every class on every rung, or the remainder of that if a journal
// says the run was interrupted. It walks the plan and does no deciding of its own.
func runGrid(repeat int, onlyTasks, journalPath string) (gridRun, error) {
	chosen, err := pickTasks(tasks(), onlyTasks)
	if err != nil {
		return gridRun{}, err
	}

	j, err := openJournal(journalPath)
	if err != nil {
		return gridRun{}, err
	}
	defer j.close()

	// A journal that already holds calls is a paused run, not a fresh one. What is on
	// disk is loaded back so the report covers the whole measurement.
	run := gridRun{repeat: repeat, tasks: chosen, rungs: ladder, startedAt: time.Now()}
	if j.entries > 0 {
		loaded, warnings, err := loadRun(journalPath, chosen, repeat)
		if err != nil {
			return gridRun{}, err
		}
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "⚠ %s\n", w)
		}
		run = loaded
		fmt.Fprintf(os.Stderr, "продолжаю прерванный прогон: в журнале %s уже %d вызовов\n",
			journalPath, countOutcomes(run))
	}

	plan := planCalls(run, chosen, repeat)
	total := repeat * len(chosen) * len(ladder)
	already := countOutcomes(run)

	fmt.Fprintf(os.Stderr, "СЕТКА · %d прогонов на клетку · %d классов × %d ступеней = %d вызовов\n",
		repeat, len(chosen), len(ladder), total)
	fmt.Fprintf(os.Stderr, "порядок: обязательная тройка (слабая → облачные), затем бонусные размеры\n")
	if already > 0 {
		fmt.Fprintf(os.Stderr, "осталось сделать %d — остальное уже в журнале\n", len(plan))
	}
	fmt.Fprintln(os.Stderr)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	// Named cachedClient, not clientFor: a closure called clientFor would shadow the
	// package function it means to wrap and call itself forever.
	clients := map[string]*llm.Client{}
	cachedClient := func(r rung) (*llm.Client, error) {
		if c, ok := clients[r.id]; ok {
			return c, nil
		}
		c, err := clientFor(r)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.id, err)
		}
		clients[r.id] = c
		return c, nil
	}

	startedAt := time.Now()
	done := already
	warmedRung := ""

	for _, call := range plan {
		client, err := cachedClient(call.rung)
		if err != nil {
			return run, err
		}
		// Warming happens once per contiguous local block, and never for a rung the
		// plan has nothing left to call: a resumed run must not load weights for a
		// model it will not use.
		if call.rung.local && warmedRung != call.rung.id {
			warm(ctx, client, call.rung)
			warmedRung = call.rung.id
		}

		o := runOnce(ctx, client, call.task)
		run.record(call.task, call.rung, o)
		if err := j.append(call.task.key, call.rung.id, o); err != nil {
			return run, fmt.Errorf("журнал не записался, дальше идти нельзя: %w", err)
		}

		done++
		elapsed := time.Since(startedAt)
		per := elapsed / time.Duration(max(1, done-already))
		left := (per * time.Duration(total-done)).Round(time.Minute).String()
		fmt.Fprintf(os.Stderr, "\r%d/%d · %-18s %-3s · прошло %s · осталось ~%s      ",
			done, total, call.rung.id, call.task.key, elapsed.Round(time.Second), left)
	}

	run.finishedAt = time.Now()
	fmt.Fprintln(os.Stderr)
	return run, nil
}
