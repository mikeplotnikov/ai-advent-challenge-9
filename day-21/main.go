package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultOllama = "http://127.0.0.1:11434"
	defaultModel  = "bge-m3"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("day-21", flag.ContinueOnError)
	fs.SetOutput(stderr)
	indexMode := fs.Bool("index", false, "построить оба индекса")
	compareMode := fs.Bool("compare", false, "сравнить индексы на замороженных вопросах")
	searchText := fs.String("search", "", "найти релевантные чанки для вопроса")
	strategy := fs.String("strategy", "both", "стратегия поиска: fixed, structure или both")
	topK := fs.Int("k", 5, "число результатов поиска")
	ollama := fs.String("ollama", defaultOllama, "адрес Ollama")
	model := fs.String("model", defaultModel, "модель эмбеддингов")
	timeout := fs.Duration("timeout", 10*time.Minute, "общий таймаут")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	searchMode := false
	fs.Visit(func(current *flag.Flag) {
		if current.Name == "search" {
			searchMode = true
		}
	})
	modes := 0
	for _, active := range []bool{*indexMode, *compareMode, searchMode} {
		if active {
			modes++
		}
	}
	if modes != 1 {
		fmt.Fprintln(stderr, "выберите ровно один режим: -index, -compare или -search \"вопрос\"")
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "позиционные аргументы не поддерживаются; вопрос передайте через -search")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "-timeout должен быть больше нуля")
		return 2
	}
	if searchMode {
		if strings.TrimSpace(*searchText) == "" {
			fmt.Fprintln(stderr, "-search требует непустой вопрос")
			return 2
		}
		if *strategy != "fixed" && *strategy != "structure" && *strategy != "both" {
			fmt.Fprintln(stderr, "-strategy: допустимы fixed, structure или both")
			return 2
		}
		if *topK <= 0 {
			fmt.Fprintln(stderr, "-k должен быть больше нуля")
			return 2
		}
	}

	corpusDir := projectPath(defaultCorpusDir)
	documents, manifest, manifestSHA, err := loadCorpus(corpusDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client := &OllamaClient{BaseURL: *ollama, Model: *model, HTTP: &httpClientNoFixedTimeout}
	indexDir := projectPath(defaultIndexDir)

	switch {
	case *indexMode:
		for _, name := range []string{"fixed", "structure"} {
			index, err := buildIndex(ctx, name, documents, manifestSHA, *model, client, stderr)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			path := filepath.Join(indexDir, name+".json")
			if err := writeIndexAtomic(path, index); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			fmt.Fprintf(stdout, "%s: %d чанков, %d токенов, %s, %s\n", name, index.Header.ChunkCount, index.Header.TotalTokens, durationMS(index.Header.EmbeddingDurationMS), path)
		}
		return 0

	case *compareMode:
		fixedPath := filepath.Join(indexDir, "fixed.json")
		structurePath := filepath.Join(indexDir, "structure.json")
		fixed, err := readIndex(fixedPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := validateIndex(fixed, "fixed", *model, manifestSHA, 0); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		structure, err := readIndex(structurePath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := validateIndex(structure, "structure", *model, manifestSHA, fixed.Header.Dimension); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		questionsPath := projectPath(defaultQuestionsPath)
		questions, questionsSHA, err := loadQuestions(questionsPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := validateQuestions(questions, documents); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		comparison, fixedMetrics, structureMetrics, err := runComparison(ctx, *model, manifestSHA, questionsSHA, questions, fixed, structure, client, stderr)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		comparePath := projectPath(defaultComparePath)
		resultsPath := projectPath(defaultResultsPath)
		if err := writeJSONAtomic(comparePath, comparison); err != nil {
			fmt.Fprintln(stderr, "записать compare.json:", err)
			return 1
		}
		if err := writeResults(resultsPath, comparison, manifest, documents, questions, fixed, structure, fixedMetrics, structureMetrics, fixedPath, structurePath); err != nil {
			fmt.Fprintln(stderr, "записать RESULTS.md:", err)
			return 1
		}
		fmt.Fprintf(stdout, "сравнение записано: %s и %s\n", resultsPath, comparePath)
		return 0

	case searchMode:
		names := []string{*strategy}
		if *strategy == "both" {
			names = []string{"fixed", "structure"}
		}
		indexes := make([]Index, 0, len(names))
		dimension := 0
		for _, name := range names {
			path := filepath.Join(indexDir, name+".json")
			index, err := readIndex(path)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if err := validateIndex(index, name, *model, manifestSHA, dimension); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if dimension == 0 {
				dimension = index.Header.Dimension
			}
			indexes = append(indexes, index)
		}
		vector, _, _, err := client.Embed(ctx, "search-query", *searchText)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if len(vector) != dimension {
			fmt.Fprintf(stderr, "размерность ответа модели %d, индекс ожидает %d: повторите `go run ./day-21 -index`\n", len(vector), dimension)
			return 1
		}
		for i, index := range indexes {
			results, err := rankChunks(index, vector, *topK)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if i > 0 {
				fmt.Fprintln(stdout)
			}
			fmt.Fprintf(stdout, "## %s\n", names[i])
			printSearchResults(stdout, results)
		}
		return 0
	}
	return 2
}

var httpClientNoFixedTimeout = http.Client{}

func printSearchResults(output io.Writer, results []SearchResult) {
	for _, result := range results {
		preview := []rune(collapseWhitespace(result.Chunk.Text))
		if len(preview) > 200 {
			preview = preview[:200]
		}
		section := result.Chunk.Section
		if section == "" {
			section = "—"
		}
		fmt.Fprintf(output, "%d. %.4f · %s\n   source: %s\n   section: %s\n   text: %s\n", result.Rank, result.Similarity, result.Chunk.ChunkID, result.Chunk.Source, section, string(preview))
	}
}
