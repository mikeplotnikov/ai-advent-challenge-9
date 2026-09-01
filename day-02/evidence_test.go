package main

import (
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// The whole point of day 2 is that a constraint is demonstrated, not asserted.
// A truncated answer is the case where that breaks: it carries no stop marker
// and no closing brace, so the naive check credits the wrong control.
func TestEvidence(t *testing.T) {
	withStop := mode{opts: llm.Options{MaxTokens: 400, Stop: []string{stopMarker}}}
	withJSON := mode{opts: llm.Options{MaxTokens: 400, ResponseFormat: "json_object"}}
	plain := mode{opts: llm.Options{}}
	capped := mode{opts: llm.Options{MaxTokens: 60}}

	cases := []struct {
		name    string
		mode    mode
		answer  llm.Answer
		want    []string // substrings that must appear
		notWant []string // claims the data does not support
	}{
		{
			name:   "stop сработал",
			mode:   withStop,
			answer: llm.Answer{Content: "1. один\n2. два\n3. три", FinishReason: "stop"},
			want:   []string{"stop срезал его"},
		},
		{
			name:    "модель проигнорировала маркер",
			mode:    withStop,
			answer:  llm.Answer{Content: "1. один\n" + stopMarker + "\nхвост", FinishReason: "stop"},
			want:    []string{"stop не сработал"},
			notWant: []string{"stop срезал его"},
		},
		{
			name:    "ответ обрублен до маркера — заслуга не stop",
			mode:    withStop,
			answer:  llm.Answer{Content: "1. один, и тут всё обор", FinishReason: "length"},
			want:    []string{"упёрся в max_tokens", "потолке токенов"},
			notWant: []string{"stop срезал его"},
		},
		{
			name:   "JSON валиден",
			mode:   withJSON,
			answer: llm.Answer{Content: `{"тема":"HTTPS"}`, FinishReason: "stop"},
			want:   []string{"валидный JSON"},
		},
		{
			name:    "JSON обрублен по длине — формат тут ни при чём",
			mode:    withJSON,
			answer:  llm.Answer{Content: `{"тема":"HTTPS","шаги":["пер`, FinishReason: "length"},
			want:    []string{"формат тут ни при чём"},
			notWant: []string{"это не разбирается как JSON"},
		},
		{
			name:   "JSON сломан не по длине",
			mode:   withJSON,
			answer: llm.Answer{Content: "вот тебе JSON: почти", FinishReason: "stop"},
			want:   []string{"это не разбирается как JSON"},
		},
		{
			name:   "голый max_tokens",
			mode:   capped,
			answer: llm.Answer{Content: "HTTPS — это прото", FinishReason: "length"},
			want:   []string{"потолке токенов"},
		},
		{
			name:   "без ограничений — доказывать нечего",
			mode:   plain,
			answer: llm.Answer{Content: "длинный ответ", FinishReason: "stop"},
			want:   nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(evidence(c.mode, c.answer), " | ")
			if len(c.want) == 0 && got != "" {
				t.Fatalf("ожидали пустой вывод, получили %q", got)
			}
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("нет %q в %q", w, got)
				}
			}
			for _, n := range c.notWant {
				if strings.Contains(got, n) {
					t.Errorf("необоснованное утверждение %q в %q", n, got)
				}
			}
		})
	}
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		args     []string
		question string
		dump     bool
	}{
		{[]string{"Как", "работает", "HTTPS"}, "Как работает HTTPS", false},
		{[]string{"-json", "Как работает HTTPS"}, "Как работает HTTPS", true},
		// flag.Parse stops at the first positional argument; this is the case
		// that used to paste "-json" into the question.
		{[]string{"Как работает HTTPS", "-json"}, "Как работает HTTPS", true},
		{[]string{"--json"}, "", true},
		{nil, "", false},
	}
	for _, c := range cases {
		question, dump := parseArgs(c.args)
		if question != c.question || dump != c.dump {
			t.Errorf("parseArgs(%q) = (%q, %v), ожидали (%q, %v)",
				c.args, question, dump, c.question, c.dump)
		}
	}
}
