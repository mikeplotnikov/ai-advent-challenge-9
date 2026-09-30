package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

// Searcher embeds a query with bge-m3 and ranks the day-21 structure index.
type Searcher interface {
	Search(ctx context.Context, query string, k int) ([]Candidate, error)
}

type indexSearcher struct {
	index       rag.Index
	embedder    rag.Embedder
	manifestSHA string
}

var openSearcherFn = openSearcher

func openSearcher(ollamaURL string) (Searcher, string, error) {
	manifestSHA, err := rag.ManifestSHA256(rag.ProjectPath(defaultCorpus))
	if err != nil {
		return nil, "", err
	}
	index, err := rag.ReadIndex(rag.ProjectPath(defaultIndex))
	if err != nil {
		return nil, "", err
	}
	if err := rag.ValidateIndex(index, strategyName, embeddingModel, manifestSHA, 0); err != nil {
		return nil, "", err
	}
	return &indexSearcher{index: index, embedder: &rag.OllamaClient{BaseURL: ollamaURL, Model: embeddingModel}, manifestSHA: manifestSHA}, manifestSHA, nil
}

func (s *indexSearcher) Search(ctx context.Context, query string, k int) ([]Candidate, error) {
	vector, _, _, err := s.embedder.Embed(ctx, "search-query", query)
	if err != nil {
		return nil, err
	}
	if len(vector) != s.index.Header.Dimension {
		return nil, fmt.Errorf("размерность ответа модели %d, индекс ожидает %d: повторите `go run ./day-21 -index`", len(vector), s.index.Header.Dimension)
	}
	results, err := rag.RankChunks(s.index, vector, k)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, len(results))
	for i, result := range results {
		out[i] = Candidate{Rank: result.Rank, Similarity: result.Similarity, ChunkID: result.Chunk.ChunkID,
			Source: result.Chunk.Source, Section: result.Chunk.Section, Tokens: result.Chunk.Tokens, Text: result.Chunk.Text}
	}
	return out, nil
}

// markHits fills the evidence and source marks for a known question.
func markHits(candidates []Candidate, question Question) {
	for i := range candidates {
		candidates[i].EvidenceHit = question.Evidence != "" && rag.ContainsEvidence(candidates[i].Text, question.Evidence)
		candidates[i].SourceHit = containsString(question.Sources, candidates[i].Source)
	}
}

// rewriteQuery asks the model for a search query and flattens it to one line.
func rewriteQuery(ctx context.Context, client *llm.Client, question string) (string, Call, error) {
	call, err := callModel(ctx, client, "rewrite", rewriteMessages(question), rewriteOptions(), func(content string) error {
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("rewrite: пустой запрос")
		}
		return nil
	})
	if err != nil {
		return "", call, fmt.Errorf("rewrite: %w", err)
	}
	return flattenQuery(call.Response), call, nil
}

func flattenQuery(text string) string { return strings.Join(strings.Fields(text), " ") }

type rerankReply struct {
	Scores []struct {
		ID    json.Number `json:"id"`
		Score json.Number `json:"score"`
	} `json:"scores"`
}

// parseRerank checks the reranker's JSON against the contract: every id 1..n exactly
// once, each score an integer 0..3. json_object guarantees JSON, not this shape.
func parseRerank(content string, n int) ([]int, error) {
	var reply rerankReply
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&reply); err != nil {
		return nil, fmt.Errorf("ответ реранкера — не JSON: %w", err)
	}
	if len(reply.Scores) != n {
		return nil, fmt.Errorf("реранкер вернул %d оценок на %d фрагментов", len(reply.Scores), n)
	}
	scores := make([]int, n)
	seen := make([]bool, n)
	for _, item := range reply.Scores {
		id, err := item.ID.Int64()
		if err != nil || id < 1 || int(id) > n {
			return nil, fmt.Errorf("реранкер: неверный id %q", item.ID.String())
		}
		if seen[id-1] {
			return nil, fmt.Errorf("реранкер: id %d повторён", id)
		}
		score, err := item.Score.Int64()
		if err != nil || score < 0 || score > 3 {
			return nil, fmt.Errorf("реранкер: неверная оценка %q у id %d", item.Score.String(), id)
		}
		seen[id-1] = true
		scores[id-1] = int(score)
	}
	return scores, nil
}

