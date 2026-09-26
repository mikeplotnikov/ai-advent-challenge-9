package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcprouter"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/pipelinemcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed bench-scenarios.json
var benchFS embed.FS

const (
	benchToday            = "2026-09-25"
	benchRepeats          = 3
	benchPromptDisclosure = "Промпт уточнён по сценариям L1, S3 и S4 этого же замера (decisions.md дня 20), поэтому замер — не отложенная выборка."
)

type ExpectedConversion struct {
	Amount float64 `json:"amount"`
	From   string  `json:"from"`
	To     string  `json:"to"`
	Date   string  `json:"date"`
}

type ExpectedRate struct {
	Currency string `json:"currency"`
	Date     string `json:"date"`
}

type ExpectedPipeline struct {
	Currency string `json:"currency"`
	Period   string `json:"period"`
	DateFrom string `json:"dateFrom,omitempty"`
	DateTo   string `json:"dateTo,omitempty"`
}

type BenchScenario struct {
	ID          string               `json:"id"`
	Group       string               `json:"group"`
	Question    string               `json:"question"`
	Servers     []string             `json:"servers"`
	Conversions []ExpectedConversion `json:"conversions,omitempty"`
	Rates       []ExpectedRate       `json:"rates,omitempty"`
	Pipelines   []ExpectedPipeline   `json:"pipelines,omitempty"`
	DateOnly    bool                 `json:"dateOnly,omitempty"`
}

type BenchVerdict struct {
	ServersExact     bool   `json:"serversExact"`
	RequiredTools    bool   `json:"requiredTools"`
	ArgsExact        bool   `json:"argsExact"`
	HandoffExact     bool   `json:"handoffExact"`
	OrderOK          bool   `json:"orderOK"`
	RoutedOK         bool   `json:"routedOK"`
	ProvenanceStrict bool   `json:"provenanceStrict"`
	AnswerOK         bool   `json:"answerOK"`
	FlowOK           bool   `json:"flowOK"`
	ExtraCalls       int    `json:"extraCalls"`
	RejectedCalls    int    `json:"rejectedCalls"`
	ServerFailures   int    `json:"serverFailures"`
	DirectControl    bool   `json:"directControl"`
	Excluded         string `json:"excluded,omitempty"`
}

type BenchRun struct {
	Scenario BenchScenario `json:"scenario"`
	Repeat   int           `json:"repeat"`
	Trace    Day20Trace    `json:"trace"`
	Verdict  BenchVerdict  `json:"verdict"`
	Error    string        `json:"error,omitempty"`
}

type BenchReport struct {
	Revision string     `json:"revision"`
	Started  string     `json:"started"`
	Today    string     `json:"today"`
	Runs     []BenchRun `json:"runs"`
}

type benchHooks struct {
	beforeRun     func(*mcprouter.Router, BenchScenario, int)
	beforeControl func(*mcprouter.Router, BenchScenario, int)
}

type benchExecution struct {
	scenarios                     []BenchScenario
	repeats                       int
	timeout                       time.Duration
	model                         toolagent.LLM
	open                          func() (*mcprouter.Router, error)
	benchPath, resultsPath        string
	readmePath                    string
	stdout                        io.Writer
	hooks                         benchHooks
	revision, started, benchToday string
}

func loadBenchScenarios() ([]BenchScenario, error) {
	raw, err := benchFS.ReadFile("bench-scenarios.json")
	if err != nil {
		return nil, err
	}
	var scenarios []BenchScenario
	if err := json.Unmarshal(raw, &scenarios); err != nil {
		return nil, err
	}
	if len(scenarios) != 18 {
		return nil, fmt.Errorf("ожидалось 18 сценариев, получено %d", len(scenarios))
	}
	today, _ := time.Parse("2006-01-02", benchToday)
	for i := range scenarios {
		for j := range scenarios[i].Conversions {
			scenarios[i].Conversions[j].Date = resolveDate(today, scenarios[i].Conversions[j].Date)
		}
		for j := range scenarios[i].Rates {
			scenarios[i].Rates[j].Date = resolveDate(today, scenarios[i].Rates[j].Date)
		}
		for j := range scenarios[i].Pipelines {
			from, to := resolvePeriod(today, scenarios[i].Pipelines[j].Period)
			scenarios[i].Pipelines[j].DateFrom, scenarios[i].Pipelines[j].DateTo = from, to
		}
	}
	return scenarios, nil
}

