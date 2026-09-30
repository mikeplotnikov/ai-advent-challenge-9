package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// The answer prompts are day 22's, verbatim: the only thing day 23 changes is which
// chunks reach them. A test compares them with day-22/showcase.json.
const SystemPrompt = "Ты отвечаешь на вопросы о репозитории ai-advent-challenge-9 — решениях заданий челленджа AI Advent Challenge #9. Отвечай по-русски, по существу, не длиннее пяти предложений. Если сведений для ответа у тебя нет, ответь ровно одной строкой: [[НЕТ ДАННЫХ]] — и ничего не придумывай."
const RAGUserTemplate = "Фрагменты базы знаний:\n[<ранг>] source: <source> · section: <section> · chunk_id: <chunk_id>\n<текст чанка>\n\nОтвечай только по этим фрагментам. После каждого утверждения укажи источник в виде [источник: <source>]. Если во фрагментах ответа нет — ответь [[НЕТ ДАННЫХ]].\nВопрос: <вопрос>"

const RewriteSystemPrompt = "Ты переписываешь вопрос пользователя в поисковый запрос по базе документов репозитория ai-advent-challenge-9 (решения заданий челленджа AI Advent Challenge #9: README и RESULTS по дням, агенты, замеры). Сохрани смысл, раскрой местоимения и сокращения, добавь 3–6 ключевых слов, которые вероятно стоят в тексте ответа. На вопрос не отвечай. Верни одну строку — запрос, без пояснений."
const RewriteUserTemplate = "Вопрос: <вопрос>"

const RerankSystemPrompt = `Ты оцениваешь, помогает ли фрагмент базы знаний ответить на вопрос. Оценки: 3 — фрагмент прямо содержит ответ; 2 — содержит существенную часть ответа; 1 — та же тема, но ответа нет; 0 — не относится к вопросу. Верни только JSON вида {"scores":[{"id":1,"score":0}]} — ровно по одному элементу на каждый фрагмент.`
const RerankUserTemplate = "Фрагменты:\n[<id>] source: <source> · section: <section>\n<текст чанка>\n\nВопрос: <вопрос>"

func rewriteMessages(question string) []llm.Message {
	return []llm.Message{{Role: "system", Content: RewriteSystemPrompt}, {Role: "user", Content: "Вопрос: " + promptSafe(question)}}
}

func rerankMessages(question string, candidates []Candidate) []llm.Message {
	var user strings.Builder
	user.WriteString("Фрагменты:\n")
	for i, candidate := range candidates {
		if i > 0 {
			user.WriteByte('\n')
		}
		fmt.Fprintf(&user, "[%d] source: %s · section: %s\n%s\n", i+1,
			promptSafe(candidate.Source), promptSafe(candidate.Section), promptSafe(candidate.Text))
	}
	user.WriteString("\nВопрос: ")
	user.WriteString(promptSafe(question))
	return []llm.Message{{Role: "system", Content: RerankSystemPrompt}, {Role: "user", Content: user.String()}}
}

// ragMessages is day 22's builder; the rank printed is the chunk's place in the context.
func ragMessages(question string, context []Candidate) []llm.Message {
	var user strings.Builder
	user.WriteString("Фрагменты базы знаний:\n")
	for i, chunk := range context {
		if i > 0 {
			user.WriteByte('\n')
		}
		fmt.Fprintf(&user, "[%d] source: %s · section: %s · chunk_id: %s\n%s\n", chunk.Position,
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
	parts := []string{SystemPrompt, RAGUserTemplate, RewriteSystemPrompt, RewriteUserTemplate, RerankSystemPrompt, RerankUserTemplate}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}

func promptTexts() map[string]string {
	return map[string]string{
		"system": SystemPrompt, "rag_user": RAGUserTemplate,
		"rewrite_system": RewriteSystemPrompt, "rewrite_user": RewriteUserTemplate,
		"rerank_system": RerankSystemPrompt, "rerank_user": RerankUserTemplate,
	}
}
