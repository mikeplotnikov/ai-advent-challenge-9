// Day 11 measures the memory layers: what lands in each layer, and how each layer
// changes the answers. It writes raw rows to JSONL; RESULTS.md is rendered from that
// file by -report, so no measured number is typed into documentation by hand.
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

// aggregate adds the probe cells to the run's spend and counts the cells that ended
// in an error. Calls counts attempts, billed or not.
func aggregate(spend agent.Totals, results []probeRow) (agent.Totals, int) {
	errorsCount := 0
	for _, r := range results {
		if r.Error != "" {
			errorsCount++
		}
		spend.Calls += r.Attempts
		spend.PromptTokens += r.Usage.Prompt
		spend.CompletionTokens += r.Usage.Completion
		spend.CachedTokens += r.Usage.Cached
		spend.MissedTokens += r.Usage.Missed
		spend.ReasoningTokens += r.Usage.Reasoning
		spend.Cost += r.Usage.Cost
		if (r.Usage.Prompt > 0 || r.Usage.Completion > 0) && !r.Usage.Priced {
			spend.Unpriced++
		}
	}
	return spend, errorsCount
}

type planRow struct {
	Kind             string   `json:"kind"`
	Run              string   `json:"run"`
	Started          string   `json:"started"`
	Model            string   `json:"model"`
	Commit           string   `json:"commit"`
	Seed             int64    `json:"seed"`
	RecallRepeats    int      `json:"recallRepeats"`
	BehaviourRepeats int      `json:"behaviourRepeats"`
	RecallArms       []arm    `json:"recallArms"`
	BehaviourArms    []arm    `json:"behaviourArms"`
	RecallProbes     []probe  `json:"recallProbes"`
	BehaviourProbes  []probe  `json:"behaviourProbes"`
	Cells            int      `json:"cells"`
	FixtureSHA256    string   `json:"fixtureSha256"`
	Pilot            bool     `json:"pilot"`
	Notes            []string `json:"notes"`
}

type lifecycleRow struct {
	Kind       string              `json:"kind"`
	Run        string              `json:"run"`
	Checkpoint string              `json:"checkpoint"`
	Files      map[string]string   `json:"files"`
	Found      map[string][]string `json:"found"`
	Expected   map[string][]string `json:"expected"`
	OK         bool                `json:"ok"`
	Usage      *usageRow           `json:"usage,omitempty"`
}

type probeRow struct {
	Kind           string           `json:"kind"`
	Run            string           `json:"run"`
	Order          int              `json:"order"`
	Family         string           `json:"family"`
	Arm            string           `json:"arm"`
	Inject         []string         `json:"inject"`
	Task           string           `json:"task,omitempty"`
	Probe          string           `json:"probe"`
	Repeat         int              `json:"repeat"`
	Question       string           `json:"question"`
	Answer         string           `json:"answer"`
	Want           string           `json:"want,omitempty"`
	Forbidden      []string         `json:"forbidden,omitempty"`
	Value          *string          `json:"value,omitempty"`
	Verdict        string           `json:"verdict,omitempty"`
	Pass           bool             `json:"pass"`
	Truncated      bool             `json:"truncated"`
	SentViolations []string         `json:"sentViolations"`
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
	FixtureSHA256After string       `json:"fixtureSha256After"`
	Spend              agent.Totals `json:"spend"`
}

