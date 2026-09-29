package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/stats"
)

const (
	defaultResultsPath = "day-21/RESULTS.md"
	defaultComparePath = "day-21/compare.json"
)

type QuestionResult struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	FixedRank     int    `json:"fixed_rank,omitempty"`
	StructureRank int    `json:"structure_rank,omitempty"`
}

type Comparison struct {
	Revision        string           `json:"revision"`
	Started         string           `json:"started"`
	Model           string           `json:"model"`
	ManifestSHA256  string           `json:"manifest_sha256"`
	QuestionsSHA256 string           `json:"questions_sha256"`
	Questions       []QuestionResult `json:"questions"`
}

type ResultTables struct {
	Chunks  [][]string `json:"chunks"`
	Search  [][]string `json:"search"`
	McNemar [][]string `json:"mcnemar"`
}

func evaluate(index Index, questions []Question, vectors [][]float64) (RetrievalMetrics, error) {
	if len(questions) != len(vectors) {
		return RetrievalMetrics{}, fmt.Errorf("вопросов %d, векторов %d", len(questions), len(vectors))
	}
	ranks := make([]int, len(questions))
	for i, question := range questions {
		results, err := rankChunks(index, vectors[i], 10)
		if err != nil {
			return RetrievalMetrics{}, err
		}
		ranks[i] = firstEvidenceRank(results, question.Evidence)
	}
	return metricsFromRanks(ranks), nil
}

func runComparison(ctx context.Context, model, manifestSHA, questionsSHA string, questions []Question, fixed, structure Index, embedder Embedder, stderrWriter interface{ Write([]byte) (int, error) }) (Comparison, RetrievalMetrics, RetrievalMetrics, error) {
	vectors := make([][]float64, len(questions))
	for i, question := range questions {
		vector, _, _, err := embedder.Embed(ctx, "question:"+question.ID, question.Question)
		if err != nil {
			return Comparison{}, RetrievalMetrics{}, RetrievalMetrics{}, err
		}
		if len(vector) != fixed.Header.Dimension {
			return Comparison{}, RetrievalMetrics{}, RetrievalMetrics{}, fmt.Errorf("размерность вопроса %s: %d, в индексе %d: повторите `go run ./day-21 -index`", question.ID, len(vector), fixed.Header.Dimension)
		}
		vectors[i] = vector
		if (i+1)%10 == 0 {
			fmt.Fprintf(stderrWriter, "compare: %d/%d\n", i+1, len(questions))
		}
	}
	fixedMetrics, err := evaluate(fixed, questions, vectors)
	if err != nil {
		return Comparison{}, RetrievalMetrics{}, RetrievalMetrics{}, err
	}
	structureMetrics, err := evaluate(structure, questions, vectors)
	if err != nil {
		return Comparison{}, RetrievalMetrics{}, RetrievalMetrics{}, err
	}
	comparison := Comparison{
		Revision: revision(), Started: time.Now().Format(time.RFC3339), Model: model,
		ManifestSHA256: manifestSHA, QuestionsSHA256: questionsSHA,
		Questions: make([]QuestionResult, len(questions)),
	}
	for i, question := range questions {
		comparison.Questions[i] = QuestionResult{ID: question.ID, Source: question.Source, FixedRank: fixedMetrics.Ranks[i], StructureRank: structureMetrics.Ranks[i]}
	}
	return comparison, fixedMetrics, structureMetrics, nil
}

