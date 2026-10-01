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
	"reflect"
	"sort"
	"strings"
	"time"
)

const questionsSHA = "270e1a7e2d68d21668828f5945a09a966acd57484b70a696299424a0aaf0c78f"

func digest(raw []byte) string               { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func fileSHA256(path string) (string, error) { raw, e := os.ReadFile(path); return digest(raw), e }
func encodeJSON(v any) ([]byte, error) {
	raw, e := json.MarshalIndent(v, "", "  ")
	return append(raw, '\n'), e
}
func loadQuestions() ([]Question, error) {
	raw, e := os.ReadFile(rag.ProjectPath(defaultQuestions))
	if e != nil {
		return nil, e
	}
	if digest(raw) != questionsSHA {
		return nil, fmt.Errorf("day22 questions changed")
	}
	var qs []Question
	e = json.Unmarshal(raw, &qs)
	return qs, e
}
func inputHashes() (map[string]string, error) {
	paths := map[string]string{"questions": defaultQuestions, "index": defaultIndex, "manifest": defaultCorpus + "/MANIFEST.json"}
	m := map[string]string{}
	for k, p := range paths {
		h, e := fileSHA256(rag.ProjectPath(p))
		if e != nil {
			return nil, e
		}
		m[k] = h
	}
	b, _ := encodeJSON(promptTexts())
	m["prompts"] = digest(b)
	files, e := filepath.Glob(rag.ProjectPath("day-24") + "/*.go")
	if e != nil {
		return nil, e
	}
	sort.Strings(files)
	var src strings.Builder
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		h, e := fileSHA256(p)
		if e != nil {
			return nil, e
		}
		fmt.Fprintf(&src, "%s:%s\n", filepath.Base(p), h)
	}
	m["source"] = digest([]byte(src.String()))
	manifest, _, e := rag.ReadManifest(rag.ProjectPath(defaultCorpus))
	if e != nil {
		return nil, e
	}
	for _, f := range manifest.Files {
		h, e := fileSHA256(rag.ProjectPath(defaultCorpus + "/" + f.Path))
		if e != nil {
			return nil, e
		}
		if h != f.SHA256 {
			return nil, fmt.Errorf("corpus file changed: %s", f.Path)
		}
	}
	return m, nil
}
func measureQuestion(ctx context.Context, client *llm.Client, searcher Searcher, q Question, p Params) (QuestionRun, error) {
	item := QuestionRun{Question: q, Candidates: []Candidate{}, Semantic: Semantic{q.ID, "pending", "Независимая смысловая оценка ещё не выполнена."}}
	rewrite, call, e := rewriteQuery(ctx, client, q.Question)
	item.Rewrite = call
	item.Rewritten = rewrite
	if e != nil {
		return item, e
	}
	pool, e := searcher.Search(ctx, rewrite, p.KBefore)
	if e != nil {
		return item, e
	}
	markHits(pool, q)
	selected, e := selectContext(ctx, client, q.Question, rewrite, pool, p)
	item.Candidates = selected.Candidates
	item.Rerank = selected.Rerank
	if e != nil {
		return item, e
	}
	e = answerQuestion(ctx, client, &item)
	return item, e
}
func questionCalls(q QuestionRun) []Call {
	out := []Call{q.Rewrite}
	if q.Rerank != nil {
		out = append(out, *q.Rerank)
	}
	if q.AnswerCall != nil {
		out = append(out, *q.AnswerCall)
	}
	return out
}
func totalRun(r *Run) {
	r.Meta.TotalCostKnown = true
	r.Meta.TotalCostUSD = 0
	r.Meta.ModelCalls = 0
	for _, q := range r.Questions {
		for _, c := range questionCalls(q) {
			r.Meta.TotalCostKnown = r.Meta.TotalCostKnown && c.CostKnown
			r.Meta.TotalCostUSD += c.CostUSD
			r.Meta.ModelCalls += len(c.Attempts)
		}
	}
}
func runEvaluation(ctx context.Context, client *llm.Client, s Searcher, w io.Writer) (Run, error) {
	qs, e := loadQuestions()
	if e != nil {
		return Run{}, e
	}
	hashes, e := inputHashes()
	if e != nil {
		return Run{}, e
	}
	head, e := exec.Command("git", "rev-parse", "HEAD").Output()
	if e != nil {
		return Run{}, e
	}
	commit := strings.TrimSpace(string(head))
	r := Run{Meta: Meta{Commit: commit, CorpusCommit: commit, CorpusBase: defaultCorpus, StartedAt: time.Now().UTC(), RequestedModel: modelName, Params: evalParams, Hashes: hashes}, Questions: []QuestionRun{}}
	for i, q := range qs {
		fmt.Fprintf(w, "%d/%d %s\n", i+1, len(qs), q.ID)
		item, e := measureQuestion(ctx, client, s, q, evalParams)
		if e != nil {
			return r, fmt.Errorf("%s: %w", q.ID, e)
		}
		r.Questions = append(r.Questions, item)
	}
	totalRun(&r)
	return r, nil
}
func writeBytesAtomic(path string, raw []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".day24-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(raw); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}
func saveRun(path string, r Run) error {
	raw, e := encodeJSON(r)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if e != nil {
		return e
	}
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return writeBytesAtomic(path+".sha256", []byte(digest(raw)+"\n"))
}
func validateRun(r Run) error {
	qs, e := loadQuestions()
	if e != nil {
		return e
	}
	hashes, e := inputHashes()
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(r.Meta.Hashes, hashes) {
		return fmt.Errorf("input/prompts/source hashes differ from saved run")
	}
	if r.Meta.Params != evalParams || r.Meta.RequestedModel != modelName || len(r.Questions) != len(qs) {
		return fmt.Errorf("run contract mismatch")
	}
	idx, e := rag.ReadIndex(rag.ProjectPath(defaultIndex))
	if e != nil {
		return e
	}
	byID := map[string]rag.Chunk{}
	for _, c := range idx.Chunks {
		byID[c.ChunkID] = c
	}
	for i, q := range r.Questions {
		if !reflect.DeepEqual(q.Question, qs[i]) {
			return fmt.Errorf("question %d changed", i)
		}
		for _, c := range q.Candidates {
			original, ok := byID[c.ChunkID]
			if !ok || c.Source != original.Source || c.Section != original.Section || c.Text != original.Text {
				return fmt.Errorf("%s: candidate changed", q.Question.ID)
			}
		}
		var a Answer
		reason := ""
		if len(q.Context()) == 0 {
			if q.AnswerCall != nil {
				return fmt.Errorf("empty context made answer call")
			}
			a = Answer{"Не знаю.", true, "Уточните термин или задачу, о которой спрашиваете.", []Source{}}
			reason = "empty_context"
		} else {
			if q.AnswerCall == nil || q.AnswerCall.Error != "" {
				return fmt.Errorf("missing/failed answer")
			}
			a, e = parseAnswer(q.AnswerCall.Response, q.Context())
			if e != nil {
				return e
			}
			if a.Unknown {
				reason = "model_unknown"
			}
		}
		if !reflect.DeepEqual(q.Answer, a) || !reflect.DeepEqual(q.Checks, checkAnswer(q.Question, a, reason)) {
			return fmt.Errorf("%s: answer/checks changed", q.Question.ID)
		}
		for _, c := range questionCalls(q) {
			if len(c.Attempts) < 1 || len(c.Attempts) > 2 || c.Error != "" {
				return fmt.Errorf("invalid call")
			}
			sum := Call{CostKnown: true}
			for _, at := range c.Attempts {
				cost, known := llm.CostAt(at.Model, at.Usage, at.StartedAt)
				if cost != at.CostUSD || known != at.CostKnown {
					return fmt.Errorf("attempt cost changed")
				}
				sum.CostUSD += cost
				sum.CostKnown = sum.CostKnown && known
				addUsage(&sum.Usage, at.Usage)
			}
			if sum.CostUSD != c.CostUSD || sum.CostKnown != c.CostKnown || sum.Usage != c.Usage || c.Response != c.Attempts[len(c.Attempts)-1].Response {
				return fmt.Errorf("call totals/raw changed")
			}
		}
	}
	copy := r
	totalRun(&copy)
	if copy.Meta.TotalCostUSD != r.Meta.TotalCostUSD || copy.Meta.TotalCostKnown != r.Meta.TotalCostKnown || copy.Meta.ModelCalls != r.Meta.ModelCalls {
		return fmt.Errorf("run totals changed")
	}
	return nil
}
