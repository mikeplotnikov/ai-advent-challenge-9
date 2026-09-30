package rag

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func validIndex() Index {
	return Index{Header: IndexHeader{
		Strategy: "structure", Model: "bge-m3", Dimension: 2, ManifestSHA256: "manifest",
		Parameters: StrategyParameters{MaxRunes: StructureMaxSize, Rewind: FixedRewind}, ChunkCount: 1,
	}, Chunks: []Chunk{{ChunkID: "structure:a:0", Strategy: "structure", Embedding: []float64{1, 0}}}}
}

func TestValidateIndexRejectsIncompatibleIndex(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Index)
	}{
		{"model", func(index *Index) { index.Header.Model = "other" }},
		{"manifest", func(index *Index) { index.Header.ManifestSHA256 = "other" }},
		{"strategy", func(index *Index) { index.Header.Strategy = "fixed" }},
		{"parameters", func(index *Index) { index.Header.Parameters.MaxRunes++ }},
		{"expected dimension", func(index *Index) { index.Header.Dimension = 3; index.Chunks[0].Embedding = []float64{1, 0, 0} }},
		{"chunk dimension", func(index *Index) { index.Chunks[0].Embedding = []float64{1} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index := validIndex()
			test.edit(&index)
			if err := ValidateIndex(index, "structure", "bge-m3", "manifest", 2); err == nil {
				t.Fatal("ValidateIndex accepted an incompatible index")
			} else if !strings.Contains(err.Error(), "go run ./day-21 -index") {
				t.Fatalf("error is not actionable: %v", err)
			}
		})
	}
}

func TestReadIndexMissingIsActionable(t *testing.T) {
	_, err := ReadIndex(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil || !strings.Contains(err.Error(), "go run ./day-21 -index") {
		t.Fatalf("err=%v", err)
	}
}

func TestOllamaUnavailableIsActionable(t *testing.T) {
	client := &OllamaClient{BaseURL: "http://127.0.0.1:1", Model: "bge-m3", HTTP: &http.Client{Transport: failingTransport{}}}
	_, _, _, err := client.Embed(context.Background(), "q", "question")
	if err == nil || !strings.Contains(err.Error(), "ollama serve") {
		t.Fatalf("err=%v", err)
	}
}

func TestOllamaRequestAndNormalization(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Model    string `json:"model"`
			Input    string `json:"input"`
			Truncate bool   `json:"truncate"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "bge-m3" || body.Input != "question" || body.Truncate {
			t.Fatalf("request=%+v", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"embeddings":[[3,4]],"prompt_eval_count":7}`)), Header: make(http.Header)}, nil
	})
	client := &OllamaClient{BaseURL: "http://ollama.invalid", Model: "bge-m3", HTTP: &http.Client{Transport: transport}}
	vector, tokens, normalized, err := client.Embed(context.Background(), "q", "question")
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 7 || !normalized || len(vector) != 2 || vector[0] != 0.6 || vector[1] != 0.8 {
		t.Fatalf("vector=%v tokens=%d normalized=%v", vector, tokens, normalized)
	}
}

