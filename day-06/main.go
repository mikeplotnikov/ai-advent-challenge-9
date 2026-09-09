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
	"path/filepath"
	"strconv"
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

		// Day 7: the conversation survives the process.
		session  = flag.String("session", "default", "имя беседы; у каждой своя история")
		storeDir = flag.String("store-dir", ".sessions", "каталог, где лежат истории бесед")
		noMemory = flag.Bool("no-memory", false, "не читать и не писать историю — агент дня 6, забывающий всё при выходе")
		forget   = flag.Bool("forget", false, "забыть эту беседу перед началом")

		// Day 8: tokens as a resource with a price and a ceiling.
		maxContext = flag.Int("max-context", 0, "потолок контекста агента в токенах по локальной оценке; 0 — без потолка")
		onOverflow = flag.String("on-overflow", "", "что делать при превышении потолка: refuse, trim, warn (по умолчанию refuse)")
		showTokens = flag.Bool("tokens", false, "печатать предполётную оценку запроса и накопленный расход беседы")
		totalsOnly = flag.Bool("totals", false, "напечатать расход беседы и выйти, ничего не спрашивая у модели")
		tokenProbe = flag.String("token-probe", "", "замер дня 8: growth, window, ceiling или output")
		probeTurns = flag.Int("probe-turns", 12, "ходов в замерах growth и ceiling")
		// Ступени задаются в токенах ПО НАШЕЙ ОЦЕНКЕ, а она завышает: на заполнителе
		// с номерами строк замерено 1.315 — худшее значение дня (day-08/RESULTS.md).
		// Верхняя ступень поэтому 1 500 000 — это ≈1 141 000 токенов по счёту
		// поставщика при окне 1 048 576. Дефолт 1 200 000 давал бы ≈913 000: запрос
		// прошёл бы, стоил бы около $0.20 и ничего бы не показал.
		windowSize = flag.String("window-sizes", "50000,1500000", "ступени замера окна в токенах ПО ЛОКАЛЬНОЙ ОЦЕНКЕ, через запятую: сначала контроль, затем за пределом окна модели")
		tokenRows  = flag.String("token-rows", "", "куда дописывать строки замера дня 8; по умолчанию day-08/<замер>.jsonl")

		ctxProbe  = flag.Int("context-probe", 0, "замер: столько ходов подряд, с записью роста контекста и доли кэша")
		probeSalt = flag.String("probe-salt", "", "метка в начале системного промпта замера: делает префикс уникальным, чтобы померить холодный кэш ещё раз")
		probeOut  = flag.String("probe-rows", "day-07/context-probe-split.jsonl", "куда дописывать строки замера контекста")
	)
	flag.Parse()

	if *dump {
		if err := writeDump(os.Stdout); err != nil {
			fail(err)
		}
		return
	}

	cfg := agent.Config{
		Name:             *name,
		SystemPrompt:     *system,
		Model:            *model,
		MaxTokens:        *maxTokens,
		ResponseFormat:   formatOf(*jsonOut),
		MaxTurns:         *maxTurns,
		ReasoningEffort:  *effort,
		MaxContextTokens: *maxContext,
		OnOverflow:       agent.OverflowPolicy(*onOverflow),
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
	// The measurement runs under its own system prompt, and the interface does not
	// get to choose it: week 1 measured that the provider's cache gives no discount
	// at all on a short prefix, so a probe under the CLI's ordinary short prompt
	// would measure prompt length and report it as a fact about restarts.
	if *ctxProbe > 0 {
		cfg.SystemPrompt = agent.ProbeSystemPrompt
		if *probeSalt != "" {
			// The mark goes FIRST. The provider caches a prefix in blocks from the
			// start, so a mark appended at the end would leave the opening blocks
			// identical and the cache warm — the run would measure a warm cache and
			// call it cold.
			cfg.SystemPrompt = "Метка прогона: " + *probeSalt + ". " + agent.ProbeSystemPrompt
		}
	}

	// The growth and ceiling runs speak under their own system prompt: the answers
	// have to be long enough for the history to grow at a rate worth plotting, and
	// the CLI's ordinary "коротко и по делу" would measure brevity instead.
	if *tokenProbe == "growth" || *tokenProbe == "ceiling" {
		cfg.SystemPrompt = agent.GrowthSystemPrompt
	}

	// The interface picks which conversation and where it lives. It does not know
	// what a stored conversation looks like: the format, the atomic write and the
	// refusal to continue from a broken file are the agent's business, and this
	// package could not parse the file if it wanted to — it may not import
	// encoding/json at all.
	var store *agent.FileStore
	if !*noMemory {
		store = agent.NewFileStore(agent.SessionPath(*storeDir, *session))
		cfg.Store = store
	}

	question := strings.TrimSpace(strings.Join(flag.Args(), " "))

	if *probe {
		if question == "" {
			fail(errors.New(`для замера нужен вопрос: go run ./day-06 -reasoning-probe "вопрос"`))
		}
		// The reasoning probe asks the same question twice and compares. Letting the
		// second agent see the first one's exchange would compare two different
		// conversations and call the difference reasoning.
		probeCfg := cfg
		probeCfg.Store = nil
		if err := runReasoningProbe(probeCfg, *model, question); err != nil {
			fail(err)
		}
		return
	}

	if *forget {
		if store == nil {
			fail(errors.New("-forget и -no-memory вместе бессмысленны: забывать нечего"))
		}
		if err := store.Clear(); err != nil {
			fail(err)
		}
		fmt.Fprintf(os.Stderr, "беседа %q забыта\n", *session)
	}

	a, err := build(cfg, *model)
	if err != nil {
		fail(err)
	}
	announceMemory(a, store)

	if *ctxProbe > 0 {
		if err := runContextProbe(a, *ctxProbe, *probeOut); err != nil {
			fail(err)
		}
		return
	}

	if *totalsOnly {
		fmt.Println(a.Totals())
		return
	}

	if *tokenProbe != "" {
		// Замеры growth и ceiling дописывают синтетические ходы в ту беседу, на
		// которую указывает -session, и переписывают её системный промпт своим. В
		// пустую беседу это безобидно, в чужую — необратимо: Save атомарно кладёт
		// файл поверх, копии не остаётся. Поэтому продолжать существующую беседу
		// им запрещено, а не «не рекомендуется».
		//
		// Только им: window и output строят себе агента без хранилища вовсе, и
		// запрет для них был бы не просто лишним — он советовал бы стереть живую
		// беседу (-forget) ради замера, который к ней не притронется. Ровно та
		// потеря данных, от которой запрет и заводился, только руками владельца.
		if *tokenProbe == "growth" || *tokenProbe == "ceiling" {
			if r := a.Restored(); r.Turns > 0 {
				fail(fmt.Errorf("замер %s: беседа %q уже содержит ходов: %d — замер допишет в неё свои и перепишет системный промпт. Возьми свободное имя (-session) или сотри эту (-forget)",
					*tokenProbe, *session, r.Turns))
			}
		}
		if err := runTokenProbe(*tokenProbe, a, cfg, *model, *probeTurns, *windowSize, *tokenRows); err != nil {
			fail(err)
		}
		return
	}

	if question != "" {
		if err := askOnce(a, question, *quiet, *showTokens); err != nil {
			fail(err)
		}
		return
	}
	if err := converse(a, *quiet, *showTokens); err != nil {
		fail(err)
	}
}

