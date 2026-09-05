package main

import (
	"testing"
	"time"
)

// planCalls is the whole of the resume decision, and it had no test at all: the
// second review wave replaced the loop that used to hold it with "always do repeat
// calls, ignore the journal" and the entire suite stayed green. That mutation would
// re-run and re-bill every already-measured cell on every resume — the exact
// four-hours-lost failure the journal was written to prevent.

func planFor(t *testing.T, repeat int, filled map[[2]string]int) []plannedCall {
	t.Helper()
	chosen := tasks()
	run := gridRun{repeat: repeat, tasks: chosen, rungs: ladder}
	for key, n := range filled {
		var task task
		found := false
		for _, candidate := range chosen {
			if candidate.key == key[0] {
				task, found = candidate, true
			}
		}
		if !found {
			t.Fatalf("в сетке нет класса %s", key[0])
		}
		rung, ok := rungByID(key[1])
		if !ok {
			t.Fatalf("в лесенке нет ступени %s", key[1])
		}
		for i := 0; i < n; i++ {
			run.record(task, rung, outcome{at: time.Now(), correct: true, parsed: true})
		}
	}
	return planCalls(run, chosen, repeat)
}

func countIn(plan []plannedCall, taskKey, rungID string) int {
	n := 0
	for _, c := range plan {
		if c.task.key == taskKey && c.rung.id == rungID {
			n++
		}
	}
	return n
}

func TestAFreshRunPlansEveryCall(t *testing.T) {
	plan := planFor(t, 3, nil)
	if want := 3 * len(tasks()) * len(ladder); len(plan) != want {
		t.Fatalf("запланировано %d вызовов, ожидалось %d", len(plan), want)
	}
	for _, r := range ladder {
		for _, task := range tasks() {
			if got := countIn(plan, task.key, r.id); got != 3 {
				t.Errorf("%s на %s: запланировано %d, ожидалось 3", task.key, r.id, got)
			}
		}
	}
}

func TestAFullCellIsNotPlannedAgain(t *testing.T) {
	// The mutation this file exists for: if the plan ignored what the journal holds,
	// this cell would be measured — and paid for — a second time.
	plan := planFor(t, 3, map[[2]string]int{
		{"T1", "deepseek-v4-pro"}: 3,
		{"T0", "qwen3:1.7b"}:      3,
	})
	if got := countIn(plan, "T1", "deepseek-v4-pro"); got != 0 {
		t.Errorf("полная облачная клетка перепланирована %d раз", got)
	}
	if got := countIn(plan, "T0", "qwen3:1.7b"); got != 0 {
		t.Errorf("полная локальная клетка перепланирована %d раз", got)
	}
	if want := 3*len(tasks())*len(ladder) - 6; len(plan) != want {
		t.Errorf("в плане %d вызовов, ожидалось %d", len(plan), want)
	}
}

func TestAPartialCellPlansExactlyTheRemainder(t *testing.T) {
	plan := planFor(t, 30, map[[2]string]int{
		{"T2", "deepseek-v4-flash"}: 11,
		{"T3", "qwen3:14b"}:         29,
		{"T1", "qwen3:4b"}:          1,
	})
	for _, c := range []struct {
		task, rung string
		want       int
	}{
		{"T2", "deepseek-v4-flash", 19},
		{"T3", "qwen3:14b", 1},
		{"T1", "qwen3:4b", 29},
	} {
		if got := countIn(plan, c.task, c.rung); got != c.want {
			t.Errorf("%s на %s: запланировано %d, ожидалось %d", c.task, c.rung, got, c.want)
		}
	}
}

func TestAnOverfilledCellPlansNothingRatherThanNegative(t *testing.T) {
	// A journal can hold more than the target if a later run asked for fewer repeats.
	// The remainder is zero, not a negative count that would wrap a loop.
	plan := planFor(t, 5, map[[2]string]int{{"T1", "qwen3:8b"}: 9})
	if got := countIn(plan, "T1", "qwen3:8b"); got != 0 {
		t.Errorf("переполненная клетка запланирована %d раз", got)
	}
}

