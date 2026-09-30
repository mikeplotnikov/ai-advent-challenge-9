package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

const (
	secRetrieval = "Поиск по режимам (40 вопросов с эталоном)"
	secNegatives = "Вопросы без ответа в базе"
	secMcNemar   = "Макнемар: эталон в контексте"
	secSweep     = "Развертка порога косинуса (только косинус, без реранкера)"
	secAnswers   = "Ответы (10 вопросов дня 22)"
	secOutcomes  = "Исходы ответов (N = 10 на режим)"
	secCost      = "Токены и цена по стадиям"
	secPerQ      = "По вопросам"
)

var reportOrder = []string{secRetrieval, secNegatives, secMcNemar, secSweep, secAnswers, secOutcomes, secCost, secPerQ}

// writeReport rebuilds RESULTS.md and showcase.json from run.json alone, after
// checking that the question sets and prompts are the ones the run used.
func writeReport(runPath, day21Path, day22Path, resultsPath, showcasePath string) error {
	run, err := readRun(runPath)
	if err != nil {
		return err
	}
	_, _, day21SHA, day22SHA, err := loadQuestions(day21Path, day22Path)
	if err != nil {
		return err
	}
	if day21SHA != run.Meta.Day21SHA256 {
		return fmt.Errorf("sha256 набора дня 21 разошёлся: run=%s current=%s", run.Meta.Day21SHA256, day21SHA)
	}
	if day22SHA != run.Meta.Day22SHA256 {
		return fmt.Errorf("sha256 набора дня 22 разошёлся: run=%s current=%s", run.Meta.Day22SHA256, day22SHA)
	}
	if current := promptsSHA256(); current != run.Meta.PromptsSHA256 {
		return fmt.Errorf("sha256 текстов промптов разошёлся: run=%s current=%s", run.Meta.PromptsSHA256, current)
	}
	sections := buildSections(run)
	showcase, err := encodeJSON(buildShowcase(run, sections))
	if err != nil {
		return err
	}
	if err := writeBytesAtomic(showcasePath, showcase); err != nil {
		return fmt.Errorf("записать showcase.json: %w", err)
	}
	if err := writeBytesAtomic(resultsPath, renderResults(run, sections)); err != nil {
		return fmt.Errorf("записать RESULTS.md: %w", err)
	}
	return nil
}

// buildShowcase keeps the ten answered questions and drops the exact messages: the
// page shows the prompt templates, and the chunk texts are already in the candidates.
func buildShowcase(run Run, sections map[string][][]string) Showcase {
	var questions []QuestionRun
	for _, question := range run.Questions {
		if !question.Answered {
			continue
		}
		question.Rewrite.Messages = nil
		modes := make([]ModeRun, len(question.Modes))
		for i, mode := range question.Modes {
			if mode.Rerank != nil {
				rerank := *mode.Rerank
				rerank.Messages = nil
				mode.Rerank = &rerank
			}
			if mode.Answer != nil {
				answer := *mode.Answer
				answer.Messages = nil
				mode.Answer = &answer
			}
			modes[i] = mode
		}
		question.Modes = modes
		questions = append(questions, question)
	}
	return Showcase{Meta: run.Meta, Prompts: promptTexts(), Questions: questions, Results: sections, Order: reportOrder}
}

