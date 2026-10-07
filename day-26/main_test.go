package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnlyLoopbackWithoutProxyOrRedirects(t *testing.T) {
	for _, bad := range []string{"https://127.0.0.1:11434", "http://example.org", "http://192.168.1.2:11434", "http://127.0.0.1:11434/api", "http://u:p@127.0.0.1:11434", "http://127.0.0.1:11434?x=1"} {
		if _, _, err := localClient(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	for _, good := range []string{"http://localhost:11434", "http://127.0.0.1:11434", "http://[::1]:11434"} {
		c, _, err := localClient(good)
		if err != nil {
			t.Fatal(err)
		}
		if c.Transport.(*http.Transport).Proxy != nil {
			t.Fatal("proxy enabled")
		}
	}
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++; w.Write([]byte(`{}`)) }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer s.Close()
	c, base, _ := localClient(s.URL)
	var x any
	if _, err := call(c, base, "/", nil, &x); err == nil {
		t.Fatal("redirect accepted")
	}
	if targetCalls != 0 {
		t.Fatal("redirect target reached")
	}
}

func TestSyntheticAnswerChecks(t *testing.T) {
	tests := []struct {
		id, text string
		want     bool
	}{
		{"arithmetic", "42", true}, {"arithmetic", "41", false}, {"arithmetic", "42 пояснение", false},
		{"extraction", `{"city":"Казань","date":"2026-10-12","participants":12}`, true},
		{"extraction", `{"city":"Казань","date":"2026-10-12","participants":"12"}`, false},
		{"extraction", `{"city":"Москва","date":"2026-10-12","participants":12}`, false},
		{"extraction", `{"city":"Казань","date":"2026-10-13","participants":12}`, false},
		{"extraction", `{"city":"Казань","date":"2026-10-12","participants":12.5}`, false},
		{"extraction", `{"city":"Казань","date":"2026-10-12","participants":12,"extra":0}`, false},
		{"optimization", `{"selected":["A","D"],"cost":4500,"hours":3,"score":14}`, true},
		{"optimization", `{"selected":["C","D"],"cost":4000,"hours":3,"score":13}`, false},
		{"optimization", `{"selected":["A","D"],"cost":1,"hours":3,"score":14}`, false},
		{"optimization", `{"selected":["B","D"],"cost":5500,"hours":4,"score":18}`, false},
		{"optimization", `{"selected":["D","D"],"cost":5000,"hours":2,"score":16}`, false},
		{"optimization", `{"selected":["Z"],"cost":0,"hours":0,"score":14}`, false},
		{"optimization", `{"selected":["A","D"],"cost":4500,"hours":3,"score":14,"extra":0}`, false},
		{"optimization", `{}`, false}, {"optimization", `bad`, false},
	}
	for _, tt := range tests {
		if ok, _ := verify(tt.id, tt.text); ok != tt.want {
			t.Errorf("%s %s = %t want %t", tt.id, tt.text, ok, tt.want)
		}
	}
}

func fixture(t *testing.T, mode string) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			io.WriteString(w, `{"version":"fixture"}`)
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"qwen3:4b","digest":"local-digest"},{"name":"x-cloud","digest":"fixture-cloud-digest"}]}`)
		case "/api/show":
			if mode == "remote-model" {
				io.WriteString(w, `{"capabilities":["completion"],"remote_model":"remote"}`)
			} else if mode == "cloud" {
				io.WriteString(w, `{"capabilities":["completion"],"remote_host":"https://ollama.com"}`)
			} else if mode == "embedding" {
				io.WriteString(w, `{"capabilities":["embedding"]}`)
			} else {
				io.WriteString(w, `{"capabilities":["completion"],"details":{"family":"qwen3"}}`)
			}
		case "/api/chat":
			calls++
			var req request
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if r.Method != "POST" || req.Stream || req.Think || req.Model != "qwen3:4b" || len(req.Messages) != 2 {
				t.Errorf("wrong request %+v", req)
			}
			if mode == "http" {
				http.Error(w, "model failed", 500)
				return
			}
			if mode == "error" {
				io.WriteString(w, `{"error":"failed"}`)
				return
			}
			if mode == "malformed" {
				io.WriteString(w, `{broken`)
				return
			}
			if mode == "trailing" {
				io.WriteString(w, `{} {}`)
				return
			}
			texts := []string{"42", `{"city":"Казань","date":"2026-10-12","participants":12}`, `{"selected":["A","D"],"cost":4500,"hours":3,"score":14}`}
			if mode == "wrong" {
				texts[0] = "рассуждения </think> 42"
				texts[2] = `{"selected":[],"cost":0,"hours":0,"score":0}`
			}
			resp := response{Model: req.Model, Done: true, DoneReason: "stop", Message: message{"assistant", texts[calls-1]}, EvalCount: 10, EvalDuration: 1000000000}
			if mode == "empty" {
				resp.Message.Content = ""
			}
			if mode == "incomplete" {
				resp.Done = false
			}
			if mode == "truncated" || mode == "fail-at-3" && calls == 3 {
				resp.DoneReason = "length"
			}
			json.NewEncoder(w).Encode(resp)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	return s, &calls
}

func TestRealHTTPContractAndCapture(t *testing.T) {
	s, n := fixture(t, "ok")
	defer s.Close()
	c, base, _ := localClient(s.URL)
	var terminal strings.Builder
	r, err := run(c, base, "qwen3:4b", &terminal)
	if err != nil {
		t.Fatal(err)
	}
	if *n != 3 || len(r.Results) != 3 || r.Digest != "local-digest" {
		t.Fatalf("wrong capture: %+v calls=%d", r, *n)
	}
	for _, item := range r.Results {
		if !item.Passed || !json.Valid(item.Response) {
			t.Errorf("bad captured item %+v", item)
		}
	}
	if !strings.Contains(terminal.String(), "Ответ: 42") {
		t.Fatal("reply was not displayed")
	}
}

func TestFailuresNeverProceedAsSuccess(t *testing.T) {
	for _, mode := range []string{"cloud", "remote-model", "embedding", "fail-at-3", "http", "error", "malformed", "trailing", "empty", "incomplete", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			s, n := fixture(t, mode)
			defer s.Close()
			c, base, _ := localClient(s.URL)
			if _, err := run(c, base, "qwen3:4b", io.Discard); err == nil {
				t.Fatal("accepted failure")
			}
			if mode == "fail-at-3" && *n != 3 {
				t.Fatalf("failed after %d requests, wanted 3", *n)
			}
			if (mode == "cloud" || mode == "remote-model" || mode == "embedding") && *n != 0 {
				t.Fatal("generation ran before local model validation")
			}
		})
	}
	s, n := fixture(t, "ok")
	defer s.Close()
	c, base, _ := localClient(s.URL)
	if _, err := run(c, base, "missing", io.Discard); err == nil || *n != 0 {
		t.Fatal("missing model accepted")
	}
}

func TestSavedCaptureMatchesChecks(t *testing.T) {
	data, err := os.ReadFile("showcase.json")
	if err != nil {
		t.Fatal(err)
	}
	var saved report
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Results) != 3 {
		t.Fatal("capture must contain three actual replies")
	}
	for i, item := range saved.Results {
		expected, _ := json.Marshal(cases(saved.Model)[i].Request)
		actual, _ := json.Marshal(item.Request)
		if string(expected) != string(actual) {
			t.Errorf("captured request drift for %s", item.ID)
		}
		var reply response
		if err := json.Unmarshal(item.Response, &reply); err != nil {
			t.Fatal(err)
		}
		ok, note := verify(item.ID, reply.Message.Content)
		if ok != item.Passed || note != item.Check {
			t.Errorf("stale answer check for %s", item.ID)
		}
		if !reply.Done || reply.DoneReason != "stop" {
			t.Error("saved response incomplete")
		}
	}
}

func TestWrongAnswersAreCapturedWithoutHidingFailure(t *testing.T) {
	s, n := fixture(t, "wrong")
	defer s.Close()
	c, base, _ := localClient(s.URL)
	var terminal strings.Builder
	r, err := run(c, base, "qwen3:4b", &terminal)
	if err != nil {
		t.Fatal(err)
	}
	if *n != 3 || len(r.Results) != 3 {
		t.Fatal("wrong answers interrupted capture")
	}
	for i, want := range []bool{false, true, false} {
		if r.Results[i].Passed != want {
			t.Fatal("incorrect answer status")
		}
	}
	if !strings.Contains(terminal.String(), "Проверка: false · Критерий:") {
		t.Fatal("failure not displayed")
	}
}

func TestCaptureNeverOverwritesExistingEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	if err := saveCapture(path, []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := saveCapture(path, []byte("replacement")); err == nil {
		t.Fatal("existing capture overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatal("original changed")
	}
}

func TestCloudNamedModelRejectedBeforeGeneration(t *testing.T) {
	s, n := fixture(t, "ok")
	defer s.Close()
	c, base, _ := localClient(s.URL)
	if _, err := run(c, base, "x-cloud", io.Discard); err == nil || *n != 0 {
		t.Fatal("cloud-named model accepted")
	}
}