func revision() string {
	output, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

func writeJSONAtomic(path string, value any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

func writeResults(path string, comparison Comparison, manifest Manifest, documents []Document, questions []Question, fixedIndex, structureIndex Index, fixedMetrics, structureMetrics RetrievalMetrics, fixedPath, structurePath string) error {
	tables, significant, paired := buildResultTables(questions, documents, fixedIndex, structureIndex, fixedMetrics, structureMetrics, fixedPath, structurePath)
	var builder strings.Builder
	fmt.Fprintln(&builder, "<!-- Сгенерировано `go run ./day-21 -compare`. Руками не править. -->")
	fmt.Fprintln(&builder)
	fmt.Fprintf(&builder, "Коммит: `%s`. Дата запуска: %s.\n\n", comparison.Revision, comparison.Started)
	fmt.Fprintln(&builder, "# День 21 — сравнение индексов")
	fmt.Fprintln(&builder)
	fmt.Fprintln(&builder, "## Корпус")
	totalRunes := 0
	for _, file := range manifest.Files {
		totalRunes += file.Runes
	}
	fmt.Fprintf(&builder, "Файлов: %d. Знаков: %d. Страниц по 1 800 знаков: %d.\n\n", len(manifest.Files), totalRunes, (totalRunes+1799)/1800)
	fmt.Fprintf(&builder, "Манифест: sha256 %s. Модель: `%s`, размерность: %d.\n\n", comparison.ManifestSHA256, comparison.Model, fixedIndex.Header.Dimension)
	fmt.Fprintf(&builder, "Вопросы: sha256 %s\n\n", comparison.QuestionsSHA256)

	fmt.Fprintln(&builder, "## Чанки")
	fmt.Fprintln(&builder, markdownTableRow(tables.Chunks[0]))
	fmt.Fprintln(&builder, "|---|---:|---:|---:|---|---:|---:|")
	for _, row := range tables.Chunks[1:] {
		fmt.Fprintln(&builder, markdownTableRow(row))
	}
	fmt.Fprintln(&builder)

	fmt.Fprintln(&builder, "## Поиск")
	fmt.Fprintln(&builder, markdownTableRow(tables.Search[0]))
	fmt.Fprintln(&builder, "|---|---:|---:|---:|---:|")
	for _, row := range tables.Search[1:] {
		fmt.Fprintln(&builder, markdownTableRow(row))
	}
	fmt.Fprintln(&builder)
	fmt.Fprintln(&builder, markdownTableRow(tables.McNemar[0]))
	fmt.Fprintln(&builder, "|---:|---:|---:|---:|---:|---:|---|")
	for _, row := range tables.McNemar[1:] {
		fmt.Fprintln(&builder, markdownTableRow(row))
	}
	fmt.Fprintln(&builder)
	fmt.Fprintln(&builder, "По построению набора каждая цитата помещается в чанк обеих стратегий, поэтому сравнивается ранжирование, а не способность вместить ответ.")
	fmt.Fprintln(&builder)
	fmt.Fprintln(&builder, "## По вопросам")
	fmt.Fprintln(&builder, "| ID | Файл | fixed | structure |")
	fmt.Fprintln(&builder, "|---|---|---:|---:|")
	for _, result := range comparison.Questions {
		fmt.Fprintf(&builder, "| %s | `%s` | %s | %s |\n", result.ID, result.Source, rankText(result.FixedRank), rankText(result.StructureRank))
	}
	fmt.Fprintln(&builder)
	fmt.Fprintln(&builder, "## Выводы")
	for i, k := range []int{1, 3, 5} {
		if !significant[i] {
			fmt.Fprintf(&builder, "- hit@%d: стратегии не различимы на 40 вопросах после поправки Холма.\n", k)
		} else if paired[i][1] > paired[i][2] {
			fmt.Fprintf(&builder, "- hit@%d: fixed выше; разница показана тестом Макнемара после поправки Холма.\n", k)
		} else {
			fmt.Fprintf(&builder, "- hit@%d: structure выше; разница показана тестом Макнемара после поправки Холма.\n", k)
		}
	}
	return writeTextAtomic(path, builder.String())
}

func buildResultTables(questions []Question, documents []Document, fixedIndex, structureIndex Index, fixedMetrics, structureMetrics RetrievalMetrics, fixedPath, structurePath string) (ResultTables, []bool, [][4]int) {
	fixedExtra := fmt.Sprintf("spans > 1: %d/%d; разрезано цитат: %d/40", countMultiSpan(fixedIndex.Chunks), len(fixedIndex.Chunks), countCutEvidence(questions, fixedIndex.Chunks, documents))
	structureExtra := fmt.Sprintf("разделов поделено: %d; чанков < 50 токенов: %d", splitSectionCount(structureIndex.Chunks), countShortChunks(structureIndex.Chunks))
	tables := ResultTables{
		Chunks: [][]string{
			{"Стратегия", "Чанков", "Токены min / медиана / p95 / max", "Граница внутри абзаца", "Дополнительно", "Размер индекса", "Время эмбеддинга"},
			{"fixed", fmt.Sprint(len(fixedIndex.Chunks)), tokenSummary(fixedIndex.Chunks), boundaryShare(fixedIndex.Chunks, documents), fixedExtra, fileSize(fixedPath), durationMS(fixedIndex.Header.EmbeddingDurationMS)},
			{"structure", fmt.Sprint(len(structureIndex.Chunks)), tokenSummary(structureIndex.Chunks), boundaryShare(structureIndex.Chunks, documents), structureExtra, fileSize(structurePath), durationMS(structureIndex.Header.EmbeddingDurationMS)},
		},
		Search: [][]string{
			{"Стратегия", "hit@1", "hit@3", "hit@5", "MRR@10 (95% bootstrap)"},
			{"fixed", hitShare(fixedMetrics.Hits[1], len(questions)), hitShare(fixedMetrics.Hits[3], len(questions)), hitShare(fixedMetrics.Hits[5], len(questions)), mrrSummary(fixedMetrics.Ranks)},
			{"structure", hitShare(structureMetrics.Hits[1], len(questions)), hitShare(structureMetrics.Hits[3], len(questions)), hitShare(structureMetrics.Hits[5], len(questions)), mrrSummary(structureMetrics.Ranks)},
		},
		McNemar: [][]string{{"k", "Обе попали", "Только fixed (b)", "Только structure (c)", "Обе мимо", "p Макнемара", "Холм α=0,05"}},
	}
	pValues := make([]float64, 3)
	paired := make([][4]int, 3)
	for i, k := range []int{1, 3, 5} {
		paired[i] = pairedTable(fixedMetrics.Ranks, structureMetrics.Ranks, k)
		pValues[i] = stats.McNemarExact(paired[i][1], paired[i][2])
	}
	significant := stats.Holm(pValues, 0.05)
	for i, k := range []int{1, 3, 5} {
		verdict := "разница не показана"
		if significant[i] {
			verdict = "разница показана"
		}
		table := paired[i]
		tables.McNemar = append(tables.McNemar, []string{fmt.Sprint(k), fmt.Sprint(table[0]), fmt.Sprint(table[1]), fmt.Sprint(table[2]), fmt.Sprint(table[3]), fmt.Sprintf("%.6f", pValues[i]), verdict})
	}
	return tables, significant, paired
}

func markdownTableRow(cells []string) string {
	return "| " + strings.Join(cells, " | ") + " |"
}

func writeTextAtomic(path, text string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(name)
		}
	}()
	if _, err := temporary.WriteString(text); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	keep = true
	return nil
}

