// Day 2, CLI — the same question sent four times under different levels of
// control, so the difference between asking for a shape and enforcing one is
// visible rather than asserted.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// The stop marker travels twice: the prompt asks the model to end with it, and
// the API is told to cut on it. If it never shows up in the answer, both worked.
const stopMarker = "<<<END>>>"

const defaultQuestion = "Объясни, как работает HTTPS"

// Run A asks for nothing beyond an answer: no shape, no length, no ending.
// Anything else here would make "without constraints" a lie.
const systemPlain = "Ты ассистент, работающий на модели DeepSeek через официальный API DeepSeek. " +
	"Отвечай на русском языке."

const systemShaped = systemPlain + "\n\n" +
	"Формат ответа: ровно три пункта, пронумерованных «1.», «2.», «3.», каждый с новой строки. " +
	"Никакого вступления и никакого заключения — только эти три пункта.\n" +
	"Длина: не больше 20 слов в пункте.\n" +
	"Закончи ответ отдельной последней строкой " + stopMarker + " и не пиши после неё ничего."

const systemJSON = systemPlain + "\n\n" +
	"Отвечай строго одним объектом JSON, без markdown и без пояснений вокруг. Схема:\n" +
	`{"тема": "строка", "шаги": ["строка", "строка", "строка"], "итог": "строка"}` + "\n" +
	"В «шаги» ровно три элемента, каждый не длиннее 20 слов."

type mode struct {
	key     string
	title   string
	control string
	system  string
	opts    llm.Options
}

type result struct {
	mode   mode
	answer llm.Answer
	err    error
}

func main() {
	dumpJSON := flag.Bool("json", false, "печатать тело каждого запроса целиком")
	flag.Parse()

	llm.LoadDotEnv(".env")

	question := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if question == "" {
		question = defaultQuestion
	}

	client, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY2")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	modes := []mode{
		{
			key:     "A",
			title:   "Без ограничений",
			control: "ничего не задано: ни формата, ни длины, ни условия завершения",
			system:  systemPlain,
			opts:    llm.Options{},
		},
		{
			key:     "B",
			title:   "Ограничения: промпт + API",
			control: "формат и длина описаны в system, завершение — маркер в промпте и в stop",
			system:  systemShaped,
			opts: llm.Options{
				MaxTokens: 400,
				Stop:      []string{stopMarker},
			},
		},
		{
			key:     "C",
			title:   "Формат средствами API",
			control: "response_format=json_object: структуру задаёт не просьба, а поле запроса",
			system:  systemJSON,
			opts: llm.Options{
				MaxTokens:      400,
				ResponseFormat: "json_object",
			},
		},
		{
			key:     "D",
			title:   "Длина средствами API",
			control: "тот же промпт, что в A, но max_tokens=60 — жёсткий потолок генерации",
			system:  systemPlain,
			opts:    llm.Options{MaxTokens: 60},
		},
	}

	fmt.Printf("Вопрос (один и тот же во всех прогонах):\n  %s\n\nМодель: %s\n", question, client.Model)

	results := make([]result, 0, len(modes))
	for _, m := range modes {
		answer, err := client.AskWith(context.Background(), []llm.Message{
			{Role: "system", Content: m.system},
			{Role: "user", Content: question},
		}, m.opts)
		results = append(results, result{mode: m, answer: answer, err: err})
		report(m, answer, err, *dumpJSON)
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
	fmt.Printf("finish_reason: %s · %d токенов ответа · %d символов%s\n",
		answer.FinishReason, answer.Usage.CompletionTokens,
		utf8.RuneCountInString(answer.Content), evidence(m, answer))
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

// evidence turns each mode's claim into something checkable: the stop sequence
// really removed the marker, the response_format really produced valid JSON.
func evidence(m mode, answer llm.Answer) string {
	switch {
	case len(m.opts.Stop) > 0:
		if strings.Contains(answer.Content, stopMarker) {
			return " · маркер " + stopMarker + " остался в тексте — stop не сработал"
		}
		return " · маркера " + stopMarker + " в тексте нет: stop срезал его"
	case m.opts.ResponseFormat == "json_object":
		var probe map[string]any
		if json.Unmarshal([]byte(answer.Content), &probe) != nil {
			return " · это не разбирается как JSON"
		}
		return " · разбирается как валидный JSON"
	case answer.FinishReason == "length":
		return " · ответ оборван на потолке токенов"
	}
	return ""
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
