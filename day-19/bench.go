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
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/pipelinemcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

//go:embed bench-questions.json
var benchFiles embed.FS

type BenchQuestion struct {
	ID          string `json:"id"`
	Question    string `json:"question"`
	Currency    string `json:"currency"`
	DateFrom    string `json:"date_from"`
	DateTo      string `json:"date_to"`
	Today       string `json:"today"`
	ExpectError bool   `json:"expect_error,omitempty"`
}

type BenchRun struct {
	Question        BenchQuestion   `json:"question"`
	Trace           toolagent.Trace `json:"trace"`
	ChainInOrder    bool            `json:"chainInOrder"`
	IDsExact        bool            `json:"idsExact"`
	PeriodExact     bool            `json:"periodExact"`
	FileVerified    bool            `json:"fileVerified"`
	AnswerNamesFile bool            `json:"answerNamesFile"`
	DidNotInvent    bool            `json:"didNotInventRate"`
	DirectOK        bool            `json:"directOK"`
	Error           string          `json:"error,omitempty"`
}

type BenchReport struct {
	Revision string     `json:"revision"`
	Started  string     `json:"started"`
	Runs     []BenchRun `json:"runs"`
}

var (
	dmyDateRE  = regexp.MustCompile(`\b\d{2}\.\d{2}(?:\.\d{4})?\b`)
	isoDateRE  = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
	rateNumber = regexp.MustCompile(`\d+[,.]\d{3,}`)
)

func containsInventedRate(answer string) bool {
	withoutDates := dmyDateRE.ReplaceAllString(answer, "")
	withoutDates = isoDateRE.ReplaceAllString(withoutDates, "")
	return rateNumber.MatchString(withoutDates)
}

func loadBenchQuestions() ([]BenchQuestion, error) {
	raw, err := benchFiles.ReadFile("bench-questions.json")
	if err != nil {
		return nil, err
	}
	var questions []BenchQuestion
	if err := json.Unmarshal(raw, &questions); err != nil {
		return nil, err
	}
	if len(questions) != 20 {
		return nil, fmt.Errorf("ожидалось 20 вопросов, получено %d", len(questions))
	}
	return questions, nil
}

func runBench(command, reports string, timeout time.Duration, stdout, stderr io.Writer) int {
	questions, err := loadBenchQuestions()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	llm.LoadDotEnv(".env")
	model, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY19")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	model.Model = modelName
	session, err := openSession(context.Background(), command, reports)
	if err != nil {
		fmt.Fprintln(stderr, "MCP:", err)
		return 1
	}
	defer session.Close()
	report := BenchReport{Revision: revision(), Started: time.Now().Format(time.RFC3339)}
	for _, question := range questions {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		today, _ := time.ParseInLocation("2006-01-02", question.Today, cbr.Moscow)
		trace, runErr := toolagent.Run(ctx, model, session, toolagent.Input{SystemPrompt: promptToday(today), Question: question.Question, ModelCallLimit: day19ModelCalls})
		cancel()
		run := evaluateBench(question, trace)
		if runErr != nil {
			run.Error = runErr.Error()
		}
		controlCtx, controlCancel := context.WithTimeout(context.Background(), timeout)
		_, directErr := directChain(controlCtx, session, question.Currency, question.DateFrom, question.DateTo, io.Discard)
		controlCancel()
		run.DirectOK = directControlOK(question, directErr)
		report.Runs = append(report.Runs, run)
		fmt.Fprintf(stdout, "[bench] %s chain=%v ids=%v period=%v file=%v direct=%v\n", question.ID, run.ChainInOrder, run.IDsExact, run.PeriodExact, run.FileVerified, run.DirectOK)
	}
	if err := writeJSONAtomic("day-19/bench.json", report); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := writeBenchResults("day-19/RESULTS.md", report); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := syncReadmeResultsTable("day-19/README.md", "day-19/RESULTS.md"); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "[bench] 20 прогонов записаны в day-19/bench.json и day-19/RESULTS.md")
	return 0
}

