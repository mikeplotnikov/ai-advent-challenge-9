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

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/pipelinemcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	modelName         = "deepseek-v4-flash"
	day19ModelCalls   = 6
	systemPrompt      = "Ты — ассистент по официальным курсам валют ЦБ РФ. Сегодня {today} по Москве. Если пользователь просит курсы, сводку или отчёт, обязательно выполни цепочку fetch_rates → summarize_rates → save_report. Передавай dataset_id и summary_id дословно из результата предыдущего инструмента. Если период не указан, используй последние 30 календарных дней, включая сегодня. В итоговом ответе назови сохранённый файл. Никогда не выдумывай курсы и числа. Если инструмент вернул ошибку, объясни её пользователю; исправляй только очевидную ошибку аргументов. Отвечай по-русски и кратко."
	defaultServerCmd  = "go run ./day-19/mcp-server"
	defaultReportsDir = "day-19/reports"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func promptToday(now time.Time) string {
	return strings.Replace(systemPrompt, "{today}", now.In(cbr.Moscow).Format("2006-01-02"), 1)
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("day-19", flag.ContinueOnError)
	fs.SetOutput(stderr)
	command := fs.String("command", defaultServerCmd, "команда MCP-сервера")
	reports := fs.String("reports", defaultReportsDir, "папка для отчётов")
	direct := fs.Bool("direct", false, "прогнать цепочку без модели")
	currency := fs.String("currency", "", "код валюты для -direct")
	from := fs.String("from", "", "начало периода для -direct")
	to := fs.String("to", "", "конец периода для -direct")
	verify := fs.String("verify", "", "проверить Markdown-отчёт")
	bench := fs.Bool("bench", false, "прогнать замер из 20 вопросов")
	dump := fs.Bool("dump", false, "выгрузить определения и эталоны")
	save := fs.String("save", "", "сохранить JSON-трассу")
	timeout := fs.Duration("timeout", 90*time.Second, "общий таймаут")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	modes := 0
	for _, active := range []bool{*direct, *verify != "", *bench, *dump} {
		if active {
			modes++
		}
	}
	if modes > 1 {
		fmt.Fprintln(stderr, "-direct, -verify, -bench и -dump нельзя сочетать")
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
	if *verify != "" {
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "-verify не принимает вопрос")
			return 2
		}
		raw, err := readReportFile(*verify)
		if err != nil {
			fmt.Fprintln(stderr, "формат:", err)
			return 1
		}
		result := pipelinemcp.VerifyReport(raw)
		printVerification(stdout, result)
		if !result.OK {
			return 1
		}
		return 0
	}
	if *direct {
		if fs.NArg() != 0 || *currency == "" || *from == "" || *to == "" {
			fmt.Fprintln(stderr, "usage: day-19 -direct -currency USD -from ГГГГ-ММ-ДД -to ГГГГ-ММ-ДД")
			return 2
		}
		return runDirect(*command, *reports, *currency, *from, *to, *timeout, stdout, stderr)
	}
	if *bench {
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "-bench не принимает вопрос")
			return 2
		}
		return runBench(*command, *reports, *timeout, stdout, stderr)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: day-19 [флаги] \"вопрос\"")
		return 2
	}
	return runAgent(*command, *reports, strings.Join(fs.Args(), " "), *save, *timeout, stdout, stderr)
}

func openSession(ctx context.Context, command, reports string) (*mcpclient.Session, error) {
	if err := os.Setenv("DAY19_REPORTS_DIR", reports); err != nil {
		return nil, err
	}
	// The MCP subprocess does not need provider credentials. CommandTransport
	// inherits the parent environment, so remove every DeepSeek key only while
	// the child is started, then restore the agent's environment immediately.
	keys := make(map[string]string)
	for _, item := range os.Environ() {
		name, value, ok := strings.Cut(item, "=")
		if ok && strings.HasPrefix(name, "DEEPSEEK_API_KEY") {
			keys[name] = value
			_ = os.Unsetenv(name)
		}
	}
	defer func() {
		for name, value := range keys {
			_ = os.Setenv(name, value)
		}
	}()
	transport, err := mcpclient.NewTransport("", command)
	if err != nil {
		return nil, err
	}
	return mcpclient.NewNamed("ai-advent-day-19-agent", "1").Open(ctx, transport)
}

