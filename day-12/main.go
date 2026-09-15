// Day 12 measures personalization: how the same question is answered under different
// user profiles, which preference the model actually follows, what a preference costs,
// and what happens when the question contradicts it. Raw rows go to JSONL; RESULTS.md
// is rendered from that file by -report, so no measured number is typed by hand.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// Outcomes of one call. The split between empty and transport is day 12's own: day 11
// scored an empty answer as the model's behaviour, and on 15.09 the provider spent the
// day dropping content (chat #2879-2884) while a participant blamed a max-token cap
// (#2885-2886). Two explanations, one symptom — so the run records which of the two it
// saw instead of folding both into "the model answered nothing".
const (
	outcomeOK        = "ok"
	outcomeEmpty     = "empty"
	outcomeTransport = "transport"
)

// emptyShareCeiling is the share of empty answers above which an arm's numbers are not
// interpreted. It is pre-registered here rather than chosen after seeing the run.
const emptyShareCeiling = 0.05

type usageRow struct {
	Prompt     int     `json:"prompt"`
	Completion int     `json:"completion"`
	Cached     int     `json:"cached"`
	Missed     int     `json:"missed"`
	Reasoning  int     `json:"reasoning"`
	Cost       float64 `json:"cost"`
	Priced     bool    `json:"priced"`
}

// add folds one attempt into the cell. An attempt that reported no tokens was not
// billed and says nothing about pricing; among billed attempts the cell is priced only
// if every one of them was.
func (u *usageRow) add(r agent.Usage) {
	if r.PromptTokens == 0 && r.CompletionTokens == 0 {
		return
	}
	billedBefore := u.Prompt > 0 || u.Completion > 0
	u.Prompt += r.PromptTokens
	u.Completion += r.CompletionTokens
	u.Cached += r.CachedTokens
	u.Missed += r.MissedTokens
	u.Reasoning += r.ReasoningTokens
	u.Cost += r.Cost
	u.Priced = r.Priced && (u.Priced || !billedBefore)
}

type planRow struct {
	Kind              string           `json:"kind"`
	Run               string           `json:"run"`
	Started           string           `json:"started"`
	Model             string           `json:"model"`
	Commit            string           `json:"commit"`
	Seed              int64            `json:"seed"`
	Profiles          []profileFixture `json:"profiles"`
	BehaviourArms     []arm            `json:"behaviourArms"`
	ConflictArms      []arm            `json:"conflictArms"`
	PipelineArms      []arm            `json:"pipelineArms"`
	BehaviourProbes   []probe          `json:"behaviourProbes"`
	ConflictProbes    []probe          `json:"conflictProbes"`
	PipelineProbes    []probe          `json:"pipelineProbes"`
	Criteria          []criterionRow   `json:"criteria"`
	Cells             int              `json:"cells"`
	FixtureSHA256     string           `json:"fixtureSha256"`
	EmptyShareCeiling float64          `json:"emptyShareCeiling"`
	Pilot             bool             `json:"pilot"`
	Notes             []string         `json:"notes"`
}

// criterionRow is a criterion as the report and the showcase see it: name, what it
// checks, and the fixtures that prove it can both fire and stay silent.
type criterionRow struct {
	Name   string   `json:"name"`
	What   string   `json:"what"`
	Accept []string `json:"accept"`
	Reject []string `json:"reject"`
}

// showcaseRow is E0: slide 16 made literal — one question, every profile, answers kept
// whole. It is a demonstration, not a statistic.
type showcaseRow struct {
	Kind     string            `json:"kind"`
	Run      string            `json:"run"`
	Profile  string            `json:"profile"`
	Question string            `json:"question"`
	Answer   string            `json:"answer"`
	Scores   map[string]bool   `json:"scores"`
	Sent     map[string]string `json:"sent"`
	Usage    usageRow          `json:"usage"`
	Outcome  string            `json:"outcome"`
	Error    string            `json:"error,omitempty"`
}