func main() {
	out := flag.String("out", "", "куда записать JSONL прогона")
	report := flag.String("report", "", "построить RESULTS.md из этого JSONL и напечатать в stdout")
	model := flag.String("model", "deepseek-flash", "модель; задаётся явно, в строках пишется и ответившая модель")
	recallRepeats := flag.Int("recall-repeats", 20, "повторов на клетку проб вспоминания")
	behaviourRepeats := flag.Int("behaviour-repeats", 30, "повторов на клетку поведенческих проб")
	workers := flag.Int("workers", 4, "параллельных вызовов")
	seed := flag.Int64("seed", 0, "seed перемешивания порядка; 0 — от времени, записывается в план")
	pilot := flag.Bool("pilot", false, "пилот: одна клетка на пробу вспоминания в руке full, без поведенческих и E0")
	dump := flag.Bool("dump", false, "выгрузить правила сборки запроса со слоями для сверки витрины и выйти")
	flag.Parse()

	if *dump {
		if err := agent.WriteMemoryDefinitions(os.Stdout, webSystem); err != nil {
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
		fail(errors.New("-out обязателен, например: -out day-11/layers.jsonl"))
	}
	if *recallRepeats < 1 || *behaviourRepeats < 1 || *workers < 1 {
		fail(errors.New("повторы и число потоков должны быть положительными"))
	}
	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	if err := runAll(context.Background(), *out, *model, *recallRepeats, *behaviourRepeats, *workers, *seed, *pilot); err != nil {
		fail(err)
	}
}

// recorder wraps the real client and keeps the bytes of the last request: the
// evidence of which layers travelled.
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

// fixedStore replays one short-term snapshot and never writes: every cell starts
// from the same conversation, and no answer of one cell reaches another.
type fixedStore struct{ snap agent.Snapshot }

func (s fixedStore) Load() (agent.Snapshot, error) { return s.snap, nil }
func (fixedStore) Save(agent.Snapshot) error       { return nil }
func (fixedStore) Clear() error                    { return nil }

func runAll(ctx context.Context, out, model string, recallRepeats, behaviourRepeats, workers int, seed int64, pilot bool) error {
	llm.LoadDotEnv(".env")
	client, err := llm.New()
	if err != nil {
		return err
	}
	client.Model = model
	run := fmt.Sprintf("day11-%d", time.Now().UnixNano())

	fixture, err := os.MkdirTemp("", "day11-fixture-")
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

	cells := planCells(recallRepeats, behaviourRepeats, pilot)
	rand.New(rand.NewSource(seed)).Shuffle(len(cells), func(i, j int) { cells[i], cells[j] = cells[j], cells[i] })
	plan := planRow{
		Kind: "plan", Run: run, Started: time.Now().UTC().Format(time.RFC3339), Model: model, Commit: gitCommit(),
		Seed: seed, RecallRepeats: recallRepeats, BehaviourRepeats: behaviourRepeats,
		RecallArms: recallArms, BehaviourArms: behaviourArms, RecallProbes: recallProbes, BehaviourProbes: behaviourProbes,
		Cells: len(cells), FixtureSHA256: fixtureHash, Pilot: pilot,
		Notes: []string{
			"порядок клеток перемешан по seed",
			"temperature не отправляется, thinking выключен",
			"метка прогона стоит первой в system: кэш прогона холодный на старте",
			"пустой ответ модели — вердикт empty, без повтора; иная ошибка вызова повторяется один раз, причина и расход первой попытки пишутся в строку",
		},
	}
	rows := []any{plan}

	var spend agent.Totals
	if !pilot {
		lifecycle, lifeSpend, err := runLifecycle(ctx, client, model, run)
		for _, r := range lifecycle {
			rows = append(rows, r)
		}
		spend = addTotals(spend, lifeSpend)
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
	spend, errorsCount := aggregate(spend, results)
	after, err := treeHash(fixture)
	if err != nil {
		return err
	}
	rows = append(rows, completeRow{
		Kind: "complete", Run: run, Finished: time.Now().UTC().Format(time.RFC3339),
		ProbeRows: len(results), Errors: errorsCount, FixtureSHA256After: after, Spend: spend,
	})
	fmt.Fprintf(os.Stderr, "готово: клеток %d, ошибок %d, %s\n", len(results), errorsCount, spend)
	return writeRows(out, rows, nil)
}

type cell struct {
	Family string
	Arm    arm
	Probe  probe
	Repeat int
}

func planCells(recallRepeats, behaviourRepeats int, pilot bool) []cell {
	var cells []cell
	if pilot {
		for _, p := range recallProbes {
			cells = append(cells, cell{familyRecall, recallArms[0], p, 1})
		}
		return cells
	}
	for _, a := range recallArms {
		for _, p := range recallProbes {
			for r := 1; r <= recallRepeats; r++ {
				cells = append(cells, cell{familyRecall, a, p, r})
			}
		}
	}
	for _, a := range behaviourArms {
		for _, p := range behaviourProbes {
			for r := 1; r <= behaviourRepeats; r++ {
				cells = append(cells, cell{familyBehaviour, a, p, r})
			}
		}
	}
	return cells
}

// buildFixture writes both users' layers through the agent's own API, and never
// through the model: the fixture is identical in every arm by construction.
func buildFixture(dir string) error {
	recall, err := agent.New(&recorder{}, agent.Config{Memory: &agent.MemoryConfig{Dir: dir, User: recallUser, Session: fixtureSession}})
	if err != nil {
		return err
	}
	steps := []func() error{
		func() error { return recall.StartTask(taskOther) },
		func() error { return recall.Remember(agent.TargetTask, "export_code", markerOldTask) },
		func() error { return recall.StartTask(taskActive) },
		func() error { return recall.Remember(agent.TargetTask, "export_code", markerTask) },
		func() error { return recall.Remember(agent.TargetDecision, "storage", decisionValue) },
		func() error { return recall.Remember(agent.TargetKnowledge, "staging_host", knowledgeValue) },
	}
	behaviour, err := agent.New(&recorder{}, agent.Config{Memory: &agent.MemoryConfig{Dir: dir, User: behaviourUser, Session: fixtureSession}})
	if err != nil {
		return err
	}
	steps = append(steps,
		func() error { return behaviour.Remember(agent.TargetProfile, "answer_format", styleProfile) },
		func() error { return behaviour.Remember(agent.TargetProfile, "stack", stackProfile) },
	)
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func shortSnapshot(system string) agent.Snapshot {
	return agent.Snapshot{
		Version: agent.SnapshotVersion, Model: "", System: system, Turns: 1,
		Messages: []agent.Message{{Role: agent.RoleUser, Content: shortTurnUser}, {Role: agent.RoleAssistant, Content: shortTurnAnswer}},
	}
}

func runCell(ctx context.Context, client agent.Caller, run, fixture, model string, order int, c cell) probeRow {
	row := probeRow{
		Kind: "probe", Run: run, Order: order, Family: c.Family, Arm: c.Arm.Name, Task: c.Arm.Task,
		Probe: c.Probe.Name, Repeat: c.Repeat, Question: c.Probe.Question, RequestedModel: model,
		SentViolations: []string{},
	}
	for _, l := range c.Arm.Inject {
		row.Inject = append(row.Inject, string(l))
	}
	if row.Inject == nil {
		row.Inject = []string{}
	}
	rec := &recorder{client: client}
	cfg := agent.Config{Name: "day-11", Model: model, Thinking: "disabled"}
	user := behaviourUser
	if c.Family == familyRecall {
		user = recallUser
		cfg.SystemPrompt = "Run " + run + ". " + recallSystem
		cfg.ResponseFormat = "json_object"
		cfg.MaxTokens = recallMaxTokens
		cfg.Store = fixedStore{shortSnapshot(cfg.SystemPrompt)}
	} else {
		cfg.SystemPrompt = "Run " + run + ". " + behaviourSystem
		cfg.MaxTokens = behaviourTokens
	}
	cfg.Memory = &agent.MemoryConfig{Dir: fixture, User: user, Session: fixtureSession, Task: c.Arm.Task, Inject: c.Arm.Inject}

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
		// A failed attempt can still be billed: its usage joins the cell's, so the
		// run's spend is not understated by the retry.
		row.Usage.add(reply.Usage)
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
	row.Truncated = reply.Truncated
	if violations := sentViolations(rec.wire, expectedSent(c.Family, c.Arm)); len(violations) > 0 {
		row.SentViolations = violations
	}
	if errors.Is(err, llm.ErrEmptyContent) {
		row.Verdict, row.Pass = verdictEmpty, false
		if c.Family == familyRecall {
			row.Want, row.Forbidden = recallExpectation(c.Arm, c.Probe)
		}
		return row
	}
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.Answer = reply.Text
	if c.Family == familyRecall {
		want, forbidden := recallExpectation(c.Arm, c.Probe)
		score := scoreRecall(reply.Text, want, forbidden)
		row.Want, row.Forbidden, row.Value, row.Verdict, row.Pass = want, forbidden, score.Value, score.Verdict, score.Pass
	} else {
		row.Pass = scoreBehaviour(c.Probe, reply.Text)
	}
	return row
}

// runLifecycle is E0: real turns and real writes through one user's layers, with a
// marker scan of the files after every step that could move data between layers.
func runLifecycle(ctx context.Context, client agent.Caller, model, run string) ([]lifecycleRow, agent.Totals, error) {
	dir, err := os.MkdirTemp("", "day11-lifecycle-")
	if err != nil {
		return nil, agent.Totals{}, err
	}
	defer os.RemoveAll(dir)
	const user = "e0"
	system := "Run " + run + " E0. You are a developer's assistant. Answer in Russian, in one short sentence."
	build := func(session, task string) (*agent.Agent, error) {
		return agent.New(client, agent.Config{
			Name: "day-11-e0", Model: model, SystemPrompt: system, Thinking: "disabled", MaxTokens: 60,
			Store:  agent.NewFileStore(agent.MemorySessionPath(dir, user, session)),
			Memory: &agent.MemoryConfig{Dir: dir, User: user, Session: session, Task: task},
		})
	}
	var rows []lifecycleRow
	var spend agent.Totals
	markers := []string{markerShort, markerTask, markerOldTask, markerDecision, markerKnowledge}
	scan := func(checkpoint string, expected map[string][]string) {
		row := lifecycleRow{Kind: "lifecycle", Run: run, Checkpoint: checkpoint, Files: map[string]string{}, Found: map[string][]string{}, Expected: expected, OK: true}
		userDir := agent.MemoryUserDir(dir, user)
		groups := map[string]string{"short": filepath.Join(userDir, "sessions"), "working": filepath.Join(userDir, "tasks"), "long": filepath.Join(userDir, "long-term.json")}
		for _, layer := range []string{"short", "working", "long"} {
			content, hash := readTree(groups[layer])
			row.Files[layer] = hash
			for _, marker := range markers {
				if strings.Contains(content, marker) {
					row.Found[marker] = append(row.Found[marker], layer)
				}
			}
		}
		for _, marker := range markers {
			if row.Found[marker] == nil {
				row.Found[marker] = []string{}
			}
			if strings.Join(row.Found[marker], ",") != strings.Join(expected[marker], ",") {
				row.OK = false
			}
		}
		rows = append(rows, row)
	}
	layersOf := func(short, working, long []string) func(string) []string {
		table := map[string][]string{}
		for _, m := range short {
			table[m] = append(table[m], "short")
		}
		for _, m := range working {
			table[m] = append(table[m], "working")
		}
		for _, m := range long {
			table[m] = append(table[m], "long")
		}
		return func(m string) []string {
			if table[m] == nil {
				return []string{}
			}
			return table[m]
		}
	}
	expect := func(f func(string) []string) map[string][]string {
		out := map[string][]string{}
		for _, m := range markers {
			out[m] = f(m)
		}
		return out
	}

	a, err := build("s1", "")
	if err != nil {
		return rows, spend, err
	}
	for _, q := range []string{shortTurnUser, "Спасибо. Подтверди одним словом, что понял."} {
		if _, err := a.Ask(ctx, q); err != nil {
			return rows, addTotals(spend, a.Totals()), err
		}
	}
	for _, step := range []func() error{
		func() error { return a.Remember(agent.TargetDecision, "storage", decisionValue) },
		func() error { return a.Remember(agent.TargetKnowledge, "staging_host", knowledgeValue) },
		func() error { return a.StartTask(taskOther) },
		func() error { return a.Remember(agent.TargetTask, "export_code", markerOldTask) },
		func() error { return a.FinishTask() },
		func() error { return a.StartTask(taskActive) },
		func() error { return a.Remember(agent.TargetTask, "export_code", markerTask) },
	} {
		if err := step(); err != nil {
			return rows, addTotals(spend, a.Totals()), err
		}
	}
	spend = addTotals(spend, a.Totals())
	long := []string{markerDecision, markerKnowledge}
	scan("after-write", expect(layersOf([]string{markerShort}, []string{markerTask}, long)))
	if err := a.Reset(); err != nil {
		return rows, spend, err
	}
	scan("after-reset", expect(layersOf(nil, []string{markerTask}, long)))
	if _, err := build("s1", taskActive); err != nil {
		return rows, spend, err
	}
	scan("after-restart", expect(layersOf(nil, []string{markerTask}, long)))
	b, err := build("s2", taskActive)
	if err != nil {
		return rows, spend, err
	}
	scan("after-new-session", expect(layersOf(nil, []string{markerTask}, long)))
	if err := b.FinishTask(); err != nil {
		return rows, spend, err
	}
	scan("after-task-done", expect(layersOf(nil, nil, long)))
	return rows, spend, nil
}

// readTree concatenates a file or every file under a directory, with a hash of the
// same bytes; a missing path is empty and hashes as "absent".
func readTree(path string) (string, string) {
	var paths []string
	filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	if len(paths) == 0 {
		return "", "absent"
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		b.WriteString(filepath.Base(p))
		b.Write(raw)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return b.String(), hex.EncodeToString(sum[:])
}

func treeHash(dir string) (string, error) {
	_, hash := readTree(dir)
	if hash == "absent" {
		return "", fmt.Errorf("фикстура %s пуста", dir)
	}
	return hash, nil
}

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
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

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// writeRows writes JSONL atomically. A failed run still writes what it has to a
// separate path, and returns the run's error so the partial file is never mistaken
// for a result.
func writeRows(path string, rows []any, runErr error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errors.Join(runErr, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".day11-*.tmp")
	if err != nil {
		return errors.Join(runErr, err)
	}
	enc := json.NewEncoder(tmp)
	enc.SetEscapeHTML(false)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return errors.Join(runErr, err)
		}
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return errors.Join(runErr, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return errors.Join(runErr, err)
	}
	return runErr
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
