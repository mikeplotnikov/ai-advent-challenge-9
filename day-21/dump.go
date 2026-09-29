package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

const defaultShowcasePath = "day-21/showcase.json"

var showcaseRevision = revision

type Showcase struct {
	Meta       ShowcaseMeta       `json:"meta"`
	Corpus     []ShowcaseDocument `json:"corpus"`
	Strategies ShowcaseStrategies `json:"strategies"`
	Questions  []ShowcaseQuestion `json:"questions"`
	Results    ResultTables       `json:"results"`
}

type ShowcaseMeta struct {
	Commit          string `json:"commit"`
	Revision        string `json:"revision"`
	Started         string `json:"started"`
	Model           string `json:"model"`
	Dimension       int    `json:"dimension"`
	ManifestSHA256  string `json:"manifest_sha256"`
	QuestionsSHA256 string `json:"questions_sha256"`
	Files           int    `json:"files"`
	Runes           int    `json:"runes"`
	Pages           int    `json:"pages"`
}

type ShowcaseDocument struct {
	Source string `json:"source"`
	Title  string `json:"title"`
	Text   string `json:"text"`
}

type ShowcaseStrategies struct {
	Fixed     ShowcaseStrategy `json:"fixed"`
	Structure ShowcaseStrategy `json:"structure"`
}

type ShowcaseStrategy struct {
	Parameters StrategyParameters `json:"parameters"`
	Chunks     []ShowcaseChunk    `json:"chunks"`
}

type ShowcaseChunk struct {
	ChunkID         string    `json:"chunk_id"`
	Source          string    `json:"source"`
	Section         string    `json:"section"`
	Spans           int       `json:"spans"`
	Part            int       `json:"part"`
	Start           int       `json:"start"`
	End             int       `json:"end"`
	Runes           int       `json:"runes"`
	Tokens          int       `json:"tokens"`
	InsideParagraph bool      `json:"inside_paragraph"`
	EmbeddingHead   []float64 `json:"embedding_head"`
}

type ShowcaseQuestion struct {
	ID        string                 `json:"id"`
	Question  string                 `json:"question"`
	Source    string                 `json:"source"`
	Evidence  string                 `json:"evidence"`
	Fixed     ShowcaseQuestionResult `json:"fixed"`
	Structure ShowcaseQuestionResult `json:"structure"`
}

type ShowcaseQuestionResult struct {
	Top10    []ShowcaseSearchResult `json:"top_10"`
	FirstHit *int                   `json:"first_hit"`
}

type ShowcaseSearchResult struct {
	ChunkID string  `json:"chunk_id"`
	Score   float64 `json:"score"`
	Hit     bool    `json:"hit"`
}

func writeShowcase(ctx context.Context, path, model, manifestSHA, questionsSHA string, manifest Manifest, documents []Document, questions []Question, fixed, structure Index, comparison Comparison, fixedPath, structurePath string, embedder Embedder, progress io.Writer) error {
	commit := showcaseRevision()
	if commit == "" || commit == "unknown" {
		return fmt.Errorf("не определить коммит A9 для showcase.json: запустите команду из Git checkout")
	}
	showcase, err := buildShowcase(ctx, commit, model, manifestSHA, questionsSHA, manifest, documents, questions, fixed, structure, comparison, fixedPath, structurePath, embedder, progress)
	if err != nil {
		return err
	}
	if err := writeJSONAtomic(path, showcase); err != nil {
		return fmt.Errorf("записать showcase.json: %w", err)
	}
	return nil
}

