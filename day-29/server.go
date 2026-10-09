package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (a *app) compare(ctx context.Context, q string, id string, emit func(string, any)) map[string]any {
	p, _ := findProfile(id)
	base, _ := findProfile("baseline")
	// Retrieve once. Generation never participates in retrieval.
	retrieval := a.retrieve(ctx, q, emit)
	if retrieval.Error == "" && len(retrieval.Context) > 0 {
		emit("baseline", nil)
	}
	before := a.generateRetrieved(ctx, retrieval, base)
	var after result
	if ctx.Err() != nil {
		after = result{Question: q, Context: retrieval.Context, Attempts: []attempt{}, Error: "Запрос отменён", ErrorKind: "technical"}
	} else {
		if retrieval.Error == "" && len(retrieval.Context) > 0 {
			emit("optimized", nil)
		}
		after = a.generateRetrieved(ctx, retrieval, p)
	}
	return map[string]any{"question": q, "context": retrieval.Context, "baseline": before, "optimized": after, "profile": id}
}
func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	if r.Host != a.host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+a.host) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(w, 403, fmt.Errorf("используйте локальный адрес приложения"))
		return
	}
	if r.URL.Path == "/api/ask" {
		if r.Method != "POST" {
			fail(w, 405, fmt.Errorf("нужен POST"))
			return
		}
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
			fail(w, 415, fmt.Errorf("нужен application/json"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		var in struct {
			Question string `json:"question"`
			Profile  string `json:"profile"`
		}
		if e := d.Decode(&in); e != nil {
			fail(w, 400, e)
			return
		}
		if e := d.Decode(new(any)); e != io.EOF {
			fail(w, 400, fmt.Errorf("нужен один JSON-объект"))
			return
		}
		if strings.TrimSpace(in.Question) == "" || len(in.Question) > 4000 {
			fail(w, 400, fmt.Errorf("введите вопрос до 4000 байт"))
			return
		}
		if in.Profile == "" {
			in.Profile = a.winner()
		}
		p, e := findProfile(in.Profile)
		if e != nil || p.ID == "baseline" {
			fail(w, 400, fmt.Errorf("выберите профиль для сравнения"))
			return
		}
		select {
		case a.busy <- struct{}{}:
			defer func() { <-a.busy }()
		default:
			fail(w, 429, fmt.Errorf("предыдущий запрос ещё выполняется"))
			return
		}
		base, _ := findProfile("baseline")
		for _, pr := range []profile{base, p} {
			if _, e := a.pinned(r.Context(), pr); e != nil {
				fail(w, 503, e)
				return
			}
		}
		if _, e := a.config(r.Context()); e != nil {
			fail(w, 503, e)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.WriteHeader(200)
		emit := func(kind string, data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"event": kind, "data": data})
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Minute)
		defer cancel()
		emit("result", a.compare(ctx, in.Question, in.Profile, emit))
		return
	}
	if r.Method != "GET" {
		fail(w, 405, fmt.Errorf("нужен GET"))
		return
	}
	switch r.URL.Path {
	case "/api/config", "/config.json":
		c, e := a.config(r.Context())
		if e != nil {
			fail(w, 503, e)
			return
		}
		writeJSON(w, 200, c)
	case "/showcase.json":
		http.ServeFile(w, r, a.capturePath)
	case "/", "/index.html", "/app.js", "/style.css":
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		b, e := assets.ReadFile(path)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", map[string]string{"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "style.css": "text/css; charset=utf-8"}[path])
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}
