package main

import (
	"bytes"
	"encoding/json"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCrossChunkProvenanceAndRemainingSchemaEdges(t *testing.T) {
	a := Candidate{Source: "a.md", Section: "A", ChunkID: "a", Text: "Цитата A.", Position: 1}
	b := Candidate{Source: "b.md", Section: "B", ChunkID: "b", Text: "Цитата B.", Position: 2}
	answer := Answer{Answer: "Вывод.", Sources: []Source{{Source: a.Source, Section: a.Section, ChunkID: a.ChunkID, Quote: a.Text}}}
	for name, mutate := range map[string]func(*Answer){
		"source from different chunk":  func(v *Answer) { v.Sources[0].Source = b.Source },
		"section from different chunk": func(v *Answer) { v.Sources[0].Section = b.Section },
		"id from different chunk":      func(v *Answer) { v.Sources[0].ChunkID = b.ChunkID },
		"quote from different chunk":   func(v *Answer) { v.Sources[0].Quote = b.Text },
		"whitespace quote":             func(v *Answer) { v.Sources[0].Quote = " \n\t" },
		"substantive clarification":    func(v *Answer) { v.Clarification = "Уточните?" },
	} {
		t.Run(name, func(t *testing.T) {
			v := answer
			v.Sources = append([]Source(nil), answer.Sources...)
			mutate(&v)
			raw, _ := encodeJSON(v)
			if _, e := parseAnswer(string(raw), []Candidate{a, b}); e == nil {
				t.Fatal("accepted cross-chunk/schema violation")
			}
		})
	}
	raw, _ := encodeJSON(answer)
	extra := strings.Replace(string(raw), `"quote": "Цитата A."`, `"quote": "Цитата A.", "extra": true`, 1)
	if _, e := parseAnswer(extra, []Candidate{a, b}); e == nil {
		t.Fatal("accepted source extra field")
	}
	cut := QuestionRun{Candidates: []Candidate{a, b}}
	cut.Candidates[1].Position = 0
	answer.Sources = []Source{{Source: b.Source, Section: b.Section, ChunkID: b.ChunkID, Quote: b.Text}}
	raw, _ = encodeJSON(answer)
	if _, e := parseAnswer(string(raw), cut.Context()); e == nil {
		t.Fatal("accepted quote from filtered-out chunk")
	}
}

func savedCall(stage, response string) Call {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	usage := llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, PromptCacheMissTokens: 10}
	cost, known := llm.CostAt(modelName, usage, start)
	at := Attempt{StartedAt: start, Response: response, FinishReason: "stop", Usage: usage, Model: modelName, CostUSD: cost, CostKnown: known}
	return Call{Stage: stage, Response: response, FinishReason: "stop", Usage: usage, Model: modelName, CostUSD: cost, CostKnown: known, Attempts: []Attempt{at}}
}
func mixedRun(t *testing.T) Run {
	t.Helper()
	c := Candidate{Rank: 1, Similarity: .8, Source: "s.md", Section: "S", ChunkID: "c", Text: "60 19 management", Position: 1}
	original := defaultIndex
	defaultIndex = filepath.Join(t.TempDir(), "index.json")
	t.Cleanup(func() { defaultIndex = original })
	raw, _ := encodeJSON(rag.Index{Chunks: []rag.Chunk{{Source: c.Source, Section: c.Section, ChunkID: c.ChunkID, Text: c.Text}}})
	if e := os.WriteFile(defaultIndex, raw, 0644); e != nil {
		t.Fatal(e)
	}
	r := syntheticRun(t)
	for index, text := range map[int]string{0: "60 19", 2: "management", 3: "Нет подходящих фактов."} {
		q := &r.Questions[index]
		q.Candidates = []Candidate{c}
		q.Answer = Answer{Answer: text, Sources: []Source{{Source: c.Source, Section: c.Section, ChunkID: c.ChunkID, Quote: c.Text}}}
		q.Checks = checkAnswer(q.Question, q.Answer, "")
		raw, _ := encodeJSON(q.Answer)
		call := savedCall("answer", string(raw))
		q.AnswerCall = &call
	}
	totalRun(&r)
	return r
}
func TestMixedReportDenominatorsFactsAndBlankLabels(t *testing.T) {
	r := mixedRun(t)
	if e := validateRun(r); e != nil {
		t.Fatal(e)
	}
	want := map[int]string{0: "correct", 2: "partial", 3: "wrong", 1: "unknown", 8: "declined", 9: "declined"}
	for index, outcome := range want {
		if got := r.Questions[index].Checks.FactOutcome; got != outcome {
			t.Fatalf("index%d got %s want %s", index, got, outcome)
		}
	}
	s, text := buildReport(r, "sha", nil)
	for key, want := range map[string]int{"substantive": 3, "unknown": 7, "source_pass": 3, "quote_pass": 3, "fact_correct": 1, "in_base_unknown": 4, "semantic_pending": 10} {
		if s.Results[key] != want {
			t.Fatalf("%s=%d want %d", key, s.Results[key], want)
		}
	}
	for _, part := range []string{"Источники: 3/3", "Цитаты: 3/3", "Отказы: 7/10", "Уточнение: —", "Причина отказа: —"} {
		if !strings.Contains(text, part) {
			t.Fatal("missing", part)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimRight(line, " \t") != line {
			t.Fatalf("trailing whitespace %q", line)
		}
	}
	dir := t.TempDir()
	if e := saveRun(filepath.Join(dir, "run.json"), r); e != nil {
		t.Fatal(e)
	}
	if e := writeReport(dir); e != nil {
		t.Fatal(e)
	}
	first, _ := os.ReadFile(filepath.Join(dir, "RESULTS.md"))
	if e := writeReport(dir); e != nil {
		t.Fatal(e)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "RESULTS.md"))
	readme, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if !bytes.Equal(first, second) || !bytes.HasSuffix(readme, first) {
		t.Fatal("mixed report/README not reproducible")
	}
}
func TestSubstantiveRunRejectsRecordedAnswerCandidateAndCallTampering(t *testing.T) {
	r := mixedRun(t)
	for name, mutate := range map[string]func(*Run){
		"candidate text":   func(v *Run) { v.Questions[0].Candidates[0].Text = "changed" },
		"candidate source": func(v *Run) { v.Questions[0].Candidates[0].Source = "changed.md" },
		"saved answer":     func(v *Run) { v.Questions[0].Answer.Answer = "changed" },
		"saved quote":      func(v *Run) { v.Questions[0].Answer.Sources[0].Quote = "changed" },
		"raw quote": func(v *Run) {
			v.Questions[0].AnswerCall.Response = strings.Replace(v.Questions[0].AnswerCall.Response, "60 19 management", "invented", 1)
		},
		"missing attempts": func(v *Run) { v.Questions[0].AnswerCall.Attempts = nil },
		"too many attempts": func(v *Run) {
			at := v.Questions[0].AnswerCall.Attempts[0]
			v.Questions[0].AnswerCall.Attempts = []Attempt{at, at, at}
		},
		"call usage":        func(v *Run) { v.Questions[0].AnswerCall.Usage.TotalTokens++ },
		"call cost":         func(v *Run) { v.Questions[0].AnswerCall.CostUSD++ },
		"attempt usage":     func(v *Run) { v.Questions[0].AnswerCall.Attempts[0].Usage.PromptTokens++ },
		"last raw response": func(v *Run) { v.Questions[0].AnswerCall.Attempts[0].Response = "changed" },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := encodeJSON(r)
			var v Run
			json.Unmarshal(raw, &v)
			mutate(&v)
			if e := validateRun(v); e == nil {
				t.Fatal("accepted tampering")
			}
		})
	}
}
func TestCLIAskPrintAndPersistentErrorsThroughHTTP(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replies []string
		want    int
	}{{"valid", []string{"query", `{"scores":[{"id":1,"score":3}]}`, validAnswer()}, 0}, {"invalid twice", []string{"query", `{"scores":[{"id":1,"score":3}]}`, "invalid", "invalid"}, 1}, {"API failure", nil, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			client, bodies := testModel(t, tc.replies)
			t.Setenv("DEEPSEEK_API_KEY_DAY24", "test")
			t.Setenv("DEEPSEEK_API_URL", client.URL)
			t.Chdir(t.TempDir()) // No settings file exists; only test credentials and local httptest are used.
			original := openSearcherFn
			openSearcherFn = func(string) (Searcher, string, error) {
				c := fixtureChunk()
				c.Position = 0
				return &searchStub{pool: []Candidate{c}}, "test", nil
			}
			t.Cleanup(func() { openSearcherFn = original })
			var out, errout bytes.Buffer
			if got := runCLI([]string{"-ask", "Вопрос?"}, &out, &errout); got != tc.want {
				t.Fatalf("exit%d want%d: %s", got, tc.want, errout.String())
			}
			if tc.want == 0 {
				for _, part := range []string{"Вопрос: Вопрос?", "Rewrite: query", "cos=", "Ответ: Точный текст.", "Источник: s.md", "Цитата: Точный\nтекст.", "схема=true", "вход 10 / выход 5", "Итого:"} {
					if !strings.Contains(out.String(), part) {
						t.Fatal("missing", part, out.String())
					}
				}
			} else {
				if out.Len() != 0 || errout.Len() == 0 {
					t.Fatal("error printed as accepted answer", out.String(), errout.String())
				}
			}
			if len(*bodies) > 4 {
				t.Fatal("unbounded model retry")
			}
		})
	}
}
func TestCLIEvalOverwriteGuardBeforeSettingsOrNetwork(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if e := os.Mkdir("day-24", 0755); e != nil {
		t.Fatal(e)
	}
	path := "day-24/run.json"
	if e := os.WriteFile(path, []byte("preserved"), 0644); e != nil {
		t.Fatal(e)
	}
	original := openSearcherFn
	openSearcherFn = func(string) (Searcher, string, error) {
		t.Fatal("overwrite guard reached searcher")
		return nil, "", nil
	}
	t.Cleanup(func() { openSearcherFn = original })
	var errors bytes.Buffer
	if got := runCLI([]string{"-eval"}, io.Discard, &errors); got != 1 || !strings.Contains(errors.String(), "run.json") {
		t.Fatal(got, errors.String())
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "preserved" {
		t.Fatal("existing run changed")
	}
}
func TestPrintQuestionSanitizesAndShowsUnknownCost(t *testing.T) {
	q := QuestionRun{Question: Question{Question: "Q\x1b[31m"}, Rewritten: "query", Answer: Answer{Answer: "Answer\u202e", Sources: []Source{{Source: "s", ChunkID: "c", Quote: "Quote\x1b[2J"}}}, Checks: Checks{Schema: true, Sources: "pass", Quotes: "pass"}}
	q.Rewrite = savedCall("rewrite", "query")
	q.Rewrite.CostKnown = false
	var out bytes.Buffer
	printQuestion(&out, q, evalParams)
	text := out.String()
	if strings.ContainsAny(text, "\x1b\u202e") {
		t.Fatal("terminal control escaped sanitization")
	}
	if !strings.Contains(text, "Источник: s") || !strings.Contains(text, "Цитата: Quote?") || !strings.Contains(text, "Итого: неизвестна") {
		t.Fatal(text)
	}
}
func TestCapturedArtifactsReproduceWithoutNetwork(t *testing.T) {
	dir := rag.ProjectPath("day-24")
	raw, e := os.ReadFile(filepath.Join(dir, "run.json"))
	if os.IsNotExist(e) {
		t.Skip("captured run not present in this checkout")
	}
	if e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(rag.ProjectPath(defaultIndex)); os.IsNotExist(e) {
		t.Skip("local frozen index not present in this checkout")
	} else if e != nil {
		t.Fatal(e)
	}
	var r Run
	if e = json.Unmarshal(raw, &r); e != nil {
		t.Fatal(e)
	}
	if e = validateRun(r); e != nil {
		t.Fatal(e)
	}
	sha := digest(raw)
	seal, e := os.ReadFile(filepath.Join(dir, "run.json.sha256"))
	if e != nil || strings.TrimSpace(string(seal)) != sha {
		t.Fatal("run seal mismatch", e)
	}
	review, e := readReview(filepath.Join(dir, "semantic-review.json"), sha, r.Questions)
	if e != nil {
		t.Fatal(e)
	}
	showcase, text := buildReport(r, sha, review)
	showcaseRaw, e := encodeJSON(showcase)
	if e != nil {
		t.Fatal(e)
	}
	for name, want := range map[string][]byte{"RESULTS.md": []byte(text), "README.md": []byte(readmeIntro + text), "showcase.json": showcaseRaw} {
		got, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s differs from captured run generation: %v", name, e)
		}
	}
	root, e := filepath.Abs(filepath.Join(dir, ".."))
	if e != nil {
		t.Fatal(e)
	}
	copyPath := filepath.Join(root, "..", "uchebnik-ai-advent", "challeng", "public", "day-24", "showcase.json")
	t.Run("sibling showcase copy", func(t *testing.T) {
		got, e := os.ReadFile(copyPath)
		if os.IsNotExist(e) {
			t.Skip("sibling showcase checkout absent")
		}
		if e != nil || !bytes.Equal(got, showcaseRaw) {
			t.Fatal("A9 and UB showcase copies differ", e)
		}
	})
}
