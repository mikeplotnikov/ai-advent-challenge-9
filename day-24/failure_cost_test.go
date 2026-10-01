package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func failureReplies() []string {
	return []string{"query", `{"scores":[{"id":1,"score":3}]}`, validAnswer(), "query", `{"scores":[{"id":1,"score":3}]}`, "invalid", "invalid"}
}
func TestCLIAskFailureSurfacesPaidAttemptsAndUnknownAPICost(t *testing.T) {
	for _, tc := range []struct {
		name     string
		replies  []string
		attempts int
		total    int
		known    bool
	}{
		{"schema", failureReplies()[3:], 4, 60, true},
		{"API", nil, 2, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, bodies := testModel(t, tc.replies)
			t.Setenv("DEEPSEEK_API_KEY_DAY24", "test")
			t.Setenv("DEEPSEEK_API_URL", client.URL)
			t.Chdir(t.TempDir())
			original := openSearcherFn
			openSearcherFn = func(string) (Searcher, string, error) {
				c := fixtureChunk()
				c.Position = 0
				return &searchStub{pool: []Candidate{c}}, "test", nil
			}
			t.Cleanup(func() { openSearcherFn = original })
			var out, errout bytes.Buffer
			if code := runCLI([]string{"-ask", "Вопрос?"}, &out, &errout); code != 1 {
				t.Fatalf("code%d: %s", code, errout.String())
			}
			text := errout.String()
			if out.Len() != 0 || len(*bodies) != tc.attempts {
				t.Fatal("accepted output or lost attempts", out.String(), len(*bodies))
			}
			for _, part := range []string{"Затраты до ошибки:", fmt.Sprintf("попыток %d", tc.attempts), fmt.Sprintf("всего %d токенов", tc.total)} {
				if !strings.Contains(text, part) {
					t.Fatal("missing", part, text)
				}
			}
			if tc.known {
				if !strings.Contains(text, "цена $") || strings.Contains(text, "цена $0.00000000") {
					t.Fatal("lost paid retries", text)
				}
			} else if !strings.Contains(text, "цена неизвестна") {
				t.Fatal("API cost claimed known", text)
			}
			if strings.Contains(text, "Не знаю.") {
				t.Fatal("error presented as refusal")
			}
		})
	}
}

func TestFailedEvaluationRetainsEarlierAndPartialCalls(t *testing.T) {
	original := defaultIndex
	defaultIndex = filepath.Join(t.TempDir(), "index.json")
	t.Cleanup(func() { defaultIndex = original })
	if e := os.WriteFile(defaultIndex, []byte(`{"header":{},"chunks":[]}`), 0644); e != nil {
		t.Fatal(e)
	}
	client, bodies := testModel(t, failureReplies())
	c := fixtureChunk()
	c.Position = 0
	r, e := runEvaluation(context.Background(), client, &searchStub{pool: []Candidate{c}}, io.Discard)
	if e == nil || len(r.Questions) != 2 || len(*bodies) != 7 || r.Meta.ModelCalls != 7 || !r.Meta.TotalCostKnown || r.Meta.TotalCostUSD <= 0 {
		t.Fatalf("failed run lost progress: %+v %v", r.Meta, e)
	}
	if r.Questions[0].AnswerCall == nil || r.Questions[0].AnswerCall.Error != "" || r.Questions[0].Answer.Answer == "" {
		t.Fatal("earlier successful result lost")
	}
	failed := r.Questions[1]
	if failed.AnswerCall == nil || len(failed.AnswerCall.Attempts) != 2 || failed.AnswerCall.Error == "" || failed.Answer.Unknown || failed.Checks.Schema {
		t.Fatal("partial failure replaced", failed)
	}
	cost := 0.0
	for _, q := range r.Questions {
		for _, call := range questionCalls(q) {
			cost += call.CostUSD
		}
	}
	if cost != r.Meta.TotalCostUSD {
		t.Fatal("partial attempts excluded from totals")
	}
	var out bytes.Buffer
	printConsumedCosts(&out, r.Questions)
	if !strings.Contains(out.String(), "попыток 7; вход 70 / выход 35 / всего 105 токенов") {
		t.Fatal(out.String())
	}
}

func TestCLIEvalFailurePrintsAccumulatedCostAndSavesNoRun(t *testing.T) {
	// Copy only named nonsecret evaluation inputs into a temporary project. The git
	// metadata is read-only; the model and searcher are local test substitutes.
	gitdir, e := exec.Command("git", "rev-parse", "--absolute-git-dir").Output()
	if e != nil {
		t.Fatal(e)
	}
	// Resolve the project root from the known questions path.
	questionsPath, e := filepath.Abs(rag.ProjectPath(defaultQuestions))
	if e != nil {
		t.Fatal(e)
	}
	sourceRoot := filepath.Dir(filepath.Dir(filepath.Dir(questionsPath)))
	dir := t.TempDir()
	copyInput := func(relative string) {
		t.Helper()
		raw, e := os.ReadFile(filepath.Join(sourceRoot, relative))
		if e != nil {
			t.Fatal(e)
		}
		target := filepath.Join(dir, relative)
		if e = os.MkdirAll(filepath.Dir(target), 0755); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(target, raw, 0644); e != nil {
			t.Fatal(e)
		}
	}
	copyInput(defaultQuestions)
	manifest, _, e := rag.ReadManifest(filepath.Join(sourceRoot, defaultCorpus))
	if e != nil {
		t.Fatal(e)
	}
	copyInput(defaultCorpus + "/MANIFEST.json")
	for _, f := range manifest.Files {
		copyInput(defaultCorpus + "/" + f.Path)
	}
	if e = os.MkdirAll(filepath.Join(dir, "day-24"), 0755); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "day-24", "fixture.go"), []byte("package main\n"), 0644); e != nil {
		t.Fatal(e)
	}
	originalIndex := defaultIndex
	defaultIndex = filepath.Join(dir, "index.json")
	if e = os.WriteFile(defaultIndex, []byte(`{"header":{},"chunks":[]}`), 0644); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { defaultIndex = originalIndex })
	client, _ := testModel(t, failureReplies())
	t.Setenv("DEEPSEEK_API_KEY_DAY24", "test")
	t.Setenv("DEEPSEEK_API_URL", client.URL)
	t.Setenv("GIT_DIR", strings.TrimSpace(string(gitdir)))
	t.Chdir(dir)
	originalSearcher := openSearcherFn
	openSearcherFn = func(string) (Searcher, string, error) {
		c := fixtureChunk()
		c.Position = 0
		return &searchStub{pool: []Candidate{c}}, "test", nil
	}
	t.Cleanup(func() { openSearcherFn = originalSearcher })
	var out, errout bytes.Buffer
	if code := runCLI([]string{"-eval"}, &out, &errout); code != 1 {
		t.Fatalf("code%d: %s", code, errout.String())
	}
	if !strings.Contains(errout.String(), "попыток 7; вход 70 / выход 35 / всего 105 токенов; цена $") {
		t.Fatal("failed eval lost consumed costs", errout.String())
	}
	if strings.Contains(out.String(), "run.json сохранён") || strings.Contains(out.String(), "Не знаю.") {
		t.Fatal("failure presented as successful result")
	}
	for _, name := range []string{"run.json", "run.json.sha256", "showcase.json", "RESULTS.md"} {
		if _, e = os.Stat(filepath.Join(dir, "day-24", name)); !os.IsNotExist(e) {
			t.Fatal("failed evaluation published", name, e)
		}
	}
}
