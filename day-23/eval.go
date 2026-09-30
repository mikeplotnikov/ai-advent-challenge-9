package main

import (
	"context"
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

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

// runEvaluation measures the four modes: retrieval on all 43 questions, answers on
// day 22's ten. The first call that fails after its retry aborts the whole run.
func runEvaluation(ctx context.Context, client *llm.Client, searcher Searcher, manifestSHA string, stdout io.Writer) (Run, error) {
	questions, answered, day21SHA, day22SHA, err := loadQuestions(rag.ProjectPath(defaultDay21), rag.ProjectPath(defaultDay22))
	if err != nil {
		return Run{}, err
	}
	indexSHA, err := fileSHA256(rag.ProjectPath(defaultIndex))
	if err != nil {
		return Run{}, fmt.Errorf("sha256 индекса: %w", err)
	}
	p := evalParams
	run := Run{Meta: RunHeader{
		Commit: gitCommit(), StartedAt: time.Now().UTC(), RunPath: defaultRun, RequestedModel: modelName,
		Params: p, Day21SHA256: day21SHA, Day22SHA256: day22SHA, IndexSHA256: indexSHA,
		ManifestSHA256: manifestSHA, PromptsSHA256: promptsSHA256(),
	}}
	for qi, question := range questions {
		fmt.Fprintf(stdout, "%d/%d %s\n", qi+1, len(questions), question.ID)
		item, err := measureQuestion(ctx, client, searcher, question, answered[question.ID], p)
		if err != nil {
			return Run{}, fmt.Errorf("%s: %w", question.ID, err)
		}
		run.Questions = append(run.Questions, item)
	}
	finishHeader(&run)
	return run, nil
}

func measureQuestion(ctx context.Context, client *llm.Client, searcher Searcher, question Question, answer bool, p Params) (QuestionRun, error) {
	item := QuestionRun{Question: question, Answered: answer}
	rewritten, rewriteCall, err := rewriteQuery(ctx, client, question.Question)
	item.Rewrite = rewriteCall
	if err != nil {
		return item, err
	}
	item.Rewritten = rewritten
	pools := map[string][]Candidate{}
	for _, query := range []string{question.Question, rewritten} {
		if _, done := pools[query]; done {
			continue
		}
		pool, err := searcher.Search(ctx, query, poolSize(p))
		if err != nil {
			return item, err
		}
		markHits(pool, question)
		pools[query] = pool
	}
	for _, mode := range allModes {
		query := question.Question
		if usesRewrite(mode) {
			query = rewritten
		}
		modeRun, err := selectContext(ctx, client, mode, question.Question, query, pools[query], p)
		if err != nil {
			return item, fmt.Errorf("%s: %w", mode, err)
		}
		if answer {
			if err := answerMode(ctx, client, question, &modeRun); err != nil {
				return item, fmt.Errorf("%s: %w", mode, err)
			}
		}
		item.Modes = append(item.Modes, modeRun)
	}
	return item, nil
}

// finishHeader totals what the run cost and which models answered.
func finishHeader(run *Run) {
	models := map[string]bool{}
	run.Meta.TotalCostKnown = true
	run.Meta.TotalCostUSD, run.Meta.ModelCallsCount = 0, 0
	run.Meta.RetrievalN, run.Meta.AnsweredN = 0, 0
	for _, question := range run.Questions {
		if hasEvidence(question.Question) {
			run.Meta.RetrievalN++
		}
		if question.Answered {
			run.Meta.AnsweredN++
		}
		for _, call := range questionCalls(question) {
			run.Meta.ModelCallsCount += len(call.Attempts)
			run.Meta.TotalCostUSD += call.CostUSD
			run.Meta.TotalCostKnown = run.Meta.TotalCostKnown && call.CostKnown
			if call.Model != "" {
				models[call.Model] = true
			}
		}
	}
	run.Meta.ResponseModels = nil
	for model := range models {
		run.Meta.ResponseModels = append(run.Meta.ResponseModels, model)
	}
	sort.Strings(run.Meta.ResponseModels)
}

// questionCalls lists the paid calls of a question; a filtered-out answer made none.
func questionCalls(question QuestionRun) []Call {
	calls := []Call{question.Rewrite}
	for _, mode := range question.Modes {
		if mode.Rerank != nil {
			calls = append(calls, *mode.Rerank)
		}
		if mode.Answer != nil && !mode.Answer.FilteredOut {
			calls = append(calls, *mode.Answer)
		}
	}
	return calls
}

func readRun(path string) (Run, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Run{}, fmt.Errorf("прочитать run.json: %w", err)
	}
	var run Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return Run{}, fmt.Errorf("разобрать run.json: %w", err)
	}
	return run, nil
}

func encodeJSON(value any) ([]byte, error) {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

func writeBytesAtomic(path string, data []byte) error {
	temporary, err := prepareAtomicFile(path, data)
	if err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func prepareAtomicFile(path string, data []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	name := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(name)
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	keep = true
	return name, nil
}

func fileSHA256(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func gitCommit() string {
	output, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}
