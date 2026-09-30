package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pinned hash moves only when a prompt text changes; a change after the measured
// run must be disclosed, so this test makes it deliberate.
const pinnedPromptsSHA256 = "5d9eb7418c79a0d287aca728784ed6c0c5cc786437576c844d85e087b3a8fdc9"

func TestPromptsPinned(t *testing.T) {
	if got := promptsSHA256(); got != pinnedPromptsSHA256 {
		t.Fatalf("prompt texts changed: sha256=%s", got)
	}
}

func TestAnswerPromptsAreDay22Verbatim(t *testing.T) {
	var day22 struct {
		Prompts map[string]string `json:"prompts"`
	}
	readJSONFile(t, "../day-22/showcase.json", &day22)
	if day22.Prompts["system"] != SystemPrompt || day22.Prompts["rag_user"] != RAGUserTemplate {
		t.Fatal("answer prompts differ from day 22")
	}
	messages := ragMessages("вопрос", []Candidate{{Position: 1, Source: "a.md", Section: "A", ChunkID: "c", Text: "текст"}})
	want := "Фрагменты базы знаний:\n[1] source: a.md · section: A · chunk_id: c\nтекст\n\nОтвечай только по этим фрагментам. После каждого утверждения укажи источник в виде [источник: <source>]. Если во фрагментах ответа нет — ответь [[НЕТ ДАННЫХ]].\nВопрос: вопрос"
	if messages[1].Content != want {
		t.Fatalf("layout differs from day 22:\n%q", messages[1].Content)
	}
}

// TestScoringCopyReplaysDay22 feeds day 22's recorded answers through the copied
// scorer and expects the outcomes day 22 recorded.
func TestScoringCopyReplaysDay22(t *testing.T) {
	var day22 struct {
		Questions []struct {
			Question Question    `json:"question"`
			Chunks   []Candidate `json:"chunks"`
			NoRAG    []Call      `json:"norag"`
			RAG      []Call      `json:"rag"`
		} `json:"questions"`
	}
	readJSONFile(t, "../day-22/run.json", &day22)
	checked := 0
	for _, question := range day22.Questions {
		for mode, calls := range map[string][]Call{"norag": question.NoRAG, "rag": question.RAG} {
			chunks := question.Chunks
			if mode == "norag" {
				chunks = nil
			}
			for _, call := range calls {
				got := scoreAnswer(question.Question, call.Response, chunks, call.Score.Outcome == "empty")
				if got.Outcome != call.Score.Outcome || got.Success != call.Score.Success || len(got.MatchedFacts) != len(call.Score.MatchedFacts) {
					t.Errorf("%s %s: got %s, day 22 recorded %s", question.Question.ID, mode, got.Outcome, call.Score.Outcome)
				}
				checked++
			}
		}
	}
	if checked != 60 {
		t.Fatalf("replayed %d answers, want 60", checked)
	}
}

func TestQuestionSetsMerge(t *testing.T) {
	questions, answered, _, _, err := loadQuestions("../day-21/eval/questions.json", "../day-22/eval/questions.json")
	if err != nil {
		t.Fatal(err)
	}
	evidence, negatives := 0, 0
	for _, question := range questions {
		if hasEvidence(question) {
			evidence++
		}
		if isNegative(question) {
			negatives++
		}
	}
	if len(questions) != 43 || len(answered) != 10 || evidence != 40 || negatives != 2 {
		t.Fatalf("questions=%d answered=%d evidence=%d negatives=%d", len(questions), len(answered), evidence, negatives)
	}
	if questions[4].ID != "q05" || questions[4].Kind != "in_base" || len(questions[4].Facts) == 0 {
		t.Fatalf("q05 must carry day 22's facts: %+v", questions[4])
	}
}

func TestCommittedPlainSearchMatchesDay21(t *testing.T) {
	requireFile(t, "run.json")
	run := readCommittedRun(t)
	var day21 struct {
		Questions []struct {
			ID        string `json:"id"`
			Structure struct {
				Top10 []struct {
					ChunkID string `json:"chunk_id"`
				} `json:"top_10"`
			} `json:"structure"`
		} `json:"questions"`
	}
	readJSONFile(t, "../day-21/showcase.json", &day21)
	byID := map[string]QuestionRun{}
	for _, question := range run.Questions {
		byID[question.Question.ID] = question
	}
	for _, want := range day21.Questions {
		got := byID[want.ID].mode(modePlain)
		filter := byID[want.ID].mode(modeFilter)
		if got == nil || filter == nil || len(got.Candidates) != 5 || len(filter.Candidates) != 10 {
			t.Fatalf("%s: plain/filter candidates missing", want.ID)
		}
		for i := 0; i < 10; i++ {
			if filter.Candidates[i].ChunkID != want.Structure.Top10[i].ChunkID || i < 5 && got.Candidates[i].ChunkID != want.Structure.Top10[i].ChunkID {
				t.Fatalf("%s rank %d differs from day 21", want.ID, i+1)
			}
		}
	}
}

func TestCommittedReportReproduces(t *testing.T) {
	requireFile(t, "run.json")
	dir := t.TempDir()
	results := filepath.Join(dir, "RESULTS.md")
	showcase := filepath.Join(dir, "showcase.json")
	if err := writeReport("run.json", "../day-21/eval/questions.json", "../day-22/eval/questions.json", results, showcase); err != nil {
		t.Fatal(err)
	}
	assertSameFile(t, "RESULTS.md", results)
	assertSameFile(t, "showcase.json", showcase)
}

func TestReportRejectsDivergence(t *testing.T) {
	requireFile(t, "run.json")
	for name, mutate := range map[string]func(*Run){
		"набора дня 21": func(r *Run) { r.Meta.Day21SHA256 = "x" },
		"набора дня 22": func(r *Run) { r.Meta.Day22SHA256 = "x" },
		"промптов":      func(r *Run) { r.Meta.PromptsSHA256 = "x" },
	} {
		run := readCommittedRun(t)
		mutate(&run)
		dir := t.TempDir()
		raw, _ := encodeJSON(run)
		path := filepath.Join(dir, "run.json")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		err := writeReport(path, "../day-21/eval/questions.json", "../day-22/eval/questions.json", filepath.Join(dir, "R.md"), filepath.Join(dir, "s.json"))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}

func TestCommittedREADMECarriesResults(t *testing.T) {
	requireFile(t, "RESULTS.md")
	results, _ := os.ReadFile("RESULTS.md")
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(results), "## "+secRetrieval)
	end := strings.Index(string(results), "## "+secPerQ)
	if start < 0 || end <= start {
		t.Fatal("RESULTS.md has no report tables")
	}
	if !strings.Contains(string(readme), string(results[start:end])) {
		t.Fatal("README.md does not carry the RESULTS.md tables verbatim")
	}
}

func TestCommittedShowcaseCopy(t *testing.T) {
	ub := filepath.Clean("../../uchebnik-ai-advent")
	if _, err := os.Stat(ub); os.IsNotExist(err) {
		t.Skip("sibling uchebnik-ai-advent repository is absent")
	}
	requireFile(t, "showcase.json")
	copyPath := filepath.Join(ub, "challeng/public/day-23/showcase.json")
	requireFile(t, copyPath)
	assertSameFile(t, "showcase.json", copyPath)
}

func readCommittedRun(t *testing.T) Run {
	t.Helper()
	run, err := readRun("run.json")
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func requireFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s is required: %v", path, err)
	}
}

func assertSameFile(t *testing.T, a, b string) {
	t.Helper()
	left, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	right, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatalf("%s and %s differ", a, b)
	}
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}