func hitShare(successes, n int) string {
	lo, hi := stats.Wilson(successes, n)
	return fmt.Sprintf("%d/%d (%.1f%%; 95%% %.1f–%.1f%%)", successes, n, 100*float64(successes)/float64(n), 100*lo, 100*hi)
}

func mrrSummary(ranks []int) string {
	values := make([]float64, len(ranks))
	for i, rank := range ranks {
		if rank > 0 && rank <= 10 {
			values[i] = 1 / float64(rank)
		}
	}
	statistic := func(sample []int) float64 {
		total := 0.0
		for _, index := range sample {
			total += values[index]
		}
		return total / float64(len(sample))
	}
	lo, hi := stats.BootstrapCI(len(values), 10000, 210921, statistic)
	point := 0.0
	for _, value := range values {
		point += value
	}
	if len(values) > 0 {
		point /= float64(len(values))
	}
	return fmt.Sprintf("%.3f (%.3f–%.3f)", point, lo, hi)
}

func pairedTable(fixed, structure []int, k int) [4]int {
	var table [4]int
	for i := range fixed {
		fixedHit := fixed[i] > 0 && fixed[i] <= k
		structureHit := structure[i] > 0 && structure[i] <= k
		switch {
		case fixedHit && structureHit:
			table[0]++
		case fixedHit:
			table[1]++
		case structureHit:
			table[2]++
		default:
			table[3]++
		}
	}
	return table
}

func tokenSummary(chunks []Chunk) string {
	if len(chunks) == 0 {
		return "—"
	}
	values := make([]int, len(chunks))
	for i, chunk := range chunks {
		values[i] = chunk.Tokens
	}
	sort.Ints(values)
	median := float64(values[len(values)/2])
	if len(values)%2 == 0 {
		median = float64(values[len(values)/2-1]+values[len(values)/2]) / 2
	}
	p95 := values[int(math.Ceil(0.95*float64(len(values))))-1]
	return fmt.Sprintf("%d / %.1f / %d / %d", values[0], median, p95, values[len(values)-1])
}

