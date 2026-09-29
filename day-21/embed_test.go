package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func handlerHTTPClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
}

func TestOllamaRequestAndNormalization(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if request["model"] != "bge-m3" || request["input"] != "текст" || request["truncate"] != false {
			t.Errorf("тело запроса: %s", body)
		}
		_, _ = io.WriteString(w, `{"embeddings":[[2,0]],"prompt_eval_count":7}`)
	})
	client := &OllamaClient{BaseURL: "http://ollama.test", Model: "bge-m3", HTTP: handlerHTTPClient(handler)}
	vector, tokens, normalized, err := client.Embed(context.Background(), "fixed:x:0", "текст")
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 7 || !normalized || len(vector) != 2 || vector[0] != 1 || vector[1] != 0 {
		t.Fatalf("vector=%v tokens=%d normalized=%v", vector, tokens, normalized)
	}
}

func TestOllamaReadableFailures(t *testing.T) {
	cases := []struct {
		name, response, want string
		status               int
	}{
		{"контекст", `{"error":"the input length exceeds the context length"}`, "fixed:x:7", 400},
		{"модель", `{"error":"model 'bge-m3' not found, try pulling it first"}`, "ollama pull bge-m3", 404},
		{"два вектора", `{"embeddings":[[1,0],[0,1]],"prompt_eval_count":2}`, "2 векторов", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.response)
			})
			client := &OllamaClient{BaseURL: "http://ollama.test", Model: "bge-m3", HTTP: handlerHTTPClient(handler)}
			_, _, _, err := client.Embed(context.Background(), "fixed:x:7", "текст")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ошибка=%v, ожидалась строка %q", err, tc.want)
			}
		})
	}
}

func TestOllamaConnectionFailureSuggestsServe(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	client := &OllamaClient{BaseURL: "http://127.0.0.1:1", Model: "bge-m3", HTTP: httpClient}
	_, _, _, err := client.Embed(context.Background(), "x", "текст")
	if err == nil || !strings.Contains(err.Error(), "ollama serve") {
		t.Fatalf("ошибка=%v", err)
	}
}

func TestLiveOllamaBgeM3(t *testing.T) {
	connection, err := net.DialTimeout("tcp4", "127.0.0.1:11434", 250*time.Millisecond)
	if err != nil {
		t.Skipf("Ollama на 127.0.0.1:11434 недоступна: %v", err)
	}
	_ = connection.Close()
	client := &OllamaClient{BaseURL: defaultOllama, Model: defaultModel, HTTP: &http.Client{Timeout: 20 * time.Second}}
	vector, tokens, _, err := client.Embed(context.Background(), "live-test", "Короткий текст для проверки эмбеддинга.")
	if err != nil {
		t.Fatal(err)
	}
	if len(vector) != 1024 || tokens <= 0 {
		t.Fatalf("dimension=%d tokens=%d", len(vector), tokens)
	}
	norm := 0.0
	for _, value := range vector {
		norm += value * value
	}
	if math.Abs(math.Sqrt(norm)-1) > 1e-3 {
		t.Fatalf("L2-норма = %.6f", math.Sqrt(norm))
	}
}
