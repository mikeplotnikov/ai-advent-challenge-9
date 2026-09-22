package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

type benchQuestion struct {
	group, question, tool, date, code, from, to string
	amount                                      float64
}
type BenchRun struct {
	Group          string          `json:"group"`
	Question       string          `json:"question"`
	Repeat         int             `json:"repeat"`
	Arm            string          `json:"arm"`
	Trace          toolagent.Trace `json:"trace"`
	CalledAny      bool            `json:"calledAny"`
	CalledExpected bool            `json:"calledExpected"`
	ArgsValid      bool            `json:"argsValid"`
	UsedResult     bool            `json:"usedResult"`
	DeadTurn       bool            `json:"deadTurn"`
	FalseCall      bool            `json:"falseCall"`
	Error          string          `json:"error,omitempty"`
}
type Bench struct {
	Revision string     `json:"revision"`
	Started  string     `json:"started"`
	Runs     []BenchRun `json:"runs"`
}

// The branches distinguish decimals from grouped thousands. In particular, the
// first group may be longer than three digits, otherwise 12345.67 is split.
var numberRE = regexp.MustCompile(`[-+]?(?:\d+(?:[ \x{00A0}\x{202F}]\d{3})+(?:[,.]\d+)?|\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:[,.]\d+)?)`)

func numbers(text string) (out []float64) {
	for _, raw := range numberRE.FindAllString(text, -1) {
		value := strings.NewReplacer(" ", "", "\u00a0", "", "\u202f", "").Replace(raw)
		if strings.Contains(value, ",") && strings.Contains(value, ".") {
			value = strings.ReplaceAll(value, ",", "")
		} else {
			value = strings.ReplaceAll(value, ",", ".")
		}
		if number, err := strconv.ParseFloat(value, 64); err == nil {
			out = append(out, number)
		}
	}
	return out
}
func usesValue(answer string, want float64) bool {
	for _, number := range numbers(answer) {
		if math.Abs(number-want) <= .01 {
			return true
		}
	}
	return false
}
func usesAnyValue(answer string, values []float64) bool {
	for _, value := range values {
		if usesValue(answer, value) {
			return true
		}
	}
	return false
}

func runBench(command, endpoint string, timeout time.Duration, stdout, stderr io.Writer) int {
	llm.LoadDotEnv(".env")
	model, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY17")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	transport, err := mcpclient.NewTransport(endpoint, command)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	session, err := mcpclient.NewNamed("ai-advent-day-17-agent", "1").Open(context.Background(), transport)
	if err != nil {
		fmt.Fprintln(stderr, "MCP:", err)
		return 1
	}
	defer session.Close()
	server := toolagent.Server{Name: session.ServerName, Version: session.ServerVersion, Protocol: session.ProtocolVersion, Transport: transport.Description}
	bench := Bench{Revision: revision(), Started: time.Now().Format(time.RFC3339)}
	references := map[string]float64{}
	for _, question := range benchQuestions() {
		for repeat := 1; repeat <= 3; repeat++ {
			run := measure(model, session, question, repeat, "tools", false, 0, timeout)
			run.Trace.Server = server
			if _, exists := references[question.question]; !exists && run.Error == "" && run.ArgsValid {
				if values := toolValues(run.Trace, question); len(values) > 0 {
					references[question.question] = values[0]
				}
			}
			bench.Runs = append(bench.Runs, run)
		}
	}
	for _, question := range benchQuestions() {
		if question.group != "N" {
			run := measure(model, nil, question, 1, "no-tools", true, references[question.question], timeout)
			run.Trace.Server = server
			bench.Runs = append(bench.Runs, run)
		}
	}
	if err := saveBench("day-17/bench.json", bench); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := writeResults("day-17/RESULTS.md", bench); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "[bench] %d прогонов записано\n", len(bench.Runs))
	return 0
}

