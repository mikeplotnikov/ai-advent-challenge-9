package main

import (
	"math"
	"testing"
)

func TestRankingOrdersBySimilarityThenChunkID(t *testing.T) {
	index := Index{
		Header: IndexHeader{Dimension: 2},
		Chunks: []Chunk{
			{ChunkID: "fixed:z:0", Embedding: []float64{0.5, 0}},
			{ChunkID: "fixed:b:0", Embedding: []float64{1, 0}},
			{ChunkID: "fixed:a:0", Embedding: []float64{1, 0}},
		},
	}
	results, err := rankChunks(index, []float64{1, 0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"fixed:a:0", "fixed:b:0", "fixed:z:0"}
	for i, result := range results {
		if result.Rank != i+1 || result.Chunk.ChunkID != want[i] {
			t.Errorf("место %d: %+v, ожидался %s", i+1, result, want[i])
		}
	}
}

func TestRetrievalMetricsMatchManualExample(t *testing.T) {
	metrics := metricsFromRanks([]int{1, 4, 0})
	if metrics.Hits[1] != 1 || metrics.Hits[3] != 1 || metrics.Hits[5] != 2 {
		t.Fatalf("hit counts: %+v", metrics.Hits)
	}
	wantMRR := (1.0 + 0.25) / 3
	if math.Abs(metrics.MRR10-wantMRR) > 1e-12 {
		t.Fatalf("MRR=%.12f, ожидалось %.12f", metrics.MRR10, wantMRR)
	}
}

func TestEvidenceMatchingCollapsesWhitespace(t *testing.T) {
	if !containsEvidence("ответ\n  с   разными\tпробелами", "ответ с разными пробелами") {
		t.Fatal("цитата с эквивалентными пробелами не найдена")
	}
}
