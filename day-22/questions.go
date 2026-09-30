package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

func loadQuestions(path string) ([]Question, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("прочитать набор вопросов: %w", err)
	}
	var questions []Question
	if err := json.Unmarshal(raw, &questions); err != nil {
		return nil, "", fmt.Errorf("разобрать набор вопросов: %w", err)
	}
	digest := sha256.Sum256(raw)
	return questions, hex.EncodeToString(digest[:]), nil
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

func validateQuestions(questions []Question) error {
	if len(questions) != 10 {
		return fmt.Errorf("в наборе %d вопросов, ожидалось 10", len(questions))
	}
	kinds := map[string]int{}
	ids := map[string]bool{}
	for _, question := range questions {
		if question.ID == "" || ids[question.ID] {
			return fmt.Errorf("пустой или повторный id %q", question.ID)
		}
		ids[question.ID] = true
		kinds[question.Kind]++
		if strings.TrimSpace(question.Question) == "" || strings.TrimSpace(question.Expectation) == "" {
			return fmt.Errorf("%s: вопрос и ожидание обязательны", question.ID)
		}
		for _, fact := range question.Facts {
			if fact.Name == "" || len(fact.Patterns) == 0 {
				return fmt.Errorf("%s: факт без имени или шаблонов", question.ID)
			}
			for _, pattern := range fact.Patterns {
				if _, err := compilePattern(pattern); err != nil {
					return fmt.Errorf("%s: неверный шаблон %q: %w", question.ID, pattern, err)
				}
			}
		}
		for _, pattern := range question.Neighbours {
			if _, err := compilePattern(pattern); err != nil {
				return fmt.Errorf("%s: неверный neighbour %q: %w", question.ID, pattern, err)
			}
		}
	}
	if kinds["in_base"] != 7 || kinds["general"] != 1 || kinds["out_of_base"] != 2 {
		return fmt.Errorf("виды вопросов: in_base=%d general=%d out_of_base=%d, ожидалось 7/1/2", kinds["in_base"], kinds["general"], kinds["out_of_base"])
	}
	return nil
}
