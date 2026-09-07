// Day 6, CLI — the interface half of "первый агент". It reads input, prints replies
// and knows nothing about how a model is called: no endpoint, no key, no message
// roles, no HTTP. Everything about talking to the model lives in internal/agent,
// and this package does not import internal/llm at all — encapsulation_test.go fails
// the build if that ever stops being true.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

const defaultSystemPrompt = "Ты ассистент, работающий через официальный API DeepSeek. " +
	"Отвечай кратко и по делу, на русском языке."

func main() {
	var (
		model     = flag.String("model", "", "модель (по умолчанию из DEEPSEEK_MODEL или дефолт клиента)")
		system    = flag.String("system", defaultSystemPrompt, "системный промпт — роль и инструкции агента")
		name      = flag.String("name", "агент", "имя агента в интерфейсе")
		temp      = flag.Float64("temp", -1, "temperature; -1 — не отправлять параметр вовсе")
		maxTokens = flag.Int("max-tokens", 0, "потолок генерации; 0 — не отправлять")
		stop      = flag.String("stop", "", "stop-последовательность; пусто — не отправлять")
		jsonOut   = flag.Bool("json", false, "response_format=json_object")
		thinking  = flag.Bool("thinking", false, "включить встроенное рассуждение модели")
		effort    = flag.String("effort", "", "reasoning_effort при включённом рассуждении: high, max")
		maxTurns  = flag.Int("max-turns", 0, "сколько последних обменов держать в контексте; 0 — все")
		probe     = flag.Bool("reasoning-probe", false, "замер: один вопрос дважды, с выключенным и включённым рассуждением")
		dump      = flag.Bool("dump", false, "выгрузить определения агента для сверки витрины и выйти")
		quiet     = flag.Bool("quiet", false, "не печатать строку расхода")
	)
	flag.Parse()

	if *dump {
		if err := writeDump(os.Stdout); err != nil {
			fail(err)
		}
		return
	}

	cfg := agent.Config{
		Name:            *name,
		SystemPrompt:    *system,
		Model:           *model,
		MaxTokens:       *maxTokens,
		ResponseFormat:  formatOf(*jsonOut),
		MaxTurns:        *maxTurns,
		ReasoningEffort: *effort,
	}
	if *temp >= 0 {
		t := *temp
		cfg.Temperature = &t
	}
	if *stop != "" {
		cfg.Stop = []string{*stop}
	}
	if *thinking {
		cfg.Thinking = "enabled"
	}

	question := strings.TrimSpace(strings.Join(flag.Args(), " "))

	if *probe {
		if question == "" {
			fail(errors.New(`для замера нужен вопрос: go run ./day-06 -reasoning-probe "вопрос"`))
		}
		if err := runReasoningProbe(cfg, *model, question); err != nil {
			fail(err)
		}
		return
	}

	a, err := build(cfg, *model)
	if err != nil {
		fail(err)
	}

	if question != "" {
		if err := askOnce(a, question, *quiet); err != nil {
			fail(err)
		}
		return
	}
	if err := converse(a, *quiet); err != nil {
		fail(err)
	}
}

// askOnce is the one-shot mode: a question in, an answer out.
func askOnce(a *agent.Agent, question string, quiet bool) error {
	reply, err := a.Ask(context.Background(), question)
	if err != nil {
		return err
	}
	fmt.Println(reply.Text)
	if !quiet {
		fmt.Fprintln(os.Stderr, spend(reply))
	}
	return nil
}

// converse is the dialogue mode. It exists because the agent carries the message
// stack itself: without more than one turn, that would be an untested claim.
func converse(a *agent.Agent, quiet bool) error {
	fmt.Fprintf(os.Stderr, "%s готов. /reset — начать заново, /stack — сколько ходов в контексте, /exit — выход.\n",
		a.Name())
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for {
		fmt.Fprint(os.Stderr, "> ")
		if !in.Scan() {
			break
		}
		line := strings.TrimSpace(in.Text())
		switch line {
		case "":
			continue
		case "/exit", "/quit":
			return nil
		case "/reset":
			a.Reset()
			fmt.Fprintln(os.Stderr, "контекст очищен")
			continue
		case "/stack":
			fmt.Fprintf(os.Stderr, "ходов в контексте: %d\n", a.Turns())
			continue
		}
		reply, err := a.Ask(context.Background(), line)
		if err != nil {
			// A failed turn is reported and the conversation goes on: the agent
			// guarantees the stack was left untouched.
			fmt.Fprintln(os.Stderr, "ошибка:", err)
			continue
		}
		fmt.Println(reply.Text)
		if !quiet {
			fmt.Fprintln(os.Stderr, spend(reply))
		}
	}
	return in.Err()
}

