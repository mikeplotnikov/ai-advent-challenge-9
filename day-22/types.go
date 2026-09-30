package main

import (
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

const (
	modelName      = "deepseek-flash"
	embeddingModel = "bge-m3"
	strategyName   = "structure"
	defaultK       = 5
	evalRepeats    = 3
	defaultOllama  = "http://127.0.0.1:11434"
)

var (
	defaultIndex     = "day-21/index/structure.json"
	defaultCorpus    = "day-21/corpus"
	defaultQuestions = "day-22/eval/questions.json"
	defaultRun       = "day-22/run.json"
	defaultResults   = "day-22/RESULTS.md"
	defaultShowcase  = "day-22/showcase.json"
)

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
	Expectation string   `json:"expectation"`
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

type Call struct {
	StartedAt    time.Time     `json:"started_at"`
	Mode         string        `json:"mode"`
	Repeat       int           `json:"repeat"`
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
}

type FoundChunk struct {
	Rank        int     `json:"rank"`
	Similarity  float64 `json:"similarity"`
	ChunkID     string  `json:"chunk_id"`
	Source      string  `json:"source"`
	Section     string  `json:"section"`
	Text        string  `json:"text"`
	EvidenceHit bool    `json:"evidence_hit"`
	SourceHit   bool    `json:"source_hit"`
}

type QuestionRun struct {
	Question Question     `json:"question"`
	Chunks   []FoundChunk `json:"chunks"`
	NoRAG    []Call       `json:"norag"`
	RAG      []Call       `json:"rag"`
}

type RunHeader struct {
	Commit          string    `json:"commit"`
	StartedAt       time.Time `json:"started_at"`
	RunPath         string    `json:"run_path"`
	RequestedModel  string    `json:"requested_model"`
	ResponseModels  []string  `json:"response_models"`
	Temperature     float64   `json:"temperature"`
	MaxTokens       int       `json:"max_tokens"`
	K               int       `json:"k"`
	Repeats         int       `json:"repeats"`
	QuestionsSHA256 string    `json:"questions_sha256"`
	IndexSHA256     string    `json:"index_sha256"`
	ManifestSHA256  string    `json:"manifest_sha256"`
	PromptsSHA256   string    `json:"prompts_sha256"`
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
}

func foundChunks(results []rag.SearchResult, question Question) []FoundChunk {
	out := make([]FoundChunk, len(results))
	for i, result := range results {
		out[i] = FoundChunk{
			Rank: result.Rank, Similarity: result.Similarity, ChunkID: result.Chunk.ChunkID,
			Source: result.Chunk.Source, Section: result.Chunk.Section, Text: result.Chunk.Text,
			EvidenceHit: question.Evidence != "" && rag.ContainsEvidence(result.Chunk.Text, question.Evidence),
			SourceHit:   containsString(question.Sources, result.Chunk.Source),
		}
	}
	return out
}