func buildSections(run Run) map[string][][]string {
	sections := map[string][][]string{}
	modeHeader := append([]string{}, allModes...)

	retrieval := [][]string{{"Режим", "Эталон в контексте", "Чанков в контексте (среднее)", "Токенов контекста (среднее)"}}
	for _, mode := range allModes {
		hits, n, chunks, tokens := 0, 0, 0, 0
		for _, question := range run.Questions {
			if !hasEvidence(question.Question) {
				continue
			}
			n++
			m := question.mode(mode)
			if m.EvidencePosition() > 0 {
				hits++
			}
			for _, candidate := range m.Context() {
				chunks++
				tokens += candidate.Tokens
			}
		}
		retrieval = append(retrieval, []string{mode, rateCell(hits, n), average(chunks, n), average(tokens, n)})
	}
	sections[secRetrieval] = retrieval

	negatives := [][]string{append([]string{"Вопрос"}, modeHeader...)}
	for _, question := range run.Questions {
		if !isNegative(question.Question) {
			continue
		}
		row := []string{question.Question.ID}
		for _, mode := range allModes {
			row = append(row, contextCell(*question.mode(mode)))
		}
		negatives = append(negatives, row)
	}
	sections[secNegatives] = negatives

	b, c, n := 0, 0, 0
	for _, question := range run.Questions {
		if !hasEvidence(question.Question) {
			continue
		}
		n++
		plain := question.mode(modePlain).EvidencePosition() > 0
		improved := question.mode(modeRewriteFilter).EvidencePosition() > 0
		if improved && !plain {
			b++
		}
		if plain && !improved {
			c++
		}
	}
	p := stats.McNemarExact(b, c)
	verdict := fmt.Sprintf("не показано на N = %d", n)
	if p < 0.05 && b > c {
		verdict = "показано при α = 0,05: rewrite_filter чаще доносит эталон"
	} else if p < 0.05 && c > b {
		verdict = "показано обратное при α = 0,05: plain чаще доносит эталон"
	}
	sections[secMcNemar] = [][]string{
		{"Гипотеза", "Только rewrite_filter", "Только plain", "p", "Вывод"},
		{"rewrite_filter и plain доносят эталон до модели с разной частотой", fmt.Sprint(b), fmt.Sprint(c), fmt.Sprintf("%.4f", p), verdict},
	}

	sweep := [][]string{{"Порог косинуса", "Эталон сохранён в top-10", "Чанков после порога (среднее)", "Без ответа: пустой контекст"}}
	for _, threshold := range cosSweep {
		hits, n, kept, emptyNeg, negN := 0, 0, 0, 0, 0
		for _, question := range run.Questions {
			pool := question.mode(modeFilter).Candidates
			left, hit := 0, false
			for _, candidate := range pool {
				if candidate.Similarity >= threshold {
					left++
					hit = hit || candidate.EvidenceHit
				}
			}
			if hasEvidence(question.Question) {
				n++
				kept += left
				if hit {
					hits++
				}
			}
			if isNegative(question.Question) {
				negN++
				if left == 0 {
					emptyNeg++
				}
			}
		}
		label := fmt.Sprintf("%.2f", threshold)
		if threshold == run.Meta.Params.Cos {
			label += " (выбран)"
		}
		sweep = append(sweep, []string{label, fmt.Sprintf("%d/%d", hits, n), average(kept, n), fmt.Sprintf("%d/%d", emptyNeg, negN)})
	}
	sections[secSweep] = sweep

	answers := [][]string{append([]string{"Срез"}, modeHeader...)}
	for _, slice := range []struct {
		label   string
		include func(Question) bool
	}{
		{"Все вопросы", func(Question) bool { return true }},
		{"in_base", func(q Question) bool { return q.Kind == "in_base" }},
		{"general", func(q Question) bool { return q.Kind == "general" }},
		{"out_of_base", func(q Question) bool { return q.Kind == "out_of_base" }},
	} {
		row := []string{slice.label}
		for _, mode := range allModes {
			success, n := 0, 0
			for _, question := range run.Questions {
				if !question.Answered || !slice.include(question.Question) {
					continue
				}
				n++
				if answer := question.mode(mode).Answer; answer != nil && answer.Score.Success {
					success++
				}
			}
			row = append(row, rateCell(success, n))
		}
		answers = append(answers, row)
	}
	sections[secAnswers] = answers

	outcomes := [][]string{append([]string{"Исход"}, modeHeader...)}
	for _, outcome := range []string{"correct", "partial", "unknown", "wrong", "declined", "fabricated", "empty", "filtered_out"} {
		row := []string{outcome}
		for _, mode := range allModes {
			count := 0
			for _, question := range run.Questions {
				answer := question.mode(mode).Answer
				if answer == nil {
					continue
				}
				if answer.Score.Outcome == outcome || outcome == "filtered_out" && answer.FilteredOut {
					count++
				}
			}
			row = append(row, fmt.Sprint(count))
		}
		outcomes = append(outcomes, row)
	}
	sections[secOutcomes] = outcomes

	cost := [][]string{{"Стадия", "Вызовов", "Вход", "Выход", "Кэш", "Цена"}}
	type stage struct {
		label string
		pick  func(QuestionRun) *Call
	}
	stages := []stage{{"rewrite (общий для rewrite и rewrite_filter)", func(q QuestionRun) *Call { return &q.Rewrite }}}
	for _, mode := range []string{modeFilter, modeRewriteFilter} {
		mode := mode
		stages = append(stages, stage{"rerank · " + mode, func(q QuestionRun) *Call { return q.mode(mode).Rerank }})
	}
	for _, mode := range allModes {
		mode := mode
		stages = append(stages, stage{"ответ · " + mode, func(q QuestionRun) *Call {
			if answer := q.mode(mode).Answer; answer != nil && !answer.FilteredOut {
				return answer
			}
			return nil
		}})
	}
	for _, s := range stages {
		calls, input, output, cache := 0, 0, 0, 0
		total, known := 0.0, true
		for _, question := range run.Questions {
			call := s.pick(question)
			if call == nil {
				continue
			}
			calls += len(call.Attempts)
			input += call.Usage.PromptTokens
			output += call.Usage.CompletionTokens
			cache += call.Usage.PromptCacheHitTokens
			total += call.CostUSD
			known = known && call.CostKnown
		}
		cost = append(cost, []string{s.label, fmt.Sprint(calls), fmt.Sprint(input), fmt.Sprint(output), fmt.Sprint(cache), costCell(total, known)})
	}
	cost = append(cost, []string{"Итого", fmt.Sprint(run.Meta.ModelCallsCount), "", "", "", costCell(run.Meta.TotalCostUSD, run.Meta.TotalCostKnown)})
	sections[secCost] = cost

	perQuestion := [][]string{{"ID", "Вопрос", "Переписанный запрос", "Эталон: plain", "rewrite", "filter", "rewrite_filter", "Ответы (plain / rewrite / filter / rewrite_filter)"}}
	for _, question := range run.Questions {
		row := []string{question.Question.ID, question.Question.Question, question.Rewritten}
		for _, mode := range allModes {
			row = append(row, evidenceCell(question, *question.mode(mode)))
		}
		answersCell := "—"
		if question.Answered {
			var values []string
			for _, mode := range allModes {
				values = append(values, question.mode(mode).Answer.Score.Outcome)
			}
			answersCell = strings.Join(values, " / ")
		}
		perQuestion = append(perQuestion, append(row, answersCell))
	}
	sections[secPerQ] = perQuestion
	return sections
}

