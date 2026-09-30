package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

type testRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func modelResponse(content string) string {
	return `{"id":"x","model":"deepseek-flash","choices":[{"message":{"role":"assistant","content":` + quoteJSON(content) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":10}}`
}

func quoteJSON(value string) string { raw, _ := json.Marshal(value); return string(raw) }

func testClient(server *httptest.Server) *llm.Client {
	return &llm.Client{APIKey: "test", Model: modelName, URL: server.URL, HTTP: server.Client()}
}

func TestRequestContractAndShownPrompt(t *testing.T) {
	for _, mode := range []string{"norag", "rag"} {
		t.Run(mode, func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				io.WriteString(w, modelResponse("ok"))
			}))
			defer server.Close()
			messages := noRAGMessages("question")
			if mode == "rag" {
				messages = ragMessages("question", []FoundChunk{{Rank: 1, Source: "a.md", Section: "A", ChunkID: "c", Text: "text"}})
			}
			call, err := callModel(context.Background(), testClient(server), mode, 1, messages)
			if err != nil {
				t.Fatal(err)
			}
			if call.Response != "ok" {
				t.Fatalf("response=%q", call.Response)
			}
			if body["model"] != modelName || body["max_tokens"] != float64(600) || body["temperature"] != float64(0) {
				t.Fatalf("request=%v", body)
			}
			thinking := body["thinking"].(map[string]any)
			if thinking["type"] != "disabled" {
				t.Fatalf("thinking=%v", thinking)
			}
			rawMessages, _ := json.Marshal(body["messages"])
			var sent []llm.Message
			if err := json.Unmarshal(rawMessages, &sent); err != nil {
				t.Fatal(err)
			}
			if len(sent) != len(messages) || sent[0] != messages[0] || sent[1] != messages[1] {
				t.Fatalf("sent=%+v want=%+v", sent, messages)
			}
			var shown bytes.Buffer
			printPrompt(&shown, mode, messages)
			want := "[" + mode + " · system]\n" + messages[0].Content + "\n[" + mode + " · user]\n" + messages[1].Content + "\n"
			if shown.String() != want {
				t.Fatalf("shown prompt differs\ngot=%q\nwant=%q", shown.String(), want)
			}
		})
	}
}

func TestSingleAskModesMakeOneCallAndShowSentPrompt(t *testing.T) {
	for _, mode := range []string{"norag", "rag"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			var sent struct {
				Messages []llm.Message `json:"messages"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Error(err)
				}
				io.WriteString(w, modelResponse("ok"))
			}))
			defer server.Close()
			oldSearch := loadSearchFn
			loadSearchFn = func(context.Context, string, string, int) ([]FoundChunk, rag.IndexHeader, string, error) {
				return []FoundChunk{{Rank: 1, Source: "a.md", Section: "A", ChunkID: "c", Text: "text"}}, rag.IndexHeader{}, "", nil
			}
			defer func() { loadSearchFn = oldSearch }()
			var stdout, stderr bytes.Buffer
			if code := runAsk(context.Background(), testClient(server), "unused", "question", mode, 5, true, &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if calls.Load() != 1 {
				t.Fatalf("calls=%d", calls.Load())
			}
			shown := "[" + mode + " · system]\n" + sent.Messages[0].Content + "\n[" + mode + " · user]\n" + sent.Messages[1].Content + "\n"
			if !strings.Contains(stdout.String(), shown) {
				t.Fatalf("shown prompt differs from sent messages\nstdout=%q\nsent=%q", stdout.String(), shown)
			}
		})
	}
}

func TestCostUsesResponseModelAndAttemptTime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := strings.Replace(modelResponse("ok"), `"model":"deepseek-flash"`, `"model":"deepseek-v4-flash"`, 1)
		io.WriteString(w, response)
	}))
	defer server.Close()
	call, err := callModel(context.Background(), testClient(server), "norag", 1, noRAGMessages("q"))
	if err != nil {
		t.Fatal(err)
	}
	want, known := llm.CostAt("deepseek-v4-flash", call.Usage, call.Attempts[0].StartedAt)
	if call.Model != "deepseek-v4-flash" || call.CostKnown != known || call.CostUSD != want {
		t.Fatalf("call cost/model=%+v want cost=%v known=%v", call, want, known)
	}
}

func TestEmptyContentIsNotRetriedAndKeepsUsage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, modelResponse("")) }))
	defer server.Close()
	call, err := callModel(context.Background(), testClient(server), "norag", 1, noRAGMessages("q"))
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || call.Score.Outcome != "empty" || call.Usage.PromptTokens != 10 {
		t.Fatalf("calls=%d call=%+v", calls.Load(), call)
	}
}

func TestTransportFailureRetriesOnce(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		io.WriteString(w, modelResponse("ok"))
	}))
	defer server.Close()
	call, err := callModel(context.Background(), testClient(server), "norag", 1, noRAGMessages("q"))
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(call.Attempts) != 2 || call.Response != "ok" {
		t.Fatalf("calls=%d call=%+v", calls.Load(), call)
	}
}

func TestRetryKeepsUsageFromFirstAttempt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			io.WriteString(w, `{"id":"x","model":"deepseek-flash","choices":[{"message":{"role":"assistant","content":"","reasoning_content":"unfinished"},"finish_reason":"length"}],"usage":{"prompt_tokens":7,"completion_tokens":4,"total_tokens":11,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":7}}`)
			return
		}
		io.WriteString(w, modelResponse("ok"))
	}))
	defer server.Close()
	call, err := callModel(context.Background(), testClient(server), "norag", 1, noRAGMessages("q"))
	if err != nil {
		t.Fatal(err)
	}
	if call.Usage.PromptTokens != 17 || call.Usage.CompletionTokens != 6 || len(call.Attempts) != 2 {
		t.Fatalf("usage from attempts was lost: %+v", call)
	}
}

