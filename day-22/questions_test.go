package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuestionSetControls(t *testing.T) {
	questions, _, err := loadQuestions("eval/questions.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQuestions(questions); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../day-21/eval/questions.json")
	if err != nil {
		t.Fatal(err)
	}
	var old []struct{ ID, Question, Source, Evidence string }
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	oldByID := map[string]struct{ ID, Question, Source, Evidence string }{}
	for _, q := range old {
		oldByID[q.ID] = q
	}
	corpus := strings.Builder{}
	err = filepath.Walk("../day-21/corpus", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err == nil {
			corpus.Write(raw)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	corpusLower := strings.ToLower(corpus.String())
	wantInBase := map[string]bool{"q05": true, "q10": true, "q15": true, "q20": true, "q25": true, "q30": true, "q35": true}
	gotInBase := map[string]bool{}
	for _, question := range questions {
		if question.Kind == "in_base" {
			gotInBase[question.ID] = true
			oldQuestion, ok := oldByID[question.ID]
			if !ok {
				t.Fatalf("%s absent from day 21", question.ID)
			}
			if question.Question != oldQuestion.Question || question.Source != oldQuestion.Source || question.Evidence != oldQuestion.Evidence {
				t.Fatalf("%s differs from day 21", question.ID)
			}
		}
		for _, fact := range question.Facts {
			positive := question.Evidence
			if question.Kind == "general" {
				positive = question.Expectation
			}
			oneMatches := false
			for _, pattern := range fact.Patterns {
				re, err := compilePattern(pattern)
				if err != nil {
					t.Fatalf("%s: %v", question.ID, err)
				}
				if re.MatchString(question.Question) {
					t.Fatalf("%s pattern %q matches question", question.ID, pattern)
				}
				matched := re.MatchString(positive)
				oneMatches = oneMatches || matched
				for _, example := range fact.Examples {
					matched = matched || re.MatchString(example)
				}
				if !matched {
					t.Fatalf("%s pattern %q has no positive fixture", question.ID, pattern)
				}
			}
			if !oneMatches {
				t.Fatalf("%s fact %q does not match evidence/expectation", question.ID, fact.Name)
			}
			for _, example := range fact.Examples {
				matched := false
				for _, pattern := range fact.Patterns {
					re, _ := compilePattern(pattern)
					matched = matched || re.MatchString(example)
				}
				if !matched {
					t.Fatalf("%s example %q is not matched", question.ID, example)
				}
			}
		}
		for _, term := range question.AbsentTerms {
			if strings.Contains(corpusLower, strings.ToLower(term)) {
				t.Fatalf("%s absent term %q exists in corpus", question.ID, term)
			}
		}
	}
	if len(gotInBase) != len(wantInBase) {
		t.Fatalf("in_base IDs=%v want=%v", gotInBase, wantInBase)
	}
	for id := range wantInBase {
		if !gotInBase[id] {
			t.Fatalf("required in_base question %s is absent", id)
		}
	}
}
