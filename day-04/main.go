// Day 4, CLI — the same request sent at temperature 0, 0.7 and 1.2, so "what
// does temperature change" is answered with hit rates and diversity scores
// instead of an impression of two sample answers.
//
// The parameter is not universal, and the provider says so: DeepSeek's thinking
// mode "does not support parameters like temperature, top_p... Setting these
// parameters will not cause an error, but they will have no effect on the
// model's output" (https://api-docs.deepseek.com/guides/thinking_mode, checked
// 2026-09-03). Every measurement here therefore runs with thinking disabled,
// and -thinking-demo exists to show the silent ignore rather than describe it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

const defaultModel = "deepseek-v4-flash"

// bootstrapResamples is fixed rather than a flag: it is a property of the
// arithmetic, not of the experiment, and 10000 is enough that the printed
// interval does not wobble in its last digit.
const bootstrapResamples = 10000

// setting is one column of the grid: a temperature, or the deliberate absence
// of one. "Не отправлять параметр" is not the same request as temperature 0,
// and days 1 to 3 all sent the former — without this column there is nothing
// to read the three required values against.
type setting struct {
	label    string
	temp     *float64
	thinking string
}

func (s setting) demo() bool { return s.thinking == "enabled" }

type run struct {
	answer  string
	raw     string
	parsed  bool
	correct bool
	usage   llm.Usage
	elapsed time.Duration
	finish  string
	err     error
}

type cell struct {
	setting setting
	runs    []run
}

func main() {
	llm.LoadDotEnv(".env")

	repeat := flag.Int("repeat", 5, "прогонов на каждую температуру (минимум 2)")
	temps := flag.String("temps", "0,0.7,1.2", "температуры через запятую")
	only := flag.String("task", "", "только эти классы задач, например T1,T2")
	model := flag.String("model", defaultModel, "модель DeepSeek")
	workers := flag.Int("workers", 5, "сколько запросов держать в воздухе одновременно")
	withDefault := flag.Bool("with-default", true, "добавить колонку «параметр не отправлен»")
	judgePasses := flag.Int("judge-runs", 3, "проходов судьи по творческой задаче, 0 — без судьи")
	thinkingDemo := flag.Bool("thinking-demo", false,
		"добавить прогоны с thinking=enabled: показать, что температура там игнорируется")
	seed := flag.Int64("seed", 1, "seed для перемешивания у судьи и для bootstrap")
	full := flag.Bool("full", false, "печатать все ответы целиком")
	flag.Parse()

	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "лишний аргумент %q: задачи дня 4 зашиты в код\n", flag.Arg(0))
		flag.Usage()
		os.Exit(2)
	}
	// Two is the floor, not a nicety: diversity over one answer is undefined,
	// and this day compares diversity.
	if *repeat < 2 {
		fmt.Fprintln(os.Stderr, "-repeat должен быть не меньше 2: разнообразие одного ответа не определено")
		os.Exit(2)
	}
	if *workers < 1 {
		fmt.Fprintln(os.Stderr, "-workers должен быть не меньше 1")
		os.Exit(2)
	}

	selected, err := pickTasks(tasks(), *only)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	settings, err := buildSettings(*temps, *withDefault, *thinkingDemo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	client, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY4")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client.Model = *model

	fmt.Printf("Модель: %s · прогонов на температуру: %d · seed: %d\n",
		client.Model, *repeat, *seed)
	fmt.Println("Во всех замерах thinking выключен, top_p не отправляется: единственная")
	fmt.Println("переменная — температура. Иначе сравнивать было бы нечего.")

	ctx := context.Background()
	for _, t := range selected {
		cells := make([]cell, 0, len(settings))
		for _, s := range settings {
			cells = append(cells, cell{setting: s, runs: runCell(ctx, client, t, s, *repeat, *workers)})
		}
		reportTask(ctx, os.Stdout, client, t, cells, client.Model, *seed, *judgePasses, *full)
	}

	fmt.Println()
	fmt.Println("Что здесь измерено, а что нет:")
	fmt.Println("  • точность — доля совпадений с эталоном, который считает наш код;")
	fmt.Println("  • разнообразие — свойство собранных ответов, считается без участия модели;")
	fmt.Println("  • креативность НЕ измерена. У неё нет эталона; есть оценка слепого судьи")
	fmt.Println("    и сами ответы на экране. «Оценка судьи» — это мнение модели, не измерение.")
	fmt.Println("Замер сделан на одной модели, этих задачах и одном поставщике. Вывод —")
	fmt.Println("ровно такого охвата.")
}

