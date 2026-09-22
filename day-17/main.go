package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

const systemPrompt = "Ты — ассистент по официальным курсам валют ЦБ РФ. Курсы и пересчёты бери только из инструментов и никогда не называй курс по памяти. Даты передавай в формате ГГГГ-ММ-ДД; сегодня {today}. В ответе назови дату курса, которую вернул инструмент, и числа из его результата. Если инструмент вернул ошибку — исправь аргументы или объясни пользователю, что не так. Если вопрос не про курсы и пересчёт валют — отвечай без инструментов. Отвечай по-русски и кратко."

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func promptToday(now time.Time) string {
	return strings.Replace(systemPrompt, "{today}", now.In(time.FixedZone("MSK", 3*3600)).Format("2006-01-02"), 1)
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("day-17", flag.ContinueOnError)
	fs.SetOutput(stderr)
	command := fs.String("command", "go run ./day-17/mcp-server", "команда MCP-сервера")
	endpoint := fs.String("endpoint", "", "Streamable HTTP MCP endpoint")
	noTools := fs.Bool("no-tools", false, "не передавать инструменты модели")
	save := fs.String("save", "", "путь для JSON-трассы")
	timeout := fs.Duration("timeout", 90*time.Second, "общий таймаут")
	bench := fs.Bool("bench", false, "замер")
	dump := fs.Bool("dump", false, "выгрузка определений")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	explicitCommand := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "command" {
			explicitCommand = true
		}
	})
	if *endpoint != "" && explicitCommand {
		fmt.Fprintln(stderr, "-endpoint нельзя сочетать с явно указанным -command")
		return 2
	}
	if *dump {
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "-dump не принимает вопрос")
			return 2
		}
		if err := writeDump(stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if *bench {
		if *noTools {
			fmt.Fprintln(stderr, "-no-tools нельзя сочетать с -bench")
			return 2
		}
		selectedCommand := *command
		if *endpoint != "" {
			selectedCommand = ""
		}
		return runBench(selectedCommand, *endpoint, *timeout, stdout, stderr)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: day-17 [флаги] \"вопрос\"")
		return 2
	}
	question := strings.Join(fs.Args(), " ")
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	llm.LoadDotEnv(".env")
	client, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY17")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var session *mcpclient.Session
	var server toolagent.Server
	if !*noTools {
		selectedCommand := *command
		if *endpoint != "" {
			selectedCommand = ""
		}
		transport, err := mcpclient.NewTransport(*endpoint, selectedCommand)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		session, err = mcpclient.NewNamed("ai-advent-day-17-agent", "1").Open(ctx, transport)
		if err != nil {
			fmt.Fprintln(stderr, "MCP: "+err.Error())
			return 1
		}
		defer session.Close()
		server = toolagent.Server{Name: session.ServerName, Version: session.ServerVersion, Protocol: session.ProtocolVersion, Transport: transport.Description}
		fmt.Fprintf(stdout, "[MCP] сервер %s %s · протокол %s · транспорт %s\n", mcpclient.SafeForTerminal(session.ServerName), mcpclient.SafeForTerminal(session.ServerVersion), mcpclient.SafeForTerminal(session.ProtocolVersion), transport.Description)
	}
	trace, err := toolagent.Run(ctx, client, session, toolagent.Input{SystemPrompt: promptToday(time.Now()), Question: question, NoTools: *noTools})
	trace.Server = server
	printTrace(stdout, trace)
	if trace.FinalAnswer != "" {
		fmt.Fprintf(stdout, "[ответ]\n%s\n", safeMultiline(trace.FinalAnswer))
	}
	printSummary(stderr, trace)
	if *save != "" {
		if saveErr := saveTrace(*save, trace); saveErr != nil {
			fmt.Fprintln(stderr, "трассу сохранить не удалось:", saveErr)
			return 1
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func printTrace(w io.Writer, trace toolagent.Trace) {
	if len(trace.Tools) > 0 {
		parts := make([]string, 0, len(trace.Tools))
		for _, t := range trace.Tools {
			parts = append(parts, mcpclient.SafeForTerminal(signature(t)))
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
			if tool.Rejected != "" {
				fmt.Fprintf(w, "[агент → модели] вызов отклонён без MCP: %s\n", mcpclient.SafeForTerminal(tool.Rejected))
				continue
			}
			label := "результат"
			if tool.IsError {
				label = "ОШИБКА ИНСТРУМЕНТА"
			}
			fmt.Fprintf(w, "[MCP tools/call → %s] %s\n", label, mcpclient.SafeForTerminal(tool.ResultText))
		}
	}
}
func signature(t toolagent.Tool) string {
	var s struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(t.InputSchema, &s)
	keys := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	required := map[string]bool{}
	for _, k := range s.Required {
		required[k] = true
	}
	for i, k := range keys {
		if !required[k] {
			keys[i] += "?"
		}
	}
	return t.Name + "(" + strings.Join(keys, ", ") + ")"
}
func printSummary(w io.Writer, t toolagent.Trace) {
	price := "цена неизвестна"
	if t.Totals.CostKnown {
		price = fmt.Sprintf("$%.6f", t.Totals.Cost)
	}
	fmt.Fprintf(w, "[day-17: %d вызовов инструментов · %d обращений к модели · %d ток. вход (%d из кэша) · %d выход · %s · %.2f s]\n", t.Totals.ToolCalls, t.Totals.ModelCalls, t.Totals.PromptTokens, t.Totals.CachedTokens, t.Totals.OutputTokens, price, t.Totals.Duration.Seconds())
}

// safeMultiline keeps the answer's line breaks: SafeForTerminal flattens \n to a space,
// which is right for one-line fields like a tool name and wrong for a model's list.
// Every line still goes through it, so control and bidi characters are neutralized.
func safeMultiline(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = mcpclient.SafeForTerminal(line)
	}
	return strings.Join(lines, "\n")
}
