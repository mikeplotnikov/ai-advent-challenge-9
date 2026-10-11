// Day 30: a local LLM (Ollama) exposed as a private HTTP service.
//
// The service sits in front of Ollama and adds what a shared model needs:
// a bearer token, a per-client rate limit, a one-at-a-time queue and a
// context-size limit that rejects oversized requests before they reach the model.
// It is published to the internet through a Cloudflare Quick Tunnel (see run.sh).
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const systemPrompt = "Ты приватный ассистент. Отвечай по-русски, коротко и по делу."

type config struct {
	addr       string
	ollama     string
	model      string
	token      string
	ratePerMin int
	maxChars   int
	numCtx     int
	numPredict int
	maxWaiting int
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type event struct {
	At     time.Time `json:"at"`
	Client string    `json:"client"`
	Status int       `json:"status"`
	Ahead  int       `json:"ahead"`
	WaitMs int64     `json:"wait_ms"`
	GenMs  int64     `json:"gen_ms"`
}

type server struct {
	cfg    config
	client *http.Client
	slot   chan struct{} // capacity 1: one generation at a time

	mu      sync.Mutex
	inQueue int // waiting + generating
	hits    map[string][]time.Time
	recent  []event
}

func main() {
	var cfg config
	check := flag.String("check", "", "run network checks against this public URL and write day-30/check.json")
	flag.StringVar(&cfg.addr, "addr", "127.0.0.1:8030", "listen address")
	flag.StringVar(&cfg.ollama, "ollama", "http://127.0.0.1:11434", "Ollama base URL")
	flag.StringVar(&cfg.model, "model", "qwen3:4b-instruct-2507-q4_K_M", "Ollama model (the instruct variant: the default qwen3:4b tag always reasons)")
	flag.IntVar(&cfg.ratePerMin, "rate", 8, "chat requests per minute per client IP")
	flag.IntVar(&cfg.maxChars, "max-chars", 8000, "max characters across all messages of one request")
	flag.IntVar(&cfg.numCtx, "num-ctx", 4096, "model context window, tokens")
	flag.IntVar(&cfg.numPredict, "num-predict", 512, "max output tokens")
	flag.IntVar(&cfg.maxWaiting, "max-queue", 16, "max requests in the queue, including the one generating")
	flag.Parse()
	cfg.token = os.Getenv("DAY30_TOKEN")
	if cfg.token == "" {
		log.Fatal("DAY30_TOKEN is required")
	}
	if *check != "" {
		if err := runCheck(strings.TrimRight(*check, "/"), cfg.token); err != nil {
			log.Fatal(err)
		}
		return
	}
	s := &server{cfg: cfg, client: &http.Client{Timeout: 3 * time.Minute}, slot: make(chan struct{}, 1), hits: map[string][]time.Time{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("GET /api/status", s.auth(s.status))
	mux.HandleFunc("POST /api/chat", s.auth(s.chat))
	log.Printf("day-30 listening on %s, model %s, rate %d/min, max %d chars, num_ctx %d", cfg.addr, cfg.model, cfg.ratePerMin, cfg.maxChars, cfg.numCtx)
	log.Fatal(http.ListenAndServe(cfg.addr, cors(mux)))
}

// cors lets the static showcase page on another origin call the API. The token
// travels in a header, not a cookie, so a wildcard origin grants nothing by itself.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Expose-Headers", "Retry-After")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) auth(next http.HandlerFunc) http.HandlerFunc {
	want := []byte("Bearer " + s.cfg.token)
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			writeJSON(w, 401, map[string]any{"error": "Нужен токен доступа"})
			return
		}
		next(w, r)
	}
}

// clientID is the visitor IP as reported by Cloudflare, shortened to a stable
// pseudonym so the status log can tell clients apart without showing addresses.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func pseudonym(ip string) string {
	sum := sha256.Sum256([]byte(ip))
	return strings.ToUpper(hex.EncodeToString(sum[:])[:4])
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"model": s.cfg.model, "you": pseudonym(clientIP(r)), "in_queue": s.inQueue,
		"limits": map[string]int{"rate_per_minute": s.cfg.ratePerMin, "max_chars": s.cfg.maxChars,
			"num_ctx": s.cfg.numCtx, "num_predict": s.cfg.numPredict, "max_queue": s.cfg.maxWaiting},
		"recent": s.recent,
	})
}

func (s *server) record(e event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.At = time.Now()
	s.recent = append(s.recent, e)
	if len(s.recent) > 20 {
		s.recent = s.recent[len(s.recent)-20:]
	}
	log.Printf("client %s status %d ahead %d wait %dms gen %dms", e.Client, e.Status, e.Ahead, e.WaitMs, e.GenMs)
}

