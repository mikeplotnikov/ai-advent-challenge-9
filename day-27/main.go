package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed index.html
var page []byte

//go:embed app.js
var script []byte

//go:embed style.css
var style []byte

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type chatRequest struct {
	Messages []message `json:"messages"`
}
type ollamaRequest struct {
	Model    string         `json:"model"`
	Messages []message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Think    bool           `json:"think"`
	Options  map[string]any `json:"options"`
}
type ollamaResponse struct {
	Model      string  `json:"model"`
	Message    message `json:"message"`
	Done       bool    `json:"done"`
	DoneReason string  `json:"done_reason"`
	Error      string  `json:"error"`
}
type identity struct {
	Model    string `json:"model"`
	Endpoint string `json:"endpoint"`
	Digest   string `json:"digest"`
}
type turn struct {
	Identity  identity        `json:"identity"`
	Request   ollamaRequest   `json:"request"`
	Response  json.RawMessage `json:"response"`
	WallMS    int64           `json:"wall_ms"`
	CreatedAt string          `json:"created_at"`
}
type app struct {
	client                *http.Client
	endpoint, model, host string
}

func localClient(endpoint string) (*http.Client, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, "", err
	}
	host := u.Hostname()
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, "", fmt.Errorf("only an HTTP loopback Ollama endpoint is allowed")
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	u.Host = net.JoinHostPort(host, port)
	u.Path = ""
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Transport: transport, Timeout: 3 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("redirects are not allowed") }}, u.String(), nil
}

func (a *app) call(ctx context.Context, path string, payload any, target any) (json.RawMessage, error) {
	method := http.MethodGet
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, a.endpoint+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Ollama недоступна: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, fmt.Errorf("ответ Ollama превышает 2 MiB")
	}
	if res.StatusCode != 200 {
		var envelope struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &envelope)
		return nil, fmt.Errorf("Ollama %s: HTTP %d %s", path, res.StatusCode, envelope.Error)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return nil, fmt.Errorf("Ollama вернула некорректный JSON: %w", err)
	}
	return data, nil
}

