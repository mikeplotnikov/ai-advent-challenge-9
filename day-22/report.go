package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

type reportData struct {
	Sections map[string][][]string
	Order    []string
}

func writeReport(runPath, questionsPath, resultsPath, showcasePath string) error {
	run, err := readRun(runPath)
	if err != nil {
		return err
	}
	_, questionsSHA, err := loadQuestions(questionsPath)
	if err != nil {
		return err
	}
	if questionsSHA != run.Meta.QuestionsSHA256 {
		return fmt.Errorf("sha256 questions.json разошёлся: run=%s current=%s", run.Meta.QuestionsSHA256, questionsSHA)
	}
	if current := promptsSHA256(); current != run.Meta.PromptsSHA256 {
		return fmt.Errorf("sha256 текстов промптов разошёлся: run=%s current=%s", run.Meta.PromptsSHA256, current)
	}
	report := buildReportData(run)
	markdown := renderResults(run, runPath, report)
	showcase := Showcase{Meta: run.Meta, Prompts: map[string]string{
		"system": SystemPrompt, "norag_user": NoRAGUserTemplate, "rag_user": RAGUserTemplate,
	}, Questions: run.Questions, Results: report.Sections}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(showcase); err != nil {
		return err
	}
	if err := writeReportPairAtomic(resultsPath, markdown, showcasePath, encoded.Bytes()); err != nil {
		return err
	}
	return nil
}