func TestBothKeepsSuccessfulAnswerWhenOtherModeFails(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 2 {
			http.Error(w, "fail", 500)
			return
		}
		io.WriteString(w, modelResponse("rag\x1b survives\nnext"))
	}))
	defer server.Close()
	old := loadSearchFn
	loadSearchFn = func(context.Context, string, string, int) ([]FoundChunk, rag.IndexHeader, string, error) {
		return []FoundChunk{{Rank: 1, Source: "a\x1b", ChunkID: "c", Text: "text\x1b"}}, rag.IndexHeader{}, "", nil
	}
	defer func() { loadSearchFn = old }()
	var stdout, stderr bytes.Buffer
	code := runAsk(context.Background(), testClient(server), "unused", "q", "both", 5, false, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), "ошибка") || !strings.Contains(stdout.String(), "rag? survives\nnext") || strings.ContainsRune(stdout.String(), '\x1b') {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestPrintCallPreservesLineBreaksAndRemovesControls(t *testing.T) {
	for _, call := range []Call{
		{Mode: "rag", Response: "first\n\x1b[31msecond"},
		{Mode: "rag", Error: "first\n\x1b[31msecond"},
	} {
		var output bytes.Buffer
		printCall(&output, call)
		if !strings.Contains(output.String(), "first\n?[31msecond") {
			t.Fatalf("line break was not preserved: %q", output.String())
		}
		if strings.ContainsRune(output.String(), '\x1b') {
			t.Fatalf("ESC reached terminal output: %q", output.String())
		}
	}
}

func TestChunkPreviewCollapsesWhitespaceBeforeSanitizing(t *testing.T) {
	oldSearch := loadSearchFn
	loadSearchFn = func(context.Context, string, string, int) ([]FoundChunk, rag.IndexHeader, string, error) {
		return []FoundChunk{{Rank: 1, Source: "a.md", Section: "A", ChunkID: "c", Text: "## Заголовок\n\nТекст\tс   разными\u2003пробелами\r\nКонец"}}, rag.IndexHeader{}, "", nil
	}
	t.Cleanup(func() { loadSearchFn = oldSearch })
	client := &llm.Client{
		APIKey: "test",
		Model:  modelName,
		URL:    "http://deepseek.invalid",
		HTTP: &http.Client{Transport: testRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(modelResponse("ok")))}, nil
		})},
	}
	var stdout, stderr bytes.Buffer
	if code := runAsk(context.Background(), client, "unused", "q", "rag", 5, false, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "## Заголовок Текст с разными пробелами Конец") || strings.ContainsRune(stdout.String(), '?') {
		t.Fatalf("preview=%q", stdout.String())
	}
}

