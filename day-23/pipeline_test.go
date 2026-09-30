package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

func modelResponse(content string) string {
	raw, _ := json.Marshal(content)
	return `{"id":"x","model":"deepseek-flash","choices":[{"message":{"role":"assistant","content":` + string(raw) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":10}}`
}

// fakeModel answers each stage from a script keyed by the system prompt and records
// every request body.
type fakeModel struct {
	mu       sync.Mutex
	bodies   []map[string]any
	rewrite  []string
	rerank   []string
	answer   []string
	requests map[string]int
}

func (f *fakeModel) server(t *testing.T) *httptest.Server {
	f.requests = map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.bodies = append(f.bodies, body)
		system := body["messages"].([]any)[0].(map[string]any)["content"].(string)
		var queue *[]string
		stage := ""
		switch system {
		case RewriteSystemPrompt:
			queue, stage = &f.rewrite, "rewrite"
		case RerankSystemPrompt:
			queue, stage = &f.rerank, "rerank"
		default:
			queue, stage = &f.answer, "answer"
		}
		f.requests[stage]++
		if len(*queue) == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		next := (*queue)[0]
		if len(*queue) > 1 {
			*queue = (*queue)[1:]
		}
		io.WriteString(w, modelResponse(next))
	}))
	t.Cleanup(server.Close)
	return server
}

func testClient(server *httptest.Server) *llm.Client {
	return &llm.Client{APIKey: "test", Model: modelName, URL: server.URL, HTTP: server.Client()}
}

type fakeSearcher struct {
	pools   map[string][]Candidate
	queries []string
}

func (s *fakeSearcher) Search(_ context.Context, query string, k int) ([]Candidate, error) {
	s.queries = append(s.queries, query)
	pool := s.pools[query]
	if pool == nil {
		pool = s.pools["*"]
	}
	if len(pool) > k {
		pool = pool[:k]
	}
	return append([]Candidate(nil), pool...), nil
}

func pool(similarities ...float64) []Candidate {
	out := make([]Candidate, len(similarities))
	for i, similarity := range similarities {
		out[i] = Candidate{Rank: i + 1, Similarity: similarity, ChunkID: "c" + string(rune('a'+i)), Source: "s.md", Section: "S", Tokens: 100, Text: "text"}
	}
	return out
}

func TestParseRerankContract(t *testing.T) {
	good, err := parseRerank(`{"scores":[{"id":2,"score":3},{"id":1,"score":0}]}`, 2)
	if err != nil || good[0] != 0 || good[1] != 3 {
		t.Fatalf("scores=%v err=%v", good, err)
	}
	for name, content := range map[string]string{
		"not json":     `оценки: 3, 0`,
		"missing id":   `{"scores":[{"id":1,"score":3}]}`,
		"repeated id":  `{"scores":[{"id":1,"score":3},{"id":1,"score":2}]}`,
		"id too big":   `{"scores":[{"id":1,"score":3},{"id":3,"score":2}]}`,
		"score high":   `{"scores":[{"id":1,"score":4},{"id":2,"score":2}]}`,
		"score low":    `{"scores":[{"id":1,"score":-1},{"id":2,"score":2}]}`,
		"score float":  `{"scores":[{"id":1,"score":2.5},{"id":2,"score":2}]}`,
		"extra entry":  `{"scores":[{"id":1,"score":1},{"id":2,"score":2},{"id":2,"score":2}]}`,
		"id as string": `{"scores":[{"id":"a","score":1},{"id":2,"score":2}]}`,
	} {
		if _, err := parseRerank(content, 2); err == nil {
			t.Errorf("%s: accepted %s", name, content)
		}
	}
}

func TestApplyScoresCutsSortsAndKeepsTopK(t *testing.T) {
	candidates := pool(0.60, 0.58, 0.44, 0.55, 0.52, 0.50)
	candidates[2].Cut = cutCos
	survivors := []int{0, 1, 3, 4, 5}
	// scores: rank1=2, rank2=1, rank4=3, rank5=2, rank6=2 → order 4, 1, 5 kept; 6 top_k; 2 score.
	applyScores(candidates, survivors, []int{2, 1, 3, 2, 2}, Params{MinScore: 2, KAfter: 3})
	want := map[int]struct {
		position int
		cut      string
	}{1: {2, ""}, 2: {0, cutScore}, 3: {0, cutCos}, 4: {1, ""}, 5: {3, ""}, 6: {0, cutTopK}}
	for _, candidate := range candidates {
		if got := want[candidate.Rank]; candidate.Position != got.position || candidate.Cut != got.cut {
			t.Errorf("rank %d: position=%d cut=%q, want %d %q", candidate.Rank, candidate.Position, candidate.Cut, got.position, got.cut)
		}
	}
	run := ModeRun{Candidates: candidates}
	var order []int
	for _, candidate := range run.Context() {
		order = append(order, candidate.Rank)
	}
	if len(order) != 3 || order[0] != 4 || order[1] != 1 || order[2] != 5 {
		t.Fatalf("context order=%v", order)
	}
}