// runReasoningProbe is the day's one measurement: the same question, same agent
// config, with the model's built-in reasoning off and then on. Day 5 established two
// ways the switch fails — but only on ollama. On the cloud model it has never been
// checked, so this prints what actually came back rather than what was asked for.
func runReasoningProbe(cfg agent.Config, model, question string) error {
	type run struct {
		label    string
		thinking string
	}
	runs := []run{{"рассуждение выключено", "disabled"}, {"рассуждение включено", "enabled"}}

	fmt.Printf("вопрос: %s\n\n", question)
	for _, r := range runs {
		c := cfg
		c.Thinking = r.thinking
		if r.thinking == "disabled" {
			c.ReasoningEffort = ""
		}
		a, err := build(c, model)
		if err != nil {
			return err
		}
		reply, err := a.Ask(context.Background(), question)
		if err != nil {
			return fmt.Errorf("%s: %w", r.label, err)
		}
		asked := r.thinking == "enabled"
		fmt.Printf("--- %s (thinking=%s)\n", r.label, r.thinking)
		fmt.Printf("модель рассуждала: %v%s\n", reply.Reasoned, mismatch(asked, reply.Reasoned))
		fmt.Printf("токенов: %d промпт (%d из кэша) + %d ответ = %d\n",
			reply.Usage.PromptTokens, reply.Usage.CachedTokens,
			reply.Usage.CompletionTokens, reply.Usage.TotalTokens)
		fmt.Printf("цена: %s · время: %s\n", price(reply.Usage), reply.Elapsed.Round(time.Millisecond))
		fmt.Printf("ответ: %s\n\n", reply.Text)
	}
	fmt.Println("Замер сравнивает запрошенное с полученным. Расхождение выше — это не сбой" +
		" прогона, а результат: день 5 нашёл два способа, которыми выключатель отказывает" +
		" на ollama, и на облачной модели это до сих пор не проверялось.")
	return nil
}

// mismatch names the case day 5 spent a run on: asked for one thing, got the other.
func mismatch(asked, got bool) string {
	if asked == got {
		return ""
	}
	if asked {
		return "  ← просили рассуждать, модель не стала"
	}
	return "  ← рассуждение выключали, модель всё равно рассуждала"
}

func spend(reply agent.Reply) string {
	cache := ""
	if reply.Usage.CachedTokens > 0 {
		cache = fmt.Sprintf(", %d из кэша", reply.Usage.CachedTokens)
	}
	reasoned := ""
	if reply.Reasoned {
		reasoned = ", с рассуждением"
	}
	return fmt.Sprintf("[ход %d · %s · %d+%d токенов%s · %s · %s%s]",
		reply.Turn, reply.Model,
		reply.Usage.PromptTokens, reply.Usage.CompletionTokens, cache,
		price(reply.Usage), reply.Elapsed.Round(time.Millisecond), reasoned)
}

// price says "unknown" rather than "$0.000000" when the model is not in the price
// table: a silent zero would read as a free call.
func price(u agent.Usage) string {
	if !u.Priced {
		return "цена неизвестна"
	}
	return fmt.Sprintf("$%.6f", u.Cost)
}

func formatOf(jsonOut bool) string {
	if jsonOut {
		return "json_object"
	}
	return ""
}

// build asks the agent package for a configured agent. The interface never names a
// provider, a key or an endpoint — that is the encapsulation the task asks for.
func build(cfg agent.Config, model string) (*agent.Agent, error) {
	cfg.Model = model
	return agent.FromEnv(cfg)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
