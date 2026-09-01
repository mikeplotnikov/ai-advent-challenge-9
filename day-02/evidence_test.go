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
		question, dump, custom, err := parseArgs(c.args)
		if err != nil {
			t.Fatalf("parseArgs(%q): %v", c.args, err)
		}
		if custom != nil {
			t.Errorf("parseArgs(%q) вернул рычаги без -custom", c.args)
		}
		if question != c.question || dump != c.dump {
			t.Errorf("parseArgs(%q) = (%q, %v), ожидали (%q, %v)",
				c.args, question, dump, c.question, c.dump)
		}
	}
}

func TestParseCustom(t *testing.T) {
	question, _, custom, err := parseArgs([]string{"вопрос", "-custom", "format,marker,max=400"})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if question != "вопрос" {
		t.Errorf("вопрос = %q", question)
	}
	if custom == nil {
		t.Fatal("рычаги не разобраны")
	}
	want := levers{Format: true, Marker: true, MaxTokens: 400}
	if *custom != want {
		t.Errorf("рычаги = %+v, ожидали %+v", *custom, want)
	}

	// Ровно тот случай, ради которого песочница и делается: маркер просят в
	// промпте, но поле stop не отправляют — значит маркер обязан остаться.
	m := custom.build("X", "Своя комбинация", "")
	if len(m.opts.Stop) != 0 {
		t.Errorf("stop не должен уходить в запрос: %v", m.opts.Stop)
	}
	if !strings.Contains(m.system, stopMarker) {
		t.Error("инструкция про маркер должна остаться в промпте")
	}

	if _, _, _, err := parseArgs([]string{"-custom", "выдумка"}); err == nil {
		t.Error("неизвестный рычаг должен быть ошибкой")
	}
	if _, _, _, err := parseArgs([]string{"-custom", "max=-5"}); err == nil {
		t.Error("отрицательный max должен быть ошибкой")
	}
	if _, _, _, err := parseArgs([]string{"-custom"}); err == nil {
		t.Error("-custom без списка должен быть ошибкой")
	}
}

func TestBuildComposesLevers(t *testing.T) {
	full := levers{Format: true, Marker: true, Stop: true, MaxTokens: 400}.build("B", "", "")
	if !strings.Contains(full.system, "ровно три пункта") {
		t.Error("описание формата не попало в промпт")
	}
	if len(full.opts.Stop) != 1 || full.opts.Stop[0] != stopMarker {
		t.Errorf("stop = %v", full.opts.Stop)
	}
	if full.opts.MaxTokens != 400 {
		t.Errorf("max_tokens = %d", full.opts.MaxTokens)
	}

	// JSON вытесняет список пунктов: два описания формата в одном промпте
	// противоречили бы друг другу.
	both := levers{Format: true, JSON: true}.build("C", "", "")
	if strings.Contains(both.system, "ровно три пункта") {
		t.Error("список пунктов и схема JSON не должны стоять в промпте одновременно")
	}
	if both.opts.ResponseFormat != "json_object" {
		t.Errorf("response_format = %q", both.opts.ResponseFormat)
	}

	bare := levers{}.build("A", "", "")
	if bare.opts.MaxTokens != 0 || bare.opts.Stop != nil || bare.opts.ResponseFormat != "" {
		t.Errorf("прогон без рычагов не должен слать ничего: %+v", bare.opts)
	}
	if bare.system != systemPlain {
		t.Error("промпт без рычагов должен остаться нейтральным")
	}
}
