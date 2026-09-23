package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const systemPrompt = "Ты — агент наблюдений за официальными курсами ЦБ РФ. Данные о курсах бери только из инструментов и никогда не называй курс по памяти. create_watch создаёт наблюдение, list_watches показывает их состояние, stop_watch останавливает наблюдение без удаления данных, get_watch_summary возвращает агрегат. В сводке для каждой валюты назови последний курс и дату курса, изменение за окно в рублях и процентах, число опросов, новых публикаций и сбоев. Сейчас {now}. Отвечай по-русски и кратко."

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type synchronizedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}
func synchronizeWriter(w io.Writer) io.Writer {
	if _, ok := w.(*synchronizedWriter); ok {
		return w
	}
	return &synchronizedWriter{w: w}
}

type cliOptions struct {
	tick, daemon, report, schemaCost bool
	sample, store, command, digests  string
	digestEvery, window, timeout     time.Duration
	until                            string
	question                         string
	commandExplicit                  bool
}

func run(args []string, stdout, stderr io.Writer) int {
	stderr = synchronizeWriter(stderr)
	opts, code := parseFlags(args, stderr)
	if code != 0 {
		return code
	}
	if opts.sample != "" {
		return runSample(opts.sample, stderr)
	}
	if opts.report {
		return runReportMode(opts, stderr)
	}
	if opts.schemaCost {
		return runSchemaCostMode(opts, stdout, stderr)
	}
	if opts.command == "" {
		opts.command = fmt.Sprintf("go run ./day-18/mcp-server -store %s", opts.store)
	}
	if !opts.commandExplicit && strings.ContainsAny(opts.store, " \t\r\n") {
		fmt.Fprintln(stderr, "путь -store без пробелов обязателен для команды сервера по умолчанию")
		return 2
	}
	transport, err := mcpclient.NewTransport("", opts.command)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if command, ok := transport.MCP.(*mcp.CommandTransport); ok {
		command.Command.Stderr = stderr
		command.Command.Env = withoutProviderSecrets(os.Environ())
	}

	base := context.Background()
	if opts.daemon {
		ctx, stop := signal.NotifyContext(base, os.Interrupt, syscall.SIGTERM)
		defer stop()
		session, err := mcpclient.NewNamed("ai-advent-day-18-agent", "1").Open(ctx, transport)
		if err != nil {
			fmt.Fprintln(stderr, "MCP:", err)
			return 1
		}
		defer session.Close()
		return runDaemon(ctx, session, &lazyModel{stderr: stderr}, opts.digests, opts.digestEvery, int(opts.window/time.Hour), opts.timeout, 15*time.Second, stdout, stderr)
	}
	ctx, cancel := context.WithTimeout(base, opts.timeout)
	defer cancel()
	session, err := mcpclient.NewNamed("ai-advent-day-18-agent", "1").Open(ctx, transport)
	if err != nil {
		fmt.Fprintln(stderr, "MCP:", err)
		return 1
	}
	defer session.Close()
	if opts.tick {
		return runTick(ctx, session, &lazyModel{stderr: stderr}, opts.digests, opts.digestEvery, int(opts.window/time.Hour), stdout, stderr)
	}
	client, err := newModel(stderr)
	if err != nil {
		return 1
	}
	return runQuestion(ctx, session, client, opts.question, stdout, stderr)
}

func parseFlags(args []string, stderr io.Writer) (cliOptions, int) {
	var opts cliOptions
	fs := flag.NewFlagSet("day-18", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&opts.tick, "tick", false, "выпустить одну сводку для текущего слота")
	fs.BoolVar(&opts.daemon, "daemon", false, "работать постоянно")
	fs.BoolVar(&opts.report, "report", false, "сгенерировать RESULTS.md")
	fs.BoolVar(&opts.schemaCost, "schema-cost", false, "измерить цену схем инструментов")
	fs.StringVar(&opts.sample, "sample", "", "каталог для детерминированного примера")
	fs.StringVar(&opts.store, "store", "day-18/state/store.json", "хранилище наблюдений")
	fs.StringVar(&opts.command, "command", "", "команда MCP-сервера")
	fs.StringVar(&opts.digests, "digests", "day-18/state/digests.json", "файл сводок")
	fs.DurationVar(&opts.digestEvery, "digest-every", 3*time.Hour, "период сводок")
	fs.DurationVar(&opts.window, "window", 24*time.Hour, "окно сводки")
	fs.StringVar(&opts.until, "until", "", "правая граница отчёта RFC3339")
	fs.DurationVar(&opts.timeout, "timeout", 90*time.Second, "дедлайн вопроса или сводки")
	if err := fs.Parse(args); err != nil {
		usage(stderr)
		return opts, 2
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "command" {
			opts.commandExplicit = true
		}
	})
	if fs.NArg() > 0 {
		opts.question = strings.Join(fs.Args(), " ")
	}
	modes := 0
	for _, active := range []bool{opts.question != "", opts.tick, opts.daemon, opts.report, opts.schemaCost, opts.sample != ""} {
		if active {
			modes++
		}
	}
	validWindow := opts.window >= time.Hour && opts.window <= 720*time.Hour && opts.window%time.Hour == 0
	if modes != 1 || opts.digestEvery < time.Minute || opts.digestEvery > 24*time.Hour || !validWindow || opts.timeout <= 0 || (!opts.report && opts.until != "") || (opts.report && opts.until == "") {
		usage(stderr)
		return opts, 2
	}
	if opts.report {
		if _, err := time.Parse(time.RFC3339, opts.until); err != nil {
			fmt.Fprintln(stderr, "-until: нужен RFC3339")
			usage(stderr)
			return opts, 2
		}
	}
	return opts, 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: day-18 [флаги] \"вопрос\" | -tick | -daemon | -report -until RFC3339 | -schema-cost | -sample каталог")
}