func buildReportData(run Run) reportData {
	sections := map[string][][]string{}
	main := [][]string{{"Срез", "Без RAG", "С RAG"}}
	appendSuccess := func(label string, include func(Question) bool) {
		n, no, with := 0, 0, 0
		for _, question := range run.Questions {
			if !include(question.Question) {
				continue
			}
			n++
			if majority(question.NoRAG) {
				no++
			}
			if majority(question.RAG) {
				with++
			}
		}
		main = append(main, []string{label, rateCell(no, n), rateCell(with, n)})
	}
	appendSuccess("Все вопросы", func(Question) bool { return true })
	appendSuccess("in_base", func(q Question) bool { return q.Kind == "in_base" })
	appendSuccess("general", func(q Question) bool { return q.Kind == "general" })
	appendSuccess("out_of_base", func(q Question) bool { return q.Kind == "out_of_base" })
	sections["Успех по вопросам"] = main

	b, c := 0, 0
	for _, question := range run.Questions {
		no, with := majority(question.NoRAG), majority(question.RAG)
		if !no && with {
			b++
		}
		if no && !with {
			c++
		}
	}
	p := stats.McNemarExact(b, c)
	verdict := "не показано на N = 10"
	if p < 0.05 && b > c {
		verdict = "показано при α = 0,05"
	} else if p < 0.05 && c > b {
		verdict = "показано обратное: без RAG лучше при α = 0,05"
	}
	needed := "—"
	if verdict == "не показано на N = 10" {
		needed = neededDiscordance(b, c, len(run.Questions))
	}
	sections["Макнемар"] = [][]string{
		{"Гипотеза", "RAG лучше", "Без RAG лучше", "p", "Вывод", "Нужно несогласных пар"},
		{"RAG увеличивает число успешных вопросов", fmt.Sprint(b), fmt.Sprint(c), fmt.Sprintf("%.4f", p), verdict, needed},
	}

	outcomes := []string{"correct", "partial", "unknown", "wrong", "declined", "fabricated", "empty", "marker_with_facts"}
	outcomeRows := [][]string{{"Исход", "Без RAG", "С RAG"}}
	for _, outcome := range outcomes {
		counts := []int{0, 0}
		for _, question := range run.Questions {
			for mode, calls := range [][]Call{question.NoRAG, question.RAG} {
				for _, call := range calls {
					if call.Score.Outcome == outcome || outcome == "marker_with_facts" && call.Score.MarkerWithFacts {
						counts[mode]++
					}
				}
			}
		}
		outcomeRows = append(outcomeRows, []string{outcome, fmt.Sprint(counts[0]), fmt.Sprint(counts[1])})
	}
	sections["Исходы ответов (N = 30 на режим)"] = outcomeRows

	searchRows := [][]string{{"Вопрос", "Evidence rank", "Evidence hit@5", "Source hit@5"}}
	evidenceHits, sourceHits, searchN := 0, 0, 0
	for _, question := range run.Questions {
		if question.Question.Kind != "in_base" {
			continue
		}
		searchN++
		rank, source := 0, false
		for _, chunk := range question.Chunks {
			if rank == 0 && chunk.EvidenceHit {
				rank = chunk.Rank
			}
			if chunk.SourceHit {
				source = true
			}
		}
		if rank > 0 && rank <= run.Meta.K {
			evidenceHits++
		}
		if source {
			sourceHits++
		}
		searchRows = append(searchRows, []string{question.Question.ID, rankCell(rank), yesNo(rank > 0 && rank <= run.Meta.K), yesNo(source)})
	}
	searchRows = append(searchRows, []string{"Итого (N = 7)", "—", fmt.Sprintf("%d/%d", evidenceHits, searchN), fmt.Sprintf("%d/%d", sourceHits, searchN)})
	sections["Поиск"] = searchRows

	citationRows := [][]string{{"Метрика", "Значение"}}
	allFound, expected, uncited, citationN := 0, 0, 0, 0
	for _, question := range run.Questions {
		if question.Question.Kind != "in_base" {
			continue
		}
		for _, call := range question.RAG {
			citationN++
			if len(call.Score.Citations) > 0 && call.Score.AllCitationsFound {
				allFound++
			}
			if call.Score.ExpectedCitation {
				expected++
			}
			if call.Score.Uncited {
				uncited++
			}
		}
	}
	citationRows = append(citationRows,
		[]string{"Все ссылки ведут на найденные чанки", fmt.Sprintf("%d/%d", allFound, citationN)},
		[]string{"Есть ссылка на ожидаемый источник", fmt.Sprintf("%d/%d", expected, citationN)},
		[]string{"Факты без ссылок (uncited)", fmt.Sprint(uncited)})
	sections["Ссылки RAG (7 in_base)"] = citationRows

	economy := [][]string{{"Режим", "Вход", "Выход", "Кэш", "Цена", "Медиана времени"}}
	for _, mode := range []string{"norag", "rag"} {
		var input, output, cache int
		var cost float64
		known := true
		var durations []int64
		for _, question := range run.Questions {
			calls := question.NoRAG
			if mode == "rag" {
				calls = question.RAG
			}
			for _, call := range calls {
				input += call.Usage.PromptTokens
				output += call.Usage.CompletionTokens
				cache += call.Usage.PromptCacheHitTokens
				cost += call.CostUSD
				known = known && call.CostKnown
				durations = append(durations, call.DurationMS)
			}
		}
		costCell := "неизвестна"
		if known {
			costCell = fmt.Sprintf("$%.6f", cost)
		}
		economy = append(economy, []string{mode, fmt.Sprint(input), fmt.Sprint(output), fmt.Sprint(cache), costCell, fmt.Sprintf("%d ms", median(durations))})
	}
	sections["Токены, цена и время"] = economy

	perQuestion := [][]string{{"ID", "Вид", "Вопрос", "Ожидание", "Ранг эталона", "Без RAG ×3", "С RAG ×3"}}
	for _, question := range run.Questions {
		rank := 0
		for _, chunk := range question.Chunks {
			if rank == 0 && chunk.EvidenceHit {
				rank = chunk.Rank
			}
		}
		perQuestion = append(perQuestion, []string{question.Question.ID, question.Question.Kind, question.Question.Question,
			question.Question.Expectation, rankCell(rank), outcomesCell(question.NoRAG), outcomesCell(question.RAG)})
	}
	sections["По вопросам"] = perQuestion
	return reportData{Sections: sections, Order: []string{"Успех по вопросам", "Макнемар", "Исходы ответов (N = 30 на режим)", "Поиск", "Ссылки RAG (7 in_base)", "Токены, цена и время", "По вопросам"}}
}

