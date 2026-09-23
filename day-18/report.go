package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/watch"
)

func runReportMode(opts cliOptions, stderr io.Writer) int {
	until, _ := time.Parse(time.RFC3339, opts.until)
	if err := writeReport("day-18/RESULTS.md", opts.store, opts.digests, "day-18/schema-cost.json", until); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

type coverageRow struct {
	ID                        string
	Expected, Covered, Failed int
	Longest                   time.Duration
	Publications              []watch.Publication
}

func writeReport(output, storePath, digestsPath, schemaPath string, until time.Time) error {
	state, err := watch.NewStore(storePath).Read()
	if err != nil {
		return err
	}
	digests, err := readDigests(digestsPath)
	if err != nil {
		return err
	}
	storeHash, err := fileSHA(storePath)
	if err != nil {
		return err
	}
	digestHash, err := fileSHA(digestsPath)
	if err != nil {
		return err
	}
	revision := "неизвестен"
	if raw, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		revision = strings.TrimSpace(string(raw))
	}
	rows := make([]coverageRow, 0, len(state.Watches))
	periodStart := until
	for _, current := range state.Watches {
		row := coverage(current, until)
		rows = append(rows, row)
		if len(current.Polls) > 0 {
			if at, err := time.Parse(time.RFC3339, current.Polls[0].At); err == nil && at.Before(periodStart) {
				periodStart = at
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# День 18 — измерение планировщика\n\n- Коммит: `%s`\n- store sha256: `%s`\n- digests sha256: `%s`\n- Правая граница: `%s`\n- Период данных: `%s` — `%s`\n\n", revision, storeHash, digestHash, until.UTC().Format(time.RFC3339), periodStart.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339))
	b.WriteString("## Покрытие 24/7\n\n| Наблюдение | Покрыто / ожидалось | Доля, 95% Wilson | Неудачных опросов | Самый длинный разрыв |\n|---|---:|---:|---:|---:|\n")
	for _, row := range rows {
		low, high := stats.Wilson(row.Covered, row.Expected)
		pct := 0.0
		if row.Expected > 0 {
			pct = 100 * float64(row.Covered) / float64(row.Expected)
		}
		fmt.Fprintf(&b, "| %s | %d / %d | %.1f%% (%.1f–%.1f%%) | %d | %s |\n", row.ID, row.Covered, row.Expected, pct, low*100, high*100, row.Failed, row.Longest)
	}
	b.WriteString("\n### Обнаруженные публикации\n\n")
	for _, row := range rows {
		for _, publication := range row.Publications {
			at, _ := time.Parse(time.RFC3339, publication.FirstSeenAt)
			fmt.Fprintf(&b, "- %s: %s, впервые замечена %s МСК\n", row.ID, publication.RatesDate, at.In(cbr.Moscow).Format("02.01.2006 15:04"))
		}
	}
	b.WriteString("\n## Сводки\n\n")
	failed, called, quoted := 0, 0, 0
	var prompt, cached, outputTokens int
	var totalCost float64
	knownCosts := 0
	perStepPrompt, perStepCached := map[int]int{}, map[int]int{}
	groups := map[string]int{}
	groupCost := map[string]float64{}
	groupKnown := map[string]int{}
	for _, digest := range digests {
		if digest.Error != "" {
			failed++
		}
		if digest.Checks.CalledSummary {
			called++
		}
		if digest.Checks.QuotesLastRates {
			quoted++
		}
		prompt += digest.Tokens.Prompt
		cached += digest.Tokens.Cached
		outputTokens += digest.Tokens.Output
		if digest.CostKnown {
			totalCost += digest.Cost
			knownCosts++
			groupCost[digest.DigestEvery] += digest.Cost
			groupKnown[digest.DigestEvery]++
		}
		for i, call := range digest.Tokens.PerCall {
			perStepPrompt[i+1] += call.Prompt
			perStepCached[i+1] += call.Cached
		}
		groups[digest.DigestEvery]++
	}
	fmt.Fprintf(&b, "Всего: %d; с ошибкой: %d.\n\n", len(digests), failed)
	writeRate(&b, "Успешно вызван `get_watch_summary`", called, len(digests))
	writeRate(&b, "Текст цитирует последние курсы", quoted, len(digests))
	b.WriteString("\n## Экономика\n\n")
	if len(digests) > 0 {
		fmt.Fprintf(&b, "Среднее на сводку: %.1f prompt, %.1f cached, %.1f output токенов.\n\n", float64(prompt)/float64(len(digests)), float64(cached)/float64(len(digests)), float64(outputTokens)/float64(len(digests)))
	}
	steps := make([]int, 0, len(perStepPrompt))
	for step := range perStepPrompt {
		steps = append(steps, step)
	}
	sort.Ints(steps)
	for _, step := range steps {
		share := 0.0
		if perStepPrompt[step] > 0 {
			share = 100 * float64(perStepCached[step]) / float64(perStepPrompt[step])
		}
		fmt.Fprintf(&b, "- Ход %d: %.1f%% входа из кэша\n", step, share)
	}
	if knownCosts > 0 {
		fmt.Fprintf(&b, "\nСредняя известная цена: $%.6f; суммарная: $%.6f.\n", totalCost/float64(knownCosts), totalCost)
	}
	periods := make([]string, 0, len(groups))
	for period := range groups {
		periods = append(periods, period)
	}
	sort.Strings(periods)
	for _, period := range periods {
		count := groups[period]
		if duration, err := time.ParseDuration(period); err == nil && groupKnown[period] > 0 {
			average := groupCost[period] / float64(groupKnown[period])
			fmt.Fprintf(&b, "Цена суток для периода %s: $%.6f (%d сводок в данных).\n", period, average*24/duration.Hours(), count)
		}
	}
	b.WriteString("\n## Цена схем\n\n")
	if raw, err := os.ReadFile(schemaPath); err == nil {
		var measured SchemaCost
		if err := json.Unmarshal(raw, &measured); err != nil {
			return fmt.Errorf("schema-cost: %w", err)
		}
		if len(measured.Differences) > 0 {
			fmt.Fprintf(&b, "Токенов схем на ход: %d. За все ходы сводок: %d токенов.\n", measured.Differences[0], measured.Differences[0]*sumModelCalls(digests))
		}
		if measured.Mismatch {
			b.WriteString("Три измерения расходятся; значение нельзя считать стабильным.\n")
		}
	} else if os.IsNotExist(err) {
		b.WriteString("Цена схем не измерена.\n")
	} else {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	return os.WriteFile(output, []byte(b.String()), 0o600)
}

func coverage(current watch.Watch, until time.Time) coverageRow {
	row := coverageRow{ID: current.ID, Publications: []watch.Publication{}}
	if len(current.Polls) == 0 || current.EveryMinutes <= 0 {
		return row
	}
	period := time.Duration(current.EveryMinutes) * time.Minute
	first, err := time.Parse(time.RFC3339, current.Polls[0].At)
	if err != nil {
		return row
	}
	boundary := until
	if current.StoppedAt != "" {
		if stopped, err := time.Parse(time.RFC3339, current.StoppedAt); err == nil && stopped.Before(boundary) {
			boundary = stopped
		}
	}
	firstSlot, lastSlot := watch.Slot(first, period), watch.Slot(boundary, period)-1
	if lastSlot >= firstSlot {
		row.Expected = int(lastSlot - firstSlot + 1)
	}
	covered := map[int64]bool{}
	publications := map[string]string{}
	var success []time.Time
	for _, poll := range current.Polls {
		at, err := time.Parse(time.RFC3339, poll.At)
		if err != nil {
			continue
		}
		slot := watch.Slot(at, period)
		if slot < firstSlot || slot > lastSlot {
			continue
		}
		if poll.OK {
			covered[slot] = true
			success = append(success, at)
			if _, ok := publications[poll.RatesDate]; !ok {
				publications[poll.RatesDate] = poll.At
			}
		} else {
			row.Failed++
		}
	}
	row.Covered = len(covered)
	sort.Slice(success, func(i, j int) bool { return success[i].Before(success[j]) })
	for i := 1; i < len(success); i++ {
		if gap := success[i].Sub(success[i-1]); gap > row.Longest {
			row.Longest = gap
		}
	}
	dates := make([]string, 0, len(publications))
	for date := range publications {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	for _, date := range dates {
		row.Publications = append(row.Publications, watch.Publication{RatesDate: date, FirstSeenAt: publications[date]})
	}
	return row
}

func writeRate(b *strings.Builder, label string, successes, n int) {
	low, high := stats.Wilson(successes, n)
	pct := 0.0
	if n > 0 {
		pct = 100 * float64(successes) / float64(n)
	}
	fmt.Fprintf(b, "- %s: %d/%d (%.1f%%; 95%% %.1f–%.1f%%)\n", label, successes, n, pct, low*100, high*100)
}
func sumModelCalls(digests []Digest) int {
	total := 0
	for _, digest := range digests {
		total += digest.ModelCalls
	}
	return total
}
func fileSHA(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