// askOnce is the one-shot mode: a question in, an answer out.
func askOnce(a *agent.Agent, question string, quiet, tokens bool) error {
	if tokens {
		fmt.Fprintln(os.Stderr, "до отправки:", a.Preflight(question))
	}
	reply, err := a.Ask(context.Background(), question)
	// A turn that was answered but not written down is not a failed turn: the answer
	// is real and was paid for. It is a failed day 7, though, so the answer is
	// printed and the warning is loud.
	if errors.Is(err, agent.ErrNotSaved) {
		fmt.Println(reply.Text)
		fmt.Fprintf(os.Stderr, "ВНИМАНИЕ: %v — этот ход не переживёт перезапуск\n", err)
		if !quiet {
			fmt.Fprintln(os.Stderr, spend(reply))
		}
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Println(reply.Text)
	if !quiet {
		fmt.Fprintln(os.Stderr, spend(reply))
	}
	if tokens {
		fmt.Fprintln(os.Stderr, a.Totals())
	}
	return nil
}

// announceMemory says out loud whether this run remembers anything, and what it
// found. Day 7's demo is a claim about state on disk; leaving it to be inferred from
// the model's answers is how a broken store passes for a working one.
func announceMemory(a *agent.Agent, store *agent.FileStore) {
	if store == nil {
		fmt.Fprintln(os.Stderr, "память: выключена (-no-memory) — агент забудет всё при выходе")
		return
	}
	r := a.Restored()
	switch {
	case r.Turns == 0:
		fmt.Fprintf(os.Stderr, "память: %s — история пуста, начинаем с нуля\n", store.Path())
	default:
		fmt.Fprintf(os.Stderr, "память: %s — загружено ходов: %d, сообщений в контексте: %d, последняя запись %s\n",
			store.Path(), r.Turns, r.Messages, r.Updated.Local().Format("02.01 15:04:05"))
	}
	for _, w := range r.Warnings {
		fmt.Fprintln(os.Stderr, "ВНИМАНИЕ:", w)
	}
}

// runTokenProbe is day 8's measurement, in four flavours. The rows go to a file the
// showcase reads; the human-readable table goes to stdout so a screencast has
// something to show.
func runTokenProbe(mode string, a *agent.Agent, cfg agent.Config, model string, turns int, sizes, rowsPath string) error {
	if rowsPath == "" {
		rowsPath = filepath.Join("day-08", mode+".jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(rowsPath), 0o755); err != nil {
		return fmt.Errorf("каталог замера: %w", err)
	}
	f, err := os.OpenFile(rowsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("файл замера %s: %w", rowsPath, err)
	}
	defer f.Close()
	run := fmt.Sprintf("pid-%d", os.Getpid())

	switch mode {
	case "growth":
		err = agent.RunGrowthProbe(a, run, turns, os.Stdout, f)
	case "ceiling":
		err = agent.RunCeilingProbe(a, run, turns, os.Stdout, f)
	case "window":
		ladder, perr := parseSizes(sizes)
		if perr != nil {
			return perr
		}
		// The window run resets between rungs, and a reset clears the stored
		// conversation. Pointing that at the owner's session would delete a real
		// history to measure a limit, so this run has no store at all.
		probe, berr := build(withoutStore(cfg), model)
		if berr != nil {
			return berr
		}
		err = agent.RunWindowProbe(probe, run, ladder, os.Stdout, f)
	case "output":
		control, capped, berr := outputAgents(cfg, model)
		if berr != nil {
			return berr
		}
		err = agent.RunOutputProbe(control, capped, run,
			"Верни один объект JSON с полями city, country и population про Саратов. Только JSON, без пояснений.",
			os.Stdout, f)
	default:
		return fmt.Errorf("замер %q неизвестен: growth, window, ceiling или output", mode)
	}
	if err != nil {
		return err
	}
	return f.Close()
}

// outputAgents builds the pair the generation-cap run compares: the same question,
// the same format, one cap that fits the answer and one that cannot. Neither keeps a
// conversation — a truncated answer has no business ending up in a stored history.
func outputAgents(cfg agent.Config, model string) (control, capped *agent.Agent, err error) {
	base := withoutStore(cfg)
	base.ResponseFormat = "json_object"
	base.SystemPrompt = "Отвечай одним объектом JSON и ничем больше."

	generous := base
	generous.MaxTokens = 400
	control, err = build(generous, model)
	if err != nil {
		return nil, nil, err
	}
	tight := base
	tight.MaxTokens = 16
	capped, err = build(tight, model)
	if err != nil {
		return nil, nil, err
	}
	return control, capped, nil
}

func withoutStore(cfg agent.Config) agent.Config {
	cfg.Store = nil
	return cfg
}

// maxLadderTokens caps one rung of the window ladder. Three million by our estimate
// is about 2.3 million by the provider's — already twice its window — and about 16 MB
// of text held in memory.
const maxLadderTokens = 3_000_000

func parseSizes(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("ступень %q — не число: %w", p, err)
		}
		if n <= 0 {
			return nil, fmt.Errorf("ступень %d — размер должен быть положительным", n)
		}
		// Above this there is nothing left to learn — the provider's window is
		// 1 048 576 tokens — and the blob is built in memory before anything is
		// sent: a mistyped extra zero would be a self-inflicted out-of-memory
		// rather than a measurement.
		if n > maxLadderTokens {
			return nil, fmt.Errorf("ступень %d — больше потолка замера %d: окно модели меньше, а заполнитель собирается в памяти целиком",
				n, maxLadderTokens)
		}
		out = append(out, n)
	}
	if len(out) < 2 {
		return nil, errors.New("ступеней меньше двух: нужен контроль, который проходит, и ступень за пределом")
	}
	return out, nil
}

