// Day 2, CLI — the same question sent four times under different levels of
// control, so the difference between asking for a shape and enforcing one is
// visible rather than asserted. With -custom it sends one hand-picked
// combination instead, which is how the stop sequence gets proven rather than
// assumed: drop the stop lever and the marker appears in the text.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

const defaultQuestion = "Объясни, как работает HTTPS"

const usage = `usage: day-02 [вопрос] [-json] [-custom рычаг,...] [-repeat N]

  -json     печатать тело каждого запроса целиком
  -custom   вместо B/C/D прогнать одну свою комбинацию рядом с A.
            Рычаги: format · marker · stop · json · max=N (0 — не отправлять)
            Пример: -custom format,marker,max=400   (stop выключен — маркер
            останется в тексте, и видно, что срезает его именно поле stop)
  -repeat   прогнать один и тот же запрос N раз и проверить, что схема ответа
            не поплыла. Без -custom берётся режим C (response_format=json_object).
            Пример: -repeat 10`

type result struct {
	mode   mode
	answer llm.Answer
	err    error
}

func main() {
	llm.LoadDotEnv(".env")

	// Hand-rolled instead of the flag package: flag.Parse stops at the first
	// positional argument, so `day-02 "вопрос" -json` would silently paste the
	// flag into the question.
	question, dumpJSON, custom, repeat, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if question == "" {
		question = defaultQuestion
	}

	client, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY2")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if repeat > 0 {
		runRepeat(client, question, custom, repeat, dumpJSON)
		return
	}

	modes := defaultModes()
	if custom != nil {
		// A остаётся: без базы сравнивать не с чем.
		modes = []mode{modes[0], custom.build("X", "Своя комбинация", customControl(*custom))}
	}

	fmt.Printf("Вопрос (один и тот же во всех прогонах):\n  %s\n\nМодель: %s\n", question, client.Model)

	results := make([]result, 0, len(modes))
	for _, m := range modes {
		answer, err := client.AskWith(context.Background(), []llm.Message{
			{Role: "system", Content: m.system},
			{Role: "user", Content: question},
		}, m.opts)
		results = append(results, result{mode: m, answer: answer, err: err})
		report(m, answer, err, dumpJSON)
	}

	summary(results)
}

func report(m mode, answer llm.Answer, err error, dumpJSON bool) {
	fmt.Println()
	fmt.Println(strings.Repeat("─", 72))
	fmt.Printf("%s · %s\n", m.key, m.title)
	fmt.Printf("Контроль: %s\n", m.control)
	fmt.Printf("Отправлено: %s\n", sentFields(m.opts))
	fmt.Println(strings.Repeat("─", 72))

	if err != nil {
		fmt.Println("ошибка:", err)
		return
	}
	if dumpJSON {
		fmt.Printf("\nтело запроса:\n%s\n", answer.RequestBody)
	}
	fmt.Printf("\n%s\n\n", answer.Content)
	fmt.Printf("finish_reason: %s · %d токенов ответа · %d символов\n",
		answer.FinishReason, answer.Usage.CompletionTokens,
		utf8.RuneCountInString(answer.Content))
	for _, line := range evidence(m, answer) {
		fmt.Printf("  ↳ %s\n", line)
	}
}

// parseArgs takes flags from anywhere in the line and treats the rest as the question.
func parseArgs(args []string) (string, bool, *levers, int, error) {
	dump := false
	repeat := 0
	var custom *levers
	words := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-json" || a == "--json":
			dump = true
		case a == "-custom" || a == "--custom":
			if i+1 >= len(args) {
				return "", false, nil, 0, fmt.Errorf("-custom без списка рычагов")
			}
			i++
			l, err := parseLevers(args[i])
			if err != nil {
				return "", false, nil, 0, err
			}
			custom = &l
		case strings.HasPrefix(a, "-custom="):
			l, err := parseLevers(strings.TrimPrefix(a, "-custom="))
			if err != nil {
				return "", false, nil, 0, err
			}
			custom = &l
		case a == "-repeat" || a == "--repeat":
			if i+1 >= len(args) {
				return "", false, nil, 0, fmt.Errorf("-repeat без числа")
			}
			i++
			n, err := parseRepeat(args[i])
			if err != nil {
				return "", false, nil, 0, err
			}
			repeat = n
		case strings.HasPrefix(a, "-repeat="):
			n, err := parseRepeat(strings.TrimPrefix(a, "-repeat="))
			if err != nil {
				return "", false, nil, 0, err
			}
			repeat = n
		default:
			words = append(words, a)
		}
	}
	return strings.TrimSpace(strings.Join(words, " ")), dump, custom, repeat, nil
}

func parseRepeat(value string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("не разобрать -repeat %s: нужно целое число от 1", value)
	}
	return n, nil
}

// runRepeat sends the same request N times and reports whether the shape held.
// Values are expected to differ; that is the point — the format is what must not.
func runRepeat(client *llm.Client, question string, custom *levers, n int, dumpJSON bool) {
	l := levers{JSON: true, MaxTokens: 400}
	if custom != nil {
		l = *custom
	}
	m := l.build("R", "Проверка детерминированности", customControl(l))

	fmt.Printf("Вопрос (один и тот же во всех %d запросах):\n  %s\n\nМодель: %s\nРежим: %s\n",
		n, question, client.Model, m.control)
	if dumpJSON {
		fmt.Printf("\nsystem-промпт:\n%s\n", m.system)
	}

	checks := make([]schemaResult, 0, n)
	var failures []string
	for i := 0; i < n; i++ {
		answer, err := client.AskWith(context.Background(), []llm.Message{
			{Role: "system", Content: m.system},
			{Role: "user", Content: question},
		}, m.opts)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		checks = append(checks, checkSchema(answer.Content))
	}
	repeatReport(checks, failures)
}