func measure(model toolagent.LLM, session toolagent.Session, question benchQuestion, repeat int, arm string, noTools bool, reference float64, timeout time.Duration) BenchRun {
	run := BenchRun{Group: question.group, Question: question.question, Repeat: repeat, Arm: arm}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	trace, err := toolagent.Run(ctx, model, session, toolagent.Input{SystemPrompt: promptToday(time.Now()), Question: question.question, NoTools: noTools})
	run.Trace = trace
	if err != nil {
		run.Error = err.Error()
		return run
	}
	run.CalledAny = len(trace.ToolCalls) > 0
	run.FalseCall = question.group == "N" && run.CalledAny
	run.DeadTurn = question.group != "N" && !run.CalledAny && trace.FinalAnswer != ""
	for _, call := range trace.ToolCalls {
		if call.Name == question.tool {
			run.CalledExpected = true
			if validArgs(call.Arguments, question) && !call.IsError {
				run.ArgsValid = true
			}
		}
	}
	if noTools {
		run.UsedResult = reference != 0 && usesValue(trace.FinalAnswer, reference)
	} else {
		run.UsedResult = usesAnyValue(trace.FinalAnswer, toolValues(trace, question))
	}
	return run
}

func validArgs(arguments map[string]any, question benchQuestion) bool {
	if arguments == nil {
		return false
	}
	date, _ := arguments["date"].(string)
	if date != question.date {
		return false
	}
	if question.group == "R" {
		codes, _ := arguments["codes"].([]any)
		if len(codes) == 0 {
			return true
		}
		for _, code := range codes {
			if strings.EqualFold(fmt.Sprint(code), question.code) {
				return true
			}
		}
		return false
	}
	amount, ok := arguments["amount"].(float64)
	return ok && amount == question.amount && strings.EqualFold(fmt.Sprint(arguments["from"]), question.from) && strings.EqualFold(fmt.Sprint(arguments["to"]), question.to)
}

// toolValues reads only the tool result from the same run, never a fixture or a
// number in the question. Rates accept either CBR's value or its unit_rate.
func toolValues(trace toolagent.Trace, question benchQuestion) []float64 {
	for _, call := range trace.ToolCalls {
		if call.Name != question.tool || call.IsError || !validArgs(call.Arguments, question) {
			continue
		}
		var object map[string]any
		if json.Unmarshal([]byte(call.ResultText), &object) != nil {
			continue
		}
		if question.group == "C" {
			if value, ok := object["result"].(float64); ok {
				return []float64{value}
			}
			continue
		}
		rates, _ := object["rates"].([]any)
		for _, raw := range rates {
			rate, _ := raw.(map[string]any)
			if strings.EqualFold(fmt.Sprint(rate["code"]), question.code) {
				values := []float64{}
				if value, ok := rate["value"].(float64); ok {
					values = append(values, value)
				}
				if value, ok := rate["unit_rate"].(float64); ok {
					values = append(values, value)
				}
				return values
			}
		}
	}
	return nil
}

func revision() string {
	output, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}
func saveBench(path string, bench Bench) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(bench)
}

type groupStats struct{ n, excluded, any, expected, args, used, dead, falseCalls int }

func (s groupStats) metric(value int) string { return share(value, s.n) }
func benchStats(bench Bench, group, arm string) groupStats {
	var s groupStats
	for _, run := range bench.Runs {
		if run.Group != group || run.Arm != arm {
			continue
		}
		if run.Error != "" {
			s.excluded++
			continue
		}
		s.n++
		if run.CalledAny {
			s.any++
		}
		if run.CalledExpected {
			s.expected++
		}
		if run.ArgsValid {
			s.args++
		}
		if run.UsedResult {
			s.used++
		}
		if run.DeadTurn {
			s.dead++
		}
		if run.FalseCall {
			s.falseCalls++
		}
	}
	return s
}