func runAgent(command, reports, question, save string, timeout time.Duration, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	llm.LoadDotEnv(".env")
	model, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY19")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	model.Model = modelName
	session, err := openSession(ctx, command, reports)
	if err != nil {
		fmt.Fprintln(stderr, "MCP:", err)
		return 1
	}
	defer session.Close()
	server := toolagent.Server{Name: session.ServerName, Version: session.ServerVersion, Protocol: session.ProtocolVersion, Transport: "stdio"}
	fmt.Fprintf(stdout, "[MCP] сервер %s %s · протокол %s · транспорт stdio\n", mcpclient.SafeForTerminal(session.ServerName), mcpclient.SafeForTerminal(session.ServerVersion), mcpclient.SafeForTerminal(session.ProtocolVersion))
	trace, runErr := toolagent.Run(ctx, model, session, toolagent.Input{SystemPrompt: promptToday(time.Now()), Question: question, ModelCallLimit: day19ModelCalls})
	trace.Server = server
	printTrace(stdout, trace)
	if trace.FinalAnswer != "" {
		fmt.Fprintf(stdout, "[ответ]\n%s\n", safeMultiline(trace.FinalAnswer))
	}
	verificationFailed := false
	output, saved := savedOutput(trace)
	if saved {
		fmt.Fprintln(stdout, "сохранённый файл:", output.Path)
		verificationFailed = !verifySaved(stdout, output.Path)
	}
	printSummary(stderr, trace)
	if save != "" {
		if err := saveTrace(save, trace); err != nil {
			fmt.Fprintln(stderr, "трассу сохранить не удалось:", err)
			return 1
		}
	}
	if runErr != nil {
		fmt.Fprintln(stderr, runErr)
		return 1
	}
	if !saved {
		fmt.Fprintln(stderr, "агент не завершил цепочку fetch_rates → summarize_rates → save_report")
		return 1
	}
	if !strings.Contains(trace.FinalAnswer, output.Name) {
		fmt.Fprintln(stderr, "ответ модели не называет сохранённый файл", output.Name)
		return 1
	}
	if verificationFailed {
		return 1
	}
	return 0
}

func runDirect(command, reports, currency, from, to string, timeout time.Duration, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	session, err := openSession(ctx, command, reports)
	if err != nil {
		fmt.Fprintln(stderr, "MCP:", err)
		return 1
	}
	defer session.Close()
	output, err := directChain(ctx, session, currency, from, to, stdout)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "сохранённый файл:", output.Path)
	if !verifySaved(stdout, output.Path) {
		return 1
	}
	return 0
}

func directChain(ctx context.Context, session toolagent.Session, currency, from, to string, stdout io.Writer) (pipelinemcp.SaveOutput, error) {
	fetch, err := call(ctx, session, "fetch_rates", map[string]any{"currency": currency, "date_from": from, "date_to": to}, stdout)
	if err != nil {
		return pipelinemcp.SaveOutput{}, err
	}
	var fetched pipelinemcp.FetchOutput
	if err := decodeStructured(fetch, &fetched); err != nil {
		return pipelinemcp.SaveOutput{}, err
	}
	summary, err := call(ctx, session, "summarize_rates", map[string]any{"dataset_id": fetched.DatasetID}, stdout)
	if err != nil {
		return pipelinemcp.SaveOutput{}, err
	}
	var summarized pipelinemcp.SummarizeOutput
	if err := decodeStructured(summary, &summarized); err != nil {
		return pipelinemcp.SaveOutput{}, err
	}
	saved, err := call(ctx, session, "save_report", map[string]any{"summary_id": summarized.SummaryID}, stdout)
	if err != nil {
		return pipelinemcp.SaveOutput{}, err
	}
	var output pipelinemcp.SaveOutput
	if err := decodeStructured(saved, &output); err != nil {
		return pipelinemcp.SaveOutput{}, err
	}
	return output, nil
}

func call(ctx context.Context, session toolagent.Session, name string, args map[string]any, stdout io.Writer) (*mcp.CallToolResult, error) {
	encoded, _ := json.Marshal(args)
	fmt.Fprintf(stdout, "[direct → tool_call] %s %s\n", name, encoded)
	result, err := session.CallTool(ctx, name, args)
	if err != nil {
		return nil, err
	}
	text := mcpclient.ToolText(result)
	label := "результат"
	if result.IsError {
		label = "ОШИБКА ИНСТРУМЕНТА"
	}
	fmt.Fprintf(stdout, "[MCP tools/call → %s] %s\n", label, mcpclient.SafeForTerminal(text))
	if result.IsError {
		return nil, fmt.Errorf("%s", text)
	}
	return result, nil
}

