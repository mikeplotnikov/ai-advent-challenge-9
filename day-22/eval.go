package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

var searchQuestionFn = searchQuestion

func loadSearch(ctx context.Context, ollamaURL, question string, k int) ([]FoundChunk, rag.IndexHeader, string, error) {
	corpusDir := rag.ProjectPath(defaultCorpus)
	manifestSHA, err := rag.ManifestSHA256(corpusDir)
	if err != nil {
		return nil, rag.IndexHeader{}, "", err
	}
	indexPath := rag.ProjectPath(defaultIndex)
	index, err := rag.ReadIndex(indexPath)
	if err != nil {
		return nil, rag.IndexHeader{}, "", err
	}
	if err := rag.ValidateIndex(index, strategyName, embeddingModel, manifestSHA, 0); err != nil {
		return nil, rag.IndexHeader{}, "", err
	}
	embedder := &rag.OllamaClient{BaseURL: ollamaURL, Model: embeddingModel}
	vector, _, _, err := embedder.Embed(ctx, "search-query", question)
	if err != nil {
		return nil, rag.IndexHeader{}, "", err
	}
	if len(vector) != index.Header.Dimension {
		return nil, rag.IndexHeader{}, "", fmt.Errorf("размерность ответа модели %d, индекс ожидает %d: повторите `go run ./day-21 -index`", len(vector), index.Header.Dimension)
	}
	results, err := rag.RankChunks(index, vector, k)
	if err != nil {
		return nil, rag.IndexHeader{}, "", err
	}
	return foundChunks(results, Question{}), index.Header, manifestSHA, nil
}

func searchQuestion(ctx context.Context, ollamaURL string, question Question, k int) ([]FoundChunk, rag.IndexHeader, string, error) {
	chunks, header, manifestSHA, err := loadSearch(ctx, ollamaURL, question.Question, k)
	if err != nil {
		return nil, header, manifestSHA, err
	}
	for i := range chunks {
		chunks[i].EvidenceHit = question.Evidence != "" && rag.ContainsEvidence(chunks[i].Text, question.Evidence)
		chunks[i].SourceHit = containsString(question.Sources, chunks[i].Source)
	}
	return chunks, header, manifestSHA, nil
}

func runEvaluation(ctx context.Context, ollamaURL string, client *llm.Client, stdout io.Writer) (Run, error) {
	questionsPath := rag.ProjectPath(defaultQuestions)
	questions, questionsSHA, err := loadQuestions(questionsPath)
	if err != nil {
		return Run{}, err
	}
	if err := validateQuestions(questions); err != nil {
		return Run{}, err
	}
	run := Run{Meta: RunHeader{
		Commit: gitCommit(), StartedAt: time.Now().UTC(), RequestedModel: modelName,
		Temperature: 0, MaxTokens: 600, K: defaultK, Repeats: evalRepeats,
		RunPath: defaultRun, QuestionsSHA256: questionsSHA, PromptsSHA256: promptsSHA256(),
	}}
	models := map[string]bool{}
	for qi, question := range questions {
		fmt.Fprintf(stdout, "%d/%d %s\n", qi+1, len(questions), question.ID)
		chunks, _, manifestSHA, err := searchQuestionFn(ctx, ollamaURL, question, defaultK)
		if err != nil {
			return Run{}, err
		}
		if run.Meta.IndexSHA256 == "" {
			indexSHA, err := fileSHA256(rag.ProjectPath(defaultIndex))
			if err != nil {
				return Run{}, fmt.Errorf("sha256 индекса: %w", err)
			}
			run.Meta.IndexSHA256 = indexSHA
		}
		run.Meta.ManifestSHA256 = manifestSHA
		item := QuestionRun{Question: question, Chunks: chunks}
		for repeat := 1; repeat <= evalRepeats; repeat++ {
			for _, mode := range []string{"norag", "rag"} {
				messages := noRAGMessages(question.Question)
				if mode == "rag" {
					messages = ragMessages(question.Question, chunks)
				}
				call, err := callModel(ctx, client, mode, repeat, messages)
				if err != nil {
					return Run{}, fmt.Errorf("%s, %s, повтор %d: %w", question.ID, mode, repeat, err)
				}
				call.Score = scoreAnswer(question, call.Response, chunks, call.Score.Outcome == "empty")
				if call.Model != "" {
					models[call.Model] = true
				}
				if mode == "norag" {
					item.NoRAG = append(item.NoRAG, call)
				} else {
					item.RAG = append(item.RAG, call)
				}
			}
		}
		run.Questions = append(run.Questions, item)
	}
	for model := range models {
		run.Meta.ResponseModels = append(run.Meta.ResponseModels, model)
	}
	sort.Strings(run.Meta.ResponseModels)
	return run, nil
}

func writeRun(path string, run Run) error { return writeJSONAtomic(path, run) }

func readRun(path string) (Run, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Run{}, fmt.Errorf("прочитать run.json: %w", err)
	}
	var run Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return Run{}, fmt.Errorf("разобрать run.json: %w", err)
	}
	return run, nil
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
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
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
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

func writeBytesAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
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
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
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

func fileSHA256(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func gitCommit() string {
	output, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}