func writeResults(path string, bench Bench) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	models := map[string]bool{}
	totalCalls, prompt, cached, output := 0, 0, 0, 0
	cost := 0.0
	costKnown := true
	for _, run := range bench.Runs {
		totalCalls += run.Trace.Totals.ModelCalls
		prompt += run.Trace.Totals.PromptTokens
		cached += run.Trace.Totals.CachedTokens
		output += run.Trace.Totals.OutputTokens
		if !run.Trace.Totals.CostKnown {
			costKnown = false
		}
		cost += run.Trace.Totals.Cost
		for _, call := range run.Trace.ModelCalls {
			if call.Model != "" {
				models[call.Model] = true
			}
		}
	}
	modelList := make([]string, 0, len(models))
	for model := range models {
		modelList = append(modelList, model)
	}
	sort.Strings(modelList)
	if len(modelList) == 0 {
		modelList = []string{"неизвестна"}
	}
	fmt.Fprintln(file, "# Day 17 — надёжность вызова инструментов")
	fmt.Fprintln(file)
	fmt.Fprintf(file, "Коммит: `%s`. Дата запуска: %s. Модель: %s.\n\n", bench.Revision, bench.Started, strings.Join(modelList, ", "))
	fmt.Fprintln(file, "| Группа | n | исключено | calledAny | calledExpected | argsValid | usedResult | deadTurn | falseCall |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, group := range []string{"R", "C", "N"} {
		s := benchStats(bench, group, "tools")
		dead, falseCalls := "—", "—"
		if group == "N" {
			falseCalls = s.metric(s.falseCalls)
		} else {
			dead = s.metric(s.dead)
		}
		fmt.Fprintf(file, "| %s | %d | %d | %s | %s | %s | %s | %s | %s |\n", group, s.n, s.excluded, s.metric(s.any), s.metric(s.expected), s.metric(s.args), s.metric(s.used), dead, falseCalls)
	}
	fmt.Fprintln(file)
	fmt.Fprintln(file, "Контроль `-no-tools` (usedResult):")
	for _, group := range []string{"R", "C"} {
		s := benchStats(bench, group, "no-tools")
		fmt.Fprintf(file, "- %s: %s; исключено %d.\n", group, s.metric(s.used), s.excluded)
	}
	average := 0.0
	if len(bench.Runs) > 0 {
		average = float64(totalCalls) / float64(len(bench.Runs))
	}
	fmt.Fprintf(file, "\nСреднее обращений к модели на прогон: %.2f.\n", average)
	fmt.Fprintf(file, "Токены: вход %d, из кэша %d, выход %d.\n", prompt, cached, output)
	if costKnown {
		fmt.Fprintf(file, "Стоимость: $%.6f.\n", cost)
	} else {
		fmt.Fprintln(file, "Стоимость: цена неизвестна.")
	}
	return nil
}
func share(successes, n int) string {
	if n == 0 {
		return "—"
	}
	lo, hi := stats.Wilson(successes, n)
	return fmt.Sprintf("%d/%d (%.1f%%; 95%% %.1f–%.1f%%)", successes, n, 100*float64(successes)/float64(n), 100*lo, 100*hi)
}