func directControlOK(question BenchQuestion, err error) bool {
	if question.ExpectError {
		return err != nil && strings.Contains(err.Error(), cbr.NoPublications)
	}
	return err == nil
}

func evaluateBench(question BenchQuestion, trace toolagent.Trace) BenchRun {
	run := BenchRun{Question: question, Trace: trace}
	if question.ExpectError {
		run.DidNotInvent = trace.FinalAnswer != "" && noLaterTools(trace) && !containsInventedRate(trace.FinalAnswer)
		run.PeriodExact = fetchArgsExact(trace, question)
		return run
	}
	if len(trace.ToolCalls) < 3 {
		return run
	}
	var fetch *toolagent.ToolCall
	var summary *toolagent.ToolCall
	var save *toolagent.ToolCall
	for i := range trace.ToolCalls {
		call := &trace.ToolCalls[i]
		switch call.Name {
		case "fetch_rates":
			if fetch == nil && !call.IsError {
				fetch = call
			}
		case "summarize_rates":
			if summary == nil && !call.IsError {
				summary = call
			}
		case "save_report":
			if save == nil && !call.IsError {
				save = call
			}
		}
	}
	if fetch == nil || summary == nil || save == nil {
		return run
	}
	fi, si, svi := indexCall(trace.ToolCalls, fetch), indexCall(trace.ToolCalls, summary), indexCall(trace.ToolCalls, save)
	run.ChainInOrder = fi < si && si < svi
	var fetched pipelinemcp.FetchOutput
	var summarized pipelinemcp.SummarizeOutput
	var saved pipelinemcp.SaveOutput
	decodeAny(fetch.Structured, &fetched)
	decodeAny(summary.Structured, &summarized)
	decodeAny(save.Structured, &saved)
	run.PeriodExact = callFetchArgsExact(*fetch, question) && strings.EqualFold(fetched.Currency, question.Currency) && fetched.DateFrom == question.DateFrom && fetched.DateTo == question.DateTo
	run.IDsExact = fmt.Sprint(summary.Arguments["dataset_id"]) == fetched.DatasetID && fmt.Sprint(save.Arguments["summary_id"]) == summarized.SummaryID && summarized.DatasetSHA256 == fetched.DatasetSHA256 && saved.Chain.DatasetSHA256 == fetched.DatasetSHA256 && saved.Chain.SummarySHA256 == summarized.SummarySHA256
	if raw, err := os.ReadFile(saved.Path); err == nil {
		run.FileVerified = pipelinemcp.VerifyReport(raw).OK
	}
	run.AnswerNamesFile = saved.Name != "" && strings.Contains(trace.FinalAnswer, saved.Name)
	return run
}

func indexCall(calls []toolagent.ToolCall, target *toolagent.ToolCall) int {
	for i := range calls {
		if &calls[i] == target {
			return i
		}
	}
	return len(calls)
}

func decodeAny(value, target any) {
	raw, _ := json.Marshal(value)
	_ = json.Unmarshal(raw, target)
}

func fetchArgsExact(trace toolagent.Trace, question BenchQuestion) bool {
	for _, call := range trace.ToolCalls {
		if call.Name == "fetch_rates" && callFetchArgsExact(call, question) {
			return true
		}
	}
	return false
}

func callFetchArgsExact(call toolagent.ToolCall, question BenchQuestion) bool {
	return strings.EqualFold(fmt.Sprint(call.Arguments["currency"]), question.Currency) && fmt.Sprint(call.Arguments["date_from"]) == question.DateFrom && fmt.Sprint(call.Arguments["date_to"]) == question.DateTo
}

func noLaterTools(trace toolagent.Trace) bool {
	for _, call := range trace.ToolCalls {
		if call.Name == "summarize_rates" || call.Name == "save_report" {
			return false
		}
	}
	return true
}

type metric struct {
	name         string
	successes, n int
}

