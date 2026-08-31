// Day 1, web — the same single API call, but behind a chat page.
// The browser never sees the API key: it talks to this server, the server talks to DeepSeek.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

//go:embed index.html
var assets embed.FS

const (
	systemPrompt = "Ты ассистент, работающий на модели DeepSeek через официальный API DeepSeek. " +
		"Отвечай по-русски, ясно и по делу."
	maxHistory     = 20   // turns kept in one conversation
	maxPromptRunes = 4000 // guard against a pasted novel
)

type chatIn struct {
	Messages []llm.Message `json:"messages"`
}

type chatOut struct {
	Reply     string    `json:"reply,omitempty"`
	Usage     llm.Usage `json:"usage,omitempty"`
	Model     string    `json:"model,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func main() {
	llm.LoadDotEnv(".env")

	client, err := llm.New()
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", serveIndex)
	mux.HandleFunc("POST /api/chat", chatHandler(client))

	addr := ":" + port()
	log.Printf("чат поднят на http://localhost%s (модель %s)", addr, client.Model)
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

func port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "8080"
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := assets.ReadFile("index.html")
	if err != nil {
		http.Error(w, "страница не найдена", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

func chatHandler(client *llm.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in chatIn
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, chatOut{Error: "не разобрать запрос"})
			return
		}

		history := sanitize(in.Messages)
		if len(history) == 0 {
			writeJSON(w, http.StatusBadRequest, chatOut{Error: "пустой запрос"})
			return
		}

		messages := append([]llm.Message{{Role: "system", Content: systemPrompt}}, history...)

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()

		answer, err := client.Ask(ctx, messages)
		if err != nil {
			log.Printf("ошибка вызова модели: %v", err)
			writeJSON(w, http.StatusBadGateway, chatOut{Error: "модель не ответила: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, chatOut{
			Reply:     answer.Content,
			Usage:     answer.Usage,
			Model:     answer.Model,
			RequestID: answer.ID,
		})
	}
}

// sanitize keeps only well-formed user/assistant turns and trims the history.
func sanitize(messages []llm.Message) []llm.Message {
	clean := make([]llm.Message, 0, len(messages))
	for _, m := range messages {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if runes := []rune(content); len(runes) > maxPromptRunes {
			content = string(runes[:maxPromptRunes])
		}
		clean = append(clean, llm.Message{Role: m.Role, Content: content})
	}
	if len(clean) > maxHistory {
		clean = clean[len(clean)-maxHistory:]
	}
	return clean
}

func writeJSON(w http.ResponseWriter, status int, payload chatOut) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
