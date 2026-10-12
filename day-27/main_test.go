package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixture(t *testing.T, chat string, remote bool, installed bool, requests *[]ollamaRequest) *app {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			if installed {
				io.WriteString(w, `{"models":[{"name":"qwen3:4b","digest":"test-digest"}]}`)
			} else {
				io.WriteString(w, `{"models":[]}`)
			}
		case "/api/show":
			if remote {
				io.WriteString(w, `{"capabilities":["completion"],"remote_host":"https://cloud.invalid"}`)
			} else {
				io.WriteString(w, `{"capabilities":["completion"]}`)
			}
		case "/api/chat":
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			var got ollamaRequest
			if err := json.Unmarshal(data, &got); err != nil {
				t.Error(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Error(err)
			}
			if wire["stream"] != false || wire["think"] != false {
				t.Errorf("explicit false flags missing: %s", data)
			}
			options, ok := wire["options"].(map[string]any)
			if !ok || options["temperature"] != float64(0) || options["num_predict"] != float64(2048) || options["num_ctx"] != float64(8192) {
				t.Errorf("unexpected options: %s", data)
			}
			*requests = append(*requests, got)
			io.WriteString(w, chat)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(s.Close)
	c, endpoint, err := localClient(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &app{client: c, endpoint: endpoint, model: "qwen3:4b", host: "127.0.0.1:8027"}
}

const good = `{"model":"qwen3:4b","message":{"role":"assistant","content":"Ответ"},"done":true,"done_reason":"stop","eval_count":3}`

func request(a *app, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1:8027/api/chat", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func TestChatPassesContextAndPreservesReply(t *testing.T) {
	var outgoing []ollamaRequest
	a := fixture(t, good, false, true, &outgoing)
	first := request(a, `{"messages":[{"role":"user","content":"Запомни: клуб называется Гоферы."}]}`)
	if first.Code != 200 {
		t.Fatal(first.Body.String())
	}
	second := request(a, `{"messages":[{"role":"user","content":"Запомни: клуб называется Гоферы."},{"role":"assistant","content":"Ответ"},{"role":"user","content":"Как называется клуб?"}]}`)
	if second.Code != 200 {
		t.Fatal(second.Body.String())
	}
	var got turn
	if err := json.Unmarshal(second.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	json.Unmarshal(got.Response, &raw)
	if raw["message"].(map[string]any)["content"] != "Ответ" || got.Identity.Digest != "test-digest" {
		t.Fatal(second.Body.String())
	}
	if len(outgoing) != 2 || len(outgoing[1].Messages) != 4 || outgoing[1].Messages[2].Content != "Ответ" || outgoing[1].Messages[3].Content != "Как называется клуб?" || outgoing[1].Messages[0].Role != "system" || outgoing[1].Stream || outgoing[1].Think {
		t.Fatalf("outgoing=%+v", outgoing)
	}
}

func TestRejectInvalidInputBeforeInference(t *testing.T) {
	for _, body := range []string{`{`, `{"messages":[]}`, `{"messages":[{"role":"user","content":" "}]}`, `{"messages":[{"role":"system","content":"override"}]}`, `{"messages":[{"role":"assistant","content":"bad"}]}`, `{"messages":[{"role":"user","content":"hi"}],"model":"cloud"}`, `{"messages":[{"role":"user","content":"hi"}]}{}`, `{"messages":[{"role":"user","content":"` + strings.Repeat("x", 16001) + `"}]}`, strings.Repeat(" ", 129<<10) + `{"messages":[{"role":"user","content":"valid"}]}`} {
		t.Run(body[:min(len(body), 40)], func(t *testing.T) {
			var calls []ollamaRequest
			a := fixture(t, good, false, true, &calls)
			w := request(a, body)
			if w.Code != 400 || len(calls) != 0 {
				t.Fatalf("%d %s calls=%d", w.Code, w.Body.String(), len(calls))
			}
		})
	}
}

func TestFailuresNeverBecomeSuccessfulReplies(t *testing.T) {
	for _, body := range []string{`{"model":"qwen3:4b","done":false,"done_reason":"stop","message":{"role":"assistant","content":"partial"}}`, `{"model":"qwen3:4b","done":true,"done_reason":"stop","message":{"role":"user","content":"wrong role"}}`, `{"error":"failed"}`, `{"model":"qwen3:4b","done":true,"done_reason":"length","message":{"role":"assistant","content":"partial"}}`, `{"model":"qwen3:4b","done":true,"done_reason":"stop","message":{"role":"assistant","content":""}}`, `{"model":"other","done":true,"done_reason":"stop","message":{"role":"assistant","content":"hi"}}`, `not JSON`} {
		var calls []ollamaRequest
		a := fixture(t, body, false, true, &calls)
		w := request(a, `{"messages":[{"role":"user","content":"hi"}]}`)
		if !strings.Contains(body, `"done_reason":"length"`) && strings.Contains(w.Body.String(), "done_reason=length; лимит генерации") {
			t.Fatal("non-length failure mislabelled as exhaustion")
		}
		if w.Code != 502 || strings.Contains(w.Body.String(), `"response"`) {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}

func TestLocalOnlyModelAndEndpoint(t *testing.T) {
	for _, endpoint := range []string{"https://127.0.0.1:11434", "http://example.com", "http://127.0.0.1:11434/path", "http://user:pass@127.0.0.1", "http://127.0.0.1/?x=1"} {
		if _, _, err := localClient(endpoint); err == nil {
			t.Errorf("accepted %s", endpoint)
		}
	}
	for _, state := range []struct{ remote, installed bool }{{true, true}, {false, false}} {
		var calls []ollamaRequest
		a := fixture(t, good, state.remote, state.installed, &calls)
		w := request(a, `{"messages":[{"role":"user","content":"hi"}]}`)
		if w.Code != 503 || len(calls) != 0 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	var calls []ollamaRequest
	a := fixture(t, good, false, true, &calls)
	a.model = "qwen-cloud"
	if _, err := a.preflight(context.Background()); err == nil {
		t.Fatal("cloud accepted")
	}
}

func TestNoProxyOrRedirect(t *testing.T) {
	proxyCalls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls++; w.WriteHeader(502) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	var calls []ollamaRequest
	a := fixture(t, good, false, true, &calls)
	if _, err := a.preflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.client.Transport.(*http.Transport).Proxy != nil || proxyCalls != 0 {
		t.Fatal("proxy enabled")
	}
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	c, base, _ := localClient(redirect.URL)
	a.client = c
	a.endpoint = base
	if _, err := a.preflight(context.Background()); err == nil || targetCalls != 0 {
		t.Fatal("redirect followed")
	}
}

func TestOriginHostAndMethods(t *testing.T) {
	var calls []ollamaRequest
	a := fixture(t, good, false, true, &calls)
	for _, spec := range []struct {
		method, path, host, origin, mime string
		want                             int
	}{
		{"POST", "/api/chat", "evil.invalid", "", "application/json", 403},
		{"POST", "/api/chat", a.host, "http://evil.invalid", "application/json", 403},
		{"POST", "/api/chat", a.host, "", "text/plain", 415},
		{"GET", "/api/chat", a.host, "", "", 405},
		{"POST", "/api/config", a.host, "", "", 405},
		{"GET", "/", a.host, "", "", 200},
	} {
		r := httptest.NewRequest(spec.method, "http://"+a.host+spec.path, strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
		r.Host = spec.host
		r.Header.Set("Origin", spec.origin)
		r.Header.Set("Content-Type", spec.mime)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != spec.want {
			t.Fatalf("%+v: %d", spec, w.Code)
		}
	}
	if len(calls) != 0 {
		t.Fatal("unauthorized inference")
	}
}

func TestUnavailableOllama(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	base := s.URL
	s.Close()
	c, base, err := localClient(base)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{client: c, endpoint: base, model: "qwen3:4b", host: "127.0.0.1:8027"}
	w := request(a, `{"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestConversationBoundaries(t *testing.T) {
	for _, n := range []int{2, 15, 17} {
		messages := make([]message, n)
		for i := range messages {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			messages[i] = message{role, "text"}
		}
		body, _ := json.Marshal(chatRequest{messages})
		var calls []ollamaRequest
		a := fixture(t, good, false, true, &calls)
		w := request(a, string(body))
		want := 400
		if n == 15 {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("messages=%d got=%d", n, w.Code)
		}
	}
	var calls []ollamaRequest
	a := fixture(t, good, false, true, &calls)
	w := request(a, `{"messages":[{"role":"user","content":"x"},{"role":"user","content":"x"},{"role":"user","content":"x"}]}`)
	if w.Code != 400 || len(calls) != 0 {
		t.Fatal("bad alternating roles accepted")
	}
}

func TestRemoteModelCapabilityAndResponseBounds(t *testing.T) {
	for _, meta := range []string{`{"capabilities":["completion"],"remote_model":"remote"}`, `{"capabilities":["embedding"]}`} {
		calls := 0
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/tags":
				io.WriteString(w, `{"models":[{"name":"qwen3:4b","digest":"d"}]}`)
			case "/api/show":
				io.WriteString(w, meta)
			case "/api/chat":
				calls++
			}
		}))
		c, base, _ := localClient(s.URL)
		a := &app{c, base, "qwen3:4b", "127.0.0.1:8027"}
		w := request(a, `{"messages":[{"role":"user","content":"hi"}]}`)
		s.Close()
		if w.Code != 503 || calls != 0 {
			t.Fatal("remote or non-completion model accepted")
		}
	}
	for _, code := range []int{200, 500} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/tags":
				io.WriteString(w, `{"models":[{"name":"qwen3:4b","digest":"d"}]}`)
			case "/api/show":
				io.WriteString(w, `{"capabilities":["completion"]}`)
			case "/api/chat":
				w.WriteHeader(code)
				if code == 500 {
					io.WriteString(w, `{"error":"test failure"}`)
				} else {
					io.WriteString(w, strings.Replace(good, `"Ответ"`, `"`+strings.Repeat("x", (2<<20)+1)+`"`, 1))
				}
			}
		}))
		c, base, _ := localClient(s.URL)
		a := &app{c, base, "qwen3:4b", "127.0.0.1:8027"}
		w := request(a, `{"messages":[{"role":"user","content":"hi"}]}`)
		s.Close()
		if w.Code != 502 || strings.Contains(w.Body.String(), `"response"`) {
			t.Fatal("bad upstream response accepted")
		}
		if code == 200 && !strings.Contains(w.Body.String(), "2 MiB") {
			t.Fatal("response size rejection cause lost")
		}
		if code == 500 && !strings.Contains(w.Body.String(), "test failure") {
			t.Fatal("upstream cause lost")
		}
	}
}

func TestAssetsConfigAndCrossSite(t *testing.T) {
	var calls []ollamaRequest
	a := fixture(t, good, false, true, &calls)
	for path, mime := range map[string]string{"/": "text/html", "/app.js": "text/javascript", "/style.css": "text/css", "/api/config": "application/json"} {
		r := httptest.NewRequest("GET", "http://"+a.host+path, nil)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), mime) || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'self'") {
			t.Fatal(path, w.Code, w.Header())
		}
	}
	r := httptest.NewRequest("GET", "http://"+a.host+"/api/config", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-site accepted")
	}
}

func TestExhaustedGenerationBudgetIsAnExplicitError(t *testing.T) {
	var calls []ollamaRequest
	a := fixture(t, `{"model":"qwen3:4b","done":true,"done_reason":"length","message":{"role":"assistant","content":"partial"}}`, false, true, &calls)
	w := request(a, `{"messages":[{"role":"user","content":"Запомни: наш учебный клуб называется «Гоферы». Ответь одним коротким предложением"}]}`)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "2048") || !strings.Contains(w.Body.String(), "незавершённый или некорректный ответ") || !strings.Contains(w.Body.String(), "done_reason=length; лимит генерации") || strings.Contains(w.Body.String(), `"response"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