type probeRow struct {
	Kind           string           `json:"kind"`
	Run            string           `json:"run"`
	Order          int              `json:"order"`
	Family         string           `json:"family"`
	Arm            string           `json:"arm"`
	Profile        string           `json:"profile"`
	Inject         []string         `json:"inject"`
	Pipeline       string           `json:"pipeline"`
	Probe          string           `json:"probe"`
	Repeat         int              `json:"repeat"`
	Question       string           `json:"question"`
	Answer         string           `json:"answer"`
	Scores         map[string]bool  `json:"scores"`
	Outcome        string           `json:"outcome"`
	Truncated      bool             `json:"truncated"`
	SentViolations []string         `json:"sentViolations"`
	ProfileSent    string           `json:"profileSent"`
	ProfileTokens  int              `json:"profileTokens"`
	PlanCalls      int              `json:"planCalls"`
	PlanUsage      usageRow         `json:"planUsage"`
	Plan           string           `json:"plan,omitempty"`
	Memory         agent.MemorySent `json:"memory"`
	EstimateTotal  int              `json:"estimateTotal"`
	Usage          usageRow         `json:"usage"`
	RequestedModel string           `json:"requestedModel"`
	ServedModel    string           `json:"servedModel"`
	Peak           bool             `json:"peak"`
	At             string           `json:"at"`
	ElapsedMs      int64            `json:"elapsedMs"`
	Attempts       int              `json:"attempts"`
	RetryErrors    []string         `json:"retryErrors,omitempty"`
	Error          string           `json:"error,omitempty"`
}

type completeRow struct {
	Kind               string       `json:"kind"`
	Run                string       `json:"run"`
	Finished           string       `json:"finished"`
	ProbeRows          int          `json:"probeRows"`
	Errors             int          `json:"errors"`
	Empty              int          `json:"empty"`
	FixtureSHA256After string       `json:"fixtureSha256After"`
	Spend              agent.Totals `json:"spend"`
}

func main() {
	out := flag.String("out", "", "куда записать JSONL прогона")
	report := flag.String("report", "", "построить RESULTS.md из этого JSONL и напечатать в stdout")
	model := flag.String("model", "deepseek-flash", "модель; задаётся явно, в строках пишется и ответившая модель")
	workers := flag.Int("workers", 4, "параллельных вызовов")
	seed := flag.Int64("seed", 0, "seed перемешивания порядка; 0 — от времени, записывается в план")
	pilot := flag.Bool("pilot", false, "пилот: по одной клетке на пробу в руке senior, без E0 и без статистики")
	dump := flag.Bool("dump", false, "выгрузить профили, критерии и сборку запроса для сверки витрины и выйти")
	flag.Parse()

	if *dump {
		if err := writeDefinitions(os.Stdout); err != nil {
			fail(err)
		}
		return
	}
	if *report != "" {
		rows, err := readRows(*report)
		if err != nil {
			fail(err)
		}
		text, err := render(rows, *report)
		if err != nil {
			fail(err)
		}
		fmt.Print(text)
		return
	}
	if strings.TrimSpace(*out) == "" {
		fail(errors.New("-out обязателен, например: -out day-12/profiles.jsonl"))
	}
	if *workers < 1 {
		fail(errors.New("число потоков должно быть положительным"))
	}
	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	if err := runAll(context.Background(), *out, *model, *workers, *seed, *pilot); err != nil {
		fail(err)
	}
}

// recorder wraps the real client and keeps the bytes of the last request: the evidence
// of which profile travelled.
type recorder struct {
	client agent.Caller
	mu     sync.Mutex
	wire   string
}

func (r *recorder) AskWith(ctx context.Context, messages []llm.Message, opts llm.Options) (llm.Answer, error) {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(m.Role)
		b.WriteString(":")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	r.mu.Lock()
	r.wire = b.String()
	r.mu.Unlock()
	return r.client.AskWith(ctx, messages, opts)
}

func (r *recorder) lastWire() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wire
}

type cell struct {
	Family string
	Arm    arm
	Probe  probe
	Repeat int
}

func planCells(pilot bool) []cell {
	var cells []cell
	add := func(family string, arms []arm, probes []probe, repeats func(probe) int) {
		for _, a := range arms {
			for _, p := range probes {
				for r := 1; r <= repeats(p); r++ {
					cells = append(cells, cell{family, a, p, r})
				}
			}
		}
	}
	if pilot {
		one := func(probe) int { return 1 }
		add(familyBehaviour, behaviourArms[2:3], behaviourProbes, one)
		add(familyConflict, conflictArms[:1], conflictProbes, one)
		add(familyPipeline, pipelineArms[1:], pipelineProbes, one)
		return cells
	}
	full := func(p probe) int { return p.Repeats }
	add(familyBehaviour, behaviourArms, behaviourProbes, full)
	add(familyConflict, conflictArms, conflictProbes, full)
	add(familyPipeline, pipelineArms, pipelineProbes, full)
	return cells
}

