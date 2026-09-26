package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcprouter"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/pipelinemcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	modelName       = "deepseek-v4-flash"
	modelCallLimit  = 10
	defaultRegistry = "day-20/servers.json"
	defaultQuestion = "Сколько сегодня стоят 1000 долларов в евро по курсу ЦБ и как менялся евро за последние 14 дней? Сохрани отчёт."
	systemPrompt    = "Ты агент с инструментами нескольких MCP-серверов. Имя инструмента имеет вид <сервер>__<инструмент>; если описание или ошибка называет инструмент коротко, вызывай его с префиксом того же сервера. Сегодняшнюю дату ты не знаешь — узнай её инструментом. Инструменты, которым нужна дата, вызывай только после того, как получил её, и передавай дату явно. Курсы валют бери только из инструментов, не из памяти. Вызывай только те инструменты, которые нужны для вопроса. Отвечай по-русски и коротко; если сохранил отчёт, назови имя файла; если какой-то шаг не удался, скажи об этом."
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("day-20", flag.ContinueOnError)
	fs.SetOutput(stderr)
	serversPath := fs.String("servers", defaultRegistry, "путь к реестру MCP-серверов")
	list := fs.Bool("list", false, "показать серверы и инструменты")
	direct := fs.Bool("direct", false, "выполнить демонстрационный флоу без модели")
	bench := fs.Bool("bench", false, "выполнить замер 18×3")
	schemaCost := fs.Bool("schema-cost", false, "измерить цену схем")
	dump := fs.Bool("dump", false, "выгрузить определения и эталоны")
	save := fs.String("save", "", "сохранить полную JSON-трассу")
	timeout := fs.Duration("timeout", 5*time.Minute, "общий таймаут прогона")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	modes := 0
	for _, active := range []bool{*list, *direct, *bench, *schemaCost, *dump} {
		if active {
			modes++
		}
	}
	if modes > 1 {
		fmt.Fprintln(stderr, "-list, -direct, -bench, -schema-cost и -dump нельзя сочетать")
		return 2
	}
	if modes > 0 && *save != "" {
		fmt.Fprintln(stderr, "-save нельзя сочетать с -list, -direct, -bench, -schema-cost или -dump")
		return 2
	}
	if modes > 0 && fs.NArg() != 0 {
		fmt.Fprintln(stderr, "выбранный режим не принимает вопрос")
		return 2
	}
	if *dump {
		if err := writeDump(stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if modes == 0 && fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: day-20 [флаги] \"вопрос\"")
		return 2
	}
	registry, err := mcprouter.LoadRegistry(*serversPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *bench {
		return runBench(registry, *timeout, stdout, stderr)
	}
	if *schemaCost {
		return runSchemaCost(registry, *timeout, stdout, stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	router, err := mcprouter.Open(ctx, registry, mcprouter.Options{})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer router.Close()
	printRegistry(stdout, router)
	if *list {
		return 0
	}
	if *direct {
		if err := directDemo(ctx, router, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	return runAgent(ctx, router, strings.Join(fs.Args(), " "), *save, stdout, stderr)
}

func runAgent(ctx context.Context, router *mcprouter.Router, question, save string, stdout, stderr io.Writer) int {
	llm.LoadDotEnv(".env")
	model, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY20")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	model.Model = modelName
	start := len(router.Journal())
	trace, runErr := toolagent.Run(ctx, model, router, toolagent.Input{
		SystemPrompt: systemPrompt, Question: question, ModelCallLimit: modelCallLimit,
	})
	journal := router.Journal()[start:]
	checks := evaluateChecks(question, trace, router.Servers(), journal)
	wrapped := Day20Trace{Trace: trace, Servers: router.Servers(), Journal: journal, Checks: checks}
	redactTrace(&wrapped, model.APIKey)
	printTrace(stdout, wrapped)
	if save != "" {
		if err := writeJSONAtomic(save, wrapped); err != nil {
			fmt.Fprintln(stderr, "трассу сохранить не удалось:", err)
			return 1
		}
	}
	if runErr != nil {
		fmt.Fprintln(stderr, runErr)
		return 1
	}
	if trace.FinalAnswer == "" || hasFatalJournal(journal) || !checks.RoutedOK || !checks.OrderOK {
		return 1
	}
	return 0
}

func printRegistry(w io.Writer, router *mcprouter.Router) {
	for _, server := range router.Servers() {
		fmt.Fprintf(w, "[MCP] %s → %s %s · %s · %d инструмента\n", server.Alias,
			mcpclient.SafeForTerminal(server.ServerInfo.Name), mcpclient.SafeForTerminal(server.ServerInfo.Version),
			server.Transport, len(server.ToolNames))
	}
	listed, _ := router.ListTools(context.Background())
	for _, tool := range listed.Tools {
		schema, _ := json.Marshal(tool.InputSchema)
		fmt.Fprintf(w, "  %s %s\n", mcpclient.SafeForTerminal(tool.Name), mcpclient.SafeForTerminal(string(schema)))
	}
}

func printTrace(w io.Writer, wrapped Day20Trace) {
	for _, call := range wrapped.Trace.ToolCalls {
		server := "—"
		for _, entry := range wrapped.Journal {
			if entry.Name == call.Name {
				server = entry.Alias
				break
			}
		}
		fmt.Fprintf(w, "[модель · шаг %d → %s] %s %s\n", call.Step, server,
			mcpclient.SafeForTerminal(call.Name), mcpclient.SafeForTerminal(call.RawArguments))
		fmt.Fprintf(w, "[результат] %s\n", mcpclient.SafeForTerminal(call.ResultText))
	}
	fmt.Fprintf(w, "[проверки] routedOK=%v orderOK=%v provenanceStrict=%v rejected=%d\n",
		wrapped.Checks.RoutedOK, wrapped.Checks.OrderOK, wrapped.Checks.ProvenanceStrict,
		wrapped.Checks.RejectedCalls)
	for _, violation := range wrapped.Checks.OrderViolations {
		fmt.Fprintf(w, "[orderOK] %s\n", mcpclient.SafeForTerminal(violation))
	}
	if wrapped.Trace.FinalAnswer != "" {
		fmt.Fprintf(w, "[ответ]\n%s\n", safeMultiline(wrapped.Trace.FinalAnswer))
	}
	price := "цена неизвестна"
	if wrapped.Trace.Totals.CostKnown {
		price = fmt.Sprintf("$%.6f", wrapped.Trace.Totals.Cost)
	}
	fmt.Fprintf(w, "[day-20: %d инструментов · %d обращений · %d ток. вход · %d выход · %s · %.2f s]\n",
		wrapped.Trace.Totals.ToolCalls, wrapped.Trace.Totals.ModelCalls, wrapped.Trace.Totals.PromptTokens,
		wrapped.Trace.Totals.OutputTokens, price, wrapped.Trace.Totals.Duration.Seconds())
}

func directDemo(ctx context.Context, router *mcprouter.Router, stdout io.Writer) error {
	current, err := directCall(ctx, router, "clock__current_date", map[string]any{}, stdout)
	if err != nil {
		return err
	}
	var now struct {
		Date string `json:"date"`
	}
	if err := decodeStructured(current, &now); err != nil || now.Date == "" {
		return fmt.Errorf("current_date: некорректный структурированный результат: %v", err)
	}
	shifted, err := directCall(ctx, router, "clock__shift_date", map[string]any{"date": now.Date, "days": -13}, stdout)
	if err != nil {
		return err
	}
	var from struct {
		Date string `json:"date"`
	}
	if err := decodeStructured(shifted, &from); err != nil || from.Date == "" {
		return fmt.Errorf("shift_date: некорректный структурированный результат: %v", err)
	}
	if _, err := directCall(ctx, router, "rates__convert_currency", map[string]any{
		"amount": 1000, "from": "USD", "to": "EUR", "date": now.Date,
	}, stdout); err != nil {
		return err
	}
	fetch, err := directCall(ctx, router, "pipeline__fetch_rates", map[string]any{
		"currency": "EUR", "date_from": from.Date, "date_to": now.Date,
	}, stdout)
	if err != nil {
		return err
	}
	var fetched pipelinemcp.FetchOutput
	if err := decodeStructured(fetch, &fetched); err != nil || fetched.DatasetID == "" {
		return fmt.Errorf("fetch_rates: некорректный структурированный результат: %v", err)
	}
	summary, err := directCall(ctx, router, "pipeline__summarize_rates", map[string]any{"dataset_id": fetched.DatasetID}, stdout)
	if err != nil {
		return err
	}
	var summarized pipelinemcp.SummarizeOutput
	if err := decodeStructured(summary, &summarized); err != nil || summarized.SummaryID == "" {
		return fmt.Errorf("summarize_rates: некорректный структурированный результат: %v", err)
	}
	saved, err := directCall(ctx, router, "pipeline__save_report", map[string]any{"summary_id": summarized.SummaryID}, stdout)
	if err != nil {
		return err
	}
	var output pipelinemcp.SaveOutput
	if err := decodeStructured(saved, &output); err != nil || output.Path == "" {
		return fmt.Errorf("save_report: некорректный структурированный результат: %v", err)
	}
	fmt.Fprintln(stdout, "сохранённый файл:", output.Path)
	return nil
}

func directCall(ctx context.Context, session toolagent.Session, name string, args map[string]any,
	w io.Writer) (*mcp.CallToolResult, error) {
	raw, _ := json.Marshal(args)
	fmt.Fprintf(w, "[direct → %s] %s\n", name, raw)
	result, err := session.CallTool(ctx, name, args)
	if err != nil {
		return nil, err
	}
	text := mcpclient.ToolText(result)
	if result.IsError {
		return nil, fmt.Errorf("%s", text)
	}
	fmt.Fprintln(w, "[результат]", mcpclient.SafeForTerminal(text))
	return result, nil
}

func decodeStructured(result *mcp.CallToolResult, target any) error {
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func safeMultiline(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = mcpclient.SafeForTerminal(lines[i])
	}
	return strings.Join(lines, "\n")
}

func writeJSONAtomic(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(redactProviderSecrets(raw), '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func redactTrace(trace *Day20Trace, secret string) {
	if secret == "" {
		return
	}
	raw, err := json.Marshal(trace)
	if err != nil {
		return
	}
	raw = redactJSON(raw, []string{secret}, false)
	_ = json.Unmarshal(raw, trace)
}

func redactProviderSecrets(raw []byte) []byte {
	var secrets []string
	for _, item := range os.Environ() {
		name, value, ok := strings.Cut(item, "=")
		if ok && strings.HasPrefix(name, "DEEPSEEK_API_KEY") && value != "" {
			secrets = append(secrets, value)
		}
	}
	return redactJSON(raw, secrets, true)
}

func redactJSON(raw []byte, secrets []string, indent bool) []byte {
	if len(secrets) == 0 {
		return raw
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	value = redactJSONValue(value, secrets)
	var encoded []byte
	if indent {
		encoded, _ = json.MarshalIndent(value, "", "  ")
	} else {
		encoded, _ = json.Marshal(value)
	}
	return encoded
}

func redactJSONValue(value any, secrets []string) any {
	replace := func(text string) string {
		for _, secret := range secrets {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
		return text
	}
	switch typed := value.(type) {
	case string:
		return replace(typed)
	case []any:
		for i := range typed {
			typed[i] = redactJSONValue(typed[i], secrets)
		}
		return typed
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[replace(key)] = redactJSONValue(item, secrets)
		}
		return result
	default:
		return value
	}
}
