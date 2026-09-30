package main

import (
	"strings"
	"testing"
)

func TestPromptGolden(t *testing.T) {
	const system = "Ты отвечаешь на вопросы о репозитории ai-advent-challenge-9 — решениях заданий челленджа AI Advent Challenge #9. Отвечай по-русски, по существу, не длиннее пяти предложений. Если сведений для ответа у тебя нет, ответь ровно одной строкой: [[НЕТ ДАННЫХ]] — и ничего не придумывай."
	const noRAG = "Вопрос: <вопрос>"
	const withRAG = "Фрагменты базы знаний:\n[<ранг>] source: <source> · section: <section> · chunk_id: <chunk_id>\n<текст чанка>\n\nОтвечай только по этим фрагментам. После каждого утверждения укажи источник в виде [источник: <source>]. Если во фрагментах ответа нет — ответь [[НЕТ ДАННЫХ]].\nВопрос: <вопрос>"
	if SystemPrompt != system || NoRAGUserTemplate != noRAG || RAGUserTemplate != withRAG {
		t.Fatalf("prompt constants changed\nsystem=%q\nnorag=%q\nrag=%q", SystemPrompt, NoRAGUserTemplate, RAGUserTemplate)
	}
	actual := noRAGMessages("тест")[1].Content
	if actual != "Вопрос: тест" {
		t.Fatalf("unexpected no-RAG prompt: %q", actual)
	}
	lower := strings.ToLower(actual)
	if strings.Contains(lower, "фрагмент") || strings.Contains(lower, "источник") {
		t.Fatalf("no-RAG prompt mentions RAG material: %q", actual)
	}
}

func TestRAGPromptLayout(t *testing.T) {
	chunks := []FoundChunk{{Rank: 1, Source: "a.md", Section: "A", ChunkID: "c1", Text: "one"}, {Rank: 2, Source: "b.md", Section: "B", ChunkID: "c2", Text: "two"}}
	want := "Фрагменты базы знаний:\n[1] source: a.md · section: A · chunk_id: c1\none\n\n[2] source: b.md · section: B · chunk_id: c2\ntwo\n\nОтвечай только по этим фрагментам. После каждого утверждения укажи источник в виде [источник: <source>]. Если во фрагментах ответа нет — ответь [[НЕТ ДАННЫХ]].\nВопрос: why"
	if got := ragMessages("why", chunks)[1].Content; got != want {
		t.Fatalf("prompt mismatch\ngot:  %q\nwant: %q", got, want)
	}
}