func runAll(ctx context.Context, out, model string, workers int, seed int64, pilot bool) error {
	llm.LoadDotEnv(".env")
	client, err := llm.New()
	if err != nil {
		return err
	}
	client.Model = model
	run := fmt.Sprintf("day12-%d", time.Now().UnixNano())

	fixture, err := os.MkdirTemp("", "day12-fixture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(fixture)
	if err := buildFixture(fixture); err != nil {
		return fmt.Errorf("фикстура: %w", err)
	}
	fixtureHash, err := treeHash(fixture)
	if err != nil {
		return err
	}

	cells := planCells(pilot)
	rand.New(rand.NewSource(seed)).Shuffle(len(cells), func(i, j int) { cells[i], cells[j] = cells[j], cells[i] })

	var criteriaRows []criterionRow
	for _, c := range criteria {
		criteriaRows = append(criteriaRows, criterionRow{Name: c.Name, What: c.What, Accept: c.Accept, Reject: c.Reject})
	}
	plan := planRow{
		Kind: "plan", Run: run, Started: time.Now().UTC().Format(time.RFC3339), Model: model, Commit: gitCommit(),
		Seed: seed, Profiles: profileFixtures,
		BehaviourArms: behaviourArms, ConflictArms: conflictArms, PipelineArms: pipelineArms,
		BehaviourProbes: behaviourProbes, ConflictProbes: conflictProbes, PipelineProbes: pipelineProbes,
		Criteria: criteriaRows, Cells: len(cells), FixtureSHA256: fixtureHash,
		EmptyShareCeiling: emptyShareCeiling, Pilot: pilot,
		Notes: []string{
			"порядок клеток перемешан по seed",
			"temperature не отправляется, thinking выключен",
			"метка прогона стоит первой в system: кэш прогона холодный на старте",
			"пустое содержимое (outcome=empty) и сбой вызова (outcome=transport) различаются: первое не повторяется, второе повторяется один раз",
			"доля empty выше потолка делает числа руки непригодными для интерпретации",
			"один ответ оценивается несколькими независимыми критериями; критерии откалиброваны на фикстурах до прогона",
		},
	}
	rows := []any{plan}

	var spend agent.Totals
	if !pilot {
		showcase, showSpend, err := runShowcase(ctx, client, model, fixture, run)
		for _, r := range showcase {
			rows = append(rows, r)
		}
		spend = addTotals(spend, showSpend)
		if err != nil {
			return writeRows(out+".partial", rows, fmt.Errorf("E0: %w", err))
		}
	}

	results := make([]probeRow, len(cells))
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				results[i] = runCell(ctx, client, run, fixture, model, i, cells[i])
				if (i+1)%50 == 0 {
					fmt.Fprintf(os.Stderr, "клеток готово: около %d из %d\n", i+1, len(cells))
				}
			}
		}()
	}
	for i := range cells {
		next <- i
	}
	close(next)
	wg.Wait()

	for _, r := range results {
		rows = append(rows, r)
	}
	spend, errorsCount, emptyCount := aggregate(spend, results)
	after, err := treeHash(fixture)
	if err != nil {
		return err
	}
	rows = append(rows, completeRow{
		Kind: "complete", Run: run, Finished: time.Now().UTC().Format(time.RFC3339),
		ProbeRows: len(results), Errors: errorsCount, Empty: emptyCount,
		FixtureSHA256After: after, Spend: spend,
	})
	fmt.Fprintf(os.Stderr, "готово: клеток %d, ошибок %d, пустых %d, %s\n", len(results), errorsCount, emptyCount, spend)
	return writeRows(out, rows, nil)
}

// aggregate adds the probe cells to the run's spend and counts the cells that ended in
// an error and in an empty answer.
func aggregate(spend agent.Totals, results []probeRow) (agent.Totals, int, int) {
	var errorsCount, emptyCount int
	for _, r := range results {
		if r.Error != "" {
			errorsCount++
		}
		if r.Outcome == outcomeEmpty {
			emptyCount++
		}
		spend.Calls += r.Attempts + r.PlanCalls
		for _, u := range []usageRow{r.Usage, r.PlanUsage} {
			spend.PromptTokens += u.Prompt
			spend.CompletionTokens += u.Completion
			spend.CachedTokens += u.Cached
			spend.MissedTokens += u.Missed
			spend.ReasoningTokens += u.Reasoning
			spend.Cost += u.Cost
			if (u.Prompt > 0 || u.Completion > 0) && !u.Priced {
				spend.Unpriced++
			}
		}
	}
	return spend, errorsCount, emptyCount
}