// selectContext runs the second stage over the search pool of one query and returns
// the mode's candidates with their fate. pool is sorted by cosine rank.
func selectContext(ctx context.Context, client *llm.Client, mode, question, query string, pool []Candidate, p Params) (ModeRun, error) {
	run := ModeRun{Mode: mode, Query: query}
	if mode == modePlain || mode == modeRewrite {
		for i := 0; i < len(pool) && i < p.KPlain; i++ {
			candidate := pool[i]
			candidate.Position = i + 1
			run.Candidates = append(run.Candidates, candidate)
		}
		return run, nil
	}
	var survivors []int
	for i := 0; i < len(pool) && i < p.KBefore; i++ {
		candidate := pool[i]
		if candidate.Similarity < p.Cos {
			candidate.Cut = cutCos
		} else {
			survivors = append(survivors, len(run.Candidates))
		}
		run.Candidates = append(run.Candidates, candidate)
	}
	if len(survivors) == 0 {
		return run, nil
	}
	shown := make([]Candidate, len(survivors))
	for i, index := range survivors {
		shown[i] = run.Candidates[index]
	}
	var scores []int
	call, err := callModel(ctx, client, "rerank", rerankMessages(question, shown), rerankOptions(), func(content string) error {
		parsed, err := parseRerank(content, len(shown))
		if err == nil {
			scores = parsed
		}
		return err
	})
	run.Rerank = &call
	if err != nil {
		return run, fmt.Errorf("rerank: %w", err)
	}
	applyScores(run.Candidates, survivors, scores, p)
	return run, nil
}

// applyScores cuts survivors below the minimum score, orders the rest by score then by
// cosine rank, and keeps the first KAfter.
func applyScores(candidates []Candidate, survivors []int, scores []int, p Params) {
	var passed []int
	for i, index := range survivors {
		score := scores[i]
		candidates[index].RerankScore = &score
		if score < p.MinScore {
			candidates[index].Cut = cutScore
			continue
		}
		passed = append(passed, index)
	}
	sort.SliceStable(passed, func(a, b int) bool {
		sa, sb := *candidates[passed[a]].RerankScore, *candidates[passed[b]].RerankScore
		if sa != sb {
			return sa > sb
		}
		return candidates[passed[a]].Rank < candidates[passed[b]].Rank
	})
	for place, index := range passed {
		if place < p.KAfter {
			candidates[index].Position = place + 1
		} else {
			candidates[index].Cut = cutTopK
		}
	}
}

// answerMode generates the answer from the mode's context, or records the filter's
// refusal without a call when nothing survived.
func answerMode(ctx context.Context, client *llm.Client, question Question, run *ModeRun) error {
	chunks := run.Context()
	if len(chunks) == 0 {
		call := Call{Stage: "answer", Response: noDataMarker, FilteredOut: true, CostKnown: true}
		call.Score = scoreAnswer(question, noDataMarker, nil, false)
		run.Answer = &call
		return nil
	}
	call, err := callModel(ctx, client, "answer", ragMessages(question.Question, chunks), answerOptions(), nil)
	if err == nil {
		call.Score = scoreAnswer(question, call.Response, chunks, call.Score.Outcome == "empty")
	}
	run.Answer = &call
	if err != nil {
		return fmt.Errorf("answer: %w", err)
	}
	return nil
}

func poolSize(p Params) int {
	if p.KBefore > p.KPlain {
		return p.KBefore
	}
	return p.KPlain
}

func usesRewrite(mode string) bool { return mode == modeRewrite || mode == modeRewriteFilter }