func TestPlanAndJournalTogetherHitTheTargetExactly(t *testing.T) {
	// The invariant that matters more than any single count: whatever the journal
	// already holds, plus whatever the plan will add, is exactly the target for every
	// cell. Under-measuring is as wrong as double-measuring.
	const repeat = 30
	filled := map[[2]string]int{
		{"T0", "qwen3:1.7b"}:        30,
		{"T1", "qwen3:1.7b"}:        30,
		{"T2", "deepseek-v4-flash"}: 7,
		{"T2", "deepseek-v4-pro"}:   6,
		{"T4", "qwen3:14b"}:         0,
	}
	plan := planFor(t, repeat, filled)
	for _, r := range ladder {
		for _, task := range tasks() {
			have := filled[[2]string{task.key, r.id}]
			if total := have + countIn(plan, task.key, r.id); total != repeat {
				t.Errorf("%s на %s: журнал %d + план %d = %d, ожидалось %d",
					task.key, r.id, have, countIn(plan, task.key, r.id), total, repeat)
			}
		}
	}
}

func TestTheAssignmentIsPlannedBeforeTheExtras(t *testing.T) {
	// Three tiers are the task; the other local sizes are a curve we added. An
	// interruption after the cloud calls has to leave a complete weak/medium/strong
	// comparison, so every required rung is planned before any bonus one.
	plan := planFor(t, 2, nil)
	lastRequired, firstBonus := -1, len(plan)
	for i, c := range plan {
		switch c.rung.tier {
		case "слабая", "средняя", "сильная":
			lastRequired = i
		case "+ступень":
			if i < firstBonus {
				firstBonus = i
			}
		}
	}
	if lastRequired > firstBonus {
		t.Errorf("бонусная ступень запланирована на позиции %d, раньше обязательной на %d",
			firstBonus, lastRequired)
	}
}

func TestLocalCallsAreBlockedAndCloudCallsInterleave(t *testing.T) {
	// Not a style preference. Four local models weigh 20.6 GB resident against
	// 11.8 GiB of GPU memory, so interleaving them would reload weights between calls
	// until every second measured was load time. Cloud rungs have no residency to
	// protect, and blocking them would blame a network slowdown on one rung.
	plan := planFor(t, 3, nil)

	// Each local rung occupies one contiguous stretch of the plan.
	seenBlocks := map[string]int{}
	previous := ""
	for _, c := range plan {
		if !c.rung.local {
			previous = ""
			continue
		}
		if c.rung.id != previous {
			seenBlocks[c.rung.id]++
			previous = c.rung.id
		}
	}
	for id, blocks := range seenBlocks {
		if blocks != 1 {
			t.Errorf("локальная ступень %s разбита на %d блоков — веса будут перезагружаться", id, blocks)
		}
	}

	// Consecutive cloud calls alternate rungs rather than running one to exhaustion.
	var cloudRuns []string
	for _, c := range plan {
		if !c.rung.local {
			cloudRuns = append(cloudRuns, c.rung.id)
		}
	}
	if len(cloudRuns) < 4 {
		t.Fatalf("облачных вызовов в плане всего %d", len(cloudRuns))
	}
	sameInARow := 1
	worst := 1
	for i := 1; i < len(cloudRuns); i++ {
		if cloudRuns[i] == cloudRuns[i-1] {
			sameInARow++
			if sameInARow > worst {
				worst = sameInARow
			}
		} else {
			sameInARow = 1
		}
	}
	if worst > 2 {
		t.Errorf("облачные вызовы идут по %d подряд одной ступенью — это уже блок, не чередование", worst)
	}
}

func TestResumingACompleteRunPlansNothing(t *testing.T) {
	filled := map[[2]string]int{}
	for _, r := range ladder {
		for _, task := range tasks() {
			filled[[2]string{task.key, r.id}] = 4
		}
	}
	if plan := planFor(t, 4, filled); len(plan) != 0 {
		t.Errorf("для завершённого прогона запланировано %d вызовов", len(plan))
	}
}