func benchQuestions() []benchQuestion {
	return []benchQuestion{
		{"R", "Какой официальный курс доллара США установил ЦБ РФ на 1 сентября 2026?", "get_currency_rates", "2026-09-01", "USD", "", "", 0}, {"R", "Какой официальный курс евро установил ЦБ РФ на 15 августа 2026?", "get_currency_rates", "2026-08-15", "EUR", "", "", 0}, {"R", "Какой официальный курс китайского юаня установил ЦБ РФ на 1 июля 2026?", "get_currency_rates", "2026-07-01", "CNY", "", "", 0}, {"R", "Какой официальный курс казахстанского тенге установил ЦБ РФ на 10 июня 2026?", "get_currency_rates", "2026-06-10", "KZT", "", "", 0}, {"R", "Какой официальный курс японской иены установил ЦБ РФ на 20 мая 2026?", "get_currency_rates", "2026-05-20", "JPY", "", "", 0}, {"R", "Какой официальный курс фунта стерлингов установил ЦБ РФ на 1 апреля 2026?", "get_currency_rates", "2026-04-01", "GBP", "", "", 0}, {"R", "Какой официальный курс турецкой лиры установил ЦБ РФ на 3 марта 2026?", "get_currency_rates", "2026-03-03", "TRY", "", "", 0}, {"R", "Какой официальный курс армянского драма установил ЦБ РФ на 10 февраля 2026?", "get_currency_rates", "2026-02-10", "AMD", "", "", 0}, {"R", "Какой официальный курс белорусского рубля установил ЦБ РФ на 15 января 2026?", "get_currency_rates", "2026-01-15", "BYN", "", "", 0}, {"R", "Какой официальный курс индийской рупии установил ЦБ РФ на 1 декабря 2025?", "get_currency_rates", "2025-12-01", "INR", "", "", 0},
		{"C", "Сколько рублей можно было получить за 250 долларов США по курсу ЦБ на 1 сентября 2026 года?", "convert_currency", "2026-09-01", "", "USD", "RUB", 250}, {"C", "Сколько тенге можно было получить за 1000 китайских юаней по курсу ЦБ на 1 сентября 2026 года?", "convert_currency", "2026-09-01", "", "CNY", "KZT", 1000}, {"C", "Сколько евро можно было получить за 5000 рублей по курсу ЦБ на 3 августа 2026 года?", "convert_currency", "2026-08-03", "", "RUB", "EUR", 5000}, {"C", "Сколько долларов США можно было получить за 300 евро по курсу ЦБ на 15 июля 2026 года?", "convert_currency", "2026-07-15", "", "EUR", "USD", 300}, {"C", "Сколько рублей можно было получить за 10000 японских иен по курсу ЦБ на 2 июня 2026 года?", "convert_currency", "2026-06-02", "", "JPY", "RUB", 10000}, {"C", "Сколько китайских юаней можно было получить за 750 фунтов стерлингов по курсу ЦБ на 5 мая 2026 года?", "convert_currency", "2026-05-05", "", "GBP", "CNY", 750}, {"C", "Сколько рублей можно было получить за 20000 тенге по курсу ЦБ на 14 апреля 2026 года?", "convert_currency", "2026-04-14", "", "KZT", "RUB", 20000}, {"C", "Сколько евро можно было получить за 150 швейцарских франков по курсу ЦБ на 10 марта 2026 года?", "convert_currency", "2026-03-10", "", "CHF", "EUR", 150}, {"C", "Сколько долларов США можно было получить за 1200 турецких лир по курсу ЦБ на 2 февраля 2026 года?", "convert_currency", "2026-02-02", "", "TRY", "USD", 1200}, {"C", "Сколько китайских юаней можно было получить за 45000 рублей по курсу ЦБ на 20 января 2026 года?", "convert_currency", "2026-01-20", "", "RUB", "CNY", 45000},
		{"N", "Что такое код валюты по стандарту ISO 4217?", "", "", "", "", "", 0}, {"N", "Сколько будет 17 умножить на 23?", "", "", "", "", "", 0}, {"N", "Почему ЦБ указывает для некоторых валют номинал 100 или 10000?", "", "", "", "", "", 0}, {"N", "Как называется валюта Японии?", "", "", "", "", "", 0}, {"N", "Что означает аббревиатура ЦБ РФ?", "", "", "", "", "", 0}, {"N", "Чем официальный курс ЦБ отличается от курса обмена в банке?", "", "", "", "", "", 0}, {"N", "Переведи на английский: курс валюты", "", "", "", "", "", 0}, {"N", "Сколько дней в сентябре?", "", "", "", "", "", 0}, {"N", "Какая валюта в Казахстане?", "", "", "", "", "", 0}, {"N", "Что такое кросс-курс?", "", "", "", "", "", 0},
	}
}
