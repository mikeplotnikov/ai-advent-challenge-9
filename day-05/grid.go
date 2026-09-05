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

// runGrid measures every class on every rung. Two phases, for a reason that is a
// property of this machine rather than a preference:
//
// Local rungs run in blocks, one model at a time. All four weigh 20.6 GB resident
// against 11.8 GiB of GPU memory, so they cannot be held at once; interleaving them
// would evict and reload weights between calls and every second measured would be
// load time. Within a block the model stays warm.
//
// Cloud rungs interleave call by call. They have no residency to protect, and
// blocking them would attribute any network or provider slowdown to whichever rung
// happened to be running through it.
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

	// A journal that already holds calls is a paused run, not a fresh one. What is
	// on disk is loaded back so the report covers the whole measurement, and only
	// the remainder of each cell is called.
	run := gridRun{repeat: repeat, tasks: chosen, rungs: ladder, startedAt: time.Now()}
	if len(j.have) > 0 {
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

	// Three tiers are the assignment; the extra local sizes are a curve we added.
	// So the assignment finishes first: the required local rung, then the two cloud
	// rungs, then the bonus sizes. An interruption after the first two phases leaves
	// a complete weak/medium/strong comparison — the earlier ordering would have left
	// three local models and nothing to compare them against.
	var requiredLocal, bonusLocal, cloud []rung
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

	total := repeat * len(chosen) * len(ladder)
	already := countOutcomes(run)
	fmt.Fprintf(os.Stderr, "СЕТКА · %d прогонов на клетку · %d классов × %d ступеней = %d вызовов\n",
		repeat, len(chosen), len(ladder), total)
	if already > 0 {
		fmt.Fprintf(os.Stderr, "осталось сделать %d — остальное уже в журнале\n", total-already)
	}
	fmt.Fprintf(os.Stderr, "порядок: обязательная тройка (слабая → облачные), затем бонусные размеры\n")
	fmt.Fprintf(os.Stderr, "локальные блоками (модель держится в памяти), облачные с чередованием\n\n")

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	done := already
	startedAt := time.Now()
	report := func(r rung, t task) {
		done++
		elapsed := time.Since(startedAt)
		per := elapsed / time.Duration(max(1, done-already))
		left := (per * time.Duration(total-done)).Round(time.Minute).String()
		fmt.Fprintf(os.Stderr, "\r%d/%d · %-18s %-3s · прошло %s · осталось ~%s      ",
			done, total, r.id, t.key, elapsed.Round(time.Second), left)
	}

	// Local rungs run in blocks, ascending in size so the biggest model loads last
	// and the server has already evicted the smaller ones.
	runLocal := func(rungs []rung) error {
		for _, r := range rungs {
			client, err := clientFor(r)
			if err != nil {
				return fmt.Errorf("%s: %w", r.id, err)
			}
			if err := runLocalRung(ctx, client, r, chosen, repeat, &run, j, report); err != nil {
				return err
			}
		}
		return nil
	}

	if err := runLocal(requiredLocal); err != nil {
		return run, err
	}

	// Cloud rungs interleave call by call.
	clients := map[string]*llm.Client{}
	for _, r := range cloud {
		client, err := clientFor(r)
		if err != nil {
			return run, fmt.Errorf("%s: %w", r.id, err)
		}
		clients[r.id] = client
	}
	for i := 0; i < repeat; i++ {
		for _, t := range chosen {
			for _, r := range cloud {
				if len(run.cell(t, r).outcomes) >= repeat {
					continue
				}
				o := runOnce(ctx, clients[r.id], t)
				run.record(t, r, o)
				if err := j.append(t.key, r.id, o); err != nil {
					return run, fmt.Errorf("журнал не записался, дальше идти нельзя: %w", err)
				}
				report(r, t)
			}
		}
	}

	// Bonus sizes last: everything the assignment requires is already measured.
	if err := runLocal(bonusLocal); err != nil {
		return run, err
	}

	run.finishedAt = time.Now()
	fmt.Fprintln(os.Stderr)
	return run, nil
}

// runLocalRung measures every class on one local rung, keeping its weights resident
// for the whole block. Warming happens only if the rung actually has calls left, so
// a resumed run does not load a model it will not use.
func runLocalRung(ctx context.Context, client *llm.Client, r rung, chosen []task, repeat int,
	run *gridRun, j *journal, report func(rung, task)) error {
	warmed := false
	for _, t := range chosen {
		for len(run.cell(t, r).outcomes) < repeat {
			if !warmed {
				warm(ctx, client, r)
				warmed = true
			}
			o := runOnce(ctx, client, t)
			run.record(t, r, o)
			if err := j.append(t.key, r.id, o); err != nil {
				return fmt.Errorf("журнал не записался, дальше идти нельзя: %w", err)
			}
			report(r, t)
		}
	}
	return nil
}
