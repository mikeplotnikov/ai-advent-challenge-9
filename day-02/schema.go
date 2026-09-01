package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The host's own framing of day 2: "прогони свой промпт 20 раз — он тебе одно и
// то же возвращает?" and "нужно чтобы ты ему сделал 20 запросов и он тебе 20 раз
// вернул одну и ту же схему, да с разными данными ок, но формат сохранился".
//
// One constrained answer proves the constraint was applied. Only repetition
// proves the format is deterministic, which is what the day is actually about.

// declaredSchema is the shape the JSON mode asks for. Keeping it here, next to
// the checker, is what makes the check a check and not a restatement.
var declaredSchema = struct {
	Keys      []string
	StepsKey  string
	StepCount int
}{
	Keys:      []string{"тема", "шаги", "итог"},
	StepsKey:  "шаги",
	StepCount: 3,
}

type schemaResult struct {
	OK     bool
	Detail string // why it failed, or the shape that matched
	Sample string // one field's value, to see whether content varies
	Body   string // the whole answer, to count how many runs were truly identical
	// FirstTry is false when the first answer missed the schema and a retry
	// saved it. response_format guarantees valid JSON, not your schema — this
	// is where that difference becomes visible.
	FirstTry  bool
	FirstFail string // what the discarded first answer got wrong
}

// checkSchema decides whether one answer matches the declared shape. It looks at
// keys and types only: the values are supposed to differ from run to run.
func checkSchema(content string) schemaResult {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return schemaResult{Detail: "не разбирается как JSON"}
	}

	got := make([]string, 0, len(parsed))
	for k := range parsed {
		got = append(got, k)
	}
	sort.Strings(got)
	want := append([]string(nil), declaredSchema.Keys...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return schemaResult{Detail: "ключи: " + strings.Join(got, ", ")}
	}

	steps, ok := parsed[declaredSchema.StepsKey].([]any)
	if !ok {
		return schemaResult{Detail: "«" + declaredSchema.StepsKey + "» не массив"}
	}
	if len(steps) != declaredSchema.StepCount {
		return schemaResult{Detail: fmt.Sprintf("в «%s» %d элементов вместо %d",
			declaredSchema.StepsKey, len(steps), declaredSchema.StepCount)}
	}
	for i, s := range steps {
		if _, ok := s.(string); !ok {
			return schemaResult{Detail: fmt.Sprintf("элемент %d в «%s» не строка",
				i+1, declaredSchema.StepsKey)}
		}
	}

	sample, _ := parsed[declaredSchema.Keys[0]].(string)
	return schemaResult{OK: true, Detail: "тема, шаги[3], итог", Sample: sample, Body: content}
}

// repeatReport prints what N runs of the same request say about determinism.
// It reports shape and content separately, separates "right the first time"
// from "right after a retry", and never claims the values varied when they did
// not: the whole day is about evidence, not assertion.
func repeatReport(checks []schemaResult, failures []string) {
	total := len(checks) + len(failures)
	fmt.Println()
	fmt.Println(strings.Repeat("═", 72))
	fmt.Printf("ДЕТЕРМИНИРОВАННОСТЬ ФОРМАТА — %d запросов, один и тот же промпт\n", total)
	fmt.Println(strings.Repeat("═", 72))

	matched, firstTry, retried := 0, 0, 0
	topics := map[string]bool{}
	bodies := map[string]bool{}
	for i, c := range checks {
		mark, note := "✗", c.Detail
		if c.OK {
			mark = "✓"
			matched++
			topics[c.Sample] = true
			bodies[c.Body] = true
			if c.FirstTry {
				firstTry++
			} else {
				retried++
				note = c.Detail + "  ← со второй попытки; первая: " + c.FirstFail
			}
		}
		fmt.Printf("%s %s  %s\n", pad(fmt.Sprintf("%2d.", i+1), 4), mark, note)
	}
	for _, f := range failures {
		fmt.Printf("     ✗  запрос не прошёл: %s\n", f)
	}

	fmt.Println()
	fmt.Printf("Схема совпала с первой попытки: %d из %d.\n", firstTry, total)
	if retried > 0 {
		fmt.Printf("Ещё %d спасены повтором — итого %d из %d.\n", retried, matched, total)
	}
	fmt.Printf("Уникальных ответов: %d · уникальных значений «%s»: %d.\n",
		len(bodies), declaredSchema.Keys[0], len(topics))

	switch {
	case matched != total:
		fmt.Println("Формат НЕ детерминирован: часть ответов не легла в схему даже с повтором.")
	case retried > 0:
		fmt.Println("response_format гарантирует валидный JSON, но не вашу схему:")
		fmt.Println("детерминированным формат делает проверка кодом плюс повтор при расхождении.")
	case len(bodies) > 1:
		fmt.Println("Формат детерминирован: схема одна на все запросы, содержание разное.")
	default:
		fmt.Println("Схема совпала везде, но и текст вернулся идентичный —",
			"на этом вопросе модель не разошлась в содержании.")
	}
}
