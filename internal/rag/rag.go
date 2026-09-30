// Package rag contains the query-time parts of the day-21 RAG pipeline.
package rag

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	FixedSize        = 1800
	FixedOverlap     = 180
	FixedRewind      = 100
	StructureMaxSize = 3600
	ManifestName     = "MANIFEST.json"
)

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

type Chunk struct {
	ChunkID   string    `json:"chunk_id"`
	Strategy  string    `json:"strategy"`
	Source    string    `json:"source"`
	Title     string    `json:"title"`
	Section   string    `json:"section"`
	Spans     int       `json:"spans"`
	Part      int       `json:"part"`
	Start     int       `json:"start"`
	End       int       `json:"end"`
	Runes     int       `json:"runes"`
	Tokens    int       `json:"tokens"`
	Text      string    `json:"text"`
	Embedding []float64 `json:"embedding"`
}

type Index struct {
	Header IndexHeader `json:"header"`
	Chunks []Chunk     `json:"chunks"`
}

type SearchResult struct {
	Rank       int     `json:"rank"`
	Similarity float64 `json:"similarity"`
	Chunk      Chunk   `json:"chunk"`
}

type Manifest struct {
	SourceCommit string         `json:"source_commit"`
	Files        []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	Runes  int    `json:"runes"`
	SHA256 string `json:"sha256"`
}

type Embedder interface {
	Embed(context.Context, string, string) ([]float64, int, bool, error)
}

type OllamaClient struct {
	BaseURL string
	Model   string
	HTTP    *http.Client
}

type embedRequest struct {
	Model    string `json:"model"`
	Input    string `json:"input"`
	Truncate bool   `json:"truncate"`
}

