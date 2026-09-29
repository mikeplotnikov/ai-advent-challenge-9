package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const defaultQuestionsPath = "day-21/eval/questions.json"

type Question struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Source   string `json:"source"`
	Evidence string `json:"evidence"`
}

func loadQuestions(path string) ([]Question, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("прочитать вопросы: %w", err)
	}
	var questions []Question
	if err := json.Unmarshal(raw, &questions); err != nil {
		return nil, "", fmt.Errorf("разобрать вопросы: %w", err)
	}
	digest := sha256.Sum256(raw)
	return questions, hex.EncodeToString(digest[:]), nil
}

func validateQuestions(questions []Question, documents []Document) error {
	if len(questions) != 40 {
		return fmt.Errorf("в eval/questions.json %d вопросов, нужно 40", len(questions))
	}
	docBySource := make(map[string]Document, len(documents))
	for _, document := range documents {
		docBySource[document.Source] = document
	}
	fixed := fixedChunks(documents)
	structure := structureChunks(documents)
	ids := map[string]bool{}
	sources := map[string]bool{}
	for _, question := range questions {
		if question.ID == "" || ids[question.ID] {
			return fmt.Errorf("пустой или повторный id вопроса %q", question.ID)
		}
		ids[question.ID] = true
		sources[question.Source] = true
		if length := len([]rune(question.Evidence)); length < 40 || length > 150 {
			return fmt.Errorf("%s: цитата длиной %d рун, нужно 40–150", question.ID, length)
		}
		document, ok := docBySource[question.Source]
		if !ok {
			return fmt.Errorf("%s: файла %s нет в корпусе", question.ID, question.Source)
		}
		needle := collapseWhitespace(question.Evidence)
		if !strings.Contains(collapseWhitespace(document.Text), needle) {
			return fmt.Errorf("%s: цитаты нет в указанном source", question.ID)
		}
		occurrences := 0
		for _, candidate := range documents {
			occurrences += strings.Count(collapseWhitespace(candidate.Text), needle)
		}
		if occurrences != 1 {
			return fmt.Errorf("%s: цитата встречается в корпусе %d раз, нужно 1", question.ID, occurrences)
		}
		if commonFourWords(question.Question, question.Evidence) {
			return fmt.Errorf("%s: вопрос повторяет четыре слова цитаты подряд", question.ID)
		}
		if !evidenceFits(question, fixed) {
			return fmt.Errorf("%s: цитата не помещается целиком ни в один fixed-чанк", question.ID)
		}
		if !evidenceFits(question, structure) {
			return fmt.Errorf("%s: цитата не помещается целиком ни в один structure-чанк", question.ID)
		}
	}
	if len(sources) < 15 {
		return fmt.Errorf("вопросы покрывают %d файлов, нужно не меньше 15", len(sources))
	}
	return nil
}

func commonFourWords(question, evidence string) bool {
	evidenceWords := wordTokens(evidence)
	grams := map[string]bool{}
	for i := 0; i+4 <= len(evidenceWords); i++ {
		grams[strings.Join(evidenceWords[i:i+4], " ")] = true
	}
	questionWords := wordTokens(question)
	for i := 0; i+4 <= len(questionWords); i++ {
		if grams[strings.Join(questionWords[i:i+4], " ")] {
			return true
		}
	}
	return false
}

func evidenceFits(question Question, chunks []Chunk) bool {
	for _, chunk := range chunks {
		if chunk.Source == question.Source && containsEvidence(chunk.Text, question.Evidence) {
			return true
		}
	}
	return false
}
