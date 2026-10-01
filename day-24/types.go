package main

import (
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"time"
)

const (
	modelName        = "deepseek-flash"
	embeddingModel   = "bge-m3"
	strategyName     = "structure"
	defaultOllama    = "http://127.0.0.1:11434"
	defaultCorpus    = "day-21/corpus"
	defaultQuestions = "day-22/eval/questions.json"
	cutCos           = "cos"
	cutScore         = "score"
	cutTopK          = "top_k"
)

var defaultIndex = "day-21/index/structure.json"

type Params struct {
	KBefore  int     `json:"k_before"`
	Cos      float64 `json:"cos_threshold"`
	MinScore int     `json:"min_rerank_score"`
	KAfter   int     `json:"k_after"`
}

var evalParams = Params{10, 0.45, 2, 3}

type Fact struct {
	Name     string   `json:"name"`
	Patterns []string `json:"patterns"`
	Examples []string `json:"examples"`
}
type Question struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Question    string   `json:"question"`
	Source      string   `json:"source,omitempty"`
	Evidence    string   `json:"evidence,omitempty"`
	Expectation string   `json:"expectation,omitempty"`
	Sources     []string `json:"sources"`
	Facts       []Fact   `json:"facts,omitempty"`
	AbsentTerms []string `json:"absent_terms,omitempty"`
	Neighbours  []string `json:"neighbours,omitempty"`
}
type Source struct {
	Source  string `json:"source"`
	Section string `json:"section"`
	ChunkID string `json:"chunk_id"`
	Quote   string `json:"quote"`
}
type Answer struct {
	Answer        string   `json:"answer"`
	Unknown       bool     `json:"unknown"`
	Clarification string   `json:"clarification"`
	Sources       []Source `json:"sources"`
}
type Attempt struct {
	FinishReason string    `json:"finish_reason"`
	StartedAt    time.Time `json:"started_at"`
	DurationMS   int64     `json:"duration_ms"`
	Response     string    `json:"response"`
	Usage        llm.Usage `json:"usage"`
	Model        string    `json:"model"`
	CostUSD      float64   `json:"cost_usd"`
	CostKnown    bool      `json:"cost_known"`
	Error        string    `json:"error,omitempty"`
}
type Call struct {
	FinishReason string        `json:"finish_reason"`
	Stage        string        `json:"stage"`
	Messages     []llm.Message `json:"messages"`
	Response     string        `json:"response"`
	Usage        llm.Usage     `json:"usage"`
	Model        string        `json:"model"`
	CostUSD      float64       `json:"cost_usd"`
	CostKnown    bool          `json:"cost_known"`
	Attempts     []Attempt     `json:"attempts"`
	Error        string        `json:"error,omitempty"`
}
type Candidate struct {
	Rank        int     `json:"rank"`
	Similarity  float64 `json:"similarity"`
	ChunkID     string  `json:"chunk_id"`
	Source      string  `json:"source"`
	Section     string  `json:"section"`
	Tokens      int     `json:"tokens"`
	Text        string  `json:"text"`
	RerankScore *int    `json:"rerank_score,omitempty"`
	Position    int     `json:"position"`
	Cut         string  `json:"cut,omitempty"`
	EvidenceHit bool    `json:"evidence_hit"`
	SourceHit   bool    `json:"source_hit"`
}
type Checks struct {
	Schema        bool     `json:"schema"`
	Sources       string   `json:"sources"`
	Quotes        string   `json:"quotes"`
	FactOutcome   string   `json:"fact_outcome"`
	MatchedFacts  []string `json:"matched_facts"`
	RefusalReason string   `json:"refusal_reason"`
}
type Semantic struct {
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}
type QuestionRun struct {
	Question   Question    `json:"question"`
	Rewritten  string      `json:"rewritten"`
	Rewrite    Call        `json:"rewrite"`
	Candidates []Candidate `json:"candidates"`
	Rerank     *Call       `json:"rerank,omitempty"`
	AnswerCall *Call       `json:"answer_call,omitempty"`
	Answer     Answer      `json:"answer"`
	Checks     Checks      `json:"checks"`
	Semantic   Semantic    `json:"semantic"`
}

func (q QuestionRun) Context() []Candidate {
	out := []Candidate{}
	for pos := 1; ; pos++ {
		found := false
		for _, c := range q.Candidates {
			if c.Position == pos {
				out = append(out, c)
				found = true
				break
			}
		}
		if !found {
			return out
		}
	}
}

type Meta struct {
	Commit         string            `json:"commit"`
	CorpusCommit   string            `json:"corpus_commit"`
	CorpusBase     string            `json:"corpus_base"`
	StartedAt      time.Time         `json:"started_at"`
	RequestedModel string            `json:"requested_model"`
	Params         Params            `json:"params"`
	Hashes         map[string]string `json:"hashes"`
	TotalCostUSD   float64           `json:"total_cost_usd"`
	TotalCostKnown bool              `json:"total_cost_known"`
	ModelCalls     int               `json:"model_calls"`
}
type Run struct {
	Meta      Meta          `json:"meta"`
	Questions []QuestionRun `json:"questions"`
}
type Showcase struct {
	Meta      Meta              `json:"meta"`
	RunSHA256 string            `json:"run_sha256"`
	Prompts   map[string]string `json:"prompts"`
	Questions []QuestionRun     `json:"questions"`
	Results   map[string]int    `json:"results"`
}
type Review struct {
	RunSHA256 string     `json:"run_sha256"`
	Questions []Semantic `json:"questions"`
}