type embedResponse struct {
	Embeddings      [][]float64 `json:"embeddings"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	Error           string      `json:"error"`
}

func ReadIndex(path string) (Index, error) {
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

func ValidateIndex(index Index, strategy, model, manifestSHA string, expectedDimension int) error {
	var expected StrategyParameters
	switch strategy {
	case "fixed":
		expected = StrategyParameters{Size: FixedSize, Overlap: FixedOverlap, Rewind: FixedRewind}
	case "structure":
		expected = StrategyParameters{MaxRunes: StructureMaxSize, Rewind: FixedRewind}
	default:
		return fmt.Errorf("неизвестная стратегия индекса %q", strategy)
	}
	if index.Header.Strategy != strategy || index.Header.Parameters != expected {
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

func RankChunks(index Index, query []float64, limit int) ([]SearchResult, error) {
	if len(query) != index.Header.Dimension {
		return nil, fmt.Errorf("размерность запроса %d, индекс ожидает %d: повторите `go run ./day-21 -index`", len(query), index.Header.Dimension)
	}
	results := make([]SearchResult, len(index.Chunks))
	for i, chunk := range index.Chunks {
		if len(chunk.Embedding) != len(query) {
			return nil, fmt.Errorf("размерность вектора %s: %d, ожидалось %d", chunk.ChunkID, len(chunk.Embedding), len(query))
		}
		for j, value := range query {
			results[i].Similarity += value * chunk.Embedding[j]
		}
		results[i].Chunk = chunk
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Similarity == results[j].Similarity {
			return results[i].Chunk.ChunkID < results[j].Chunk.ChunkID
		}
		return results[i].Similarity > results[j].Similarity
	})
	if limit < 0 {
		limit = 0
	}
	if limit < len(results) {
		results = results[:limit]
	}
	for i := range results {
		results[i].Rank = i + 1
	}
	return results, nil
}

func ContainsEvidence(text, evidence string) bool {
	return strings.Contains(collapseWhitespace(text), collapseWhitespace(evidence))
}

func FirstEvidenceRank(results []SearchResult, evidence string) int {
	for _, result := range results {
		if ContainsEvidence(result.Chunk.Text, evidence) {
			return result.Rank
		}
	}
	return 0
}

func ReadManifest(corpusDir string) (Manifest, []byte, error) {
	path := filepath.Join(corpusDir, ManifestName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("прочитать манифест корпуса: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, nil, fmt.Errorf("разобрать манифест корпуса: %w", err)
	}
	return manifest, raw, nil
}

func ManifestSHA256(corpusDir string) (string, error) {
	_, raw, err := ReadManifest(corpusDir)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func ProjectPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	for depth, base := 0, "."; depth < 5; depth, base = depth+1, filepath.Join(base, "..") {
		if _, err := os.Stat(filepath.Join(base, "go.mod")); err == nil {
			return filepath.Clean(filepath.Join(base, path))
		}
	}
	return path
}

func (c *OllamaClient) Embed(ctx context.Context, chunkID, input string) ([]float64, int, bool, error) {
	body, err := json.Marshal(embedRequest{Model: c.Model, Input: input, Truncate: false})
	if err != nil {
		return nil, 0, false, fmt.Errorf("подготовить запрос для %s: %w", chunkID, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, 0, false, fmt.Errorf("подготовить запрос к Ollama: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, false, fmt.Errorf("Ollama недоступна: запустите `ollama serve`: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, 0, false, fmt.Errorf("прочитать ответ Ollama для %s: %w", chunkID, err)
	}
	var decoded embedResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, 0, false, fmt.Errorf("Ollama вернула неверный JSON для %s: %w", chunkID, err)
	}
	message := decoded.Error
	if message == "" && response.StatusCode != http.StatusOK {
		message = strings.TrimSpace(string(raw))
	}
	message = TerminalSafe(message, 300)
	lower := strings.ToLower(message)
	if strings.Contains(lower, "exceeds the context length") || strings.Contains(lower, "context length") && strings.Contains(lower, "exceed") {
		return nil, 0, false, fmt.Errorf("вход %s длиннее контекста модели: %s", chunkID, message)
	}
	if strings.Contains(lower, "model") && (strings.Contains(lower, "not found") || strings.Contains(lower, "pull")) {
		return nil, 0, false, fmt.Errorf("модель %s не установлена: выполните `ollama pull %s`: %s", c.Model, c.Model, message)
	}
	if response.StatusCode != http.StatusOK || message != "" {
		return nil, 0, false, fmt.Errorf("Ollama отклонила %s: HTTP %d: %s", chunkID, response.StatusCode, message)
	}
	if len(decoded.Embeddings) != 1 || len(decoded.Embeddings[0]) == 0 {
		return nil, 0, false, fmt.Errorf("Ollama вернула неверное число векторов для %s", chunkID)
	}
	vector := decoded.Embeddings[0]
	normSquared := 0.0
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, 0, false, fmt.Errorf("Ollama вернула нечисловой компонент вектора для %s", chunkID)
		}
		normSquared += value * value
	}
	norm := math.Sqrt(normSquared)
	if norm == 0 {
		return nil, 0, false, fmt.Errorf("Ollama вернула нулевой вектор для %s", chunkID)
	}
	normalized := math.Abs(norm-1) > 1e-3
	if normalized {
		for i := range vector {
			vector[i] /= norm
		}
	}
	return vector, decoded.PromptEvalCount, normalized, nil
}

func TerminalSafe(text string, limit int) string {
	var builder strings.Builder
	count := 0
	for _, r := range text {
		if count == limit {
			builder.WriteString("…")
			break
		}
		if unicode.IsControl(r) {
			r = '?'
		}
		builder.WriteRune(r)
		count++
	}
	return builder.String()
}

func TerminalSafeMultiline(text string, limit int) string {
	var builder strings.Builder
	count := 0
	for _, r := range text {
		if count == limit {
			builder.WriteString("…")
			break
		}
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			r = '?'
		}
		builder.WriteRune(r)
		count++
	}
	return builder.String()
}

func collapseWhitespace(text string) string { return strings.Join(strings.Fields(text), " ") }
