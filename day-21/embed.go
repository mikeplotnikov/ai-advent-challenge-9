package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

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

func (c *OllamaClient) Embed(ctx context.Context, chunkID, input string) ([]float64, int, bool, error) {
	requestBody, err := json.Marshal(embedRequest{Model: c.Model, Input: input, Truncate: false})
	if err != nil {
		return nil, 0, false, fmt.Errorf("подготовить запрос для %s: %w", chunkID, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/api/embed", bytes.NewReader(requestBody))
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
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, 0, false, fmt.Errorf("прочитать ответ Ollama для %s: %w", chunkID, err)
	}
	var decoded embedResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, 0, false, fmt.Errorf("Ollama вернула неверный JSON для %s: %w", chunkID, err)
	}
	message := decoded.Error
	if message == "" && response.StatusCode != http.StatusOK {
		message = strings.TrimSpace(string(body))
	}
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
	if len(decoded.Embeddings) != 1 {
		return nil, 0, false, fmt.Errorf("Ollama вернула %d векторов для %s, ожидался 1", len(decoded.Embeddings), chunkID)
	}
	vector := decoded.Embeddings[0]
	if len(vector) == 0 {
		return nil, 0, false, fmt.Errorf("Ollama вернула пустой вектор для %s", chunkID)
	}
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
