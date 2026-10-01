package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAnswerMessagesExcludeRetrievalAndGroundTruthAnnotations(t *testing.T) {
	chunk := Candidate{Source: "source.md", Section: "section", ChunkID: "chunk-1", Text: "Дословный текст.", Rank: 1, Similarity: 0.7, Tokens: 12, Position: 1}
	before := answerMessages("Вопрос?", []Candidate{chunk})
	score := 3
	chunk.SourceHit = true
	chunk.EvidenceHit = true
	chunk.Rank = 9
	chunk.Similarity = 0.99
	chunk.Tokens = 99
	chunk.Position = 3
	chunk.Cut = "score"
	chunk.RerankScore = &score
	after := answerMessages("Вопрос?", []Candidate{chunk})
	if !reflect.DeepEqual(before, after) {
		t.Fatal("model input changes when retrieval/gold annotations change")
	}
	user := before[1].Content
	prefix := "Фрагменты (JSON):\n"
	suffix := "\nВопрос: Вопрос?"
	if !strings.HasPrefix(user, prefix) || !strings.HasSuffix(user, suffix) {
		t.Fatal("missing context/question framing")
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(user, prefix), suffix)
	var got []map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{"source": "source.md", "section": "section", "chunk_id": "chunk-1", "text": "Дословный текст."}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("citation input contains unwanted fields: %#v", got)
	}
}