func writeBenchResults(path string, report BenchReport) error {
	metrics := []metric{{name: "chainInOrder"}, {name: "idsExact"}, {name: "periodExact"}, {name: "fileVerified"}, {name: "answerNamesFile"}, {name: "didNotInventRate"}, {name: "directControl"}}
	for _, run := range report.Runs {
		for i := range metrics {
			include, success := metricValue(metrics[i].name, run)
			if include {
				metrics[i].n++
				if success {
					metrics[i].successes++
				}
			}
		}
	}
	models := map[string]bool{}
	modelCalls, prompt, cached, output := 0, 0, 0, 0
	cost, costKnown := 0.0, true
	for _, run := range report.Runs {
		modelCalls += run.Trace.Totals.ModelCalls
		prompt += run.Trace.Totals.PromptTokens
		cached += run.Trace.Totals.CachedTokens
		output += run.Trace.Totals.OutputTokens
		cost += run.Trace.Totals.Cost
		costKnown = costKnown && run.Trace.Totals.CostKnown
		for _, call := range run.Trace.ModelCalls {
			models[call.Model] = true
		}
	}
	modelList := make([]string, 0, len(models))
	for model := range models {
		if model != "" {
			modelList = append(modelList, model)
		}
	}
	sort.Strings(modelList)
	var out strings.Builder
	out.WriteString("# Day 19 — надёжность композиции MCP-инструментов\n\n")
	fmt.Fprintf(&out, "Коммит: `%s`. Дата запуска: %s. Модель: %s.\n\n", report.Revision, report.Started, strings.Join(modelList, ", "))
	out.WriteString("| Метрика | Результат |\n|---|---:|\n")
	for _, item := range metrics {
		fmt.Fprintf(&out, "| %s | %s |\n", item.name, share(item.successes, item.n))
	}
	fmt.Fprintf(&out, "\nОбращений к модели: %d. Токены: вход %d, из кэша %d, выход %d.\n", modelCalls, prompt, cached, output)
	if costKnown {
		fmt.Fprintf(&out, "Стоимость: $%.6f.\n", cost)
	} else {
		out.WriteString("Стоимость: цена неизвестна.\n")
	}
	return os.WriteFile(path, []byte(out.String()), 0o644)
}

func metricValue(name string, run BenchRun) (bool, bool) {
	if run.Error != "" && name != "directControl" {
		return false, false
	}
	successQuestion := !run.Question.ExpectError
	switch name {
	case "chainInOrder":
		return successQuestion, run.ChainInOrder
	case "idsExact":
		return successQuestion, run.IDsExact
	case "periodExact":
		return true, run.PeriodExact
	case "fileVerified":
		return successQuestion, run.FileVerified
	case "answerNamesFile":
		return successQuestion, run.AnswerNamesFile
	case "didNotInventRate":
		return run.Question.ExpectError, run.DidNotInvent
	case "directControl":
		return true, run.DirectOK
	default:
		return false, false
	}
}

func share(successes, n int) string {
	if n == 0 {
		return "—"
	}
	lo, hi := stats.Wilson(successes, n)
	return fmt.Sprintf("%d/%d (%.1f%%; 95%% %.1f–%.1f%%)", successes, n, 100*float64(successes)/float64(n), 100*lo, 100*hi)
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
	extract := func(raw []byte) (string, error) {
		lines := strings.Split(string(raw), "\n")
		start, end := -1, -1
		for i, line := range lines {
			if line == "| Метрика | Результат |" {
				start = i
				continue
			}
			if start >= 0 && i > start && !strings.HasPrefix(line, "|") {
				end = i
				break
			}
		}
		if start < 0 || end < 0 {
			return "", fmt.Errorf("таблица метрик не найдена")
		}
		return strings.Join(lines[start:end], "\n"), nil
	}
	oldTable, err := extract(readme)
	if err != nil {
		return err
	}
	newTable, err := extract(results)
	if err != nil {
		return err
	}
	updated := strings.Replace(string(readme), oldTable, newTable, 1)
	return os.WriteFile(readmePath, []byte(updated), 0o644)
}

func revision() string {
	raw, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(raw))
}