// buildFixture writes every profile through the agent's own API, never through the
// model: the fixture is identical in every arm by construction.
func buildFixture(dir string) error {
	for _, f := range profileFixtures {
		a, err := agent.New(&noCaller{}, agent.Config{
			Profile: &agent.ProfileConfig{Dir: dir, User: measureUser, Name: f.Name},
		})
		if err != nil {
			return err
		}
		for _, group := range []struct {
			block   agent.ProfileBlock
			entries [][2]string
		}{{agent.BlockStyle, f.Style}, {agent.BlockConstraints, f.Constraints}, {agent.BlockContext, f.Context}} {
			for _, e := range group.entries {
				if err := a.SetPreference(group.block, e[0], e[1]); err != nil {
					return fmt.Errorf("профиль %s, %s.%s: %w", f.Name, group.block, e[0], err)
				}
			}
		}
		if err := a.SetPipeline(f.Pipeline); err != nil {
			return fmt.Errorf("профиль %s: %w", f.Name, err)
		}
	}
	return nil
}

// noCaller refuses every call: building the fixture must not talk to the provider, and
// a silent call here would be a billed surprise inside a function that looks local.
type noCaller struct{}

func (noCaller) AskWith(context.Context, []llm.Message, llm.Options) (llm.Answer, error) {
	return llm.Answer{}, errors.New("фикстура не должна вызывать модель")
}

// runShowcase is E0: slide 16 made literal. One question, every profile that differs by
// preferences, answers kept whole for the report and the page.
func runShowcase(ctx context.Context, client agent.Caller, model, fixture, run string) ([]showcaseRow, agent.Totals, error) {
	var rows []showcaseRow
	var spend agent.Totals
	for _, a := range behaviourArms {
		rec := &recorder{client: client}
		cfg := agent.Config{
			Name: "day-12", Model: model, Thinking: "disabled", MaxTokens: answerTokens,
			SystemPrompt: "Run " + run + ". " + behaviourSystem,
			Profile:      &agent.ProfileConfig{Dir: fixture, User: measureUser, Name: a.Profile, Inject: a.Inject},
		}
		ag, err := agent.New(rec, cfg)
		if err != nil {
			return rows, spend, err
		}
		row := showcaseRow{Kind: "showcase", Run: run, Profile: a.Name, Question: questionExplain, Sent: map[string]string{}}
		reply, err := ag.Ask(ctx, questionExplain)
		row.Usage.add(reply.Usage)
		spend = addTotals(spend, totalsOf(reply.Usage))
		switch {
		case errors.Is(err, llm.ErrEmptyContent):
			row.Outcome = outcomeEmpty
		case err != nil:
			row.Outcome, row.Error = outcomeTransport, err.Error()
		default:
			row.Outcome = outcomeOK
			row.Answer = reply.Text
			row.Scores = scoreAll(reply.Text)
		}
		row.Sent["profile"] = ag.ProfileState().Name
		row.Sent["wire"] = profileBlockOf(rec.lastWire())
		rows = append(rows, row)
		if row.Outcome == outcomeTransport {
			return rows, spend, fmt.Errorf("профиль %s: %v", a.Name, err)
		}
	}
	return rows, spend, nil
}