func TestSelectContextThresholdAndEmptyPool(t *testing.T) {
	fake := &fakeModel{rerank: []string{`{"scores":[{"id":1,"score":3},{"id":2,"score":0}]}`}}
	client := testClient(fake.server(t))
	p := Params{KPlain: 5, KBefore: 4, Cos: 0.5, MinScore: 2, KAfter: 3}
	run, err := selectContext(context.Background(), client, modeFilter, "q", "q", pool(0.6, 0.55, 0.49, 0.3, 0.2), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Candidates) != 4 {
		t.Fatalf("k-before not applied: %d candidates", len(run.Candidates))
	}
	if run.Candidates[2].Cut != cutCos || run.Candidates[3].Cut != cutCos || run.Candidates[1].Cut != cutScore || run.Candidates[0].Position != 1 {
		t.Fatalf("candidates=%+v", run.Candidates)
	}
	shown := fake.bodies[0]["messages"].([]any)[1].(map[string]any)["content"].(string)
	if strings.Count(shown, "source: ") != 2 {
		t.Fatalf("reranker must see only survivors of the cosine threshold:\n%s", shown)
	}

	fake.bodies = nil
	empty, err := selectContext(context.Background(), client, modeFilter, "q", "q", pool(0.4, 0.3), p)
	if err != nil || empty.Rerank != nil || len(fake.bodies) != 0 {
		t.Fatalf("all below threshold must skip the reranker: err=%v rerank=%v calls=%d", err, empty.Rerank, len(fake.bodies))
	}
	question := Question{ID: "x", Kind: "out_of_base", Neighbours: []string{"bge"}}
	if err := answerMode(context.Background(), client, question, &empty); err != nil {
		t.Fatal(err)
	}
	if len(fake.bodies) != 0 || !empty.Answer.FilteredOut || empty.Answer.Score.Outcome != "declined" {
		t.Fatalf("empty context must answer the marker without a call: calls=%d answer=%+v", len(fake.bodies), empty.Answer)
	}
	inBase := ModeRun{Candidates: pool(0.4)}
	inBase.Candidates[0].Cut = cutCos
	if err := answerMode(context.Background(), client, Question{Kind: "in_base", Facts: []Fact{{Name: "f", Patterns: []string{"x"}}}}, &inBase); err != nil || inBase.Answer.Score.Outcome != "unknown" {
		t.Fatalf("in-base filtered out must be unknown: %+v", inBase.Answer)
	}

	plain, err := selectContext(context.Background(), client, modePlain, "q", "q", pool(0.9, 0.8, 0.7, 0.6, 0.5, 0.4), p)
	if err != nil || len(plain.Candidates) != 5 || plain.Rerank != nil || plain.Candidates[4].Position != 5 {
		t.Fatalf("plain must keep top-%d without rerank: %+v err=%v", p.KPlain, plain, err)
	}
}

func TestRequestBodiesPerStage(t *testing.T) {
	fake := &fakeModel{rewrite: []string{"поисковый\nзапрос"}, rerank: []string{`{"scores":[{"id":1,"score":3}]}`}, answer: []string{"ответ [источник: s.md]"}}
	client := testClient(fake.server(t))
	searcher := &fakeSearcher{pools: map[string][]Candidate{"*": pool(0.6)}}
	question := Question{ID: "q", Kind: "in_base", Question: "вопрос", Sources: []string{"s.md"}}
	if _, err := measureQuestion(context.Background(), client, searcher, question, true, evalParams); err != nil {
		t.Fatal(err)
	}
	if len(searcher.queries) != 2 || searcher.queries[1] != "поисковый запрос" {
		t.Fatalf("search must embed the original and the flattened rewrite: %q", searcher.queries)
	}
	want := map[string]float64{RewriteSystemPrompt: rewriteMaxTokens, RerankSystemPrompt: rerankMaxTokens, SystemPrompt: answerMaxTokens}
	for _, body := range fake.bodies {
		messages := body["messages"].([]any)
		system := messages[0].(map[string]any)["content"].(string)
		user := messages[1].(map[string]any)["content"].(string)
		if body["model"] != modelName || body["temperature"] != float64(0) || body["max_tokens"] != want[system] {
			t.Errorf("body=%v", body)
		}
		if body["thinking"].(map[string]any)["type"] != "disabled" {
			t.Errorf("thinking=%v", body["thinking"])
		}
		format, hasFormat := body["response_format"]
		if (system == RerankSystemPrompt) != hasFormat || hasFormat && format.(map[string]any)["type"] != "json_object" {
			t.Errorf("response_format=%v for %q", format, system[:20])
		}
		if system != RewriteSystemPrompt && !strings.HasSuffix(user, "Вопрос: вопрос") {
			t.Errorf("rerank and answer must see the original question: %q", user)
		}
	}
	if fake.requests["rewrite"] != 1 || fake.requests["rerank"] != 2 || fake.requests["answer"] != 4 {
		t.Fatalf("requests=%v", fake.requests)
	}
}

