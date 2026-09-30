package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

const SystemPrompt = "Ты отвечаешь на вопросы о репозитории ai-advent-challenge-9 — решениях заданий челленджа AI Advent Challenge #9. Отвечай по-русски, по существу, не длиннее пяти предложений. Если сведений для ответа у тебя нет, ответь ровно одной строкой: [[НЕТ ДАННЫХ]] — и ничего не придумывай."
const NoRAGUserTemplate = "Вопрос: <вопрос>"
const RAGUserTemplate = "Фрагменты базы знаний:\n[<ранг>] source: <source> · section: <section> · chunk_id: <chunk_id>\n<текст чанка>\n\nОтвечай только по этим фрагментам. После каждого утверждения укажи источник в виде [источник: <source>]. Если во фрагментах ответа нет — ответь [[НЕТ ДАННЫХ]].\nВопрос: <вопрос>"

func noRAGMessages(question string) []llm.Message {
	safeQuestion := promptSafe(question)
	return []llm.Message{{Role: "system", Content: SystemPrompt}, {Role: "user", Content: "Вопрос: " + safeQuestion}}
}

func ragMessages(question string, chunks []FoundChunk) []llm.Message {
	var user strings.Builder
	user.WriteString("Фрагменты базы знаний:\n")
	for i, chunk := range chunks {
		if i > 0 {
			user.WriteByte('\n')
		}
		fmt.Fprintf(&user, "[%d] source: %s · section: %s · chunk_id: %s\n%s\n", chunk.Rank,
			promptSafe(chunk.Source), promptSafe(chunk.Section), promptSafe(chunk.ChunkID), promptSafe(chunk.Text))
	}
	user.WriteString("\nОтвечай только по этим фрагментам. После каждого утверждения укажи источник в виде [источник: <source>]. Если во фрагментах ответа нет — ответь [[НЕТ ДАННЫХ]].\nВопрос: ")
	user.WriteString(promptSafe(question))
	return []llm.Message{{Role: "system", Content: SystemPrompt}, {Role: "user", Content: user.String()}}
}

func promptSafe(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return '?'
		}
		return r
	}, text)
}

func promptsSHA256() string {
	digest := sha256.Sum256([]byte(SystemPrompt + "\x00" + NoRAGUserTemplate + "\x00" + RAGUserTemplate))
	return hex.EncodeToString(digest[:])
}
