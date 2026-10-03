package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Scenario struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Messages    []string `json:"messages"`
	Goal        string   `json:"expected_goal"`
	Constraints []string `json:"expected_constraints"`
}
type ScenarioRun struct {
	ID      string          `json:"id"`
	Title   string          `json:"title"`
	Session Session         `json:"session"`
	Checks  map[string]bool `json:"checks"`
}
type ChatMeta struct {
	Model        string    `json:"model"`
	StartedAt    time.Time `json:"started_at"`
	CorpusCommit string    `json:"corpus_commit"`
	IndexSHA     string    `json:"index_sha256"`
	ScenariosSHA string    `json:"scenarios_sha256"`
	PromptsSHA   string    `json:"prompts_sha256"`
	TotalCost    float64   `json:"total_cost_usd"`
	CostKnown    bool      `json:"cost_known"`
	Calls        int       `json:"model_calls"`
}
type ChatRun struct {
	Assessment *Assessment    `json:"semantic_review,omitempty"`
	RunSHA     string         `json:"run_sha256,omitempty"`
	Meta       ChatMeta       `json:"meta"`
	Scenarios  []ScenarioRun  `json:"scenarios"`
	Results    map[string]int `json:"results"`
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func containsPhrase(values []string, phrase string) bool {
	for _, v := range values {
		if strings.Contains(v, phrase) {
			return true
		}
	}
	return false
}
func scenarioChecks(s Scenario, run Session) map[string]bool {
	final := run.Turns[len(run.Turns)-1].After
	constraints := true
	for _, v := range s.Constraints {
		if !containsPhrase(final.Constraints, v) {
			constraints = false
		}
	}
	return map[string]bool{"final_goal": strings.Contains(final.Goal, s.Goal), "final_constraints": constraints, "turn_count": len(run.Turns) == len(s.Messages)}
}
func checkCaptureClear(dir string) error {
	for _, p := range []string{"run.json", "failed-turn.json"} {
		if _, e := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(e) {
			return fmt.Errorf("%s существует или недоступен; сохраните прежний захват", p)
		}
	}
	matches, e := filepath.Glob(filepath.Join(dir, "eval-sessions", "*.json"))
	if e != nil {
		return e
	}
	if len(matches) > 0 {
		return fmt.Errorf("eval-sessions содержит частичный захват; сохраните его перед новым запуском")
	}
	return nil
}
func runEvaluation(c *llm.Client, search Searcher, dir string, timeout time.Duration, out io.Writer) error {
	if e := checkCaptureClear(dir); e != nil {
		return e
	}
	b, e := os.ReadFile(dir + "/scenarios.json")
	if e != nil {
		return e
	}
	var ss []Scenario
	if e = json.Unmarshal(b, &ss); e != nil {
		return e
	}
	if len(ss) != 2 {
		return fmt.Errorf("need two scenarios")
	}
	for _, s := range ss {
		if len(s.Messages) != 12 {
			return fmt.Errorf("need exactly 12 turns")
		}
	}
	idx, e := os.ReadFile(rag.ProjectPath(defaultIndex))
	if e != nil {
		return e
	}
	manifest, _, e := rag.ReadManifest(rag.ProjectPath(defaultCorpus))
	if e != nil {
		return e
	}
	prompts, _ := encodeJSON(map[string]string{"memory": MemoryPrompt, "answer": AnswerSystemPrompt, "rerank": RerankSystemPrompt})
	for _, f := range manifest.Files {
		raw, err := os.ReadFile(rag.ProjectPath(defaultCorpus + "/" + f.Path))
		if err != nil {
			return err
		}
		if digest(raw) != f.SHA256 {
			return fmt.Errorf("corpus hash mismatch: %s", f.Path)
		}
	}
	rev, e := exec.Command("git", "rev-parse", "HEAD").Output()
	if e != nil {
		return e
	}
	corpusRevision := strings.TrimSpace(string(rev))
	r := ChatRun{Meta: ChatMeta{Model: modelName, StartedAt: time.Now().UTC(), CorpusCommit: corpusRevision, IndexSHA: digest(idx), ScenariosSHA: digest(b), PromptsSHA: digest(prompts), CostKnown: true}, Scenarios: []ScenarioRun{}}
	for _, s := range ss {
		sr := ScenarioRun{ID: s.ID, Title: s.Title, Session: emptySession()}
		for _, user := range s.Messages {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			t, err := nextTurn(ctx, c, search, sr.Session, user)
			cancel()
			if err != nil {
				printCosts(out, t)
				_ = atomicJSON(filepath.Join(dir, "failed-turn.json"), t)
				return fmt.Errorf("%s turn %d: %w (partial sessions kept in eval-sessions)", s.ID, t.Number, err)
			}
			sr.Session.Turns = append(sr.Session.Turns, t)
			if e = saveSession(filepath.Join(dir, "eval-sessions", s.ID+".json"), sr.Session); e != nil {
				return e
			}
			printTurn(out, t)
		}
		sr.Checks = scenarioChecks(s, sr.Session)
		r.Scenarios = append(r.Scenarios, sr)
	}
	if e = atomicJSON(dir+"/run.json", r); e != nil {
		return e
	}
	return writeReport(dir)
}
func summarize(r *ChatRun) {
	r.Results = map[string]int{"turns": 0, "substantive": 0, "unknown": 0, "source_pass": 0, "quote_pass": 0, "memory_pass": 0, "searches": 0}
	r.Meta.Calls = 0
	r.Meta.TotalCost = 0
	r.Meta.CostKnown = true
	for _, s := range r.Scenarios {
		if s.Checks["final_goal"] && s.Checks["final_constraints"] {
			r.Results["memory_pass"]++
		}
		for _, t := range s.Session.Turns {
			r.Results["turns"]++
			if t.SearchPerformed {
				r.Results["searches"]++
			}
			if t.RAG.Answer.Unknown {
				r.Results["unknown"]++
			} else {
				r.Results["substantive"]++
				if t.RAG.Checks.Sources == "pass" {
					r.Results["source_pass"]++
				}
				if t.RAG.Checks.Quotes == "pass" {
					r.Results["quote_pass"]++
				}
			}
			for _, c := range turnCalls(t) {
				r.Meta.TotalCost += c.CostUSD
				r.Meta.Calls += len(c.Attempts)
				r.Meta.CostKnown = r.Meta.CostKnown && c.CostKnown
			}
		}
	}
}
func writeReport(dir string) error {
	b, e := os.ReadFile(dir + "/run.json")
	if e != nil {
		return e
	}
	var r ChatRun
	if e = json.Unmarshal(b, &r); e != nil {
		return e
	}
	// Revalidate captured sessions and citations, and derive checks from frozen input.
	sb, e := os.ReadFile(dir + "/scenarios.json")
	if e != nil {
		return e
	}
	if digest(sb) != r.Meta.ScenariosSHA {
		return fmt.Errorf("scenario hash mismatch")
	}
	var ss []Scenario
	if e = json.Unmarshal(sb, &ss); e != nil {
		return e
	}
	if len(ss) != 2 {
		return fmt.Errorf("need exactly two scenarios")
	}
	for _, s := range ss {
		if len(s.Messages) != 12 {
			return fmt.Errorf("need exactly 12 turns")
		}
	}
	if len(r.Scenarios) != len(ss) {
		return fmt.Errorf("scenario count mismatch")
	}
	idxRaw, e := os.ReadFile(rag.ProjectPath(defaultIndex))
	if e != nil {
		return e
	}
	if digest(idxRaw) != r.Meta.IndexSHA {
		return fmt.Errorf("index hash mismatch")
	}
	prompts, _ := encodeJSON(map[string]string{"memory": MemoryPrompt, "answer": AnswerSystemPrompt, "rerank": RerankSystemPrompt})
	if digest(prompts) != r.Meta.PromptsSHA {
		return fmt.Errorf("prompt hash mismatch")
	}
	var idx rag.Index
	if e = json.Unmarshal(idxRaw, &idx); e != nil {
		return e
	}
	chunks := map[string]rag.Chunk{}
	for _, c := range idx.Chunks {
		chunks[c.ChunkID] = c
	}
	for i := range r.Scenarios {
		sr := &r.Scenarios[i]
		if sr.ID != ss[i].ID || len(sr.Session.Turns) != len(ss[i].Messages) {
			return fmt.Errorf("scenario identity/length mismatch")
		}
		users := []string{}
		for j, t := range sr.Session.Turns {
			if t.Number != j+1 || t.User != ss[i].Messages[j] {
				return fmt.Errorf("captured message mismatch")
			}
			if j == 0 && !emptyState(t.Before) {
				return fmt.Errorf("first turn memory must be empty")
			}
			if !t.SearchPerformed {
				return fmt.Errorf("missing search trace")
			}
			if j > 0 {
				before, _ := encodeJSON(t.Before)
				after, _ := encodeJSON(sr.Session.Turns[j-1].After)
				if string(before) != string(after) {
					return fmt.Errorf("broken memory chain")
				}
			}
			users = append(users, t.User)
			raw, _ := encodeJSON(memoryReply{t.After, t.Query})
			if _, e = parseMemory(string(raw), users); e != nil {
				return e
			}
			for _, c := range t.RAG.Candidates {
				original, ok := chunks[c.ChunkID]
				if !ok || original.Source != c.Source || original.Section != c.Section || original.Text != c.Text {
					return fmt.Errorf("candidate differs from frozen index")
				}
			}
			raw, _ = encodeJSON(t.RAG.Answer)
			if _, e = parseAnswer(string(raw), t.RAG.Context()); e != nil {
				return e
			}
		}
		for j := range sr.Session.Turns {
			t := &sr.Session.Turns[j]
			reason := ""
			if t.RAG.Answer.Unknown {
				reason = t.RAG.Checks.RefusalReason
			}
			t.RAG.Checks = checkAnswer(t.RAG.Question, t.RAG.Answer, reason)
		}
		sr.Checks = scenarioChecks(ss[i], sr.Session)
	}
	summarize(&r)
	r.RunSHA = digest(b)
	r.Assessment, e = readAssessment(dir+"/semantic-review.json", r.RunSHA, r)
	if e != nil {
		return e
	}
	var table strings.Builder
	table.WriteString("| Сценарий | Запросов | Цель в конце | Ограничения в конце |\n|---|---:|---|---|\n")
	for _, s := range r.Scenarios {
		fmt.Fprintf(&table, "| %s | %d | %t | %t |\n", s.ID, len(s.Session.Turns), s.Checks["final_goal"], s.Checks["final_constraints"])
	}
	price := "неизвестна"
	if r.Meta.CostKnown {
		price = fmt.Sprintf("$%.8f", r.Meta.TotalCost)
	}
	report := fmt.Sprintf("# День 25 — живой прогон\n\n%s\nЗапросов и поисков: %d. Содержательных ответов: %d; отказов: %d. Источники (гейт допуска): %d/%d; дословные цитаты (гейт допуска): %d/%d. Попыток LLM: %d; цена: %s.\n\nДва диалога по 12 запросов пользователя, по 24 сообщения с ответами. R=1. Финальные проверки памяти — наличие ожидаемых дословных фраз, не доказательство смысловой правильности ответа. Цитаты проверены кодом; это не проверка всех утверждений ответа. Полная история сохранена; модель получает последние 6 обменов и отдельную память.\n\nrun SHA256: %s\n", table.String(), r.Results["turns"], r.Results["substantive"], r.Results["unknown"], r.Results["source_pass"], r.Results["substantive"], r.Results["quote_pass"], r.Results["substantive"], r.Meta.Calls, price, digest(b))
	if r.Assessment == nil {
		report += "\nНезависимая смысловая оценка пока не выполнена.\n"
	} else {
		counts := map[string]int{}
		for _, v := range r.Assessment.Turns {
			counts[v.Verdict]++
		}
		report += fmt.Sprintf("\nНезависимая Sonnet-оценка: supported %d, unsupported %d, unknown %d. Это экспертное суждение по цитатам, не гарантия.\n", counts["supported"], counts["unsupported"], counts["unknown"])
		for _, v := range r.Assessment.Scenarios {
			report += fmt.Sprintf("\n- %s: память %s; итоговая цель %s. %s\n", v.ID, v.Memory, v.Final, v.Reason)
		}
	}
	if e = atomicJSON(dir+"/showcase.json", r); e != nil {
		return e
	}
	return os.WriteFile(dir+"/RESULTS.md", []byte(report), 0644)
}