func resolveDate(today time.Time, expression string) string {
	if expression == "today" {
		return today.Format("2006-01-02")
	}
	if strings.HasPrefix(expression, "today-") {
		days, _ := strconv.Atoi(strings.TrimPrefix(expression, "today-"))
		return today.AddDate(0, 0, -days).Format("2006-01-02")
	}
	return expression
}

func resolvePeriod(today time.Time, expression string) (string, string) {
	if strings.HasPrefix(expression, "last-") {
		days, _ := strconv.Atoi(strings.TrimPrefix(expression, "last-"))
		return today.AddDate(0, 0, -(days - 1)).Format("2006-01-02"), today.Format("2006-01-02")
	}
	if expression == "month-to-today" {
		return time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location()).Format("2006-01-02"),
			today.Format("2006-01-02")
	}
	parts := strings.Split(expression, "..")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

func runBench(registry mcprouter.Registry, timeout time.Duration, stdout, stderr io.Writer) int {
	llm.LoadDotEnv(".env")
	model, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY20")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	model.Model = modelName
	temp, err := os.MkdirTemp("", "day20-bench-")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer os.RemoveAll(temp)
	benchRegistry := registryForBench(registry, temp)
	open := func() (*mcprouter.Router, error) {
		return mcprouter.Open(context.Background(), benchRegistry, mcprouter.Options{})
	}
	config, err := productionBenchExecution(timeout, model, open, stdout)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, err = executeBench(config)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func productionBenchExecution(timeout time.Duration, model toolagent.LLM,
	open func() (*mcprouter.Router, error), stdout io.Writer) (benchExecution, error) {
	scenarios, err := loadBenchScenarios()
	if err != nil {
		return benchExecution{}, err
	}
	return benchExecution{
		scenarios: scenarios, repeats: benchRepeats, timeout: timeout, model: model, open: open,
		benchPath: "day-20/bench.json", resultsPath: "day-20/RESULTS.md", readmePath: "day-20/README.md",
		stdout: stdout, revision: revision(), started: time.Now().Format(time.RFC3339), benchToday: benchToday,
	}, nil
}

func executeBench(config benchExecution) (BenchReport, error) {
	report := BenchReport{Revision: config.revision, Started: config.started, Today: config.benchToday}
	if config.repeats <= 0 {
		config.repeats = 3
	}
	if config.stdout == nil {
		config.stdout = io.Discard
	}
	router, err := config.open()
	if err != nil {
		return report, err
	}
	defer func() {
		if router != nil {
			_ = router.Close()
		}
	}()
	totalRuns := len(config.scenarios) * config.repeats
	runNumber := 0
	for _, scenario := range config.scenarios {
		for repeat := 1; repeat <= config.repeats; repeat++ {
			runNumber++
			router.ResetRun()
			if config.hooks.beforeRun != nil {
				config.hooks.beforeRun(router, scenario, repeat)
			}
			start := len(router.Journal())
			ctx, cancel := context.WithTimeout(context.Background(), config.timeout)
			trace, runErr := toolagent.Run(ctx, config.model, router, toolagent.Input{
				SystemPrompt: systemPrompt, Question: scenario.Question, ModelCallLimit: modelCallLimit,
			})
			cancel()
			journal := router.Journal()[start:]
			checks := evaluateChecks(scenario.Question, trace, router.Servers(), journal)
			wrapped := Day20Trace{Trace: trace, Servers: router.Servers(), Journal: journal, Checks: checks}
			verdict := evaluateBench(scenario, wrapped)
			applyRunErrorExclusion(&verdict, runErr)
			router.ResetRun()
			if config.hooks.beforeControl != nil {
				config.hooks.beforeControl(router, scenario, repeat)
			}
			controlStart := len(router.Journal())
			controlCtx, controlCancel := context.WithTimeout(context.Background(), config.timeout)
			controlErr := directScenario(controlCtx, router, scenario)
			controlCancel()
			controlJournal := router.Journal()[controlStart:]
			verdict.DirectControl = controlErr == nil
			if controlErr != nil && verdict.Excluded == "" {
				verdict.Excluded = "failedControl"
			}
			run := BenchRun{Scenario: scenario, Repeat: repeat, Trace: wrapped, Verdict: verdict}
			if runErr != nil {
				run.Error = runErr.Error()
			}
			report.Runs = append(report.Runs, run)
			fmt.Fprintf(config.stdout, "[bench] %s/%d flow=%v direct=%v excluded=%s\n", scenario.ID, repeat,
				verdict.FlowOK, verdict.DirectControl, verdict.Excluded)
			if runNumber < totalRuns && (needsRegistryRestart(journal) || needsRegistryRestart(controlJournal)) {
				_ = router.Close()
				router, err = config.open()
				if err != nil {
					return report, err
				}
			}
		}
	}
	if err := writeJSONAtomic(config.benchPath, report); err != nil {
		return report, err
	}
	if err := writeBenchResults(config.resultsPath, report); err != nil {
		return report, err
	}
	if err := syncReadmeResultsTable(config.readmePath, config.resultsPath); err != nil {
		return report, err
	}
	return report, nil
}

func needsRegistryRestart(journal []mcprouter.JournalEntry) bool {
	for _, entry := range journal {
		switch entry.Outcome {
		case "timeout", "unavailable", "dead":
			return true
		}
	}
	return false
}

func exclusionForRunError(err error) string {
	if err == nil || strings.Contains(err.Error(), "модель не дала ответа за 10 обращений") {
		return ""
	}
	return "providerOrOverallTimeout"
}

func applyRunErrorExclusion(verdict *BenchVerdict, err error) {
	if verdict.Excluded != "" {
		return
	}
	verdict.Excluded = exclusionForRunError(err)
}

func registryForBench(source mcprouter.Registry, reports string) mcprouter.Registry {
	result := mcprouter.Registry{Servers: make([]mcprouter.Entry, len(source.Servers))}
	copy(result.Servers, source.Servers)
	for i := range result.Servers {
		result.Servers[i].Args = append([]string(nil), result.Servers[i].Args...)
		switch result.Servers[i].Alias {
		case "clock":
			result.Servers[i].Args = append(result.Servers[i].Args, "-now", "2026-09-25T12:00:00+03:00")
		case "pipeline":
			result.Servers[i].Args = replaceFlag(result.Servers[i].Args, "-reports", reports)
		}
	}
	return result
}

func replaceFlag(args []string, name, value string) []string {
	for i := range args {
		if args[i] == name && i+1 < len(args) {
			copyArgs := append([]string(nil), args...)
			copyArgs[i+1] = value
			return copyArgs
		}
	}
	return append(append([]string(nil), args...), name, value)
}

func evaluateBench(scenario BenchScenario, wrapped Day20Trace) BenchVerdict {
	verdict := BenchVerdict{OrderOK: wrapped.Checks.OrderOK, RoutedOK: wrapped.Checks.RoutedOK,
		ProvenanceStrict: wrapped.Checks.ProvenanceStrict, HandoffExact: true}
	reached := map[string]bool{}
	for _, entry := range wrapped.Journal {
		switch entry.Outcome {
		case "ok", "tool_error", "rpc_error":
			reached[entry.Alias] = true
		case "timeout", "unavailable":
			verdict.ServerFailures++
			if verdict.Excluded == "" {
				verdict.Excluded = entry.Outcome
			}
		case "limit", "dead":
			verdict.RejectedCalls++
		}
		if entry.Outcome == "tool_error" && strings.HasPrefix(entry.Text, "ЦБ недоступен:") {
			verdict.Excluded = "cbrFailure"
		}
	}
	verdict.RejectedCalls += countAgentRejected(wrapped.Trace.ToolCalls)
	verdict.ServersExact = sameSet(reached, scenario.Servers)
	required := requiredToolCounts(scenario)
	actual := successfulToolCounts(wrapped.Trace.ToolCalls)
	verdict.RequiredTools = true
	for name, count := range required {
		if actual[name] < count {
			verdict.RequiredTools = false
		}
	}
	verdict.ArgsExact = argsExact(scenario, wrapped.Trace.ToolCalls)
	files, handoff := handoffFiles(scenario, wrapped.Trace.ToolCalls)
	verdict.HandoffExact = handoff
	verdict.AnswerOK = answerOK(scenario, wrapped.Trace, files)
	requiredCalls := 0
	for _, count := range required {
		requiredCalls += count
	}
	if len(wrapped.Trace.ToolCalls) > requiredCalls {
		verdict.ExtraCalls = len(wrapped.Trace.ToolCalls) - requiredCalls
	}
	verdict.FlowOK = verdict.ServersExact && verdict.RequiredTools && verdict.ArgsExact &&
		verdict.HandoffExact && verdict.OrderOK && verdict.RoutedOK && verdict.AnswerOK
	return verdict
}

func requiredToolCounts(s BenchScenario) map[string]int {
	result := map[string]int{}
	if contains(s.Servers, "clock") {
		result["clock__current_date"] = 1
	}
	result["rates__convert_currency"] = len(s.Conversions)
	result["rates__get_currency_rates"] = len(s.Rates)
	result["pipeline__fetch_rates"] = len(s.Pipelines)
	result["pipeline__summarize_rates"] = len(s.Pipelines)
	result["pipeline__save_report"] = len(s.Pipelines)
	return result
}

func successfulToolCounts(calls []toolagent.ToolCall) map[string]int {
	result := map[string]int{}
	for _, call := range calls {
		if successful(call) {
			result[call.Name]++
		}
	}
	return result
}

func argsExact(s BenchScenario, calls []toolagent.ToolCall) bool {
	for _, expected := range s.Conversions {
		if !hasMatchingCall(calls, "rates__convert_currency", func(args map[string]any) bool {
			return number(args["amount"]) == expected.Amount && strings.EqualFold(fmt.Sprint(args["from"]), expected.From) &&
				strings.EqualFold(fmt.Sprint(args["to"]), expected.To) && fmt.Sprint(args["date"]) == expected.Date
		}) {
			return false
		}
	}
	for _, expected := range s.Rates {
		if !hasMatchingCall(calls, "rates__get_currency_rates", func(args map[string]any) bool {
			if fmt.Sprint(args["date"]) != expected.Date {
				return false
			}
			codes, exists := args["codes"]
			if !exists || codes == nil {
				return true
			}
			values, ok := codes.([]any)
			if !ok {
				return false
			}
			if len(values) == 0 {
				return true
			}
			for _, value := range values {
				if strings.EqualFold(fmt.Sprint(value), expected.Currency) {
					return true
				}
			}
			return false
		}) {
			return false
		}
	}
	for _, expected := range s.Pipelines {
		if !hasMatchingCall(calls, "pipeline__fetch_rates", func(args map[string]any) bool {
			return strings.EqualFold(fmt.Sprint(args["currency"]), expected.Currency) &&
				fmt.Sprint(args["date_from"]) == expected.DateFrom && fmt.Sprint(args["date_to"]) == expected.DateTo
		}) {
			return false
		}
	}
	return true
}

func hasMatchingCall(calls []toolagent.ToolCall, name string, match func(map[string]any) bool) bool {
	for _, call := range calls {
		if call.Name == name && successful(call) && match(call.Arguments) {
			return true
		}
	}
	return false
}

func handoffFiles(s BenchScenario, calls []toolagent.ToolCall) ([]pipelinemcp.SaveOutput, bool) {
	files := []pipelinemcp.SaveOutput{}
	for _, expected := range s.Pipelines {
		matched := false
		for _, fetch := range calls {
			if fetch.Name != "pipeline__fetch_rates" || !successful(fetch) ||
				!strings.EqualFold(fmt.Sprint(fetch.Arguments["currency"]), expected.Currency) ||
				fmt.Sprint(fetch.Arguments["date_from"]) != expected.DateFrom ||
				fmt.Sprint(fetch.Arguments["date_to"]) != expected.DateTo {
				continue
			}
			var fetched pipelinemcp.FetchOutput
			decodeAny(fetch.Structured, &fetched)
			for _, summary := range calls {
				if summary.Name != "pipeline__summarize_rates" || !successful(summary) || summary.Step <= fetch.Step ||
					fmt.Sprint(summary.Arguments["dataset_id"]) != fetched.DatasetID {
					continue
				}
				var summarized pipelinemcp.SummarizeOutput
				decodeAny(summary.Structured, &summarized)
				if summarized.DatasetSHA256 != fetched.DatasetSHA256 {
					continue
				}
				for _, save := range calls {
					if save.Name != "pipeline__save_report" || !successful(save) || save.Step <= summary.Step ||
						fmt.Sprint(save.Arguments["summary_id"]) != summarized.SummaryID {
						continue
					}
					var output pipelinemcp.SaveOutput
					decodeAny(save.Structured, &output)
					if output.Chain.DatasetSHA256 == fetched.DatasetSHA256 &&
						output.Chain.SummarySHA256 == summarized.SummarySHA256 {
						files = append(files, output)
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return files, false
		}
	}
	return files, true
}

func answerOK(s BenchScenario, trace toolagent.Trace, files []pipelinemcp.SaveOutput) bool {
	if trace.FinalAnswer == "" {
		return false
	}
	for _, file := range files {
		if file.Name == "" || !strings.Contains(trace.FinalAnswer, file.Name) {
			return false
		}
	}
	usedConversions := make([]bool, len(trace.ToolCalls))
	for _, expected := range s.Conversions {
		index := findConversionForAnswer(trace.ToolCalls, usedConversions, expected, true)
		if index < 0 {
			index = findConversionForAnswer(trace.ToolCalls, usedConversions, expected, false)
		}
		if index < 0 {
			return false
		}
		usedConversions[index] = true
		var output struct {
			Result float64 `json:"result"`
		}
		decodeAny(trace.ToolCalls[index].Structured, &output)
		if !answerHasNearNumber(trace.FinalAnswer, output.Result, 0.5) {
			return false
		}
	}
	for _, expected := range s.Rates {
		found := false
		for _, call := range trace.ToolCalls {
			if call.Name != "rates__get_currency_rates" || !successful(call) {
				continue
			}
			var output struct {
				Rates []struct {
					Code     string  `json:"code"`
					UnitRate float64 `json:"unit_rate"`
				} `json:"rates"`
			}
			decodeAny(call.Structured, &output)
			for _, rate := range output.Rates {
				if strings.EqualFold(rate.Code, expected.Currency) && answerHasNearNumber(trace.FinalAnswer, rate.UnitRate, 0.01) {
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	if s.DateOnly && !answerHasToday(trace.FinalAnswer, benchToday) {
		return false
	}
	return true
}

func findConversionForAnswer(calls []toolagent.ToolCall, used []bool, expected ExpectedConversion, exactDate bool) int {
	for i, call := range calls {
		if used[i] || call.Name != "rates__convert_currency" || !successful(call) ||
			number(call.Arguments["amount"]) != expected.Amount ||
			!strings.EqualFold(fmt.Sprint(call.Arguments["from"]), expected.From) ||
			!strings.EqualFold(fmt.Sprint(call.Arguments["to"]), expected.To) {
			continue
		}
		if exactDate && fmt.Sprint(call.Arguments["date"]) != expected.Date {
			continue
		}
		return i
	}
	return -1
}

var answerNumberRE = regexp.MustCompile(`\d(?:[\d   ]*\d)?(?:[,.]\d+)?`)

func answerHasNearNumber(answer string, expected, tolerance float64) bool {
	for _, raw := range answerNumberRE.FindAllString(answer, -1) {
		normalized := strings.NewReplacer(" ", "", "\u00a0", "", "\u202f", "", ",", ".").Replace(raw)
		value, err := strconv.ParseFloat(normalized, 64)
		if err == nil && abs(value-expected) <= tolerance {
			return true
		}
	}
	return false
}

func answerHasToday(answer, today string) bool {
	value, _ := time.Parse("2006-01-02", today)
	forms := []string{today, value.Format("02.01.2006"), fmt.Sprintf("%d сентября", value.Day())}
	for _, form := range forms {
		if strings.Contains(strings.ToLower(answer), strings.ToLower(form)) {
			return true
		}
	}
	return false
}

func directScenario(ctx context.Context, router *mcprouter.Router, s BenchScenario) error {
	if contains(s.Servers, "clock") {
		result, err := router.CallTool(ctx, "clock__current_date", map[string]any{})
		if err != nil || result.IsError {
			return fmt.Errorf("clock control: %v %s", err, toolText(result))
		}
	}
	for _, expected := range s.Conversions {
		result, err := router.CallTool(ctx, "rates__convert_currency", map[string]any{
			"amount": expected.Amount, "from": expected.From, "to": expected.To, "date": expected.Date,
		})
		if err != nil || result.IsError {
			return fmt.Errorf("convert control: %v %s", err, toolText(result))
		}
	}
	for _, expected := range s.Rates {
		result, err := router.CallTool(ctx, "rates__get_currency_rates", map[string]any{
			"date": expected.Date, "codes": []string{expected.Currency},
		})
		if err != nil || result.IsError {
			return fmt.Errorf("rates control: %v %s", err, toolText(result))
		}
	}
	for _, expected := range s.Pipelines {
		fetch, err := router.CallTool(ctx, "pipeline__fetch_rates", map[string]any{
			"currency": expected.Currency, "date_from": expected.DateFrom, "date_to": expected.DateTo,
		})
		if err != nil || fetch.IsError {
			return fmt.Errorf("fetch control: %v %s", err, toolText(fetch))
		}
		var fetched pipelinemcp.FetchOutput
		if err := decodeStructured(fetch, &fetched); err != nil || fetched.DatasetID == "" {
			return fmt.Errorf("fetch control: некорректный структурированный результат: %v", err)
		}
		summary, err := router.CallTool(ctx, "pipeline__summarize_rates", map[string]any{"dataset_id": fetched.DatasetID})
		if err != nil || summary.IsError {
			return fmt.Errorf("summary control: %v %s", err, toolText(summary))
		}
		var summarized pipelinemcp.SummarizeOutput
		if err := decodeStructured(summary, &summarized); err != nil || summarized.SummaryID == "" {
			return fmt.Errorf("summary control: некорректный структурированный результат: %v", err)
		}
		save, err := router.CallTool(ctx, "pipeline__save_report", map[string]any{"summary_id": summarized.SummaryID})
		if err != nil || save.IsError {
			return fmt.Errorf("save control: %v %s", err, toolText(save))
		}
		var saved pipelinemcp.SaveOutput
		if err := decodeStructured(save, &saved); err != nil || saved.Path == "" {
			return fmt.Errorf("save control: некорректный структурированный результат: %v", err)
		}
	}
	return nil
}

func writeBenchResults(path string, report BenchReport) error {
	metrics := []struct {
		name string
		get  func(BenchVerdict) bool
	}{
		{"serversExact", func(v BenchVerdict) bool { return v.ServersExact }},
		{"requiredTools", func(v BenchVerdict) bool { return v.RequiredTools }},
		{"argsExact", func(v BenchVerdict) bool { return v.ArgsExact }},
		{"handoffExact", func(v BenchVerdict) bool { return v.HandoffExact }},
		{"orderOK", func(v BenchVerdict) bool { return v.OrderOK }},
		{"routedOK", func(v BenchVerdict) bool { return v.RoutedOK }},
		{"provenanceStrict", func(v BenchVerdict) bool { return v.ProvenanceStrict }},
		{"answerOK", func(v BenchVerdict) bool { return v.AnswerOK }},
		{"flowOK", func(v BenchVerdict) bool { return v.FlowOK }},
		{"directControl", func(v BenchVerdict) bool { return v.DirectControl }},
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# День 20 — результаты\n\n%s\n\nДата: %s · коммит: `%s` · today: %s.\n\n",
		benchPromptDisclosure, report.Started, report.Revision, report.Today)
	b.WriteString("<!-- metrics:start -->\n| Метрика | Результат | 95% ДИ Уилсона |\n|---|---:|---:|\n")
	for _, metric := range metrics {
		passed, total := 0, 0
		for _, run := range report.Runs {
			if metric.name != "directControl" && run.Verdict.Excluded != "" {
				continue
			}
			total++
			if metric.get(run.Verdict) {
				passed++
			}
		}
		low, high := stats.Wilson(passed, total)
		fmt.Fprintf(&b, "| %s | %d/%d | %.1f%%–%.1f%% |\n", metric.name, passed, total, low*100, high*100)
		for _, group := range []string{"L", "T", "P", "S"} {
			groupPassed, groupTotal := 0, 0
			for _, run := range report.Runs {
				if run.Scenario.Group != group || (metric.name != "directControl" && run.Verdict.Excluded != "") {
					continue
				}
				groupTotal++
				if metric.get(run.Verdict) {
					groupPassed++
				}
			}
			groupLow, groupHigh := stats.Wilson(groupPassed, groupTotal)
			fmt.Fprintf(&b, "| %s (%s) | %d/%d | %.1f%%–%.1f%% |\n", metric.name, group,
				groupPassed, groupTotal, groupLow*100, groupHigh*100)
		}
	}
	b.WriteString("<!-- metrics:end -->\n")
	exclusions := map[string]int{}
	modelCalls, promptTokens, outputTokens := 0, 0, 0
	cost, costKnown := 0.0, true
	lRuns, lCalls, lServers := 0, 0, 0
	for _, run := range report.Runs {
		if run.Verdict.Excluded != "" {
			exclusions[run.Verdict.Excluded]++
		}
		modelCalls += run.Trace.Trace.Totals.ModelCalls
		promptTokens += run.Trace.Trace.Totals.PromptTokens
		outputTokens += run.Trace.Trace.Totals.OutputTokens
		cost += run.Trace.Trace.Totals.Cost
		costKnown = costKnown && run.Trace.Trace.Totals.CostKnown
		if run.Scenario.Group == "L" && run.Verdict.Excluded == "" {
			lRuns++
			lCalls += len(run.Trace.Trace.ToolCalls)
			reached := map[string]bool{}
			for _, entry := range run.Trace.Journal {
				if entry.Outcome == "ok" || entry.Outcome == "tool_error" || entry.Outcome == "rpc_error" {
					reached[entry.Alias] = true
				}
			}
			lServers += len(reached)
		}
	}
	b.WriteString("\nИсключения: ")
	if len(exclusions) == 0 {
		b.WriteString("нет. ")
	} else {
		fmt.Fprintf(&b, "%s. ", formatExclusions(exclusions))
	}
	fmt.Fprintf(&b, "Обращения к модели: %d; токены: вход %d, выход %d; ",
		modelCalls, promptTokens, outputTokens)
	if costKnown {
		fmt.Fprintf(&b, "цена: $%.6f.\n", cost)
	} else {
		b.WriteString("цена неизвестна.\n")
	}
	if lRuns > 0 {
		fmt.Fprintf(&b, "Группа L: в среднем %.2f вызова и %.2f сервера на прогон.\n",
			float64(lCalls)/float64(lRuns), float64(lServers)/float64(lRuns))
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if schemaSection := betweenMarkers(string(existing), "<!-- schema-cost:start -->", "<!-- schema-cost:end -->"); schemaSection != "" {
		if !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
		b.WriteString(schemaSection)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func formatExclusions(exclusions map[string]int) string {
	reasons := make([]string, 0, len(exclusions))
	for reason := range exclusions {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s — %d", reason, exclusions[reason]))
	}
	return strings.Join(parts, ", ")
}

func syncReadmeResultsTable(readmePath, resultsPath string) error {
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		return err
	}
	results, err := os.ReadFile(resultsPath)
	if err != nil {
		return err
	}
	section := betweenMarkers(string(results), "<!-- metrics:start -->", "<!-- metrics:end -->")
	if section == "" {
		return fmt.Errorf("таблица метрик не найдена в RESULTS")
	}
	updated := replaceBetween(string(readme), "<!-- metrics:start -->", "<!-- metrics:end -->", section)
	if updated == "" {
		return fmt.Errorf("маркеры таблицы не найдены в README")
	}
	return os.WriteFile(readmePath, []byte(updated), 0o644)
}

func betweenMarkers(text, start, end string) string {
	a, b := strings.Index(text, start), strings.Index(text, end)
	if a < 0 || b < a {
		return ""
	}
	return text[a : b+len(end)]
}

func replaceBetween(text, start, end, replacement string) string {
	a, b := strings.Index(text, start), strings.Index(text, end)
	if a < 0 || b < a {
		return ""
	}
	return text[:a] + replacement + text[b+len(end):]
}

func revision() string {
	output, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

func countAgentRejected(calls []toolagent.ToolCall) int {
	count := 0
	for _, call := range calls {
		if call.Rejected != "" {
			count++
		}
	}
	return count
}

func sameSet(actual map[string]bool, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for _, value := range expected {
		if !actual[value] {
			return false
		}
	}
	return true
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func number(value any) float64 {
	result, _ := strconv.ParseFloat(fmt.Sprint(value), 64)
	return result
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func decodeAny(value, target any) {
	raw, _ := json.Marshal(value)
	_ = json.Unmarshal(raw, target)
}

func toolText(result *mcp.CallToolResult) string { return mcpclient.ToolText(result) }