func boundaryShare(chunks []Chunk, documents []Document) string {
	bySource := map[string][]rune{}
	for _, document := range documents {
		bySource[document.Source] = document.Runes
	}
	inside := 0
	for _, chunk := range chunks {
		if boundaryInsideParagraph(bySource[chunk.Source], chunk.End) {
			inside++
		}
	}
	return fmt.Sprintf("%d/%d (%.1f%%)", inside, len(chunks), 100*float64(inside)/float64(len(chunks)))
}

func boundaryInsideParagraph(text []rune, end int) bool {
	if end <= 0 || end >= len(text) {
		return false
	}
	if text[end-1] == '\n' {
		previousStart := end - 1
		for previousStart > 0 && text[previousStart-1] != '\n' {
			previousStart--
		}
		if strings.TrimSpace(string(text[previousStart:end])) == "" {
			return false
		}
	}
	nextEnd := end
	for nextEnd < len(text) && text[nextEnd] != '\n' {
		nextEnd++
	}
	nextLine := strings.TrimSpace(string(text[end:nextEnd]))
	if nextLine == "" || isATXHeading(nextLine) {
		return false
	}
	return true
}

func isATXHeading(line string) bool {
	runes := []rune(strings.TrimLeft(line, " "))
	level := 0
	for level < len(runes) && level < 6 && runes[level] == '#' {
		level++
	}
	return level > 0 && (level == len(runes) || unicode.IsSpace(runes[level]))
}

func countMultiSpan(chunks []Chunk) int {
	count := 0
	for _, chunk := range chunks {
		if chunk.Spans > 1 {
			count++
		}
	}
	return count
}

func splitSectionCount(chunks []Chunk) int {
	count := 0
	for _, chunk := range chunks {
		if chunk.Part == 1 {
			count++
		}
	}
	return count
}

func countShortChunks(chunks []Chunk) int {
	count := 0
	for _, chunk := range chunks {
		if chunk.Tokens < 50 {
			count++
		}
	}
	return count
}

func countCutEvidence(questions []Question, chunks []Chunk, documents []Document) int {
	docBySource := map[string]Document{}
	boundaries := map[string][]int{}
	for _, document := range documents {
		docBySource[document.Source] = document
	}
	for _, chunk := range chunks {
		if chunk.Start > 0 {
			boundaries[chunk.Source] = append(boundaries[chunk.Source], chunk.Start)
		}
		if chunk.End < len(docBySource[chunk.Source].Runes) {
			boundaries[chunk.Source] = append(boundaries[chunk.Source], chunk.End)
		}
	}
	count := 0
	for _, question := range questions {
		start, end, ok := collapsedMatchRange(docBySource[question.Source].Runes, question.Evidence)
		if !ok {
			continue
		}
		for _, boundary := range boundaries[question.Source] {
			if start < boundary && boundary < end {
				count++
				break
			}
		}
	}
	return count
}

func collapsedMatchRange(text []rune, evidence string) (int, int, bool) {
	collapsed, positions := collapseWithPositions(text)
	needle := []rune(collapseWhitespace(evidence))
	index := runeIndex(collapsed, needle)
	if index < 0 || len(needle) == 0 {
		return 0, 0, false
	}
	return positions[index], positions[index+len(needle)-1] + 1, true
}

func collapseWithPositions(text []rune) ([]rune, []int) {
	var output []rune
	var positions []int
	inWhitespace := false
	for index, r := range text {
		if unicode.IsSpace(r) {
			if len(output) > 0 && !inWhitespace {
				output = append(output, ' ')
				positions = append(positions, index)
			}
			inWhitespace = true
			continue
		}
		output = append(output, r)
		positions = append(positions, index)
		inWhitespace = false
	}
	if len(output) > 0 && output[len(output)-1] == ' ' {
		output = output[:len(output)-1]
		positions = positions[:len(positions)-1]
	}
	return output, positions
}

func runeIndex(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func fileSize(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "—"
	}
	return fmt.Sprintf("%d байт", info.Size())
}

func durationMS(milliseconds int64) string {
	return (time.Duration(milliseconds) * time.Millisecond).Round(time.Millisecond).String()
}

func rankText(rank int) string {
	if rank == 0 {
		return "—"
	}
	return fmt.Sprint(rank)
}

func sha256File(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