func TestOllamaRejectsZeroVector(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"embeddings":[[0,0]]}`)), Header: make(http.Header)}, nil
	})
	client := &OllamaClient{BaseURL: "http://ollama.invalid", Model: "bge-m3", HTTP: &http.Client{Transport: transport}}
	if _, _, _, err := client.Embed(context.Background(), "q", "question"); err == nil || !strings.Contains(err.Error(), "нулевой вектор") {
		t.Fatalf("err=%v", err)
	}
}

func TestTerminalSafeReplacesControlCharacters(t *testing.T) {
	if got := TerminalSafe("a\x1b\nb\t", 10); got != "a??b?" {
		t.Fatalf("got %q", got)
	}
}

func TestTerminalSafeMultilinePreservesNewlinesAndTabs(t *testing.T) {
	if got := TerminalSafeMultiline("a\n\tb\r\x1b\u0085c", 20); got != "a\n\tb???c" {
		t.Fatalf("got %q", got)
	}
}

func TestTerminalSafeReplacesFormatAndSeparatorRunes(t *testing.T) {
	input := "a\u202eb\u2066c\u200bd\u2028e\u2029f"
	for name, got := range map[string]string{
		"single":    TerminalSafe(input, 40),
		"multiline": TerminalSafeMultiline(input, 40),
	} {
		if got != "a?b?c?d?e?f" {
			t.Fatalf("%s: got %q", name, got)
		}
	}
}

func TestRankChunksAndEvidence(t *testing.T) {
	index := validIndex()
	index.Header.ChunkCount = 2
	index.Chunks = append(index.Chunks, Chunk{ChunkID: "structure:b:0", Strategy: "structure", Text: "alpha\n beta", Embedding: []float64{0, 1}})
	results, err := RankChunks(index, []float64{0, 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Chunk.ChunkID != "structure:b:0" || FirstEvidenceRank(results, "alpha beta") != 1 {
		t.Fatalf("unexpected ranking: %+v", results)
	}
}

func TestProjectPathFindsRepositoryRootForMissingOutput(t *testing.T) {
	got := ProjectPath("day-22/not-created-yet.json")
	if _, err := os.Stat(filepath.Dir(got)); err != nil {
		t.Fatalf("resolved output directory does not exist: %s: %v", got, err)
	}
	if filepath.Base(filepath.Dir(got)) != "day-22" {
		t.Fatalf("resolved outside day-22: %s", got)
	}
	absolute := filepath.Join(t.TempDir(), "not-created-yet.json")
	if resolved := ProjectPath(absolute); resolved != absolute {
		t.Fatalf("absolute path changed: got %s want %s", resolved, absolute)
	}
}

func TestLiveStructureParity(t *testing.T) {
	connection, err := net.DialTimeout("tcp4", "127.0.0.1:11434", 250*time.Millisecond)
	if err != nil {
		t.Skip("Ollama is not listening on 127.0.0.1:11434")
	}
	_ = connection.Close()
	indexPath := ProjectPath("day-21/index/structure.json")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		t.Skip("day-21 structure index is absent")
	}
	index, err := ReadIndex(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestSHA, err := ManifestSHA256(ProjectPath("day-21/corpus"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateIndex(index, "structure", "bge-m3", manifestSHA, 0); err != nil {
		t.Fatal(err)
	}
	var questions []struct {
		Question string `json:"question"`
	}
	readJSON(t, ProjectPath("day-21/eval/questions.json"), &questions)
	var showcase struct {
		Questions []struct {
			Structure struct {
				Top10 []struct {
					ChunkID string `json:"chunk_id"`
				} `json:"top_10"`
			} `json:"structure"`
		} `json:"questions"`
	}
	readJSON(t, ProjectPath("day-21/showcase.json"), &showcase)
	if len(questions) != 40 || len(showcase.Questions) != 40 {
		t.Fatalf("expected 40 questions, got %d/%d", len(questions), len(showcase.Questions))
	}
	client := &OllamaClient{BaseURL: "http://127.0.0.1:11434", Model: "bge-m3", HTTP: &http.Client{Timeout: 2 * time.Minute}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for i, question := range questions {
		vector, _, _, err := client.Embed(ctx, "parity", question.Question)
		if err != nil {
			t.Fatalf("question %d: %v", i+1, err)
		}
		got, err := RankChunks(index, vector, 10)
		if err != nil {
			t.Fatal(err)
		}
		for j := range got {
			if got[j].Chunk.ChunkID != showcase.Questions[i].Structure.Top10[j].ChunkID {
				t.Fatalf("question %d rank %d: got %s want %s", i+1, j+1, got[j].Chunk.ChunkID, showcase.Questions[i].Structure.Top10[j].ChunkID)
			}
		}
	}
}

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}
