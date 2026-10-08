package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

func testFragment() fragment {
	return fragment{Source: "s", Section: "section", ChunkID: "id", Text: "Документ содержит факт 42.", Similarity: 1}
}
func validReply() string {
	return `{"answer":"Факт 42.","unknown":false,"clarification":"","sources":[{"source":"s","section":"section","chunk_id":"id","quote":"факт 42"}]}`
}
func testApp(t *testing.T, embed []float64, reply func(int) any) (*app, *atomic.Int32) {
	t.Helper()
	var chats atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"qwen3:4b","digest":"q"},{"name":"bge-m3:latest","digest":"b"}]}`)
		case "/api/show":
			fmt.Fprint(w, `{"capabilities":["completion","embedding"]}`)
		case "/api/version":
			fmt.Fprint(w, `{"version":"fixture"}`)
		case "/api/embed":
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			if req["truncate"] != false || req["model"] != "bge-m3" {
				t.Error("embedding contract", req)
			}
			json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float64{embed}})
		case "/api/chat":
			n := int(chats.Add(1))
			var req ollamaRequest
			if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
				t.Error(e)
			}
			if req.Think || req.Stream || req.Format == nil || req.Model != "qwen3:4b" || req.Options["num_predict"] != float64(2048) {
				t.Error("generation contract", req)
			}
			json.NewEncoder(w).Encode(reply(n))
		default:
			t.Error("unexpected path", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, base, e := localClient(server.URL)
	if e != nil {
		t.Fatal(e)
	}
	f := testFragment()
	return &app{client: client, endpoint: base, model: "qwen3:4b", host: "127.0.0.1:8028", busy: make(chan struct{}, 1), index: rag.Index{Header: rag.IndexHeader{Dimension: 2}, Chunks: []rag.Chunk{{ChunkID: f.ChunkID, Source: f.Source, Section: f.Section, Text: f.Text, Embedding: []float64{1, 0}}}}}, &chats
}
func goodEnvelope(content string) any {
	return map[string]any{"model": "qwen3:4b", "message": message{"assistant", content}, "done": true, "done_reason": "stop"}
}
func TestLivePipelineHTTP(t *testing.T) {
	a, calls := testApp(t, []float64{1, 0}, func(int) any { return goodEnvelope(validReply()) })
	req := httptest.NewRequest("POST", "http://127.0.0.1:8028/api/ask", strings.NewReader(`{"question":"Где факт?"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	d := json.NewDecoder(w.Body)
	var events []string
	var out result
	for {
		var ev struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		e := d.Decode(&ev)
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		events = append(events, ev.Event)
		if ev.Event == "result" {
			json.Unmarshal(ev.Data, &out)
		}
	}
	if strings.Join(events, ",") != "embedding,retrieval,context,generation,result" || out.Error != "" || out.Answer == nil || out.Answer.Answer != "Факт 42." || calls.Load() != 1 || len(out.Context) != 1 {
		t.Fatalf("events=%v out=%+v calls=%d", events, out, calls.Load())
	}
	request := out.Attempts[0].Request.(map[string]any)
	msgs := request["messages"].([]any)
	content := msgs[1].(map[string]any)["content"].(string)
	if !strings.Contains(content, "Документ содержит факт 42.") {
		t.Fatal("actual context not sent", content)
	}
}
func TestEmptyRetrievalAvoidsGeneration(t *testing.T) {
	a, calls := testApp(t, []float64{0, 1}, func(int) any { return goodEnvelope(validReply()) })
	r := a.runQuestion(context.Background(), "другой вопрос", func(string, any) {})
	if r.Error != "" || r.Answer == nil || !r.Answer.Unknown || r.Reason != "empty_context" || calls.Load() != 0 {
		t.Fatalf("%+v", r)
	}
}
func TestInvalidEmbeddingAvoidsGeneration(t *testing.T) {
	for _, v := range [][]float64{{0, 0}, {1}, {}} {
		t.Run(fmt.Sprint(v), func(t *testing.T) {
			a, calls := testApp(t, v, func(int) any { return goodEnvelope(validReply()) })
			r := a.runQuestion(context.Background(), "вопрос", func(string, any) {})
			if r.Error == "" || r.Answer != nil || calls.Load() != 0 {
				t.Fatalf("%+v", r)
			}
		})
	}
}
func TestOneRetryForFormatOnly(t *testing.T) {
	a, calls := testApp(t, []float64{1, 0}, func(n int) any {
		if n == 1 {
			return goodEnvelope(`{"answer":"broken"}`)
		}
		return goodEnvelope(validReply())
	})
	r := a.runQuestion(context.Background(), "вопрос", func(string, any) {})
	if r.Error != "" || len(r.Attempts) != 2 || r.Attempts[0].Error == "" || calls.Load() != 2 {
		t.Fatalf("%+v", r)
	}
}
func TestRepeatedMalformedHasNoAcceptedAnswer(t *testing.T) {
	a, calls := testApp(t, []float64{1, 0}, func(int) any { return goodEnvelope("bad") })
	r := a.runQuestion(context.Background(), "вопрос", func(string, any) {})
	if r.Error == "" || r.ErrorKind != "contract" || r.Answer != nil || calls.Load() != 2 {
		t.Fatalf("%+v", r)
	}
}
func TestIncompleteResponseIsErrorWithoutRetry(t *testing.T) {
	for _, reason := range []string{"length", ""} {
		t.Run(reason, func(t *testing.T) {
			a, calls := testApp(t, []float64{1, 0}, func(int) any {
				return map[string]any{"model": "qwen3:4b", "message": message{"assistant", validReply()}, "done": true, "done_reason": reason}
			})
			r := a.runQuestion(context.Background(), "вопрос", func(string, any) {})
			if r.Error == "" || r.Answer != nil || calls.Load() != 1 {
				t.Fatalf("%+v", r)
			}
		})
	}
}
func TestHTTPGuardsBeforeInference(t *testing.T) {
	cases := []struct {
		name, body, origin, host, ctype, method string
		status                                  int
	}{{"empty", `{"question":""}`, "", "", "application/json", "POST", 400}, {"unknown", `{"question":"x","extra":1}`, "", "", "application/json", "POST", 400}, {"trailing", `{"question":"x"}{}`, "", "", "application/json", "POST", 400}, {"long", `{"question":"` + strings.Repeat("x", 4001) + `"}`, "", "", "application/json", "POST", 400}, {"body-limit", strings.Repeat(" ", 16385) + `{}`, "", "", "application/json", "POST", 400}, {"origin", `{"question":"x"}`, "https://foreign.test", "", "application/json", "POST", 403}, {"host", `{"question":"x"}`, "", "evil.test", "application/json", "POST", 403}, {"ctype", `{"question":"x"}`, "", "", "text/plain", "POST", 415}, {"method", "", "", "", "application/json", "GET", 405}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, calls := testApp(t, []float64{1, 0}, func(int) any { return goodEnvelope(validReply()) })
			r := httptest.NewRequest(c.method, "http://127.0.0.1:8028/api/ask", strings.NewReader(c.body))
			if c.host != "" {
				r.Host = c.host
			}
			r.Header.Set("Origin", c.origin)
			r.Header.Set("Content-Type", c.ctype)
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			if w.Code != c.status || calls.Load() != 0 {
				t.Fatal(w.Code, w.Body.String(), calls.Load())
			}
		})
	}
}
func TestBusyRejectsConcurrentRequest(t *testing.T) {
	a, calls := testApp(t, []float64{1, 0}, func(int) any { return goodEnvelope(validReply()) })
	a.busy <- struct{}{}
	defer func() { <-a.busy }()
	r := httptest.NewRequest("POST", "http://127.0.0.1:8028/api/ask", strings.NewReader(`{"question":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 429 || calls.Load() != 0 {
		t.Fatal(w.Code, calls.Load())
	}
}
func TestAnswerContract(t *testing.T) {
	good := validReply()
	cases := []string{"null", good + `{}`, strings.Replace(good, `"unknown":false`, `"unknown":null`, 1), strings.Replace(good, `"answer":"Факт 42."`, `"answer":"Факт 42.","answer":"other"`, 1), strings.Replace(good, `"source":"s"`, `"source":"else"`, 1), strings.Replace(good, `"quote":"факт 42"`, `"quote":"42 фантазия"`, 1), strings.Replace(good, `"quote":"факт 42"`, `"quote":" "`, 1), strings.Replace(good, `"section":"section"`, `"section":"else"`, 1), strings.Replace(good, `"chunk_id":"id"`, `"chunk_id":"else"`, 1), strings.Replace(good, `"unknown":false`, `"unknown":true`, 1), `{"answer":"Не знаю.","unknown":true,"clarification":"","sources":[]}`, `{"answer":"text","unknown":false,"clarification":"","sources":[]}`}
	for _, text := range cases {
		if _, e := parseAnswer(text, []fragment{testFragment()}); e == nil {
			t.Errorf("accepted %s", text)
		}
	}
	for _, text := range []string{good, `{"answer":"Не знаю.","unknown":true,"clarification":"Уточните вопрос.","sources":[]}`} {
		if _, e := parseAnswer(text, []fragment{testFragment()}); e != nil {
			t.Fatal(e)
		}
	}
}
func TestLocalEndpointAndRedirectBoundary(t *testing.T) {
	for _, u := range []string{"https://127.0.0.1", "http://example.com", "http://127.0.0.1.evil.test", "http://user@127.0.0.1", "http://127.0.0.1/path", "http://127.0.0.1?x=1", "http://0.0.0.0"} {
		if _, _, e := localClient(u); e == nil {
			t.Error("accepted", u)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://example.com", 302) }))
	defer server.Close()
	client, base, e := localClient(server.URL)
	if e != nil {
		t.Fatal(e)
	}
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("proxy enabled")
	}
	if _, e = client.Get(base); e == nil {
		t.Fatal("followed redirect")
	}
}
func TestRemoteModelRejected(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprint(w, `{"models":[{"name":"qwen3:4b","digest":"x"}]}`)
		} else {
			fmt.Fprint(w, `{"remote_host":"https://cloud.test","capabilities":["completion"]}`)
		}
	}))
	defer s.Close()
	c, b, e := localClient(s.URL)
	if e != nil {
		t.Fatal(e)
	}
	a := &app{client: c, endpoint: b}
	if _, e = a.modelInfo(context.Background(), "qwen3:4b", "completion"); e == nil {
		t.Fatal("accepted remote model")
	}
}
func TestCanceledContextProducesNoSuccess(t *testing.T) {
	a, _ := testApp(t, []float64{1, 0}, func(int) any { return goodEnvelope(validReply()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := a.runQuestion(ctx, "q", func(string, any) {})
	if r.Error == "" || r.Answer != nil {
		t.Fatalf("%+v", r)
	}
}

func TestQuoteChoicesRemainExact(t *testing.T) {
	text := "Paragraph one\ncontinues here.\n\nSecond paragraph."
	choices := quoteChoices(text)
	found := false
	for _, q := range choices {
		if !strings.Contains(text, q) || strings.TrimSpace(q) == "" {
			t.Fatalf("invented quote %q", q)
		}
		if q == "Paragraph one\ncontinues here." {
			found = true
		}
	}
	if !found {
		t.Fatal("paragraph newline lost")
	}
}
func TestSchemaOffersOnlyActualSourcesAndQuotes(t *testing.T) {
	chunks := []fragment{testFragment()}
	schema := answerSchema(chunks)
	raw, _ := json.Marshal(schema)
	if !strings.Contains(string(raw), `"enum":["Документ содержит факт 42."]`) || !strings.Contains(string(raw), `"enum":["section"]`) {
		t.Fatal(string(raw))
	}
}

func TestRetrievalThresholdAndTopThree(t *testing.T) {
	for _, sim := range []float64{0.449, 0.45} {
		t.Run(fmt.Sprint(sim), func(t *testing.T) {
			a, calls := testApp(t, []float64{1, 0}, func(int) any { return goodEnvelope(validReply()) })
			a.index.Chunks[0].Embedding = []float64{sim, math.Sqrt(1 - sim*sim)}
			r := a.runQuestion(context.Background(), "x", func(string, any) {})
			if sim < 0.45 {
				if len(r.Context) != 0 || calls.Load() != 0 {
					t.Fatalf("below threshold: %+v", r)
				}
			} else if len(r.Context) != 1 || r.Context[0].ChunkID != "id" || calls.Load() != 1 {
				t.Fatalf("threshold excluded: %+v", r)
			}
		})
	}
	a, _ := testApp(t, []float64{1, 0}, func(int) any { return goodEnvelope(validReply()) })
	base := a.index.Chunks[0]
	a.index.Chunks = nil
	for i, sim := range []float64{0.99, 0.8, 0.7, 0.6, 0.449} {
		c := base
		if i > 0 {
			c.ChunkID = fmt.Sprint("id", i)
		}
		c.Embedding = []float64{sim, math.Sqrt(1 - sim*sim)}
		a.index.Chunks = append(a.index.Chunks, c)
	}
	r := a.runQuestion(context.Background(), "x", func(string, any) {})
	if r.Error != "" || len(r.Context) != 3 || r.Context[0].ChunkID != "id" || r.Context[1].ChunkID != "id1" || r.Context[2].ChunkID != "id2" {
		t.Fatalf("wrong top3 %+v", r)
	}
}
func TestCancelDuringGenerationReleasesBusySlot(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	a, _ := testApp(t, []float64{1, 0}, func(n int) any {
		if n == 1 {
			close(started)
			<-release
		}
		return goodEnvelope(validReply())
	})
	server := httptest.NewServer(a)
	defer server.Close()
	a.host = strings.TrimPrefix(server.URL, "http://")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/ask", strings.NewReader(`{"question":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("generation not reached")
	}
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for len(a.busy) > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(a.busy) > 0 {
		t.Fatal("busy slot not released after disconnect")
	}
	second, _ := http.NewRequest("POST", server.URL+"/api/ask", strings.NewReader(`{"question":"next"}`))
	second.Header.Set("Content-Type", "application/json")
	res2, e := http.DefaultClient.Do(second)
	if e != nil {
		t.Fatal(e)
	}
	defer res2.Body.Close()
	body, _ := io.ReadAll(res2.Body)
	if res2.StatusCode != 200 || !strings.Contains(string(body), `"event":"result"`) {
		t.Fatal(res2.StatusCode, string(body))
	}
}
func TestAllRemoteModelMetadataGuards(t *testing.T) {
	cases := []struct{ model, tags, show, cap string }{{"qwen3:4b", `{"models":[{"name":"qwen3:4b","digest":"x"}]}`, `{"remote_model":"remote","capabilities":["completion"]}`, "completion"}, {"qwen3:cloud", `{}`, `{}`, "completion"}, {"qwen3:4b", `{"models":[]}`, `{}`, "completion"}, {"qwen3:4b", `{"models":[{"name":"qwen3:4b","digest":""}]}`, `{}`, "completion"}, {"qwen3:4b", `{"models":[{"name":"qwen3:4b","digest":"x"}]}`, `{"capabilities":["embedding"]}`, "completion"}, {"bge-m3", `{"models":[{"name":"bge-m3:latest","digest":"x"}]}`, `{"remote_host":"remote","capabilities":["embedding"]}`, "embedding"}}
	for _, c := range cases {
		t.Run(c.model+c.show, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					fmt.Fprint(w, c.tags)
				} else {
					fmt.Fprint(w, c.show)
				}
			}))
			defer s.Close()
			client, base, _ := localClient(s.URL)
			a := &app{client: client, endpoint: base}
			if _, e := a.modelInfo(context.Background(), c.model, c.cap); e == nil {
				t.Fatal("guard accepted", c)
			}
		})
	}
}
func TestLoadAppIndexBoundary(t *testing.T) {
	manifest, e := rag.ManifestSHA256(rag.ProjectPath("day-21/corpus"))
	if e != nil {
		t.Fatal(e)
	}
	vec := make([]float64, 1024)
	vec[0] = 1
	valid := rag.Index{Header: rag.IndexHeader{Strategy: "structure", Model: "bge-m3", Dimension: 1024, ManifestSHA256: manifest, Parameters: rag.StrategyParameters{MaxRunes: 3600, Rewind: 100}, ChunkCount: 1}, Chunks: []rag.Chunk{{ChunkID: "id", Strategy: "structure", Source: "s", Section: "sec", Text: "literal", Embedding: vec}}}
	raw, _ := json.Marshal(valid)
	path := filepath.Join(t.TempDir(), "index.json")
	os.WriteFile(path, raw, 0600)
	if _, e = loadApp("http://127.0.0.1:11434", "qwen3:4b", path); e != nil {
		t.Fatal("positive index", e)
	}
	mutations := []func(*rag.Index){func(x *rag.Index) { x.Header.ManifestSHA256 = "other" }, func(x *rag.Index) { x.Header.Dimension = 3 }, func(x *rag.Index) { x.Chunks[0].Embedding[0] = 2 }, func(x *rag.Index) { x.Chunks[0].Text = "" }, func(x *rag.Index) { x.Chunks[0].Text = strings.Repeat("x", 16001) }}
	for _, mutate := range mutations {
		var x rag.Index
		json.Unmarshal(raw, &x)
		mutate(&x)
		b, _ := json.Marshal(x)
		os.WriteFile(path, b, 0600)
		if _, e = loadApp("http://127.0.0.1:11434", "qwen3:4b", path); e == nil {
			t.Fatal("bad index accepted")
		}
	}
}
func TestConfigSameOriginAndFetchSiteGuard(t *testing.T) {
	a, _ := testApp(t, []float64{1, 0}, func(int) any { return goodEnvelope(validReply()) })
	r := httptest.NewRequest("GET", "http://127.0.0.1:8028/config.json", nil)
	r.Header.Set("Origin", "http://127.0.0.1:8028")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"live"`) || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestErrorKindsDistinguishContractProtocolAndLength(t *testing.T) {
	for _, c := range []struct{ model, reason, content, kind string }{{"qwen3:4b", "stop", "bad", "contract"}, {"qwen3:4b", "length", validReply(), "incomplete"}, {"other-model", "stop", validReply(), "technical"}} {
		t.Run(c.kind, func(t *testing.T) {
			a, _ := testApp(t, []float64{1, 0}, func(int) any {
				return map[string]any{"model": c.model, "message": message{"assistant", c.content}, "done": true, "done_reason": c.reason}
			})
			r := a.runQuestion(context.Background(), "x", func(string, any) {})
			if r.ErrorKind != c.kind || r.Answer != nil {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestListenBoundary(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8028", "[::1]:8028"} {
		if err := validateListen(address); err != nil {
			t.Errorf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8028", "[::]:8028", "localhost:8028", "192.168.1.2:8028", "127.0.0.1"} {
		if err := validateListen(address); err == nil {
			t.Errorf("accepted %s", address)
		}
	}
}
func TestOversizedContextReturnsEmptyAttempts(t *testing.T) {
	a := &app{}
	answer, attempts, err := a.generate(context.Background(), []message{{Role: "user", Content: strings.Repeat("x", 64001)}}, nil)
	if answer != nil || err == nil || !strings.Contains(err.Error(), "64000") || attempts == nil || len(attempts) != 0 {
		t.Fatalf("unexpected boundary result: %v %v %v", answer, attempts, err)
	}
	raw, err := json.Marshal(attempts)
	if err != nil || string(raw) != "[]" {
		t.Fatalf("attempts JSON: %s %v", raw, err)
	}
}
