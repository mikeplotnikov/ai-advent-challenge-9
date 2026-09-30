package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

func readQuestionFile(path string) ([]Question, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("прочитать набор вопросов %s: %w", path, err)
	}
	var questions []Question
	if err := json.Unmarshal(raw, &questions); err != nil {
		return nil, "", fmt.Errorf("разобрать набор вопросов %s: %w", path, err)
	}
	digest := sha256.Sum256(raw)
	return questions, hex.EncodeToString(digest[:]), nil
}

// loadQuestions merges the two frozen sets into the measurement's 43 questions: the 40
// day-21 questions in their order (a day-22 question with the same id replaces its
// day-21 twin, keeping its facts), then day 22's questions that day 21 does not have.
// answered marks day 22's ten, the ones whose answers are generated and scored.
func loadQuestions(day21Path, day22Path string) (questions []Question, answered map[string]bool, day21SHA, day22SHA string, err error) {
	day21, day21SHA, err := readQuestionFile(day21Path)
	if err != nil {
		return nil, nil, "", "", err
	}
	day22, day22SHA, err := readQuestionFile(day22Path)
	if err != nil {
		return nil, nil, "", "", err
	}
	if len(day21) != 40 || len(day22) != 10 {
		return nil, nil, "", "", fmt.Errorf("наборы: день 21 — %d вопросов, день 22 — %d; ожидалось 40 и 10", len(day21), len(day22))
	}
	byID := map[string]Question{}
	answered = map[string]bool{}
	for _, question := range day22 {
		for _, fact := range question.Facts {
			for _, pattern := range fact.Patterns {
				if _, err := compilePattern(pattern); err != nil {
					return nil, nil, "", "", fmt.Errorf("%s: неверный шаблон %q: %w", question.ID, pattern, err)
				}
			}
		}
		byID[question.ID] = question
		answered[question.ID] = true
	}
	seen := map[string]bool{}
	for _, question := range day21 {
		if question.Evidence == "" || question.Source == "" {
			return nil, nil, "", "", fmt.Errorf("%s: у вопроса дня 21 нет эталона", question.ID)
		}
		if twin, ok := byID[question.ID]; ok {
			if twin.Question != question.Question || twin.Evidence != question.Evidence {
				return nil, nil, "", "", fmt.Errorf("%s: вопрос дня 22 разошёлся с днём 21", question.ID)
			}
			question = twin
		} else {
			question.Kind = "retrieval"
			question.Sources = []string{question.Source}
		}
		questions = append(questions, question)
		seen[question.ID] = true
	}
	for _, question := range day22 {
		if !seen[question.ID] {
			questions = append(questions, question)
		}
	}
	return questions, answered, day21SHA, day22SHA, nil
}

// hasEvidence says whether retrieval metrics count the question: it carries day 21's evidence.
func hasEvidence(question Question) bool { return question.Evidence != "" }

func isNegative(question Question) bool {
	for _, id := range negativeQuestionIDs {
		if question.ID == id {
			return true
		}
	}
	return false
}

func compilePattern(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile("(?i)" + pattern)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