// buildSettings keeps the requested order and puts the "no parameter" column
// first, because it is the baseline the three required values are read against.
func buildSettings(spec string, withDefault, thinkingDemo bool) ([]setting, error) {
	var out []setting
	if withDefault {
		out = append(out, setting{label: "не отправлен"})
	}
	seen := map[float64]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return nil, fmt.Errorf("не разобрать температуру %q", part)
		}
		// The provider documents the range as 0 to 2; a value outside it is a
		// typo, and silently sending it would produce a column nobody could
		// interpret.
		if v < 0 || v > 2 {
			return nil, fmt.Errorf("температура %v вне документированного диапазона 0…2", v)
		}
		if seen[v] {
			return nil, fmt.Errorf("температура %v указана дважды", v)
		}
		seen[v] = true
		t := v
		out = append(out, setting{label: formatTemp(v), temp: &t})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("не задано ни одной температуры")
	}
	if thinkingDemo {
		lo, hi := 0.0, 1.2
		out = append(out,
			setting{label: "0 + thinking", temp: &lo, thinking: "enabled"},
			setting{label: "1.2 + thinking", temp: &hi, thinking: "enabled"})
	}
	return out, nil
}

func formatTemp(v float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 2, 64), "0"), ".")
}

func runCell(ctx context.Context, c *llm.Client, t task, s setting, repeat, workers int) []run {
	runs := make([]run, repeat)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range runs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			runs[i] = execute(ctx, c, t, s)
		}(i)
	}
	wg.Wait()
	return runs
}

func execute(ctx context.Context, c *llm.Client, t task, s setting) run {
	start := time.Now()
	// The open task runs on a 60-token cap, which is plenty for one line and
	// nowhere near enough for a reasoning pass. A demo run left on that cap
	// would spend the budget thinking, never answer, and demonstrate truncation
	// instead of the thing it exists to demonstrate. The cap is constant inside
	// the demo, so its own 0-against-1.2 comparison is unaffected.
	maxTokens := t.maxTokens
	if s.demo() && maxTokens < tokenCap {
		maxTokens = tokenCap
	}
	answer, err := c.AskWith(ctx, []llm.Message{
		{Role: "system", Content: t.system},
		{Role: "user", Content: t.prompt},
	}, llm.Options{
		MaxTokens:   maxTokens,
		Temperature: s.temp,
		Thinking:    s.thinking,
	})
	r := run{usage: answer.Usage, elapsed: time.Since(start), finish: answer.FinishReason}
	if err != nil {
		r.err = err
		return r
	}
	r.raw = answer.Content
	r.answer, r.parsed = t.parse(answer.Content)
	r.correct = t.kind == exact && r.parsed && r.answer == t.truth
	return r
}

// answers returns the comparable answers of the runs that produced one. Runs
// that failed or never answered are left out of the diversity metrics and
// counted separately: a request that never came back is not a variant.
func answers(runs []run) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		if r.parsed {
			out = append(out, r.answer)
		}
	}
	return out
}

type tally struct {
	setting  setting
	runs     []run
	answered []string
	correct  int
	total    int
	missing  int
	// truncated counts runs the model was cut off mid-answer. A clipped name is
	// still a parsed answer, so it enters the diversity metrics as a variant —
	// and it would do so most often at the highest temperature, which is the
	// column whose diversity is the headline. Counting it is what keeps the
	// table from attributing an artefact to the parameter.
	truncated int
	tokens    int
	elapsed   time.Duration
	avgCost   float64
	priced    bool
}

func summarise(c cell, model string) tally {
	t := tally{setting: c.setting, runs: c.runs, total: len(c.runs), priced: true, answered: answers(c.runs)}
	spent := 0.0
	for _, r := range c.runs {
		// A run that never reached an answer was still billed, so its tokens
		// stay in the average. Dropping it would make the settings that fail
		// look cheap.
		t.tokens += r.usage.TotalTokens
		t.elapsed += r.elapsed
		if price, known := llm.Cost(model, r.usage); known {
			spent += price
		} else {
			t.priced = false
		}
		if r.correct {
			t.correct++
		}
		if !r.parsed {
			t.missing++
		}
		if r.finish == "length" {
			t.truncated++
		}
	}
	if t.total > 0 {
		t.avgCost = spent / float64(t.total)
	}
	return t
}
