package main

import (
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

const (
	modelName      = "deepseek-flash"
	embeddingModel = "bge-m3"
	strategyName   = "structure"
	defaultOllama  = "http://127.0.0.1:11434"

	answerMaxTokens  = 600
	rewriteMaxTokens = 200
	rerankMaxTokens  = 400
)

// Mode names, in the order every table and the page list them.
const (
	modePlain         = "plain"
	modeRewrite       = "rewrite"
	modeFilter        = "filter"
	modeRewriteFilter = "rewrite_filter"
)

var allModes = []string{modePlain, modeRewrite, modeFilter, modeRewriteFilter}

// Reasons a candidate did not reach the context.
const (
	cutCos   = "cos"
	cutScore = "score"
	cutTopK  = "top_k"
)

// Params are the knobs of the second stage. The measurement always runs evalParams;
// -ask may override them to show what a different threshold does.
type Params struct {
	KPlain   int     `json:"k_plain"`
	KBefore  int     `json:"k_before"`
	Cos      float64 `json:"cos_threshold"`
	MinScore int     `json:"min_rerank_score"`
	KAfter   int     `json:"k_after"`
}

var evalParams = Params{KPlain: 5, KBefore: 10, Cos: 0.45, MinScore: 2, KAfter: 3}

// cosSweep are the cosine thresholds the report shows side by side.
var cosSweep = []float64{0.40, 0.45, 0.50, 0.52, 0.55}

var (
	defaultIndex        = "day-21/index/structure.json"
	defaultCorpus       = "day-21/corpus"
	defaultDay21        = "day-21/eval/questions.json"
	defaultDay22        = "day-22/eval/questions.json"
	defaultRun          = "day-23/run.json"
	defaultResults      = "day-23/RESULTS.md"
	defaultShowcase     = "day-23/showcase.json"
	negativeQuestionIDs = []string{"x01", "x02"}
)

type Fact struct {
	Name     string   `json:"name"`
	Patterns []string `json:"patterns"`
	Examples []string `json:"examples"`
}

// Question is the day-22 control question; day-21 questions fill only ID, Question,
// Source, Evidence and get Kind "retrieval".
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

type MatchedFact struct {
	Name  string `json:"name"`
	Match string `json:"match"`
}

type Citation struct {
	Source   string `json:"source"`
	Found    bool   `json:"found"`
	Expected bool   `json:"expected"`
}

type Score struct {
	Outcome           string        `json:"outcome"`
	Success           bool          `json:"success"`
	Marker            bool          `json:"marker"`
	MarkerWithFacts   bool          `json:"marker_with_facts"`
	MatchedFacts      []MatchedFact `json:"matched_facts"`
	Citations         []Citation    `json:"citations"`
	AllCitationsFound bool          `json:"all_citations_found"`
	ExpectedCitation  bool          `json:"expected_citation"`
	Uncited           bool          `json:"uncited"`
}

type Attempt struct {
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	Usage      llm.Usage `json:"usage"`
	Model      string    `json:"model,omitempty"`
	CostUSD    float64   `json:"cost_usd"`
	CostKnown  bool      `json:"cost_known"`
	Error      string    `json:"error,omitempty"`
}

// Call is one model call of any stage: rewrite, rerank or answer.
type Call struct {
	StartedAt    time.Time     `json:"started_at"`
	Stage        string        `json:"stage"`
	Messages     []llm.Message `json:"messages"`
	Response     string        `json:"response"`
	FinishReason string        `json:"finish_reason"`
	Usage        llm.Usage     `json:"usage"`
	Model        string        `json:"model"`
	CostUSD      float64       `json:"cost_usd"`
	CostKnown    bool          `json:"cost_known"`
	DurationMS   int64         `json:"duration_ms"`
	Error        string        `json:"error,omitempty"`
	Attempts     []Attempt     `json:"attempts"`
	Score        Score         `json:"score"`
	FilteredOut  bool          `json:"filtered_out,omitempty"`
}

// Candidate is one chunk found by the vector search and what the second stage did to it.
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

// ModeRun is one question in one mode: the query that was embedded, every candidate
// with its fate, the rerank call if any, and the answer when the question is answered.
type ModeRun struct {
	Mode       string      `json:"mode"`
	Query      string      `json:"query"`
	Candidates []Candidate `json:"candidates"`
	Rerank     *Call       `json:"rerank,omitempty"`
	Answer     *Call       `json:"answer,omitempty"`
}

type QuestionRun struct {
	Question  Question  `json:"question"`
	Answered  bool      `json:"answered"`
	Rewrite   Call      `json:"rewrite"`
	Rewritten string    `json:"rewritten"`
	Modes     []ModeRun `json:"modes"`
}

type RunHeader struct {
	Commit          string    `json:"commit"`
	StartedAt       time.Time `json:"started_at"`
	RunPath         string    `json:"run_path"`
	RequestedModel  string    `json:"requested_model"`
	ResponseModels  []string  `json:"response_models"`
	Params          Params    `json:"params"`
	Day21SHA256     string    `json:"day21_questions_sha256"`
	Day22SHA256     string    `json:"day22_questions_sha256"`
	IndexSHA256     string    `json:"index_sha256"`
	ManifestSHA256  string    `json:"manifest_sha256"`
	PromptsSHA256   string    `json:"prompts_sha256"`
	RetrievalN      int       `json:"retrieval_n"`
	AnsweredN       int       `json:"answered_n"`
	TotalCostUSD    float64   `json:"total_cost_usd"`
	TotalCostKnown  bool      `json:"total_cost_known"`
	ModelCallsCount int       `json:"model_calls"`
}

type Run struct {
	Meta      RunHeader     `json:"meta"`
	Questions []QuestionRun `json:"questions"`
}

type Showcase struct {
	Meta      RunHeader             `json:"meta"`
	Prompts   map[string]string     `json:"prompts"`
	Questions []QuestionRun         `json:"questions"`
	Results   map[string][][]string `json:"results"`
	Order     []string              `json:"order"`
}

// mode returns the run of the named mode, or nil.
func (q QuestionRun) mode(name string) *ModeRun {
	for i := range q.Modes {
		if q.Modes[i].Mode == name {
			return &q.Modes[i]
		}
	}
	return nil
}

// Context is the candidates that reached the model, in their context order.
func (m ModeRun) Context() []Candidate {
	var out []Candidate
	for position := 1; ; position++ {
		found := false
		for _, candidate := range m.Candidates {
			if candidate.Position == position {
				out = append(out, candidate)
				found = true
				break
			}
		}
		if !found {
			return out
		}
	}
}

// EvidencePosition is the context position of the first chunk holding the evidence, 0 if none.
func (m ModeRun) EvidencePosition() int {
	for _, candidate := range m.Context() {
		if candidate.EvidenceHit {
			return candidate.Position
		}
	}
	return 0
}
