package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixtureChunk() Candidate {
	return Candidate{Rank: 1, Similarity: .8, ChunkID: "c1", Source: "s.md", Section: "", Text: "Точный\nтекст. Ещё текст.", Position: 1}
}
func validAnswer() string {
	return `{"answer":"Точный текст.","unknown":false,"clarification":"","sources":[{"source":"s.md","section":"","chunk_id":"c1","quote":"Точный\nтекст."}]}`
}

const unknownAnswer = `{"answer":"Не знаю.","unknown":true,"clarification":"Уточните задачу?","sources":[]}`

func TestAnswerStrictSchemaProvenanceAndLiteralQuotes(t *testing.T) {
	chunks := []Candidate{fixtureChunk()}
	if _, e := parseAnswer(validAnswer(), chunks); e != nil {
		t.Fatal(e)
	}
	if _, e := parseAnswer(unknownAnswer, chunks); e != nil {
		t.Fatal(e)
	}
	cases := map[string]string{"missing bool": strings.Replace(validAnswer(), `"unknown":false,`, "", 1), "bool string": strings.Replace(validAnswer(), `"unknown":false`, `"unknown":"false"`, 1), "null bool": strings.Replace(validAnswer(), `"unknown":false`, `"unknown":null`, 1), "trailing": validAnswer() + " {}", "duplicate": strings.Replace(validAnswer(), `"unknown":false`, `"unknown":false,"unknown":false`, 1), "extra": strings.Replace(validAnswer(), `"unknown":false`, `"unknown":false,"extra":1`, 1), "empty quote": strings.Replace(validAnswer(), `Точный\nтекст.`, "", 1), "modified quote": strings.Replace(validAnswer(), `Точный\nтекст.`, "Точный текст.", 1), "invented quote": strings.Replace(validAnswer(), `Точный\nтекст.`, "Нет в тексте", 1), "wrong source": strings.Replace(validAnswer(), "s.md", "other.md", 1), "wrong section": strings.Replace(validAnswer(), `"section":""`, `"section":"other"`, 1), "wrong id": strings.Replace(validAnswer(), "c1", "c2", 1), "null source": strings.Replace(validAnswer(), `"source":"s.md"`, `"source":null`, 1), "wrong quote type": strings.Replace(validAnswer(), `"quote":"Точный\nтекст."`, `"quote":12`, 1), "missing source field": strings.Replace(validAnswer(), `"section":"",`, "", 1), "unknown invents answer": strings.Replace(unknownAnswer, "Не знаю.", "Не знаю, но 42.", 1), "unknown no clarification": strings.Replace(unknownAnswer, "Уточните задачу?", "", 1), "unknown with source": strings.Replace(unknownAnswer, `"sources":[]`, `"sources":[{"source":"s.md","section":"","chunk_id":"c1","quote":"текст"}]`, 1), "substantive no sources": `{"answer":"42","unknown":false,"clarification":"","sources":[]}`, "null sources": strings.Replace(unknownAnswer, `"sources":[]`, `"sources":null`, 1)}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, e := parseAnswer(raw, chunks); e == nil {
				t.Fatalf("accepted %s", raw)
			}
		})
	}
}
func TestEmptyContextNoGenerationAndHonestMetrics(t *testing.T) {
	q := QuestionRun{Question: Question{ID: "q", Kind: "in_base"}, Candidates: []Candidate{fixtureChunk()}}
	q.Candidates[0].Position = 0
	if e := answerQuestion(context.Background(), nil, &q); e != nil {
		t.Fatal(e)
	}
	if !q.Answer.Unknown || q.AnswerCall != nil || q.Checks.RefusalReason != "empty_context" || q.Checks.Sources != "not_applicable" || q.Checks.FactOutcome != "unknown" {
		t.Fatalf("%+v", q)
	}
	s, _ := buildReport(Run{Questions: []QuestionRun{q}}, "sha", nil)
	if s.Results["source_pass"] != 0 || s.Results["substantive"] != 0 || s.Results["in_base_unknown"] != 1 || s.Results["semantic_pending"] != 1 {
		t.Fatal(s.Results)
	}
}
func modelReply(raw string) string {
	content, _ := json.Marshal(raw)
	return `{"model":"deepseek-flash","choices":[{"message":{"role":"assistant","content":` + string(content) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_miss_tokens":10}}`
}
func testModel(t *testing.T, replies []string) (*llm.Client, *[]map[string]any) {
	bodies := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		bodies = append(bodies, body)
		i := len(bodies) - 1
		if i >= len(replies) {
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, modelReply(replies[i]))
	}))
	t.Cleanup(server.Close)
	return &llm.Client{APIKey: "test", Model: modelName, URL: server.URL, HTTP: server.Client()}, &bodies
}
func TestValidationRetryRetainsBothResponsesAndCosts(t *testing.T) {
	client, bodies := testModel(t, []string{`{"answer":"bad"}`, validAnswer()})
	q := QuestionRun{Question: Question{Question: "?"}, Candidates: []Candidate{fixtureChunk()}}
	if e := answerQuestion(context.Background(), client, &q); e != nil {
		t.Fatal(e)
	}
	c := q.AnswerCall
	if len(*bodies) != 2 || len(c.Attempts) != 2 || c.Attempts[0].Error == "" || c.Attempts[0].Response != `{"answer":"bad"}` || c.Usage.TotalTokens != 30 || !c.CostKnown || c.CostUSD <= 0 || c.CostUSD != c.Attempts[0].CostUSD+c.Attempts[1].CostUSD {
		t.Fatalf("%+v", c)
	}
	for _, b := range *bodies {
		if b["max_tokens"] != float64(1200) || b["temperature"] != float64(0) || b["response_format"].(map[string]any)["type"] != "json_object" || b["thinking"].(map[string]any)["type"] != "disabled" {
			t.Fatalf("options %+v", b)
		}
	}
	if q.Answer.Unknown || !q.Checks.Schema {
		t.Fatalf("answer %+v", q)
	}
}
func TestSecondInvalidAnswerAndAPIFailureStayErrors(t *testing.T) {
	for _, replies := range [][]string{{"not JSON", "not JSON"}, {}} {
		client, bodies := testModel(t, replies)
		q := QuestionRun{Question: Question{Question: "?"}, Candidates: []Candidate{fixtureChunk()}}
		if e := answerQuestion(context.Background(), client, &q); e == nil {
			t.Fatal("expected error")
		}
		if len(*bodies) != 2 || q.Checks.Schema || q.Answer.Unknown || q.AnswerCall.Error == "" {
			t.Fatalf("%+v calls %d", q, len(*bodies))
		}
	}
	client, _ := testModel(t, []string{unknownAnswer})
	q := QuestionRun{Question: Question{Question: "?"}, Candidates: []Candidate{fixtureChunk()}}
	if e := answerQuestion(context.Background(), client, &q); e != nil || q.Checks.RefusalReason != "model_unknown" {
		t.Fatalf("%+v %v", q, e)
	}
}