// runContextProbe is day 7's measurement. The restart it is about is performed by
// running this binary a second time, not simulated inside one process.
func runContextProbe(a *agent.Agent, turns int, rowsPath string) error {
	f, err := os.OpenFile(rowsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("файл замера %s: %w", rowsPath, err)
	}
	defer f.Close()
	run := fmt.Sprintf("pid-%d", os.Getpid())
	if err := agent.RunContextProbe(a, run, turns, os.Stdout, f); err != nil {
		return err
	}
	return f.Close()
}

// converse is the dialogue mode. It exists because the agent carries the message
// stack itself: without more than one turn, that would be an untested claim.
func converse(a *agent.Agent, quiet, tokens bool) error {
	fmt.Fprintf(os.Stderr, "%s готов. /reset — начать заново и стереть сохранённое, /stack — сколько ходов в контексте, /totals — расход беседы, /exit — выход.\n",
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
		case "/reset", "/forget":
			if err := a.Reset(); err != nil {
				fmt.Fprintln(os.Stderr, "ошибка:", err)
				continue
			}
			fmt.Fprintln(os.Stderr, "контекст очищен, сохранённая история удалена")
			continue
		case "/stack":
			fmt.Fprintf(os.Stderr, "ходов в контексте: %d\n", a.Turns())
			continue
		case "/totals":
			fmt.Fprintln(os.Stderr, a.Totals())
			continue
		}
		if tokens {
			fmt.Fprintln(os.Stderr, "до отправки:", a.Preflight(line))
		}
		reply, err := a.Ask(context.Background(), line)
		if errors.Is(err, agent.ErrNotSaved) {
			fmt.Fprintf(os.Stderr, "ВНИМАНИЕ: %v — этот ход не переживёт перезапуск\n", err)
		} else if err != nil {
			// A failed turn is reported and the conversation goes on: the agent
			// guarantees the stack was left untouched.
			fmt.Fprintln(os.Stderr, "ошибка:", err)
			continue
		}
		if reply.Warning != "" {
			fmt.Fprintln(os.Stderr, "ВНИМАНИЕ:", reply.Warning)
		}
		fmt.Println(reply.Text)
		if !quiet {
			fmt.Fprintln(os.Stderr, spend(reply))
		}
		if tokens {
			fmt.Fprintln(os.Stderr, a.Totals())
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
	// The estimate is printed next to the fact it was an estimate of: the pair is
	// day 8's free measurement of the local counter, one per turn, at no cost.
	estimate := ""
	if reply.Estimated.Total > 0 && reply.Usage.PromptTokens > 0 {
		estimate = fmt.Sprintf(" · оценка входа %d (%+d)",
			reply.Estimated.Total, reply.Estimated.Total-reply.Usage.PromptTokens)
	}
	cut := ""
	if reply.Truncated {
		cut = " · ОБОРВАН по потолку генерации"
	}
	dropped := ""
	if reply.Dropped > 0 {
		dropped = fmt.Sprintf(" · отброшено обменов: %d", reply.Dropped)
	}
	return fmt.Sprintf("[ход %d · %s · %d+%d токенов%s%s · %s · %s%s%s%s]",
		reply.Turn, reply.Model,
		reply.Usage.PromptTokens, reply.Usage.CompletionTokens, cache, estimate,
		price(reply.Usage), reply.Elapsed.Round(time.Millisecond), reasoned, cut, dropped)
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