func renderResults(run Run, runPath string, report reportData) []byte {
	var out strings.Builder
	out.WriteString("# Результаты дня 22\n\n")
	fmt.Fprintf(&out, "- Коммит: `%s`\n- Время запуска: `%s`\n- Модель ответа: `%s` (запрошена `%s`)\n- Температура: %g\n- max_tokens: %d\n- k: %d\n- R: %d\n- sha256 questions.json: `%s`\n- sha256 structure.json: `%s`\n- sha256 MANIFEST.json: `%s`\n- sha256 промптов: `%s`\n- Сырьё: `%s`\n\n",
		run.Meta.Commit, run.Meta.StartedAt.Format("2006-01-02T15:04:05Z07:00"), strings.Join(run.Meta.ResponseModels, ", "), run.Meta.RequestedModel,
		run.Meta.Temperature, run.Meta.MaxTokens, run.Meta.K, run.Meta.Repeats, run.Meta.QuestionsSHA256,
		run.Meta.IndexSHA256, run.Meta.ManifestSHA256, run.Meta.PromptsSHA256, filepathSlash(run.Meta.RunPath))
	for _, name := range report.Order {
		fmt.Fprintf(&out, "## %s\n\n", name)
		out.WriteString(renderTable(report.Sections[name]))
		out.WriteByte('\n')
	}
	out.WriteString("## Ограничения\n\nОдна модель, 10 вопросов; факты проверяются шаблонами. Повторы при temperature = 0 могут падать вместе, поэтому эффективное N ближе к 10, чем к 30.\n")
	return []byte(out.String())
}

func renderTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("| " + strings.Join(escapeRow(rows[0]), " | ") + " |\n")
	separators := make([]string, len(rows[0]))
	for i := range separators {
		separators[i] = "---"
	}
	out.WriteString("| " + strings.Join(separators, " | ") + " |\n")
	for _, row := range rows[1:] {
		out.WriteString("| " + strings.Join(escapeRow(row), " | ") + " |\n")
	}
	return out.String()
}

func escapeRow(row []string) []string {
	out := make([]string, len(row))
	for i, cell := range row {
		out[i] = strings.ReplaceAll(strings.ReplaceAll(cell, "|", "\\|"), "\n", " ")
	}
	return out
}

func rateCell(successes, n int) string {
	lo, hi := stats.Wilson(successes, n)
	return fmt.Sprintf("%d/%d (%.2f–%.2f)", successes, n, lo, hi)
}
func rankCell(rank int) string {
	if rank == 0 {
		return "—"
	}
	return fmt.Sprint(rank)
}
func yesNo(value bool) string {
	if value {
		return "да"
	}
	return "нет"
}
func outcomesCell(calls []Call) string {
	values := make([]string, len(calls))
	for i, call := range calls {
		values[i] = call.Score.Outcome
	}
	return strings.Join(values, ", ")
}
func filepathSlash(path string) string { return strings.ReplaceAll(path, "\\", "/") }
func median(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	copied := append([]int64(nil), values...)
	sort.Slice(copied, func(i, j int) bool { return copied[i] < copied[j] })
	n := len(copied)
	if n%2 == 1 {
		return copied[n/2]
	}
	return (copied[n/2-1] + copied[n/2]) / 2
}
func neededDiscordance(b, c, n int) string {
	for target := b + 1; target <= n-c; target++ {
		if target > c && stats.McNemarExact(target, c) < 0.05 {
			return fmt.Sprintf("%d:%d (ещё %d в пользу RAG)", target, c, target-b)
		}
	}
	return fmt.Sprintf("недостижимо при N = %d", n)
}

func writeReportPairAtomic(resultsPath string, results []byte, showcasePath string, showcase []byte) error {
	resultsTemp, err := prepareAtomicFile(resultsPath, results)
	if err != nil {
		return fmt.Errorf("подготовить RESULTS.md: %w", err)
	}
	defer os.Remove(resultsTemp)
	showcaseTemp, err := prepareAtomicFile(showcasePath, showcase)
	if err != nil {
		return fmt.Errorf("подготовить showcase.json: %w", err)
	}
	defer os.Remove(showcaseTemp)
	previousResults, readErr := os.ReadFile(resultsPath)
	hadResults := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("прочитать прежний RESULTS.md: %w", readErr)
	}
	if err := os.Rename(resultsTemp, resultsPath); err != nil {
		return fmt.Errorf("заменить RESULTS.md: %w", err)
	}
	if err := os.Rename(showcaseTemp, showcasePath); err != nil {
		var rollbackErr error
		if hadResults {
			rollbackErr = writeBytesAtomic(resultsPath, previousResults)
		} else {
			rollbackErr = os.Remove(resultsPath)
			if os.IsNotExist(rollbackErr) {
				rollbackErr = nil
			}
		}
		if rollbackErr != nil {
			return fmt.Errorf("заменить showcase.json: %v; откатить RESULTS.md: %w", err, rollbackErr)
		}
		return fmt.Errorf("заменить showcase.json: %w", err)
	}
	return nil
}

func prepareAtomicFile(path string, data []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	name := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(name)
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	keep = true
	return name, nil
}