// parseLevers reads "format,marker,stop,json,max=400" into the five controls.
func parseLevers(spec string) (levers, error) {
	var l levers
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "" {
			continue
		}
		if value, ok := strings.CutPrefix(part, "max="); ok {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 {
				return l, fmt.Errorf("не разобрать max=%s: нужно неотрицательное число", value)
			}
			l.MaxTokens = n
			continue
		}
		switch part {
		case "format":
			l.Format = true
		case "marker":
			l.Marker = true
		case "stop":
			l.Stop = true
		case "json":
			l.JSON = true
		default:
			return l, fmt.Errorf("неизвестный рычаг %q", part)
		}
	}
	return l, nil
}

// customControl describes a hand-picked combination the way the fixed modes
// describe themselves, splitting the prompt-side levers from the API-side ones.
func customControl(l levers) string {
	var prompt, api []string
	if l.Format {
		prompt = append(prompt, "описание формата")
	}
	if l.Marker {
		prompt = append(prompt, "инструкция закончить "+stopMarker)
	}
	if l.JSON {
		prompt = append(prompt, "схема JSON")
		api = append(api, "response_format")
	}
	if l.Stop {
		api = append(api, "stop")
	}
	if l.MaxTokens > 0 {
		api = append(api, fmt.Sprintf("max_tokens=%d", l.MaxTokens))
	}
	describe := func(what string, items []string) string {
		if len(items) == 0 {
			return what + ": ничего"
		}
		return what + ": " + strings.Join(items, ", ")
	}
	return describe("в промпте", prompt) + " · " + describe("в запросе", api)
}

// sentFields spells out which controls actually left in the request body, so the
// difference between the modes is readable without diffing JSON.
func sentFields(o llm.Options) string {
	parts := []string{}
	if o.MaxTokens > 0 {
		parts = append(parts, fmt.Sprintf("max_tokens=%d", o.MaxTokens))
	} else {
		parts = append(parts, "max_tokens не отправлен")
	}
	if len(o.Stop) > 0 {
		parts = append(parts, "stop="+strings.Join(o.Stop, ","))
	} else {
		parts = append(parts, "stop не отправлен")
	}
	if o.ResponseFormat != "" {
		parts = append(parts, "response_format="+o.ResponseFormat)
	} else {
		parts = append(parts, "response_format не отправлен")
	}
	return strings.Join(parts, " · ")
}

// evidence turns each mode's claim into something checkable. It reports every
// applicable fact rather than one verdict, because a truncated answer would
// otherwise be blamed on the wrong control: an answer cut at max_tokens carries
// no stop marker and no closing brace, and saying "stop сработал" or "это не
// JSON" about it would be exactly the unfounded claim this day exists to avoid.
func evidence(m mode, answer llm.Answer) []string {
	truncated := answer.FinishReason == "length"
	var out []string

	if truncated {
		out = append(out, "ответ оборван на потолке токенов: max_tokens рубит по границе токена")
	}

	if len(m.opts.Stop) > 0 {
		switch {
		case strings.Contains(answer.Content, stopMarker):
			out = append(out, "маркер "+stopMarker+" остался в тексте — stop не сработал")
		case truncated:
			out = append(out, "до маркера "+stopMarker+" дело не дошло — ответ упёрся в max_tokens")
		default:
			out = append(out, "маркера "+stopMarker+" в тексте нет: stop срезал его")
		}
	}

	if m.opts.ResponseFormat == "json_object" {
		var probe map[string]any
		switch {
		case json.Unmarshal([]byte(answer.Content), &probe) == nil:
			out = append(out, "разбирается как валидный JSON")
		case truncated:
			out = append(out, "JSON не закрыт: ответ упёрся в max_tokens, формат тут ни при чём")
		default:
			out = append(out, "это не разбирается как JSON")
		}
	}

	return out
}

func summary(results []result) {
	fmt.Println()
	fmt.Println(strings.Repeat("═", 72))
	fmt.Println("СРАВНЕНИЕ")
	fmt.Println(strings.Repeat("═", 72))
	fmt.Printf("%s %s %s %s\n",
		pad("Прогон", 30), pad("символов", 10), pad("токенов", 9), "finish_reason")
	for _, r := range results {
		label := r.mode.key + " · " + r.mode.title
		if r.err != nil {
			fmt.Printf("%s %s\n", pad(label, 30), "ошибка: "+r.err.Error())
			continue
		}
		fmt.Printf("%s %s %s %s\n",
			pad(label, 30),
			pad(fmt.Sprint(utf8.RuneCountInString(r.answer.Content)), 10),
			pad(fmt.Sprint(r.answer.Usage.CompletionTokens), 9),
			r.answer.FinishReason)
	}
	fmt.Println()
	fmt.Println("Словами в промпте ты просишь формат — полями API ты его навязываешь.")
}

// pad aligns by runes: %-Ns counts bytes, and Cyrillic would break the columns.
func pad(s string, width int) string {
	if n := utf8.RuneCountInString(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}