// allow applies a sliding one-minute window per client IP. It returns the
// number of requests left and, when refused, how long until a slot frees up.
func (s *server) allow(ip string) (int, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	kept := s.hits[ip][:0]
	for _, t := range s.hits[ip] {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	if len(kept) >= s.cfg.ratePerMin {
		s.hits[ip] = kept
		return 0, time.Minute - now.Sub(kept[0])
	}
	s.hits[ip] = append(kept, now)
	return s.cfg.ratePerMin - len(s.hits[ip]), 0
}

func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	who := pseudonym(ip)
	left, retry := s.allow(ip)
	if retry > 0 {
		secs := int(retry.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		s.record(event{Client: who, Status: 429})
		writeJSON(w, 429, map[string]any{"error": fmt.Sprintf("Лимит %d запросов в минуту исчерпан, повторите через %d с", s.cfg.ratePerMin, secs), "retry_after": secs})
		return
	}

	var req struct {
		Messages []message `json:"messages"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.cfg.maxChars)*4+64<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": "Некорректный JSON или слишком большое тело запроса"})
		return
	}
	chars := 0
	for _, m := range req.Messages {
		if (m.Role != "user" && m.Role != "assistant") || strings.TrimSpace(m.Content) == "" {
			writeJSON(w, 400, map[string]any{"error": "Сообщения: роли user/assistant, текст не пустой"})
			return
		}
		chars += utf8.RuneCountInString(m.Content)
	}
	if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != "user" {
		writeJSON(w, 400, map[string]any{"error": "Последнее сообщение должно быть от пользователя"})
		return
	}
	if chars > s.cfg.maxChars {
		s.record(event{Client: who, Status: 413})
		writeJSON(w, 413, map[string]any{"error": fmt.Sprintf("Контекст %d символов больше лимита %d — начните новый диалог", chars, s.cfg.maxChars), "chars": chars, "max_chars": s.cfg.maxChars})
		return
	}

	s.mu.Lock()
	if s.inQueue >= s.cfg.maxWaiting {
		s.mu.Unlock()
		s.record(event{Client: who, Status: 503})
		writeJSON(w, 503, map[string]any{"error": "Очередь заполнена, попробуйте позже"})
		return
	}
	ahead := s.inQueue
	s.inQueue++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.inQueue--; s.mu.Unlock() }()

	queued := time.Now()
	select {
	case s.slot <- struct{}{}:
	case <-r.Context().Done():
		return // client gave up while waiting
	}
	defer func() { <-s.slot }()
	waitMs := time.Since(queued).Milliseconds()

	started := time.Now()
	out, err := s.generate(r.Context(), req.Messages)
	genMs := time.Since(started).Milliseconds()
	if err != nil {
		s.record(event{Client: who, Status: 502, Ahead: ahead, WaitMs: waitMs, GenMs: genMs})
		writeJSON(w, 502, map[string]any{"error": "Модель не ответила: " + err.Error()})
		return
	}
	// A thinking-only model ignores think=false and writes its reasoning into
	// the answer, closed by </think>; only the text after it is the answer.
	reply := out.Message.Content
	if i := strings.LastIndex(reply, "</think>"); i >= 0 {
		reply = reply[i+len("</think>"):]
	}
	reply = strings.TrimSpace(reply)
	s.record(event{Client: who, Status: 200, Ahead: ahead, WaitMs: waitMs, GenMs: genMs})
	writeJSON(w, 200, map[string]any{
		"reply": reply, "client": who, "ahead": ahead, "wait_ms": waitMs, "gen_ms": genMs,
		"prompt_tokens": out.PromptEvalCount, "output_tokens": out.EvalCount, "done_reason": out.DoneReason,
		"chars": chars, "rate_left": left,
	})
}

type ollamaReply struct {
	Message         message `json:"message"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	EvalCount       int     `json:"eval_count"`
	DoneReason      string  `json:"done_reason"`
	Error           string  `json:"error"`
}

func (s *server) generate(ctx context.Context, msgs []message) (ollamaReply, error) {
	body, _ := json.Marshal(map[string]any{
		"model":    s.cfg.model,
		"messages": append([]message{{"system", systemPrompt}}, msgs...),
		"stream":   false,
		"think":    false,
		// Fail instead of silently dropping the start of the conversation.
		"truncate": false,
		"shift":    false,
		"options":  map[string]any{"num_ctx": s.cfg.numCtx, "num_predict": s.cfg.numPredict, "temperature": 0.2},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.ollama+"/api/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return ollamaReply{}, err
	}
	defer resp.Body.Close()
	var out ollamaReply
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, err
	}
	if resp.StatusCode != 200 || out.Error != "" {
		return out, fmt.Errorf("ollama %d: %s", resp.StatusCode, out.Error)
	}
	return out, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