func TestInvalidRerankRetriesOnceThenFails(t *testing.T) {
	fake := &fakeModel{rerank: []string{`{"scores":[]}`}}
	client := testClient(fake.server(t))
	run, err := selectContext(context.Background(), client, modeFilter, "q", "q", pool(0.6, 0.55), evalParams)
	if err == nil || fake.requests["rerank"] != 2 || run.Rerank == nil || len(run.Rerank.Attempts) != 2 {
		t.Fatalf("err=%v requests=%v", err, fake.requests)
	}
	fake2 := &fakeModel{rerank: []string{`нет`, `{"scores":[{"id":1,"score":3},{"id":2,"score":2}]}`}}
	client2 := testClient(fake2.server(t))
	good, err := selectContext(context.Background(), client2, modeFilter, "q", "q", pool(0.6, 0.55), evalParams)
	if err != nil || len(good.Context()) != 2 || len(good.Rerank.Attempts) != 2 {
		t.Fatalf("second valid answer must be used: err=%v run=%+v", err, good)
	}
}

func TestCLIValidationBeforeCalls(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"-ask", "q", "-eval"},
		{"-ask", "  "},
		{"-ask", "q", "-mode", "all"},
		{"-ask", "q", "-cos", "1.5"},
		{"-ask", "q", "-min-score", "4"},
		{"-ask", "q", "-k-before", "21"},
		{"-ask", "q", "-k-before", "3", "-k-after", "4"},
		{"-ask", "q", "-k-after", "0"},
		{"-eval", "-cos", "0.5"},
		{"-report", "-mode", "plain"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%q: code=%d stderr=%q", args, code, stderr.String())
		}
	}
}

func TestMissingKeyNamesVariable(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	t.Setenv("DEEPSEEK_API_KEY_DAY23", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-ask", "q"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "DEEPSEEK_API_KEY") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestEvalWithExistingRunMakesNoCall(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.MkdirAll(filepath.Join(dir, "day-23"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "day-23", "run.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	opened := false
	oldOpen := openSearcherFn
	openSearcherFn = func(string) (Searcher, string, error) { opened = true; return nil, "", nil }
	defer func() { openSearcherFn = oldOpen }()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-eval"}, &stdout, &stderr); code != 1 || opened || !strings.Contains(stderr.String(), "замер уже есть") {
		t.Fatalf("code=%d opened=%v stderr=%q", code, opened, stderr.String())
	}
}

func TestAskCompareShowsFunnelAndBothAnswers(t *testing.T) {
	fake := &fakeModel{rewrite: []string{"запрос"}, rerank: []string{`{"scores":[{"id":1,"score":3},{"id":2,"score":1}]}`}, answer: []string{"ответ [источник: s.md]"}}
	client := testClient(fake.server(t))
	searcher := &fakeSearcher{pools: map[string][]Candidate{"*": pool(0.6, 0.5, 0.3)}}
	var stdout, stderr bytes.Buffer
	code := runAsk(context.Background(), client, searcher, "вопрос", askModes["compare"], evalParams, &stdout, &stderr)
	out := stdout.String()
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"[rewrite] запрос", "===== plain =====", "===== rewrite_filter =====", "✓ в контексте [1]",
		"✗ оценка реранкера ниже порога", "✗ ниже порога косинуса", "реранкер: токены"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "ответ:\n") != 2 {
		t.Fatalf("want two answers:\n%s", out)
	}
}

func TestAskKeepsOtherModeWhenOneFails(t *testing.T) {
	fake := &fakeModel{rewrite: []string{"запрос"}, answer: []string{"ответ"}} // rerank queue empty → 500
	client := testClient(fake.server(t))
	searcher := &fakeSearcher{pools: map[string][]Candidate{"*": pool(0.6)}}
	var stdout, stderr bytes.Buffer
	code := runAsk(context.Background(), client, searcher, "вопрос", askModes["compare"], evalParams, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), "ответ:\nответ") || !strings.Contains(stdout.String(), "ошибка: rerank") {
		t.Fatalf("code=%d stdout=%q", code, stdout.String())
	}
}