func TestCLIValidationHappensBeforeCalls(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY_DAY22", "test")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("DEEPSEEK_API_URL", "http://invalid.invalid")
	cases := [][]string{{"-ask", "", "-mode", "norag"}, {"-ask", "q", "-k", "0"}, {"-ask", "q", "-k", "11"}, {"-ask", "q", "-eval"}, {"-eval", "-k", "5"}}
	for _, args := range cases {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 2 {
			t.Fatalf("%v code=%d stderr=%q", args, code, err.String())
		}
	}
}

func TestKInclusiveBounds(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY_DAY22", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	for _, test := range []struct {
		k        string
		wantCode int
		wantKey  bool
	}{
		{"1", 1, true},
		{"10", 1, true},
		{"0", 2, false},
		{"11", 2, false},
	} {
		t.Run("k="+test.k, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{"-ask", "q", "-mode", "norag", "-k", test.k}, &stdout, &stderr)
			if code != test.wantCode || strings.Contains(stderr.String(), "DEEPSEEK_API_KEY_DAY22") != test.wantKey {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestZeroModesAreRejectedBeforeCalls(t *testing.T) {
	var calls atomic.Int32
	oldSearch := loadSearchFn
	loadSearchFn = func(context.Context, string, string, int) ([]FoundChunk, rag.IndexHeader, string, error) {
		calls.Add(1)
		return nil, rag.IndexHeader{}, "", errors.New("unexpected call")
	}
	t.Cleanup(func() { loadSearchFn = oldSearch })
	t.Setenv("DEEPSEEK_API_KEY_DAY22", "test")
	for _, test := range []struct {
		name string
		args []string
	}{
		{"empty", nil},
		{"k-only", []string{"-k", "5"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "выберите ровно один режим") {
				t.Fatalf("args=%v code=%d stderr=%q", test.args, code, stderr.String())
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("network phase reached %d times", calls.Load())
	}
}

func TestNoRAGDoesNotNeedOllamaOrIndex(t *testing.T) {
	var requestedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requestedModel = body.Model
		io.WriteString(w, modelResponse("answer"))
	}))
	defer server.Close()
	t.Setenv("DEEPSEEK_API_KEY_DAY22", "test")
	t.Setenv("DEEPSEEK_API_URL", server.URL)
	var out, err bytes.Buffer
	if code := run([]string{"-ask", "q", "-mode", "norag"}, &out, &err); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, err.String())
	}
	if requestedModel != modelName {
		t.Fatalf("CLI requested model %q", requestedModel)
	}
}

func TestMissingKeyNamesVariable(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY_DAY22", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	var out, err bytes.Buffer
	if code := run([]string{"-ask", "q", "-mode", "norag"}, &out, &err); code != 1 || !strings.Contains(err.String(), "DEEPSEEK_API_KEY_DAY22") {
		t.Fatalf("code=%d stderr=%q", code, err.String())
	}
}