func decodeStructured(result *mcp.CallToolResult, target any) error {
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("структурированный результат: %w", err)
	}
	return nil
}

func savedOutput(trace toolagent.Trace) (pipelinemcp.SaveOutput, bool) {
	for i := len(trace.ToolCalls) - 1; i >= 0; i-- {
		call := trace.ToolCalls[i]
		if call.Name != "save_report" || call.IsError || call.Structured == nil {
			continue
		}
		raw, _ := json.Marshal(call.Structured)
		var output pipelinemcp.SaveOutput
		if json.Unmarshal(raw, &output) == nil && output.Path != "" {
			return output, true
		}
	}
	return pipelinemcp.SaveOutput{}, false
}

func verifySaved(w io.Writer, path string) bool {
	raw, err := readReportFile(path)
	if err != nil {
		fmt.Fprintln(w, "проверка: формат —", err)
		return false
	}
	result := pipelinemcp.VerifyReport(raw)
	printVerification(w, result)
	return result.OK
}

func readReportFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, pipelinemcp.MaxReportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > pipelinemcp.MaxReportBytes {
		return nil, fmt.Errorf("файл больше 64 КиБ")
	}
	return raw, nil
}

func printVerification(w io.Writer, result pipelinemcp.VerifyResult) {
	if result.OK {
		fmt.Fprintln(w, "проверка: ok")
		return
	}
	for _, check := range result.Checks {
		if !check.OK {
			fmt.Fprintf(w, "проверка: %s — %s\n", mcpclient.SafeForTerminal(check.Name), mcpclient.SafeForTerminal(check.Reason))
		}
	}
}

func printTrace(w io.Writer, trace toolagent.Trace) {
	if len(trace.Tools) > 0 {
		parts := make([]string, 0, len(trace.Tools))
		for _, tool := range trace.Tools {
			parts = append(parts, signature(tool))
		}
		fmt.Fprintf(w, "[MCP] tools/list → %d инструмента: %s\n", len(parts), strings.Join(parts, ", "))
	}
	for i, modelCall := range trace.ModelCalls {
		fmt.Fprintf(w, "[модель · шаг %d] finish_reason=%s · вход %d (из кэша %d) · выход %d\n", i+1, mcpclient.SafeForTerminal(modelCall.FinishReason), modelCall.Usage.PromptTokens, modelCall.Usage.PromptCacheHitTokens, modelCall.Usage.CompletionTokens)
		for _, toolCall := range trace.ToolCalls {
			if toolCall.Step != i+1 {
				continue
			}
			fmt.Fprintf(w, "[модель → tool_call] %s %s\n", mcpclient.SafeForTerminal(toolCall.Name), mcpclient.SafeForTerminal(toolCall.RawArguments))
			label := "результат"
			if toolCall.IsError {
				label = "ОШИБКА ИНСТРУМЕНТА"
			}
			fmt.Fprintf(w, "[MCP tools/call → %s] %s\n", label, mcpclient.SafeForTerminal(toolCall.ResultText))
		}
	}
}

func signature(tool toolagent.Tool) string {
	var schema struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(tool.InputSchema, &schema)
	required := make(map[string]bool)
	for _, name := range schema.Required {
		required[name] = true
	}
	keys := make([]string, 0, len(schema.Properties))
	for key := range schema.Properties {
		if !required[key] {
			key += "?"
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return tool.Name + "(" + strings.Join(keys, ", ") + ")"
}

func printSummary(w io.Writer, trace toolagent.Trace) {
	price := "цена неизвестна"
	if trace.Totals.CostKnown {
		price = fmt.Sprintf("$%.6f", trace.Totals.Cost)
	}
	fmt.Fprintf(w, "[day-19: %d вызовов инструментов · %d обращений к модели · %d ток. вход (%d из кэша) · %d выход · %s · %.2f s]\n", trace.Totals.ToolCalls, trace.Totals.ModelCalls, trace.Totals.PromptTokens, trace.Totals.CachedTokens, trace.Totals.OutputTokens, price, trace.Totals.Duration.Seconds())
}

func safeMultiline(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = mcpclient.SafeForTerminal(line)
	}
	return strings.Join(lines, "\n")
}
