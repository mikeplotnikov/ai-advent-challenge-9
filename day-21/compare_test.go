package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingEmbedder struct {
	inputs []string
	ids    []string
	byText map[string][]float64
}

func (embedder *recordingEmbedder) Embed(_ context.Context, id, input string) ([]float64, int, bool, error) {
	embedder.ids = append(embedder.ids, id)
	embedder.inputs = append(embedder.inputs, input)
	return append([]float64(nil), embedder.byText[input]...), 1, false, nil
}

func TestEvaluateRanksEvidenceAndComputesMetricsEndToEnd(t *testing.T) {
	chunks := []Chunk{
		{ChunkID: "a", Text: "эталон один", Embedding: []float64{1, 0}},
		{ChunkID: "b", Text: "помеха b", Embedding: []float64{0, .9}},
		{ChunkID: "c", Text: "помеха c", Embedding: []float64{0, .8}},
		{ChunkID: "d", Text: "помеха d", Embedding: []float64{0, .7}},
		{ChunkID: "e", Text: "эталон два", Embedding: []float64{0, .6}},
		{ChunkID: "f", Text: "помеха f", Embedding: []float64{0, .5}},
		{ChunkID: "g", Text: "помеха g", Embedding: []float64{0, .4}},
		{ChunkID: "h", Text: "помеха h", Embedding: []float64{0, .3}},
		{ChunkID: "i", Text: "помеха i", Embedding: []float64{0, .2}},
		{ChunkID: "j", Text: "помеха j", Embedding: []float64{0, .1}},
		{ChunkID: "z", Text: "эталон три", Embedding: []float64{1, 0}},
	}
	index := Index{Header: IndexHeader{Dimension: 2}, Chunks: chunks}
	questions := []Question{{Evidence: "эталон один"}, {Evidence: "эталон два"}, {Evidence: "эталон три"}}
	metrics, err := evaluate(index, questions, [][]float64{{1, 0}, {0, 1}, {-1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	wantRanks := []int{1, 4, 0}
	for i, want := range wantRanks {
		if metrics.Ranks[i] != want {
			t.Fatalf("вопрос %d: rank=%d, ожидался %d", i, metrics.Ranks[i], want)
		}
	}
	if metrics.Hits[1] != 1 || metrics.Hits[3] != 1 || metrics.Hits[5] != 2 || metrics.MRR10 != (1.0+.25)/3 {
		t.Fatalf("метрики: %+v", metrics)
	}
}

func TestRunComparisonEmbedsEachQuestionAndUsesBothIndexes(t *testing.T) {
	questions := []Question{
		{ID: "q1", Question: "первый вопрос", Evidence: "ответ один", Source: "doc.md"},
		{ID: "q2", Question: "второй вопрос", Evidence: "ответ два", Source: "doc.md"},
	}
	fixed := Index{Header: IndexHeader{Dimension: 2}, Chunks: []Chunk{
		{ChunkID: "fixed:a", Text: "ответ один", Embedding: []float64{1, 0}},
		{ChunkID: "fixed:b", Text: "ответ два", Embedding: []float64{0, 1}},
	}}
	structure := Index{Header: IndexHeader{Dimension: 2}, Chunks: []Chunk{
		{ChunkID: "structure:a", Text: "ответ один", Embedding: []float64{0, 1}},
		{ChunkID: "structure:b", Text: "ответ два", Embedding: []float64{1, 0}},
	}}
	embedder := &recordingEmbedder{byText: map[string][]float64{"первый вопрос": {1, 0}, "второй вопрос": {0, 1}}}
	comparison, fixedMetrics, structureMetrics, err := runComparison(context.Background(), "bge-m3", "manifest", "questions", questions, fixed, structure, embedder, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(embedder.inputs, "|") != "первый вопрос|второй вопрос" || strings.Join(embedder.ids, "|") != "question:q1|question:q2" {
		t.Fatalf("вызовы embedder: ids=%v inputs=%v", embedder.ids, embedder.inputs)
	}
	if fixedMetrics.Ranks[0] != 1 || fixedMetrics.Ranks[1] != 1 || structureMetrics.Ranks[0] != 2 || structureMetrics.Ranks[1] != 2 {
		t.Fatalf("fixed=%v structure=%v", fixedMetrics.Ranks, structureMetrics.Ranks)
	}
	if comparison.Questions[0].FixedRank != 1 || comparison.Questions[0].StructureRank != 2 || comparison.Questions[1].FixedRank != 1 || comparison.Questions[1].StructureRank != 2 {
		t.Fatalf("сырые ранги перепутаны: %+v", comparison.Questions)
	}
}

func TestWriteResultsIncludesRequiredStatisticsAndMcNemarVerdict(t *testing.T) {
	directory := t.TempDir()
	fixedPath := filepath.Join(directory, "fixed.json")
	structurePath := filepath.Join(directory, "structure.json")
	if err := os.WriteFile(fixedPath, []byte("fixed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(structurePath, []byte("structure"), 0o644); err != nil {
		t.Fatal(err)
	}
	document := testDocument("doc.md", "# Документ\nуникальная цитата для синтетического отчёта длиной больше сорока символов")
	chunk := Chunk{ChunkID: "fixed:doc.md:0", Strategy: "fixed", Source: "doc.md", Title: "Документ", Section: "Документ", Spans: 1, Start: 0, End: len(document.Runes), Runes: len(document.Runes), Tokens: 60, Text: document.Text, Embedding: []float64{1, 0}}
	fixedIndex := Index{Header: IndexHeader{Dimension: 2, EmbeddingDurationMS: 1200}, Chunks: []Chunk{chunk}}
	structureChunk := chunk
	structureChunk.ChunkID = "structure:doc.md:0"
	structureChunk.Strategy = "structure"
	structureIndex := Index{Header: IndexHeader{Dimension: 2, EmbeddingDurationMS: 800}, Chunks: []Chunk{structureChunk}}
	questions := make([]Question, 40)
	comparison := Comparison{Revision: "abc1234", Started: "2026-09-29T12:00:00+04:00", Model: "bge-m3", ManifestSHA256: "manifest", QuestionsSHA256: "questions", Questions: make([]QuestionResult, 40)}
	for i := range questions {
		questions[i] = Question{ID: "q", Source: "doc.md", Evidence: "уникальная цитата для синтетического отчёта длиной больше сорока символов"}
		comparison.Questions[i] = QuestionResult{ID: "q", Source: "doc.md", FixedRank: 1}
	}
	fixedMetrics := metricsFromRanks(make([]int, 40))
	for i := range fixedMetrics.Ranks {
		fixedMetrics.Ranks[i] = 1
	}
	fixedMetrics = metricsFromRanks(fixedMetrics.Ranks)
	structureMetrics := metricsFromRanks(make([]int, 40))
	manifest := Manifest{Files: []ManifestFile{{Path: "doc.md", Runes: len(document.Runes)}}}
	resultsPath := filepath.Join(directory, "RESULTS.md")
	if err := writeResults(resultsPath, comparison, manifest, []Document{document}, questions, fixedIndex, structureIndex, fixedMetrics, structureMetrics, fixedPath, structurePath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"<!-- Сгенерировано `go run ./day-21 -compare`. Руками не править. -->",
		"## Корпус", "## Чанки", "## Поиск", "## По вопросам", "## Выводы",
		"| 1 | 0 | 40 | 0 | 0 |", "разница показана", "fixed выше",
		"MRR@10 (95% bootstrap)", "разрезано цитат", "разделов поделено",
		"| fixed | 1 | 60 / 60.0 / 60 / 60 | 0/1 (0.0%) | spans > 1: 0/1; разрезано цитат: 0/40 | 5 байт | 1.2s |",
		"| structure | 1 | 60 / 60.0 / 60 / 60 | 0/1 (0.0%) | разделов поделено: 0; чанков < 50 токенов: 0 | 9 байт | 800ms |",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("в отчёте нет %q", required)
		}
	}
}

func TestHolmCanRejectThreeIndividuallySignificantMcNemarResults(t *testing.T) {
	directory := t.TempDir()
	fixedPath := filepath.Join(directory, "fixed.json")
	structurePath := filepath.Join(directory, "structure.json")
	_ = os.WriteFile(fixedPath, []byte("f"), 0o644)
	_ = os.WriteFile(structurePath, []byte("s"), 0o644)
	document := testDocument("doc.md", "# D\nтекст")
	chunk := Chunk{ChunkID: "fixed:doc.md:0", Strategy: "fixed", Source: "doc.md", Title: "D", Section: "D", Spans: 1, End: len(document.Runes), Runes: len(document.Runes), Tokens: 10, Text: document.Text, Embedding: []float64{1}}
	fixed := Index{Header: IndexHeader{Dimension: 1}, Chunks: []Chunk{chunk}}
	chunk.ChunkID, chunk.Strategy = "structure:doc.md:0", "structure"
	structure := Index{Header: IndexHeader{Dimension: 1}, Chunks: []Chunk{chunk}}
	fixedRanks := make([]int, 40)
	for i := 0; i < 6; i++ {
		fixedRanks[i] = 1
	}
	comparison := Comparison{Revision: "r", Started: "t", Model: "m", ManifestSHA256: "h", QuestionsSHA256: "q", Questions: make([]QuestionResult, 40)}
	questions := make([]Question, 40)
	for i := range comparison.Questions {
		comparison.Questions[i] = QuestionResult{ID: "q", Source: "doc.md", FixedRank: fixedRanks[i]}
		questions[i] = Question{ID: "q", Source: "doc.md", Evidence: "текст"}
	}
	path := filepath.Join(directory, "RESULTS.md")
	if err := writeResults(path, comparison, Manifest{Files: []ManifestFile{{Path: "doc.md", Runes: len(document.Runes)}}}, []Document{document}, questions, fixed, structure, metricsFromRanks(fixedRanks), metricsFromRanks(make([]int, 40)), fixedPath, structurePath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Count(text, "0.031250 | разница не показана") != 3 {
		t.Fatalf("поправка Холма не отклонила три p=0.03125:\n%s", text)
	}
	if strings.Contains(text, "разница показана") {
		t.Fatal("отчёт объявил разницу по сырому p<0.05 без прохождения Холма")
	}
}

func TestChunkDiagnosticHelpersUseActualValues(t *testing.T) {
	chunks := []Chunk{{Tokens: 10, Spans: 1, Part: 1}, {Tokens: 20, Spans: 2}, {Tokens: 30, Spans: 1}, {Tokens: 40, Spans: 3}}
	if got := tokenSummary(chunks); got != "10 / 25.0 / 40 / 40" {
		t.Fatalf("token summary=%q", got)
	}
	if countMultiSpan(chunks) != 2 || splitSectionCount(chunks) != 1 || countShortChunks(chunks) != 4 {
		t.Fatalf("diagnostics: multi=%d split=%d short=%d", countMultiSpan(chunks), splitSectionCount(chunks), countShortChunks(chunks))
	}
	document := testDocument("x.md", "абзац внутри\n\n# H\nконец")
	boundaryChunks := []Chunk{{Source: "x.md", End: 6}, {Source: "x.md", End: 14}, {Source: "x.md", End: len(document.Runes)}}
	if got := boundaryShare(boundaryChunks, []Document{document}); got != "1/3 (33.3%)" {
		t.Fatalf("boundary share=%q", got)
	}
	evidence := "уникальная цитата проходит через границу фиксированного чанка целиком"
	evidenceDoc := testDocument("e.md", evidence)
	cutChunks := []Chunk{{Source: "e.md", Start: 0, End: 20}, {Source: "e.md", Start: 10, End: len(evidenceDoc.Runes)}}
	if got := countCutEvidence([]Question{{Source: "e.md", Evidence: evidence}}, cutChunks, []Document{evidenceDoc}); got != 1 {
		t.Fatalf("cut evidence=%d", got)
	}
}

func TestBoundaryDirectlyBeforeHeadingIsNotInsideParagraph(t *testing.T) {
	document := testDocument("h.md", "строка абзаца\n## Раздел\nтекст")
	end := len([]rune("строка абзаца\n"))
	if boundaryInsideParagraph(document.Runes, end) {
		t.Fatalf("граница перед заголовком без пустой строки посчитана как граница внутри абзаца")
	}
	if !boundaryInsideParagraph(document.Runes, len([]rune("строка"))) {
		t.Fatalf("граница посреди строки должна считаться внутри абзаца")
	}
}
