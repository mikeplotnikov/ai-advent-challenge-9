// Day 5, CLI — the same request sent to a ladder of models, so "what does a
// stronger model buy you" is answered with hit rates, seconds and dollars per
// correct answer instead of an impression of three sample replies.
//
// Everything the day compares travels through one code path: the model is a
// parameter, not a branch. What differs per provider is confined to clientFor.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

func main() {
	pilot := flag.Bool("pilot", false, "отбор классов задач: какие вообще различают ступени")
	repeat := flag.Int("repeat", 5, "прогонов на клетку в пилоте")
	onlyTasks := flag.String("tasks", "", "классы через запятую, например T0,T1")
	only := flag.String("only", "", "смоук: прогнать одну модель по id вместо всей лесенки")
	prompt := flag.String("prompt", "Сколько будет 17 умножить на 23? Ответь только числом.",
		"смоук: запрос, одинаковый для всех ступеней")
	flag.Parse()

	llm.LoadDotEnv(".env")
	registerFreePrices()

	if *pilot {
		if *repeat < 1 {
			fmt.Fprintln(os.Stderr, "-repeat должен быть не меньше 1")
			os.Exit(2)
		}
		if err := runPilot(*repeat, *onlyTasks); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("Запрос (байт-в-байт одинаковый для всех ступеней):\n  %s\n\n", *prompt)

	for _, r := range ladder {
		if *only != "" && r.id != *only {
			continue
		}
		report(r, *prompt)
	}
}

// report runs one rung once and prints what the call actually cost. The point of
// a smoke run is not the answer: it is that the reasoning switch took, that the
// token counts arrive, and that the price is known — three things the grid will
// depend on and none of which can be assumed.
func report(r rung, prompt string) {
	fmt.Printf("─── %s · %s · параметры %s · квантизация %s · контекст %s\n",
		r.id, r.tier, r.params, r.quant, r.context)
	fmt.Printf("    %s\n", r.why)

	client, err := clientFor(r)
	if err != nil {
		fmt.Printf("    ✗ клиент не собран: %v\n\n", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	started := time.Now()
	answer, err := client.AskWith(ctx, []llm.Message{{Role: "user", Content: prompt}},
		llm.Options{MaxTokens: 1500})
	elapsed := time.Since(started)
	if err != nil {
		fmt.Printf("    ✗ вызов не прошёл за %.1f с: %v\n\n", elapsed.Seconds(), err)
		return
	}

	u := answer.Usage
	fmt.Printf("    ответ: %s\n", oneLine(answer.Content, 90))
	fmt.Printf("    вернулась модель: %s\n", answer.Model)

	// Wall clock, not the provider's own timer: the same clock for a local server
	// and a cloud endpoint is the only way the two seconds mean the same thing.
	perSec := 0.0
	if elapsed.Seconds() > 0 {
		perSec = float64(u.CompletionTokens) / elapsed.Seconds()
	}
	fmt.Printf("    время: %.2f с · выход %d токенов · %.1f ток/с · %d символов\n",
		elapsed.Seconds(), u.CompletionTokens, perSec, len([]rune(answer.Content)))

	cacheNote := "разбивка кэша не пришла — считаем весь вход по ставке cache miss"
	if u.PromptCacheHitTokens+u.PromptCacheMissTokens == u.PromptTokens && u.PromptTokens > 0 {
		cacheNote = fmt.Sprintf("из кэша %d, мимо кэша %d", u.PromptCacheHitTokens, u.PromptCacheMissTokens)
	}
	fmt.Printf("    вход: %d токенов (%s)\n", u.PromptTokens, cacheNote)

	// The price depends on when the call happened, so it is priced at that
	// instant and the band is printed: a run that crosses 01:00 or 06:00 UTC on a
	// weekday is billed at two different rates.
	band := "off-peak"
	if llm.IsPeak(started) {
		band = "PEAK (ставки вдвое выше)"
	}
	switch cost, known := llm.CostAt(r.id, u, started); {
	case llm.IsFree(r.id):
		fmt.Printf("    стоимость: $0 — модель локальная, платить некому\n")
	case !known:
		fmt.Printf("    стоимость: НЕИЗВЕСТНА — модели нет в прайс-листе\n")
	default:
		fmt.Printf("    стоимость: $%.6f (%s, %s UTC)\n", cost, band, started.UTC().Format("15:04"))
	}

	// The switch that matters most for a cross-model comparison. Two of the three
	// plausible spellings of "do not reason" are accepted by ollama's endpoint and
	// do nothing at all, so this is checked on the answer rather than trusted.
	if answer.Reasoned() {
		fmt.Printf("    ⚠ РАССУЖДЕНИЕ НЕ ОТКЛЮЧИЛОСЬ: вернулось %d символов размышления —\n"+
			"      эта ступень сравнима с остальными только после починки\n", len([]rune(answer.Reasoning)))
	} else {
		fmt.Printf("    рассуждение выключено и не вернулось ✓\n")
	}
	fmt.Println()
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
