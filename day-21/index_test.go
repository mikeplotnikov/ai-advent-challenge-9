package main

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type staticEmbedder struct {
	vector     []float64
	tokens     int
	normalized bool
}

func (e staticEmbedder) Embed(context.Context, string, string) ([]float64, int, bool, error) {
	return append([]float64(nil), e.vector...), e.tokens, e.normalized, nil
}

func sampleIndex() Index {
	return Index{
		Header: IndexHeader{Strategy: "fixed", Model: "bge-m3", Dimension: 2, BuiltAt: "2026-09-29T00:00:00+04:00", ManifestSHA256: "abc", Parameters: StrategyParameters{Size: fixedSize, Overlap: fixedOverlap, Rewind: fixedRewind}, ChunkCount: 1, TotalTokens: 3},
		Chunks: []Chunk{{ChunkID: "fixed:x.md:0", Strategy: "fixed", Source: "x.md", Title: "X", Section: "X", Spans: 1, End: 4, Runes: 4, Tokens: 3, Text: "text", Embedding: []float64{1, 0}}},
	}
}

func TestIndexRoundTripIsAtomicAndLossless(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "fixed.json")
	want := sampleIndex()
	if err := writeIndexAtomic(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed index\nwant=%+v\ngot=%+v", want, got)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "fixed.json" {
		t.Fatalf("после atomic rename остались файлы: %v", entries)
	}
}

func TestFailedIndexWritePreservesPreviousFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "fixed.json")
	if err := os.WriteFile(path, []byte("previous complete index"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := sampleIndex()
	broken.Chunks[0].Embedding[0] = math.NaN()
	if err := writeIndexAtomic(path, broken); err == nil {
		t.Fatal("JSON с NaN неожиданно записан")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "previous complete index" {
		t.Fatalf("неудачная запись повредила старый индекс: %q", content)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "fixed.json" {
		t.Fatalf("после ошибки остались временные файлы: %v", entries)
	}
}

func TestIndexCompatibilityChecksModelManifestAndDimension(t *testing.T) {
	base := sampleIndex()
	if err := validateIndex(base, "fixed", "bge-m3", "abc", 2); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*Index)
		want string
	}{
		{"model", func(index *Index) { index.Header.Model = "other" }, "-index"},
		{"manifest", func(index *Index) { index.Header.ManifestSHA256 = "other" }, "-index"},
		{"parameters", func(index *Index) { index.Header.Parameters.Size++ }, "параметрами"},
		{"header dimension", func(index *Index) { index.Header.Dimension = 3 }, "размерность"},
		{"vector dimension", func(index *Index) { index.Chunks[0].Embedding = []float64{1} }, "размерность"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			index := sampleIndex()
			tc.edit(&index)
			if err := validateIndex(index, "fixed", "bge-m3", "abc", 2); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ошибка=%v, ожидалась строка %q", err, tc.want)
			}
		})
	}
}

func TestBuildIndexCountsNormalizedVectors(t *testing.T) {
	document := testDocument("x.md", "# X\nтекст")
	index, err := buildIndex(context.Background(), "fixed", []Document{document}, "abc", "bge-m3", staticEmbedder{vector: []float64{1, 0}, tokens: 7, normalized: true}, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if index.Header.NormalizedVectors != 1 || index.Header.TotalTokens != 7 || index.Header.Dimension != 2 || index.Chunks[0].Tokens != 7 {
		t.Fatalf("шапка или чанк: %+v %+v", index.Header, index.Chunks[0])
	}
}