func buildShowcase(ctx context.Context, commit, model, manifestSHA, questionsSHA string, manifest Manifest, documents []Document, questions []Question, fixed, structure Index, comparison Comparison, fixedPath, structurePath string, embedder Embedder, progress io.Writer) (Showcase, error) {
	if err := validateComparison(comparison, model, manifestSHA, questionsSHA, questions); err != nil {
		return Showcase{}, err
	}

	documentBySource := make(map[string]Document, len(documents))
	corpus := make([]ShowcaseDocument, len(documents))
	totalRunes := 0
	for i, document := range documents {
		documentBySource[document.Source] = document
		corpus[i] = ShowcaseDocument{Source: document.Source, Title: document.Title, Text: document.Text}
		totalRunes += len(document.Runes)
	}

	fixedChunks, err := showcaseChunks(fixed, documentBySource)
	if err != nil {
		return Showcase{}, err
	}
	structureChunks, err := showcaseChunks(structure, documentBySource)
	if err != nil {
		return Showcase{}, err
	}

	fixedRanks := make([]int, len(comparison.Questions))
	structureRanks := make([]int, len(comparison.Questions))
	showcaseQuestions := make([]ShowcaseQuestion, len(questions))
	for i, question := range questions {
		vector, _, _, err := embedder.Embed(ctx, "question:"+question.ID, question.Question)
		if err != nil {
			return Showcase{}, err
		}
		if len(vector) != fixed.Header.Dimension {
			return Showcase{}, fmt.Errorf("размерность вопроса %s: %d, в индексе %d: повторите `go run ./day-21 -index`", question.ID, len(vector), fixed.Header.Dimension)
		}
		fixedTop, err := rankChunks(fixed, vector, 10)
		if err != nil {
			return Showcase{}, err
		}
		structureTop, err := rankChunks(structure, vector, 10)
		if err != nil {
			return Showcase{}, err
		}
		fixedFirst := firstEvidenceRank(fixedTop, question.Evidence)
		structureFirst := firstEvidenceRank(structureTop, question.Evidence)
		measured := comparison.Questions[i]
		if fixedFirst != measured.FixedRank || structureFirst != measured.StructureRank {
			return Showcase{}, fmt.Errorf("%s: first_hit расходится с compare.json (fixed %s/%s, structure %s/%s); выгрузка остановлена", question.ID, rankText(fixedFirst), rankText(measured.FixedRank), rankText(structureFirst), rankText(measured.StructureRank))
		}
		fixedRanks[i] = measured.FixedRank
		structureRanks[i] = measured.StructureRank
		showcaseQuestions[i] = ShowcaseQuestion{
			ID: question.ID, Question: question.Question, Source: question.Source, Evidence: question.Evidence,
			Fixed:     showcaseQuestionResult(fixedTop, question.Evidence, fixedFirst),
			Structure: showcaseQuestionResult(structureTop, question.Evidence, structureFirst),
		}
		if (i+1)%10 == 0 {
			fmt.Fprintf(progress, "dump: %d/%d\n", i+1, len(questions))
		}
	}

	results, _, _ := buildResultTables(questions, documents, fixed, structure, metricsFromRanks(fixedRanks), metricsFromRanks(structureRanks), fixedPath, structurePath)
	return Showcase{
		Meta: ShowcaseMeta{
			Commit: commit, Revision: comparison.Revision, Started: comparison.Started,
			Model: model, Dimension: fixed.Header.Dimension, ManifestSHA256: manifestSHA, QuestionsSHA256: questionsSHA,
			Files: len(manifest.Files), Runes: totalRunes, Pages: (totalRunes + 1799) / 1800,
		},
		Corpus: corpus,
		Strategies: ShowcaseStrategies{
			Fixed:     ShowcaseStrategy{Parameters: fixed.Header.Parameters, Chunks: fixedChunks},
			Structure: ShowcaseStrategy{Parameters: structure.Header.Parameters, Chunks: structureChunks},
		},
		Questions: showcaseQuestions,
		Results:   results,
	}, nil
}