type searchStub struct {
	pool  []Candidate
	query string
	k     int
}

func (s *searchStub) Search(_ context.Context, q string, k int) ([]Candidate, error) {
	s.query = q
	s.k = k
	return append([]Candidate(nil), s.pool...), nil
}
func TestOneRewriteFilterPipelinePreservesAnswer(t *testing.T) {
	client, bodies := testModel(t, []string{"search query", `{"scores":[{"id":1,"score":2}]}`, validAnswer()})
	a := fixtureChunk()
	a.Position = 0
	s := &searchStub{pool: []Candidate{a, {Rank: 2, Similarity: .44, ChunkID: "cut", Source: "cut.md", Text: "below"}}}
	q, e := measureQuestion(context.Background(), client, s, Question{Question: "?"}, evalParams)
	if e != nil {
		t.Fatal(e)
	}
	if s.query != "search query" || s.k != 10 || len(*bodies) != 3 || q.Answer.Answer != "Точный текст." || q.Candidates[1].Cut != cutCos || len(q.Context()) != 1 {
		t.Fatalf("%+v", q)
	}
}
func TestRerankRejectsMalformedAndSelectionSorts(t *testing.T) {
	for _, raw := range []string{`{"scores":[{"id":1,"score":2}]} {}`, `{"scores":[{"id":"1","score":2}]}`, `{"scores":[{"id":1,"score":2.1}]}`, `{"scores":[{"id":1,"score":4}]}`, `{"scores":[{"id":1,"score":null}]}`, `{"scores":[{"id":1,"score":2},{"id":1,"score":3}]}`} {
		if _, e := parseRerank(raw, 1); e == nil {
			t.Fatal(raw)
		}
	}
	cs := []Candidate{{Rank: 1}, {Rank: 2}, {Rank: 3}, {Rank: 4}, {Rank: 5}}
	applyScores(cs, []int{0, 1, 2, 3, 4}, []int{2, 1, 3, 2, 3}, Params{MinScore: 2, KAfter: 3})
	if cs[2].Position != 1 || cs[4].Position != 2 || cs[0].Position != 3 || cs[1].Cut != cutScore || cs[3].Cut != cutTopK {
		t.Fatalf("%+v", cs)
	}
}
func TestCLIRejectsInvalidThresholdsWithoutNetwork(t *testing.T) {
	for _, cos := range []float64{math.NaN(), math.Inf(1), -.1, 1.1} {
		p := evalParams
		p.Cos = cos
		if validateParams(p) == nil {
			t.Fatal(cos)
		}
	}
	for _, args := range [][]string{{"-ask", "q", "-cos", "NaN"}, {"-ask", "q", "-k-after", "11"}, {"-eval", "-cos", ".5"}, {"-report", "-ask", "q"}} {
		if got := runCLI(args, io.Discard, io.Discard); got != 2 {
			t.Fatalf("%v = %d", args, got)
		}
	}
}
func syntheticRun(t *testing.T) Run {
	t.Helper()
	qs, e := loadQuestions()
	if e != nil {
		t.Fatal(e)
	}
	hashes, e := inputHashes()
	if e != nil {
		t.Fatal(e)
	}
	r := Run{Meta: Meta{Params: evalParams, RequestedModel: modelName, Hashes: hashes}, Questions: []QuestionRun{}}
	for _, q := range qs {
		item := QuestionRun{Question: q, Candidates: []Candidate{}}
		start := time.Now().UTC()
		cost, known := llm.CostAt(modelName, llm.Usage{}, start)
		at := Attempt{StartedAt: start, Response: "query", Model: modelName, CostUSD: cost, CostKnown: known}
		item.Rewrite = Call{Stage: "rewrite", Response: "query", Model: modelName, CostUSD: cost, CostKnown: known, Attempts: []Attempt{at}}
		if e := answerQuestion(context.Background(), nil, &item); e != nil {
			t.Fatal(e)
		}
		r.Questions = append(r.Questions, item)
	}
	totalRun(&r)
	return r
}
func TestOfflineReportReproducibleAndTamperingDetected(t *testing.T) {
	originalIndex := defaultIndex
	defaultIndex = filepath.Join(t.TempDir(), "index.json")
	if e := os.WriteFile(defaultIndex, []byte("{\"header\":{},\"chunks\":[]}"), 0644); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { defaultIndex = originalIndex })
	r := syntheticRun(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")
	if e := saveRun(path, r); e != nil {
		t.Fatal(e)
	}
	if e := saveRun(path, r); e == nil {
		t.Fatal("overwrote existing run")
	}
	if e := writeReport(dir); e != nil {
		t.Fatal(e)
	}
	first := map[string][]byte{}
	for _, n := range []string{"RESULTS.md", "showcase.json", "README.md"} {
		first[n], _ = os.ReadFile(filepath.Join(dir, n))
	}
	if e := writeReport(dir); e != nil {
		t.Fatal(e)
	}
	for n, want := range first {
		got, _ := os.ReadFile(filepath.Join(dir, n))
		if !bytes.Equal(got, want) {
			t.Fatal("non-deterministic", n)
		}
	}
	if !bytes.Contains(first["README.md"], first["RESULTS.md"]) {
		t.Fatal("README results mismatch")
	}
	raw, _ := os.ReadFile(path)
	review := Review{RunSHA256: digest(raw), Questions: []Semantic{}}
	for _, q := range r.Questions {
		review.Questions = append(review.Questions, Semantic{q.Question.ID, "unknown", "Контекста для ответа нет."})
	}
	b, _ := encodeJSON(review)
	if e := os.WriteFile(filepath.Join(dir, "semantic-review.json"), b, 0644); e != nil {
		t.Fatal(e)
	}
	if e := writeReport(dir); e != nil {
		t.Fatal(e)
	}
	review.RunSHA256 = "tampered"
	b, _ = encodeJSON(review)
	os.WriteFile(filepath.Join(dir, "semantic-review.json"), b, 0644)
	if e := writeReport(dir); e == nil {
		t.Fatal("accepted wrong review hash")
	}
	os.WriteFile(path, append(raw, ' '), 0644)
	if e := writeReport(dir); e == nil {
		t.Fatal("accepted changed run")
	}
	r.Questions[0].Checks.Sources = "pass"
	if validateRun(r) == nil {
		t.Fatal("accepted changed checks")
	}
	r = syntheticRun(t)
	r.Meta.Hashes["prompts"] = "changed"
	if validateRun(r) == nil {
		t.Fatal("accepted changed prompt hash")
	}
	r = syntheticRun(t)
	r.Questions[0].Rewrite.CostUSD = 1
	if validateRun(r) == nil {
		t.Fatal("accepted invented cost")
	}
}
func TestSemanticReviewNeedsExactUniqueIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.json")
	qs := []QuestionRun{{Question: Question{ID: "a"}}, {Question: Question{ID: "b"}}}
	inconsistent := Review{"sha", []Semantic{{"a", "supported", "ok"}, {"b", "unknown", "reason"}}}
	raw, _ := encodeJSON(inconsistent)
	os.WriteFile(path, raw, 0644)
	if _, e := readReview(path, "sha", qs); e == nil {
		t.Fatal("accepted unknown on substantive")
	}
	qs[0].Answer.Unknown = true
	inconsistent = Review{"sha", []Semantic{{"a", "supported", "ok"}, {"b", "unsupported", "reason"}}}
	raw, _ = encodeJSON(inconsistent)
	os.WriteFile(path, raw, 0644)
	if _, e := readReview(path, "sha", qs); e == nil {
		t.Fatal("accepted supported on unknown")
	}
	for _, review := range []Review{{"sha", []Semantic{{"a", "supported", "ok"}, {"a", "supported", "ok"}}}, {"sha", []Semantic{{"a", "supported", "ok"}, {"c", "supported", "ok"}}}, {"sha", []Semantic{{"a", "supported", "ok"}, {"b", "pending", "ok"}}}, {"sha", []Semantic{{"a", "supported", "ok"}, {"b", "unknown", ""}}}} {
		raw, _ := encodeJSON(review)
		os.WriteFile(path, raw, 0644)
		if _, e := readReview(path, "sha", qs); e == nil {
			t.Fatal(review)
		}
	}
	got, e := readReview(filepath.Join(dir, "missing"), "sha", qs)
	if e != nil || !reflect.DeepEqual(got, map[string]Semantic{}) {
		t.Fatal(got, e)
	}
}