func renderResults(run Run, sections map[string][][]string) []byte {
	var out strings.Builder
	p := run.Meta.Params
	out.WriteString("# Результаты дня 23\n\n")
	fmt.Fprintf(&out, "- Коммит: `%s`\n- Время запуска: `%s`\n- Модель: `%s` (запрошена `%s`), temperature 0\n",
		run.Meta.Commit, run.Meta.StartedAt.Format("2006-01-02T15:04:05Z07:00"), strings.Join(run.Meta.ResponseModels, ", "), run.Meta.RequestedModel)
	fmt.Fprintf(&out, "- Без фильтра: top-%d. С фильтром: top-%d → косинус ≥ %.2f → реранкер ≥ %d → top-%d\n",
		p.KPlain, p.KBefore, p.Cos, p.MinScore, p.KAfter)
	fmt.Fprintf(&out, "- Вопросов: %d с эталоном (день 21) + g01, x01, x02; ответы — на %d вопросах дня 22, R = 1\n", run.Meta.RetrievalN, run.Meta.AnsweredN)
	fmt.Fprintf(&out, "- Вызовов модели: %d, цена прогона: %s\n", run.Meta.ModelCallsCount, costCell(run.Meta.TotalCostUSD, run.Meta.TotalCostKnown))
	fmt.Fprintf(&out, "- sha256 наборов: день 21 `%s`, день 22 `%s`\n- sha256 индекса: `%s`\n- sha256 манифеста: `%s`\n- sha256 промптов: `%s`\n- Сырьё: `%s`\n\n",
		run.Meta.Day21SHA256, run.Meta.Day22SHA256, run.Meta.IndexSHA256, run.Meta.ManifestSHA256, run.Meta.PromptsSHA256, run.Meta.RunPath)
	for _, name := range reportOrder {
		fmt.Fprintf(&out, "## %s\n\n", name)
		out.WriteString(renderTable(sections[name]))
		out.WriteByte('\n')
	}
	out.WriteString("## Ограничения\n\n")
	out.WriteString("Порог косинуса выбран по данным дня 21 — тем же вопросам, на которых идёт замер: это настройка, а не проверка на отложенной выборке. ")
	out.WriteString("Вопросов без ответа в базе два; x01 отсекается одним порогом косинуса, реранкер на таком вопросе проверяет только x02. ")
	out.WriteString("Сужение контекста показано описательно (чанки, токены), не проверено тестом. Одна модель, R = 1 (в дне 22 три повтора при temperature 0 совпали во всех 20 строках); факты ответа проверяются шаблонами.\n")
	return []byte(out.String())
}

func contextCell(mode ModeRun) string {
	chunks := mode.Context()
	if len(chunks) > 0 {
		return fmt.Sprintf("%d чанков", len(chunks))
	}
	reasons := map[string]bool{}
	for _, candidate := range mode.Candidates {
		reasons[candidate.Cut] = true
	}
	var names []string
	for reason := range reasons {
		names = append(names, reason)
	}
	sort.Strings(names)
	return "пусто (" + strings.Join(names, ", ") + ")"
}

// evidenceCell is the context position of the evidence, or why it did not get there.
func evidenceCell(question QuestionRun, mode ModeRun) string {
	if !hasEvidence(question.Question) {
		return "—"
	}
	if position := mode.EvidencePosition(); position > 0 {
		return fmt.Sprintf("%d", position)
	}
	for _, candidate := range mode.Candidates {
		if candidate.EvidenceHit {
			return "отсечён (" + candidate.Cut + ")"
		}
	}
	return "не найден"
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

func average(total, n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f", float64(total)/float64(n))
}

func costCell(total float64, known bool) string {
	if !known {
		return "неизвестна"
	}
	return fmt.Sprintf("$%.6f", total)
}