func newModel(stderr io.Writer) (*llm.Client, error) {
	llm.LoadDotEnv(".env")
	client, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY18")
	if err != nil {
		fmt.Fprintln(stderr, err)
	}
	return client, err
}

func withoutProviderSecrets(env []string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DEEPSEEK_API_KEY") {
			continue
		}
		result = append(result, entry)
	}
	return result
}

type lazyModel struct {
	stderr io.Writer
	once   sync.Once
	client *llm.Client
	err    error
}

func (m *lazyModel) Converse(ctx context.Context, messages []llm.ToolMessage, opts llm.Options) (llm.Answer, error) {
	m.once.Do(func() { m.client, m.err = newModel(m.stderr) })
	if m.err != nil {
		return llm.Answer{}, m.err
	}
	return m.client.Converse(ctx, messages, opts)
}

func promptNow(now time.Time) string {
	return strings.Replace(systemPrompt, "{now}", now.In(cbr.Moscow).Format("02.01.2006 15:04 МСК"), 1)
}

func runQuestion(ctx context.Context, session *mcpclient.Session, model toolagent.LLM, question string, stdout, stderr io.Writer) int {
	fmt.Fprintf(stdout, "[MCP] сервер %s %s · протокол %s · транспорт %s\n", mcpclient.SafeForTerminal(session.ServerName), mcpclient.SafeForTerminal(session.ServerVersion), mcpclient.SafeForTerminal(session.ProtocolVersion), session.Transport.Description)
	trace, err := toolagent.Run(ctx, model, session, toolagent.Input{SystemPrompt: promptNow(time.Now()), Question: question})
	trace.Server = toolagent.Server{Name: session.ServerName, Version: session.ServerVersion, Protocol: session.ProtocolVersion, Transport: session.Transport.Description}
	printTrace(stdout, trace)
	if trace.FinalAnswer != "" {
		fmt.Fprintf(stdout, "[ответ]\n%s\n", safeMultiline(trace.FinalAnswer))
	}
	printSummary(stderr, trace)
	if err != nil {
		fmt.Fprintln(stderr, safeMultiline(err.Error()))
		return 1
	}
	return 0
}

func printTrace(w io.Writer, trace toolagent.Trace) {
	if len(trace.Tools) > 0 {
		parts := make([]string, 0, len(trace.Tools))
		for _, tool := range trace.Tools {
			parts = append(parts, mcpclient.SafeForTerminal(signature(tool)))
		}
		fmt.Fprintf(w, "[MCP] tools/list → %d инструмента: %s\n", len(parts), strings.Join(parts, ", "))
	}
	for i, call := range trace.ModelCalls {
		fmt.Fprintf(w, "[модель · шаг %d] finish_reason=%s · вход %d (из кэша %d) · выход %d\n", i+1, mcpclient.SafeForTerminal(call.FinishReason), call.Usage.PromptTokens, call.Usage.PromptCacheHitTokens, call.Usage.CompletionTokens)
		for _, tool := range trace.ToolCalls {
			if tool.Step != i+1 {
				continue
			}
			fmt.Fprintf(w, "[модель → tool_call] %s %s\n", mcpclient.SafeForTerminal(tool.Name), mcpclient.SafeForTerminal(tool.RawArguments))
			label := "результат"
			if tool.IsError {
				label = "ОШИБКА ИНСТРУМЕНТА"
			}
			fmt.Fprintf(w, "[MCP tools/call → %s] %s\n", label, mcpclient.SafeForTerminal(tool.ResultText))
		}
	}
}

func signature(tool toolagent.Tool) string {
	var schema struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(tool.InputSchema, &schema)
	keys := make([]string, 0, len(schema.Properties))
	for key := range schema.Properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	required := map[string]bool{}
	for _, key := range schema.Required {
		required[key] = true
	}
	for i, key := range keys {
		if !required[key] {
			keys[i] += "?"
		}
	}
	return tool.Name + "(" + strings.Join(keys, ", ") + ")"
}

func printSummary(w io.Writer, trace toolagent.Trace) {
	price := "цена неизвестна"
	if trace.Totals.CostKnown {
		price = fmt.Sprintf("$%.6f", trace.Totals.Cost)
	}
	fmt.Fprintf(w, "[day-18: %d вызовов инструментов · %d обращений к модели · %d ток. вход (%d из кэша) · %d выход · %s · %.2f s]\n", trace.Totals.ToolCalls, trace.Totals.ModelCalls, trace.Totals.PromptTokens, trace.Totals.CachedTokens, trace.Totals.OutputTokens, price, trace.Totals.Duration.Seconds())
}

func safeMultiline(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = mcpclient.SafeForTerminal(lines[i])
	}
	return strings.Join(lines, "\n")
}