func TestEvalExistingRunMakesNoCall(t *testing.T) {
	dir := t.TempDir()
	old := defaultRun
	defaultRun = filepath.Join(dir, "run.json")
	defer func() { defaultRun = old }()
	if err := os.WriteFile(defaultRun, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY_DAY22", "test")
	t.Setenv("DEEPSEEK_API_URL", "http://invalid.invalid")
	var out, err bytes.Buffer
	if code := run([]string{"-eval"}, &out, &err); code != 1 || !strings.Contains(err.String(), "замер уже есть") {
		t.Fatalf("code=%d stderr=%q", code, err.String())
	}
}

func TestEvalIgnoresStaleLock(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	old := defaultRun
	defaultRun = filepath.Join(dir, "run.json")
	t.Cleanup(func() { defaultRun = old })
	if err := os.WriteFile(defaultRun+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY_DAY22", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	var out, stderr bytes.Buffer
	if code := run([]string{"-eval"}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "DEEPSEEK_API_KEY_DAY22") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestEvalSecondFailureWritesNothing(t *testing.T) {
	dir := t.TempDir()
	oldRun, oldResults, oldShowcase, oldIndex := defaultRun, defaultResults, defaultShowcase, defaultIndex
	defaultRun = filepath.Join(dir, "run.json")
	defaultResults = filepath.Join(dir, "RESULTS.md")
	defaultShowcase = filepath.Join(dir, "showcase.json")
	defaultIndex = filepath.Join(dir, "index.json")
	defer func() {
		defaultRun, defaultResults, defaultShowcase, defaultIndex = oldRun, oldResults, oldShowcase, oldIndex
	}()
	if err := os.WriteFile(defaultIndex, []byte("index"), 0600); err != nil {
		t.Fatal(err)
	}
	oldSearch := searchQuestionFn
	searchQuestionFn = func(context.Context, string, Question, int) ([]FoundChunk, rag.IndexHeader, string, error) {
		return nil, rag.IndexHeader{}, "manifest", nil
	}
	defer func() { searchQuestionFn = oldSearch }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fail", 500) }))
	defer server.Close()
	t.Setenv("DEEPSEEK_API_KEY_DAY22", "test")
	t.Setenv("DEEPSEEK_API_URL", server.URL)
	var out, err bytes.Buffer
	if code := run([]string{"-eval"}, &out, &err); code != 1 {
		t.Fatalf("code=%d stderr=%q", code, err.String())
	}
	for _, path := range []string{defaultRun, defaultResults, defaultShowcase} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s exists", path)
		}
	}
}

func TestEvalMakesSixtyCallsAndSearchesOncePerQuestion(t *testing.T) {
	dir := t.TempDir()
	oldIndex := defaultIndex
	defaultIndex = filepath.Join(dir, "index.json")
	defer func() { defaultIndex = oldIndex }()
	if err := os.WriteFile(defaultIndex, []byte("index"), 0600); err != nil {
		t.Fatal(err)
	}
	var searches atomic.Int32
	oldSearch := searchQuestionFn
	searchQuestionFn = func(context.Context, string, Question, int) ([]FoundChunk, rag.IndexHeader, string, error) {
		searches.Add(1)
		return []FoundChunk{{Rank: 1, Source: "a", ChunkID: "c", Text: "text"}}, rag.IndexHeader{}, "manifest", nil
	}
	defer func() { searchQuestionFn = oldSearch }()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, modelResponse("answer"))
	}))
	defer server.Close()
	runValue, err := runEvaluation(context.Background(), "unused", testClient(server), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 60 || searches.Load() != 10 {
		t.Fatalf("calls=%d searches=%d", calls.Load(), searches.Load())
	}
	if len(runValue.Questions) != 10 {
		t.Fatalf("questions=%d", len(runValue.Questions))
	}
	for _, question := range runValue.Questions {
		if len(question.NoRAG) != 3 || len(question.RAG) != 3 {
			t.Fatalf("%s calls=%d/%d", question.Question.ID, len(question.NoRAG), len(question.RAG))
		}
		for i, call := range question.NoRAG {
			if call.Mode != "norag" || call.Repeat != i+1 || call.Messages[1].Content != noRAGMessages(question.Question.Question)[1].Content {
				t.Fatalf("%s bad no-RAG call: %+v", question.Question.ID, call)
			}
		}
		for i, call := range question.RAG {
			if call.Mode != "rag" || call.Repeat != i+1 || call.Messages[1].Content != ragMessages(question.Question.Question, question.Chunks)[1].Content {
				t.Fatalf("%s bad RAG call: %+v", question.Question.ID, call)
			}
		}
		for _, call := range append(append([]Call(nil), question.NoRAG...), question.RAG...) {
			if len(call.Messages) != 2 || call.Messages[0].Content != SystemPrompt || call.Score.Outcome == "" {
				t.Fatalf("%s incomplete call: %+v", question.Question.ID, call)
			}
		}
	}
}

func TestOllamaAndIndexErrorsAreActionable(t *testing.T) {
	client := &llm.Client{}
	old := loadSearchFn
	defer func() { loadSearchFn = old }()
	for _, test := range []struct{ message, want string }{{"Ollama недоступна", "ollama serve"}, {"индекс несовместим", "go run ./day-21 -index"}} {
		loadSearchFn = func(context.Context, string, string, int) ([]FoundChunk, rag.IndexHeader, string, error) {
			return nil, rag.IndexHeader{}, "", errors.New(test.message + ": " + test.want)
		}
		var out, err bytes.Buffer
		code := runAsk(context.Background(), client, "", "q", "rag", 5, false, &out, &err)
		if code != 1 || !strings.Contains(err.String(), test.want) {
			t.Fatalf("code=%d stderr=%q", code, err.String())
		}
	}
}