func (a *app) preflight(ctx context.Context) (identity, error) {
	id := identity{Model: a.model, Endpoint: a.endpoint}
	if strings.Contains(strings.ToLower(a.model), "cloud") {
		return id, fmt.Errorf("облачные модели запрещены")
	}
	var tags struct {
		Models []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if _, err := a.call(ctx, "/api/tags", nil, &tags); err != nil {
		return id, err
	}
	for _, model := range tags.Models {
		if model.Name == a.model {
			id.Digest = model.Digest
		}
	}
	if id.Digest == "" {
		return id, fmt.Errorf("модель %s не установлена; выполните ollama pull %s", a.model, a.model)
	}
	var meta struct {
		RemoteHost   string   `json:"remote_host"`
		RemoteModel  string   `json:"remote_model"`
		Capabilities []string `json:"capabilities"`
	}
	if _, err := a.call(ctx, "/api/show", map[string]string{"model": a.model}, &meta); err != nil {
		return id, err
	}
	if meta.RemoteHost != "" || meta.RemoteModel != "" {
		return id, fmt.Errorf("удалённые модели запрещены")
	}
	for _, c := range meta.Capabilities {
		if c == "completion" {
			return id, nil
		}
	}
	return id, fmt.Errorf("у модели нет возможности completion")
}

func validate(messages []message) error {
	if len(messages) == 0 || len(messages) > 15 || len(messages)%2 != 1 {
		return fmt.Errorf("нужны 1–15 чередующихся сообщений, заканчивающихся запросом пользователя")
	}
	for i, m := range messages {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if m.Role != role || strings.TrimSpace(m.Content) == "" || len(m.Content) > 16000 {
			return fmt.Errorf("некорректная роль, пустое или слишком длинное сообщение (лимит 16000 байт)")
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	// Reject foreign Host/Origin values before exposing the local inference service.
	if r.Host != a.host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+a.host) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(w, 403, fmt.Errorf("используйте локальный адрес приложения"))
		return
	}
	switch r.URL.Path {
	case "/", "/app.js", "/style.css":
		if r.Method != "GET" {
			fail(w, 405, fmt.Errorf("нужен GET"))
			return
		}
		data, mime := page, "text/html; charset=utf-8"
		if r.URL.Path == "/app.js" {
			data, mime = script, "text/javascript; charset=utf-8"
		}
		if r.URL.Path == "/style.css" {
			data, mime = style, "text/css; charset=utf-8"
		}
		w.Header().Set("Content-Type", mime)
		_, _ = w.Write(data)
	case "/api/config":
		if r.Method != "GET" {
			fail(w, 405, fmt.Errorf("нужен GET"))
			return
		}
		id, err := a.preflight(r.Context())
		if err != nil {
			fail(w, 503, err)
			return
		}
		writeJSON(w, 200, id)
	case "/api/chat":
		if r.Method != "POST" {
			fail(w, 405, fmt.Errorf("нужен POST"))
			return
		}
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
			fail(w, 415, fmt.Errorf("нужен application/json"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var input chatRequest
		if err := decoder.Decode(&input); err != nil {
			fail(w, 400, fmt.Errorf("некорректный запрос: %w", err))
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			fail(w, 400, fmt.Errorf("нужен один JSON-объект"))
			return
		}
		if err := validate(input.Messages); err != nil {
			fail(w, 400, err)
			return
		}
		id, err := a.preflight(r.Context())
		if err != nil {
			fail(w, 503, err)
			return
		}
		messages := append([]message{{Role: "system", Content: "Ты локальный помощник. Отвечай по-русски, кратко, без рассуждений. Учитывай предыдущие сообщения. /no_think"}}, input.Messages...)
		request := ollamaRequest{Model: a.model, Messages: messages, Stream: false, Think: false, Options: map[string]any{"temperature": 0, "num_predict": 2048, "num_ctx": 8192}}
		start := time.Now()
		var response ollamaResponse
		raw, err := a.call(r.Context(), "/api/chat", request, &response)
		if err != nil {
			fail(w, 502, err)
			return
		}
		if response.Error != "" {
			fail(w, 502, fmt.Errorf("Ollama: %s", response.Error))
			return
		}
		if !response.Done || response.DoneReason != "stop" || response.Message.Role != "assistant" || strings.TrimSpace(response.Message.Content) == "" || response.Model != a.model {
			if response.DoneReason == "length" {
				fail(w, 502, fmt.Errorf("Модель вернула незавершённый или некорректный ответ (done_reason=length; лимит генерации — %v токенов). Попробуйте другую формулировку запроса", request.Options["num_predict"]))
			} else {
				fail(w, 502, fmt.Errorf("Модель вернула некорректный или незавершённый ответ (done_reason=%s)", response.DoneReason))
			}
			return
		}
		writeJSON(w, 200, turn{Identity: id, Request: request, Response: raw, WallMS: time.Since(start).Milliseconds(), CreatedAt: start.UTC().Format(time.RFC3339)})
	default:
		http.NotFound(w, r)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:8027", "literal loopback address")
	endpoint := flag.String("ollama", "http://127.0.0.1:11434", "local Ollama URL")
	model := flag.String("model", "qwen3:4b", "installed local model")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("listen must be a literal loopback IP:port")
	}
	client, base, err := localClient(*endpoint)
	if err != nil {
		return err
	}
	a := &app{client: client, endpoint: base, model: *model, host: *listen}
	if _, err := a.preflight(context.Background()); err != nil {
		return err
	}
	server := &http.Server{Addr: *listen, Handler: a, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 4 * time.Minute, IdleTimeout: 30 * time.Second}
	fmt.Printf("Локальный чат: http://%s\nМодель: %s → %s\nCtrl+C — остановить\n", *listen, *model, base)
	return server.ListenAndServe()
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