func showcaseChunks(index Index, documents map[string]Document) ([]ShowcaseChunk, error) {
	result := make([]ShowcaseChunk, len(index.Chunks))
	for i, chunk := range index.Chunks {
		document, ok := documents[chunk.Source]
		if !ok {
			return nil, fmt.Errorf("%s: файла %s нет в корпусе: повторите `go run ./day-21 -index`", chunk.ChunkID, chunk.Source)
		}
		if chunk.Start < 0 || chunk.Start >= chunk.End || chunk.End > len(document.Runes) || chunk.Runes != chunk.End-chunk.Start || chunk.Text != string(document.Runes[chunk.Start:chunk.End]) {
			return nil, fmt.Errorf("%s: диапазон или текст не соответствует корпусу: повторите `go run ./day-21 -index`", chunk.ChunkID)
		}
		if len(chunk.Embedding) < 8 {
			return nil, fmt.Errorf("%s: вектор короче 8 компонент: повторите `go run ./day-21 -index`", chunk.ChunkID)
		}
		head := make([]float64, 8)
		for j, value := range chunk.Embedding[:8] {
			head[j] = roundFour(value)
		}
		result[i] = ShowcaseChunk{
			ChunkID: chunk.ChunkID, Source: chunk.Source, Section: chunk.Section, Spans: chunk.Spans, Part: chunk.Part,
			Start: chunk.Start, End: chunk.End, Runes: chunk.Runes, Tokens: chunk.Tokens,
			InsideParagraph: boundaryInsideParagraph(document.Runes, chunk.End), EmbeddingHead: head,
		}
	}
	return result, nil
}

func showcaseQuestionResult(results []SearchResult, evidence string, firstHit int) ShowcaseQuestionResult {
	top := make([]ShowcaseSearchResult, len(results))
	for i, result := range results {
		top[i] = ShowcaseSearchResult{ChunkID: result.Chunk.ChunkID, Score: roundFour(result.Similarity), Hit: containsEvidence(result.Chunk.Text, evidence)}
	}
	var rank *int
	if firstHit > 0 {
		value := firstHit
		rank = &value
	}
	return ShowcaseQuestionResult{Top10: top, FirstHit: rank}
}

func roundFour(value float64) float64 {
	rounded := math.Round(value*10000) / 10000
	if rounded == 0 {
		return 0
	}
	return rounded
}

func readComparison(path string) (Comparison, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Comparison{}, fmt.Errorf("compare.json не найден: сначала выполните `go run ./day-21 -compare`")
		}
		return Comparison{}, fmt.Errorf("прочитать compare.json: %w", err)
	}
	var comparison Comparison
	if err := json.Unmarshal(raw, &comparison); err != nil {
		return Comparison{}, fmt.Errorf("разобрать compare.json: %w", err)
	}
	return comparison, nil
}

func validateComparison(comparison Comparison, model, manifestSHA, questionsSHA string, questions []Question) error {
	if comparison.Revision == "" || comparison.Started == "" {
		return fmt.Errorf("compare.json не содержит revision или started: повторите `go run ./day-21 -compare`")
	}
	if comparison.Model != model || comparison.ManifestSHA256 != manifestSHA || comparison.QuestionsSHA256 != questionsSHA {
		return fmt.Errorf("compare.json собран для другой модели, корпуса или набора вопросов: повторите `go run ./day-21 -compare`")
	}
	if len(comparison.Questions) != len(questions) {
		return fmt.Errorf("в compare.json %d вопросов, ожидалось %d: повторите `go run ./day-21 -compare`", len(comparison.Questions), len(questions))
	}
	for i, question := range questions {
		measured := comparison.Questions[i]
		if measured.ID != question.ID || measured.Source != question.Source || measured.FixedRank < 0 || measured.FixedRank > 10 || measured.StructureRank < 0 || measured.StructureRank > 10 {
			return fmt.Errorf("compare.json не соответствует вопросу %s: повторите `go run ./day-21 -compare`", question.ID)
		}
	}
	return nil
}

func loadCompatibleIndexes(indexDir, model, manifestSHA string) (Index, Index, string, string, error) {
	fixedPath := filepath.Join(indexDir, "fixed.json")
	structurePath := filepath.Join(indexDir, "structure.json")
	fixed, err := readIndex(fixedPath)
	if err != nil {
		return Index{}, Index{}, "", "", err
	}
	if err := validateIndex(fixed, "fixed", model, manifestSHA, 0); err != nil {
		return Index{}, Index{}, "", "", err
	}
	structure, err := readIndex(structurePath)
	if err != nil {
		return Index{}, Index{}, "", "", err
	}
	if err := validateIndex(structure, "structure", model, manifestSHA, fixed.Header.Dimension); err != nil {
		return Index{}, Index{}, "", "", err
	}
	return fixed, structure, fixedPath, structurePath, nil
}
