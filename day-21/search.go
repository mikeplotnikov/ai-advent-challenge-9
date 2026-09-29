package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

type SearchResult struct {
	Rank       int     `json:"rank"`
	Similarity float64 `json:"similarity"`
	Chunk      Chunk   `json:"chunk"`
}

func rankChunks(index Index, query []float64, limit int) ([]SearchResult, error) {
	if len(query) != index.Header.Dimension {
		return nil, fmt.Errorf("размерность запроса %d, индекс ожидает %d: повторите `go run ./day-21 -index`", len(query), index.Header.Dimension)
	}
	results := make([]SearchResult, len(index.Chunks))
	for i, chunk := range index.Chunks {
		if len(chunk.Embedding) != len(query) {
			return nil, fmt.Errorf("размерность вектора %s: %d, ожидалось %d", chunk.ChunkID, len(chunk.Embedding), len(query))
		}
		similarity := 0.0
		for j, value := range query {
			similarity += value * chunk.Embedding[j]
		}
		results[i] = SearchResult{Similarity: similarity, Chunk: chunk}
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

func collapseWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func containsEvidence(text, evidence string) bool {
	return strings.Contains(collapseWhitespace(text), collapseWhitespace(evidence))
}

func wordTokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func firstEvidenceRank(results []SearchResult, evidence string) int {
	for _, result := range results {
		if containsEvidence(result.Chunk.Text, evidence) {
			return result.Rank
		}
	}
	return 0
}

type RetrievalMetrics struct {
	Ranks []int
	Hits  map[int]int
	MRR10 float64
}

func metricsFromRanks(ranks []int) RetrievalMetrics {
	metrics := RetrievalMetrics{Ranks: append([]int(nil), ranks...), Hits: map[int]int{1: 0, 3: 0, 5: 0}}
	for _, rank := range ranks {
		for _, k := range []int{1, 3, 5} {
			if rank > 0 && rank <= k {
				metrics.Hits[k]++
			}
		}
		if rank > 0 && rank <= 10 {
			metrics.MRR10 += 1 / float64(rank)
		}
	}
	if len(ranks) > 0 {
		metrics.MRR10 /= float64(len(ranks))
	}
	return metrics
}
