package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeSearch struct {
	queries []string
	pool    []Candidate
	err     error
}

func (s *fakeSearch) Search(_ context.Context, q string, k int) ([]Candidate, error) {
	s.queries = append(s.queries, q)
	return s.pool, s.err
}
func testClient(t *testing.T, replies []string, requests *[]string) *llm.Client {
	t.Helper()
	i := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
		}
		b, _ := json.Marshal(req.Messages)
		if requests != nil {
			*requests = append(*requests, string(b))
		}
		if i >= len(replies) {
			t.Errorf("unexpected call %d", i)
			http.Error(w, "extra", 500)
			return
		}
		content := replies[i]
		i++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"model": "deepseek-flash", "choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 12, "total_tokens": 24}})
	}))
	t.Cleanup(server.Close)
	t.Setenv("DAY25_TEST_KEY", "dummy-not-secret")
	c, e := llm.NewWithKeyEnv("DAY25_TEST_KEY")
	if e != nil {
		t.Fatal(e)
	}
	c.URL = server.URL
	return c
}
func memoryJSON(goal, query string) string {
	b, _ := encodeJSON(memoryReply{State{goal, []string{}, []string{}, []string{}}, query})
	return string(b)
}
func TestMemoryRejectsInventedAndMalformedState(t *testing.T) {
	good := memoryJSON("цель", "query")
	if _, e := parseMemory(good, []string{"Моя цель"}); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{memoryJSON("выдумано", "query"), strings.Replace(good, `"constraints": []`, `"constraints": null`, 1), strings.Replace(good, `"query": "query"`, `"query": ""`, 1), good + " {}", strings.Replace(good, `"goal": "цель"`, `"goal": "цель", "goal":"цель"`, 1)} {
		if _, e := parseMemory(raw, []string{"Моя цель"}); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestResumeAndSearchEveryTurnWithCurrentCitations(t *testing.T) {
	chunk := Candidate{Rank: 1, Similarity: .9, Source: "day-13/README.md", Section: "State", ChunkID: "c1", Text: "State uses a gateway."}
	answer := `{"answer":"State uses a gateway.","unknown":false,"clarification":"","sources":[{"source":"day-13/README.md","section":"State","chunk_id":"c1","quote":"State uses a gateway."}]}`
	var requests []string
	c := testClient(t, []string{memoryJSON("цельXYZ", "state gateway"), `{"scores":[{"id":1,"score":3}]}`, answer, memoryJSON("цельXYZ", "state gateway followup"), `{"scores":[{"id":1,"score":3}]}`, answer}, &requests)
	search := &fakeSearch{pool: []Candidate{chunk}}
	s := emptySession()
	first, e := nextTurn(context.Background(), c, search, s, "Моя цельXYZ")
	if e != nil {
		t.Fatal(e)
	}
	s.Turns = append(s.Turns, first)
	path := filepath.Join(t.TempDir(), "session.json")
	if e = saveSession(path, s); e != nil {
		t.Fatal(e)
	}
	resumed, e := loadSession(path)
	if e != nil {
		t.Fatal(e)
	}
	second, e := nextTurn(context.Background(), c, search, resumed, "А подробнее?")
	if e != nil {
		t.Fatal(e)
	}
	if second.Number != 2 || second.Before.Goal != "цельXYZ" || len(search.queries) != 2 {
		t.Fatalf("lost resume/search: %+v", second)
	}
	if !strings.Contains(requests[3], "Моя цельXYZ") || !strings.Contains(requests[5], "цельXYZ") || !strings.Contains(requests[3], "State uses a gateway.") {
		t.Fatal("history/state absent from prompts")
	}
	if !reflect.DeepEqual(search.queries, []string{"state gateway", "state gateway followup"}) {
		t.Fatal(search.queries)
	}
	// A citation from a prior turn cannot validate against today's retrieval.
	if _, e = parseAnswer(answer, []Candidate{{Source: "other", Text: chunk.Text}}); e == nil {
		t.Fatal("accepted old source")
	}
}
func TestEmptyContextStillSearchesAndDeclaresNoSources(t *testing.T) {
	c := testClient(t, []string{memoryJSON("", "неизвестно")}, nil)
	s := &fakeSearch{}
	turn, e := nextTurn(context.Background(), c, s, emptySession(), "неизвестно")
	if e != nil {
		t.Fatal(e)
	}
	if len(s.queries) != 1 || !turn.RAG.Answer.Unknown || turn.RAG.AnswerCall != nil {
		t.Fatal("empty retrieval contract")
	}
	var out strings.Builder
	printTurn(&out, turn)
	if !strings.Contains(out.String(), "Источников нет") {
		t.Fatal(out.String())
	}
}
func TestMemoryPromptWindowKeepsLatestStateAndLastSixExchanges(t *testing.T) {
	s := emptySession()
	for i := 1; i <= 9; i++ {
		s.Turns = append(s.Turns, Turn{User: fmt.Sprintf("user-%d", i), RAG: QuestionRun{Answer: Answer{Answer: fmt.Sprintf("assistant-%d", i)}}, After: State{Goal: fmt.Sprintf("goal-%d", i), Constraints: []string{"early constraint"}, Terms: []string{}, Clarifications: []string{}}})
	}
	msgs := memoryMessages(s, "followup")
	var data struct {
		Recent []struct {
			User      string `json:"user"`
			Assistant string `json:"assistant"`
		} `json:"recent"`
		State State `json:"previous_state"`
	}
	if e := json.Unmarshal([]byte(msgs[1].Content), &data); e != nil {
		t.Fatal(e)
	}
	if len(data.Recent) != 6 || data.State.Goal != "goal-9" {
		t.Fatal(data)
	}
	for i, r := range data.Recent {
		if r.User != fmt.Sprintf("user-%d", i+4) || r.Assistant != fmt.Sprintf("assistant-%d", i+4) {
			t.Fatal(data)
		}
	}
}
func TestCLIFailedTurnsPreserveFileAndPrintCosts(t *testing.T) {
	for _, failure := range []string{"memory", "search", "citation"} {
		t.Run(failure, func(t *testing.T) {
			replies := []string{"bad", "bad"}
			search := &fakeSearch{}
			if failure == "search" {
				replies = []string{memoryJSON("goal", "query")}
				search.err = errors.New("offline")
			}
			if failure == "citation" {
				replies = []string{memoryJSON("goal", "query"), `{"scores":[{"id":1,"score":3}]}`, `{"answer":"bad","unknown":false,"clarification":"","sources":[]}`, `{"answer":"bad","unknown":false,"clarification":"","sources":[]}`}
				search.pool = []Candidate{{Rank: 1, Similarity: .9, Source: "s", ChunkID: "c", Text: "text"}}
			}
			c := testClient(t, replies, nil)
			t.Setenv("DEEPSEEK_API_KEY_DAY25", "dummy-not-secret")
			t.Setenv("DEEPSEEK_API_URL", c.URL)
			t.Chdir(t.TempDir())
			old := openSearcherFn
			openSearcherFn = func(string) (Searcher, string, error) { return search, "", nil }
			t.Cleanup(func() { openSearcherFn = old })
			path := filepath.Join(t.TempDir(), "s.json")
			if e := saveSession(path, emptySession()); e != nil {
				t.Fatal(e)
			}
			before, _ := os.ReadFile(path)
			var out, errout strings.Builder
			if code := runCLI([]string{"-ask", "goal", "-session", path}, strings.NewReader(""), &out, &errout); code != 1 {
				t.Fatal(code)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) || out.Len() != 0 || !strings.Contains(errout.String(), "Попыток LLM: "+map[string]string{"memory": "2", "search": "1", "citation": "4"}[failure]) {
				t.Fatalf("persisted failed turn: %s", errout.String())
			}
		})
	}
}
func TestCLIResumeSuccessfulTurns(t *testing.T) {
	c := testClient(t, []string{memoryJSON("goal", "query1"), memoryJSON("goal", "query2")}, nil)
	t.Setenv("DEEPSEEK_API_KEY_DAY25", "dummy-not-secret")
	t.Setenv("DEEPSEEK_API_URL", c.URL)
	t.Chdir(t.TempDir())
	old := openSearcherFn
	openSearcherFn = func(string) (Searcher, string, error) { return &fakeSearch{}, "", nil }
	t.Cleanup(func() { openSearcherFn = old })
	path := filepath.Join(t.TempDir(), "s.json")
	for _, user := range []string{"goal", "followup"} {
		var out, errout strings.Builder
		if code := runCLI([]string{"-ask", user, "-session", path}, strings.NewReader(""), &out, &errout); code != 0 {
			t.Fatal(errout.String())
		}
	}
	s, e := loadSession(path)
	if e != nil || len(s.Turns) != 2 || s.Turns[1].Number != 2 || s.Turns[1].Before.Goal != "goal" {
		t.Fatalf("%+v %v", s, e)
	}
}
func validSessionFixture() Session {
	state := State{"goal", []string{}, []string{}, []string{}}
	c := Candidate{Rank: 1, Position: 1, Source: "s", Section: "sec", ChunkID: "c", Text: "literal"}
	return Session{1, []Turn{{Number: 1, User: "goal", Before: State{}, After: state, Query: "query", SearchPerformed: true, RAG: QuestionRun{Candidates: []Candidate{c}, Answer: Answer{"literal", false, "", []Source{{"s", "sec", "c", "literal"}}}}}}}
}
func TestSessionTamperingRejected(t *testing.T) {
	for _, mutate := range []func(*Session){func(s *Session) { s.Version = 2 }, func(s *Session) { s.Turns[0].Number = 2 }, func(s *Session) { s.Turns[0].After.Goal = "invented" }, func(s *Session) { s.Turns[0].RAG.Answer.Sources[0].Quote = "invented" }, func(s *Session) {
		t := s.Turns[0]
		t.Number = 2
		t.Before.Goal = "broken"
		s.Turns = append(s.Turns, t)
	}} {
		s := validSessionFixture()
		mutate(&s)
		path := filepath.Join(t.TempDir(), "s.json")
		saveSession(path, s)
		if _, e := loadSession(path); e == nil {
			t.Fatal("accepted tampered session")
		}
	}
	if s, e := loadSession(filepath.Join(t.TempDir(), "missing")); e != nil || len(s.Turns) != 0 {
		t.Fatal(s, e)
	}
}
func TestAnswerSchemaAndCitationTriple(t *testing.T) {
	s := validSessionFixture()
	a := s.Turns[0].RAG.Answer
	c := s.Turns[0].RAG.Context()
	raw, _ := encodeJSON(a)
	if _, e := parseAnswer(string(raw), c); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*Answer){func(a *Answer) { a.Sources[0].Quote = "invented" }, func(a *Answer) { a.Sources[0].Quote = "" }, func(a *Answer) { a.Sources[0].Section = "other" }, func(a *Answer) { a.Sources[0].ChunkID = "other" }, func(a *Answer) { a.Sources = nil }, func(a *Answer) { a.Unknown = true }} {
		a := s.Turns[0].RAG.Answer
		a.Sources = append([]Source{}, a.Sources...)
		mutate(&a)
		b, _ := encodeJSON(a)
		if _, e := parseAnswer(string(b), c); e == nil {
			t.Fatal("accepted fabricated answer")
		}
	}
	for _, bad := range []string{string(raw) + " {}", strings.Replace(string(raw), `"unknown": false`, `"unknown": null`, 1), strings.Replace(string(raw), `"unknown": false`, `"unknown": false,"extra":1`, 1)} {
		if _, e := parseAnswer(bad, c); e == nil {
			t.Fatal("accepted schema violation")
		}
	}
	if got := checkAnswer(Question{Kind: "chat"}, s.Turns[0].RAG.Answer, ""); got.FactOutcome != "not_assessed" {
		t.Fatal(got)
	}
}
func TestReportReproducibleAndTamperingRejected(t *testing.T) {
	runRaw, e := os.ReadFile("experiments/goal-alignment-v1/run.json")
	if e != nil {
		t.Fatal(e)
	}
	scenarioRaw, e := os.ReadFile("scenarios.json")
	if e != nil {
		t.Fatal(e)
	}
	var base ChatRun
	json.Unmarshal(runRaw, &base)
	prompts, _ := encodeJSON(map[string]string{"memory": MemoryPrompt, "answer": AnswerSystemPrompt, "rerank": RerankSystemPrompt})
	base.Meta.PromptsSHA = digest(prompts)
	makeDir := func(r ChatRun) string {
		d := t.TempDir()
		os.WriteFile(d+"/scenarios.json", scenarioRaw, 0600)
		atomicJSON(d+"/run.json", r)
		return d
	}
	d := makeDir(base)
	if e = writeReport(d); e != nil {
		t.Fatal(e)
	}
	first, _ := os.ReadFile(d + "/showcase.json")
	if e = writeReport(d); e != nil {
		t.Fatal(e)
	}
	second, _ := os.ReadFile(d + "/showcase.json")
	if string(first) != string(second) {
		t.Fatal("nondeterministic report")
	}
	for name, mutate := range map[string]func(*ChatRun){"scenario hash": func(r *ChatRun) { r.Meta.ScenariosSHA = "bad" }, "index hash": func(r *ChatRun) { r.Meta.IndexSHA = "bad" }, "prompt hash": func(r *ChatRun) { r.Meta.PromptsSHA = "bad" }, "user": func(r *ChatRun) { r.Scenarios[0].Session.Turns[0].User = "other" }, "search": func(r *ChatRun) { r.Scenarios[0].Session.Turns[0].SearchPerformed = false }, "first memory": func(r *ChatRun) { r.Scenarios[0].Session.Turns[0].Before.Goal = "invented" }, "chain": func(r *ChatRun) { r.Scenarios[0].Session.Turns[1].Before.Goal = "broken" }, "quote": func(r *ChatRun) { r.Scenarios[0].Session.Turns[0].RAG.Answer.Sources[0].Quote = "invented" }, "candidate text": func(r *ChatRun) { r.Scenarios[0].Session.Turns[0].RAG.Candidates[0].Text = "invented" }} {
		t.Run(name, func(t *testing.T) {
			b, _ := encodeJSON(base)
			var r ChatRun
			json.Unmarshal(b, &r)
			mutate(&r)
			if e := writeReport(makeDir(r)); e == nil {
				t.Fatal("accepted tamper")
			}
		})
	}
}
func TestFrozenScenarioChecksAndOverwriteGuard(t *testing.T) {
	b, e := os.ReadFile("scenarios.json")
	if e != nil {
		t.Fatal(e)
	}
	var ss []Scenario
	json.Unmarshal(b, &ss)
	if len(ss) != 2 {
		t.Fatal(len(ss))
	}
	for _, s := range ss {
		if len(s.Messages) != 12 {
			t.Fatal(s.ID)
		}
		joined := strings.Join(s.Messages, "\n")
		for _, v := range append([]string{s.Goal}, s.Constraints...) {
			if !strings.Contains(joined, v) {
				t.Fatal(v)
			}
		}
		state := State{Goal: s.Goal, Constraints: s.Constraints}
		session := Session{Turns: []Turn{{After: state}}}
		got := scenarioChecks(s, session)
		if !got["final_goal"] || !got["final_constraints"] {
			t.Fatal(got)
		}
		session.Turns[0].After = State{Goal: "wrong"}
		got = scenarioChecks(s, session)
		if got["final_goal"] || got["final_constraints"] {
			t.Fatal("false green measurement")
		}
	}
	for _, path := range []string{"run.json", "failed-turn.json", "eval-sessions/state.json"} {
		d := t.TempDir()
		if e := checkCaptureClear(d); e != nil {
			t.Fatal(e)
		}
		os.MkdirAll(filepath.Dir(filepath.Join(d, path)), 0700)
		os.WriteFile(filepath.Join(d, path), []byte("{}"), 0600)
		if e := checkCaptureClear(d); e == nil {
			t.Fatal("allowed overwrite", path)
		}
	}
	old := defaultIndex
	defaultIndex = filepath.Join(t.TempDir(), "stale.json")
	t.Cleanup(func() { defaultIndex = old })
	raw, e := os.ReadFile(rag.ProjectPath(old))
	if e != nil {
		t.Fatal(e)
	}
	var idx rag.Index
	json.Unmarshal(raw, &idx)
	idx.Header.Model = "stale"
	atomicJSON(defaultIndex, idx)
	if _, _, e = openSearcher("http://127.0.0.1:1"); e == nil {
		t.Fatal("accepted stale index")
	}
}
func TestAssessmentBoundToRunAndAllTurns(t *testing.T) {
	r := ChatRun{Scenarios: []ScenarioRun{{ID: "s", Session: validSessionFixture()}}}
	a := Assessment{RunSHA: "sha", Turns: []TurnAssessment{{"s", 1, "supported", "literal support", "yes"}}, Scenarios: []ScenarioAssessment{{"s", "retained", "addressed", "review"}}}
	p := filepath.Join(t.TempDir(), "review.json")
	atomicJSON(p, a)
	if _, e := readAssessment(p, "sha", r); e != nil {
		t.Fatal(e)
	}
	if _, e := readAssessment(p, "other", r); e == nil {
		t.Fatal("accepted mismatched run")
	}
	a.Turns = nil
	atomicJSON(p, a)
	if _, e := readAssessment(p, "sha", r); e == nil {
		t.Fatal("accepted incomplete review")
	}
}
func TestSavedSessionErrorIsTerminalSafe(t *testing.T) {
	s := validSessionFixture()
	s.Turns[0].RAG.Answer.Sources[0].Source = "\x1b[31munsafe-source"
	path := filepath.Join(t.TempDir(), "s.json")
	if e := saveSession(path, s); e != nil {
		t.Fatal(e)
	}
	var out, errout strings.Builder
	if code := runCLI([]string{"-ask", "goal", "-session", path}, strings.NewReader(""), &out, &errout); code != 1 {
		t.Fatal(code)
	}
	if strings.Contains(errout.String(), "\x1b") || !strings.Contains(errout.String(), "unsafe-source") {
		t.Fatalf("unsafe error %q", errout.String())
	}
}

func TestCLIChatKeepsEarlierSuccessfulTurnOnLaterFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			replies := []string{memoryJSON("goal", "query1"), memoryJSON("goal", "query2")}
			if fail {
				replies = []string{memoryJSON("goal", "query1"), "bad", "bad"}
			}
			c := testClient(t, replies, nil)
			t.Setenv("DEEPSEEK_API_KEY_DAY25", "dummy-not-secret")
			t.Setenv("DEEPSEEK_API_URL", c.URL)
			t.Chdir(t.TempDir())
			old := openSearcherFn
			openSearcherFn = func(string) (Searcher, string, error) { return &fakeSearch{}, "", nil }
			t.Cleanup(func() { openSearcherFn = old })
			path := filepath.Join(t.TempDir(), "s.json")
			var out, errout strings.Builder
			code := runCLI([]string{"-chat", "-session", path}, strings.NewReader("\ngoal\nfollowup\n/exit\nignored\n"), &out, &errout)
			want, n := 0, 2
			if fail {
				want, n = 1, 1
			}
			if code != want {
				t.Fatalf("%d %s", code, errout.String())
			}
			s, e := loadSession(path)
			if e != nil || len(s.Turns) != n {
				t.Fatal(s, e)
			}
			if !fail && (s.Turns[1].Number != 2 || s.Turns[1].Before.Goal != "goal") {
				t.Fatal(s)
			}
		})
	}
}
func TestEvalRejectsExistingAndPartialCaptureBeforeCalls(t *testing.T) {
	for _, name := range []string{"run.json", "failed-turn.json", "eval-sessions/state.json"} {
		t.Run(name, func(t *testing.T) {
			d := t.TempDir()
			p := filepath.Join(d, name)
			os.MkdirAll(filepath.Dir(p), 0700)
			os.WriteFile(p, []byte("preserved"), 0600)
			c := testClient(t, nil, nil)
			e := runEvaluation(c, &fakeSearch{}, d, time.Second, &strings.Builder{})
			if e == nil || !(strings.Contains(e.Error(), "существует") || strings.Contains(e.Error(), "частичный")) {
				t.Fatal(e)
			}
			b, _ := os.ReadFile(p)
			if string(b) != "preserved" {
				t.Fatal("capture changed")
			}
			t.Chdir(t.TempDir())
			os.MkdirAll(filepath.Join("day-25", filepath.Dir(name)), 0700)
			os.WriteFile(filepath.Join("day-25", name), []byte("preserved"), 0600)
			var out, errout strings.Builder
			if code := runCLI([]string{"-eval"}, strings.NewReader(""), &out, &errout); code != 1 || !(strings.Contains(errout.String(), "существует") || strings.Contains(errout.String(), "частичный")) {
				t.Fatalf("guard missed: %d %s", code, errout.String())
			}
		})
	}
}