func runCell(ctx context.Context, client agent.Caller, run, fixture, model string, order int, c cell) probeRow {
	row := probeRow{
		Kind: "probe", Run: run, Order: order, Family: c.Family, Arm: c.Arm.Name, Profile: c.Arm.Profile,
		Probe: c.Probe.Name, Repeat: c.Repeat, Question: c.Probe.Question, RequestedModel: model,
		Inject: []string{},
	}
	for _, b := range c.Arm.Inject {
		row.Inject = append(row.Inject, string(b))
	}
	if f, ok := fixtureByName(c.Arm.Profile); ok {
		row.Pipeline = string(f.Pipeline)
	}

	rec := &recorder{client: client}
	cfg := agent.Config{
		Name: "day-12", Model: model, Thinking: "disabled", MaxTokens: answerTokens,
		SystemPrompt: "Run " + run + ". " + behaviourSystem,
		Profile:      &agent.ProfileConfig{Dir: fixture, User: measureUser, Name: c.Arm.Profile, Inject: c.Arm.Inject},
	}

	var reply agent.Reply
	var err error
	for attempt := 1; attempt <= 2; attempt++ {
		row.Attempts = attempt
		var a *agent.Agent
		a, err = agent.New(rec, cfg)
		if err != nil {
			break
		}
		started := time.Now()
		reply, err = a.Ask(ctx, c.Probe.Question)
		row.At = started.UTC().Format(time.RFC3339Nano)
		row.Peak = llm.IsPeak(started)
		row.ElapsedMs = time.Since(started).Milliseconds()
		// A failed attempt can still be billed: its usage joins the cell's, so the run's
		// spend is not understated by the retry.
		row.Usage.add(reply.Usage)
		if reply.PlanAttempted {
			row.PlanCalls++
			row.PlanUsage.add(reply.PlanUsage)
		}
		// An empty answer is the model's outcome and is never retried. Anything else is
		// a failed call, and on a day the provider was dropping content that difference
		// is the whole point of recording it.
		if err == nil || errors.Is(err, llm.ErrEmptyContent) {
			break
		}
		if attempt < 2 {
			row.RetryErrors = append(row.RetryErrors, err.Error())
		}
	}
	row.ServedModel = reply.Model
	row.Memory = reply.Memory
	row.EstimateTotal = reply.Estimated.Total
	row.ProfileTokens = reply.Estimated.Profile
	row.Truncated = reply.Truncated
	row.Plan = reply.Plan
	row.ProfileSent = profileBlockOf(rec.lastWire())
	if violations := sentViolations(rec.lastWire(), expectedSent(c.Arm)); len(violations) > 0 {
		row.SentViolations = violations
	}

	switch {
	case errors.Is(err, llm.ErrEmptyContent):
		row.Outcome = outcomeEmpty
		return row
	case err != nil:
		row.Outcome, row.Error = outcomeTransport, err.Error()
		return row
	}
	row.Outcome = outcomeOK
	row.Answer = reply.Text
	row.Scores = map[string]bool{}
	for _, name := range c.Probe.Criteria {
		crit, ok := criterionByName(name)
		if !ok {
			row.Error = "неизвестный критерий " + name
			return row
		}
		row.Scores[name] = crit.Test(reply.Text)
	}
	return row
}

func scoreAll(answer string) map[string]bool {
	out := map[string]bool{}
	for _, c := range criteria {
		out[c.Name] = c.Test(answer)
	}
	return out
}

// profileBlockOf extracts the [PROFILE] block from the recorded request, so a row
// carries the evidence of what personalization actually travelled.
func profileBlockOf(wire string) string {
	start := strings.Index(wire, "[PROFILE]")
	if start < 0 {
		return ""
	}
	rest := wire[start:]
	if end := strings.Index(rest, "\n\n"); end > 0 {
		return rest[:end]
	}
	return rest
}

func totalsOf(u agent.Usage) agent.Totals {
	t := agent.Totals{Calls: 1, PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
		CachedTokens: u.CachedTokens, MissedTokens: u.MissedTokens, ReasoningTokens: u.ReasoningTokens, Cost: u.Cost}
	if (u.PromptTokens > 0 || u.CompletionTokens > 0) && !u.Priced {
		t.Unpriced = 1
	}
	return t
}

func addTotals(a, b agent.Totals) agent.Totals {
	a.Calls += b.Calls
	a.Failed += b.Failed
	a.PromptTokens += b.PromptTokens
	a.CompletionTokens += b.CompletionTokens
	a.ReasoningTokens += b.ReasoningTokens
	a.CachedTokens += b.CachedTokens
	a.MissedTokens += b.MissedTokens
	a.Cost += b.Cost
	a.Unpriced += b.Unpriced
	return a
}

func treeHash(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The timestamps inside a profile file change on every write, so the fixture
		// hash covers the preferences and not the minute they were saved.
		var normalized map[string]any
		if json.Unmarshal(raw, &normalized) == nil {
			delete(normalized, "updated")
			if raw2, err := json.Marshal(normalized); err == nil {
				raw = raw2
			}
		}
		h.Write([]byte(rel))
		h.Write(raw)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func writeRows(path string, rows []any, runErr error) error {
	f, err := os.Create(path)
	if err != nil {
		return errors.Join(runErr, err)
	}
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			f.Close()
			return errors.Join(runErr, err)
		}
	}
	if err := f.Close(); err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
}

func readRows(path string) ([]map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ошибка:", err)
	os.Exit(1)
}
