// Command day-33 is a support assistant for «Парта», a made-up online school: a web
// chat where DeepSeek answers a user's question from the product docs and FAQ (RAG over
// local bge-m3 embeddings) and from that user's CRM profile and ticket, served by an
// MCP server over a JSON file.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/docrag"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/supportmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	embedModel = "bge-m3"
	keyEnv     = "DEEPSEEK_API_KEY_DAY33"

	maxMessageRunes = 1000
	maxHistory      = 12
	maxTurnRunes    = 4000
	ratePerMinute   = 20
)

//go:embed index.html
var page embed.FS

type config struct {
	addr, product, data, indexPath, ollama, server string
	k                                              int
}

func main() {
	var cfg config
	build := flag.Bool("index", false, "построить индекс документации продукта и выйти")
	eval := flag.String("eval", "", "прогнать сценарии из файла (day-33/scenarios.json) и записать результаты в day-33/eval-results.json")
	flag.StringVar(&cfg.addr, "addr", "127.0.0.1:8033", "адрес веб-чата")
	flag.StringVar(&cfg.product, "product", filepath.Join("day-33", "product"), "документация продукта: README.md + docs/")
	flag.StringVar(&cfg.data, "data", filepath.Join("day-33", "data", "crm.json"), "JSON с пользователями, тикетами и инцидентами")
	flag.StringVar(&cfg.indexPath, "index-file", filepath.Join("day-33", "index", "docs.json"), "файл индекса")
	flag.StringVar(&cfg.ollama, "ollama", "http://127.0.0.1:11434", "адрес Ollama для эмбеддингов")
	flag.StringVar(&cfg.server, "server", filepath.Join("bin", "day33-mcp"), "бинарник MCP-сервера CRM (go build -o bin/day33-mcp ./day-33/mcp-server)")
	flag.IntVar(&cfg.k, "k", 4, "сколько фрагментов документации передавать модели")
	flag.Parse()

	embedder := &rag.OllamaClient{BaseURL: cfg.ollama, Model: embedModel}
	ctx := context.Background()
	if *build {
		fail(runIndex(ctx, cfg, embedder))
		return
	}
	llm.LoadDotEnv(".env")
	a, closeFn, err := open(ctx, cfg, embedder)
	if err != nil {
		fail(err)
	}
	defer closeFn()
	if *eval != "" {
		fail(runEval(ctx, a, *eval, filepath.Join("day-33", "eval-results.json")))
		return
	}
	fmt.Printf("Чат поддержки: http://%s\n", cfg.addr)
	fail(http.ListenAndServe(cfg.addr, newHandler(a)))
}

func runIndex(ctx context.Context, cfg config, embedder *rag.OllamaClient) error {
	fmt.Printf("Индексирую документацию %s (эмбеддинги %s через Ollama)\n", cfg.product, embedModel)
	file, err := docrag.Build(ctx, cfg.product, embedder, embedModel, func(source string, chunks int) {
		fmt.Printf("  %-20s %3d фрагм.\n", source, chunks)
	})
	if err != nil {
		return err
	}
	if err := docrag.Save(cfg.indexPath, file); err != nil {
		return err
	}
	fmt.Printf("Готово: файлов %d, фрагментов %d → %s\n", len(file.Files), file.Header.ChunkCount, cfg.indexPath)
	return nil
}

