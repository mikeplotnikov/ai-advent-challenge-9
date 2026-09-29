package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const defaultIndexDir = "day-21/index"

type StrategyParameters struct {
	Size     int `json:"size,omitempty"`
	Overlap  int `json:"overlap,omitempty"`
	Rewind   int `json:"rewind,omitempty"`
	MaxRunes int `json:"max_runes,omitempty"`
}

type IndexHeader struct {
	Strategy            string             `json:"strategy"`
	Model               string             `json:"model"`
	Dimension           int                `json:"dimension"`
	BuiltAt             string             `json:"built_at"`
	ManifestSHA256      string             `json:"manifest_sha256"`
	Parameters          StrategyParameters `json:"parameters"`
	ChunkCount          int                `json:"chunk_count"`
	TotalTokens         int                `json:"total_tokens"`
	EmbeddingDurationMS int64              `json:"embedding_duration_ms"`
	NormalizedVectors   int                `json:"normalized_vectors"`
}

type Index struct {
	Header IndexHeader `json:"header"`
	Chunks []Chunk     `json:"chunks"`
}

func buildIndex(ctx context.Context, strategy string, documents []Document, manifestSHA, model string, embedder Embedder, progress io.Writer) (Index, error) {
	var chunks []Chunk
	var parameters StrategyParameters
	switch strategy {
	case "fixed":
		chunks = fixedChunks(documents)
		parameters = StrategyParameters{Size: fixedSize, Overlap: fixedOverlap, Rewind: fixedRewind}
	case "structure":
		chunks = structureChunks(documents)
		parameters = StrategyParameters{MaxRunes: structureMaxSize, Rewind: fixedRewind}
	default:
		return Index{}, fmt.Errorf("неизвестная стратегия %q", strategy)
	}
	started := time.Now()
	dimension := 0
	totalTokens := 0
	normalizedVectors := 0
	for i := range chunks {
		vector, tokens, normalized, err := embedder.Embed(ctx, chunks[i].ChunkID, chunks[i].Text)
		if err != nil {
			return Index{}, err
		}
		if dimension == 0 {
			dimension = len(vector)
		} else if len(vector) != dimension {
			return Index{}, fmt.Errorf("размерность вектора %s: %d, ожидалось %d", chunks[i].ChunkID, len(vector), dimension)
		}
		chunks[i].Embedding = vector
		chunks[i].Tokens = tokens
		totalTokens += tokens
		if normalized {
			normalizedVectors++
		}
		if (i+1)%25 == 0 {
			fmt.Fprintf(progress, "%s: %d/%d · %s\n", strategy, i+1, len(chunks), time.Since(started).Round(time.Second))
		}
	}
	duration := time.Since(started)
	return Index{Header: IndexHeader{
		Strategy: strategy, Model: model, Dimension: dimension, BuiltAt: time.Now().Format(time.RFC3339), ManifestSHA256: manifestSHA,
		Parameters: parameters, ChunkCount: len(chunks), TotalTokens: totalTokens,
		EmbeddingDurationMS: duration.Milliseconds(), NormalizedVectors: normalizedVectors,
	}, Chunks: chunks}, nil
}

func writeIndexAtomic(path string, index Index) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("создать каталог индекса: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("создать временный индекс: %w", err)
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
	if err := encoder.Encode(index); err != nil {
		return fmt.Errorf("записать временный индекс: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("синхронизировать временный индекс: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("закрыть временный индекс: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("заменить индекс: %w", err)
	}
	keep = true
	return nil
}

func readIndex(path string) (Index, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Index{}, fmt.Errorf("индекса %s нет: сначала выполните `go run ./day-21 -index`", path)
		}
		return Index{}, fmt.Errorf("прочитать индекс %s: %w", path, err)
	}
	var index Index
	if err := json.Unmarshal(raw, &index); err != nil {
		return Index{}, fmt.Errorf("разобрать индекс %s: %w", path, err)
	}
	return index, nil
}

func validateIndex(index Index, strategy, model, manifestSHA string, expectedDimension int) error {
	var expectedParameters StrategyParameters
	switch strategy {
	case "fixed":
		expectedParameters = StrategyParameters{Size: fixedSize, Overlap: fixedOverlap, Rewind: fixedRewind}
	case "structure":
		expectedParameters = StrategyParameters{MaxRunes: structureMaxSize, Rewind: fixedRewind}
	default:
		return fmt.Errorf("неизвестная стратегия индекса %q", strategy)
	}
	if index.Header.Strategy != strategy || index.Header.Parameters != expectedParameters {
		return fmt.Errorf("индекс %s собран с другими параметрами стратегии: повторите `go run ./day-21 -index`", strategy)
	}
	if index.Header.Model != model {
		return fmt.Errorf("индекс собран моделью %q, нужна %q: повторите `go run ./day-21 -index`", index.Header.Model, model)
	}
	if index.Header.ManifestSHA256 != manifestSHA {
		return fmt.Errorf("индекс собран на другом MANIFEST.json: повторите `go run ./day-21 -index`")
	}
	if index.Header.Dimension <= 0 || expectedDimension > 0 && index.Header.Dimension != expectedDimension {
		return fmt.Errorf("размерность индекса %d несовместима с ожидаемой %d: повторите `go run ./day-21 -index`", index.Header.Dimension, expectedDimension)
	}
	if index.Header.ChunkCount != len(index.Chunks) {
		return fmt.Errorf("в индексе заявлено %d чанков, записано %d: повторите `go run ./day-21 -index`", index.Header.ChunkCount, len(index.Chunks))
	}
	ids := make(map[string]bool, len(index.Chunks))
	for _, chunk := range index.Chunks {
		if chunk.Strategy != strategy {
			return fmt.Errorf("чанк %s помечен стратегией %q вместо %q: повторите `go run ./day-21 -index`", chunk.ChunkID, chunk.Strategy, strategy)
		}
		if chunk.ChunkID == "" || ids[chunk.ChunkID] {
			return fmt.Errorf("индекс содержит пустой или повторный chunk_id %q: повторите `go run ./day-21 -index`", chunk.ChunkID)
		}
		ids[chunk.ChunkID] = true
		if len(chunk.Embedding) != index.Header.Dimension {
			return fmt.Errorf("размерность вектора %s: %d, в шапке %d: повторите `go run ./day-21 -index`", chunk.ChunkID, len(chunk.Embedding), index.Header.Dimension)
		}
	}
	return nil
}