func open(ctx context.Context, cfg config, embedder rag.Embedder) (*assistant, func(), error) {
	index, err := docrag.Load(cfg.indexPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%w (постройте индекс: go run ./day-33 -index)", err)
	}
	if stale, err := docrag.Stale(cfg.product, index); err != nil {
		return nil, nil, err
	} else if len(stale) > 0 {
		return nil, nil, fmt.Errorf("документация изменилась после индексации (%v): go run ./day-33 -index", stale)
	}
	model, err := llm.NewWithKeyEnv(keyEnv)
	if err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(cfg.server); err != nil {
		return nil, nil, fmt.Errorf("нет MCP-сервера %s: go build -o %s ./day-33/mcp-server", cfg.server, cfg.server)
	}
	command := exec.Command(cfg.server, "-data", cfg.data)
	transport := mcpclient.Transport{MCP: &mcp.CommandTransport{Command: command}, Description: "stdio", Command: cfg.server}
	session, err := mcpclient.NewNamed("day33-support", "1.0.0").Open(ctx, transport)
	if err != nil {
		return nil, nil, fmt.Errorf("MCP-сервер не запустился: %w", err)
	}
	fmt.Printf("Подключено: MCP %s %s (stdio) · индекс: фрагментов %d · модель %s\n",
		session.ServerName, session.ServerVersion, index.Header.ChunkCount, model.Model)
	a := &assistant{dataPath: cfg.data, embedder: embedder, index: index, model: model, modelName: model.Model, session: session, k: cfg.k}
	return a, func() { _ = session.Close() }, nil
}

type handler struct {
	a    *assistant
	slot chan struct{} // capacity 1: one answer at a time, the MCP session is shared

	mu   sync.Mutex
	hits []time.Time
	now  func() time.Time
}

func newHandler(a *assistant) http.Handler {
	h := &handler{a: a, slot: make(chan struct{}, 1), now: time.Now}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(page))
	mux.HandleFunc("GET /api/users", h.users)
	mux.HandleFunc("POST /api/chat", h.chat)
	return mux
}

type userCard struct {
	ID      string              `json:"id"`
	Name    string              `json:"name"`
	Plan    string              `json:"plan"`
	Tickets []supportmcp.Ticket `json:"tickets"`
}

// users feeds the "войти как" switch of the demo page; it is not part of the assistant.
func (h *handler) users(w http.ResponseWriter, _ *http.Request) {
	crm, err := supportmcp.Load(h.a.dataPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cards := []userCard{}
	for _, u := range crm.Users {
		card := userCard{ID: u.ID, Name: u.Name, Plan: u.Plan, Tickets: []supportmcp.Ticket{}}
		for _, t := range crm.Tickets {
			if t.UserID == u.ID {
				card.Tickets = append(card.Tickets, t)
			}
		}
		cards = append(cards, card)
	}
	writeJSON(w, http.StatusOK, map[string]any{"as_of": crm.AsOf, "users": cards})
}

func (h *handler) chat(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "тело запроса — JSON до 64 КБ")
		return
	}
	if err := validate(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.allow() {
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("не больше %d вопросов в минуту, подождите", ratePerMinute))
		return
	}
	select {
	case h.slot <- struct{}{}:
		defer func() { <-h.slot }()
	case <-r.Context().Done():
		return
	}
	out, err := h.a.reply(r.Context(), req)
	if err != nil {
		// The detail (paths, provider errors) stays in the server log, not on a public page.
		log.Printf("chat %s/%s: %v", req.UserID, req.TicketID, err)
		writeError(w, http.StatusBadGateway, "ассистент не смог ответить, попробуйте ещё раз")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func validate(req request) error {
	if req.UserID == "" || req.TicketID == "" {
		return errors.New("нужны user_id и ticket_id")
	}
	if n := utf8.RuneCountInString(req.Message); n == 0 || n > maxMessageRunes {
		return fmt.Errorf("сообщение — от 1 до %d символов", maxMessageRunes)
	}
	if len(req.History) > maxHistory {
		return fmt.Errorf("история — не больше %d реплик", maxHistory)
	}
	for _, t := range req.History {
		if t.Role != "user" && t.Role != "assistant" {
			return fmt.Errorf("роль %q в истории недопустима", t.Role)
		}
		if utf8.RuneCountInString(t.Content) > maxTurnRunes {
			return fmt.Errorf("реплика истории длиннее %d символов", maxTurnRunes)
		}
	}
	return nil
}

// allow is a global sliding-window limit: the page goes public through a tunnel, and
// every question spends money on the provider.
func (h *handler) allow() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	kept := h.hits[:0]
	for _, at := range h.hits {
		if now.Sub(at) < time.Minute {
			kept = append(kept, at)
		}
	}
	h.hits = kept
	if len(h.hits) >= ratePerMinute {
		return false
	}
	h.hits = append(h.hits, now)
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
