package main

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
	"strings"
	"testing"
)

func TestDumpModeWritesShowcaseWithoutLiveOllama(t *testing.T) {
	setupDumpFixture(t, true, true)
	originalClient := httpClientNoFixedTimeout
	originalRevision := showcaseRevision
	showcaseRevision = func() string { return "abc1234" }
	var embedded []string
	httpClientNoFixedTimeout = *handlerHTTPClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("тело запроса к Ollama: %v", err)
		}
		embedded = append(embedded, request.Input)
		_, _ = io.WriteString(w, `{"embeddings":[[1,0,0,0,0,0,0,0]],"prompt_eval_count":7}`)
	}))
	defer func() {
		httpClientNoFixedTimeout = originalClient
		showcaseRevision = originalRevision
	}()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-dump"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	raw, err := os.ReadFile("showcase.json")
	if err != nil {
		t.Fatal(err)
	}
	var showcase Showcase
	if err := json.Unmarshal(raw, &showcase); err != nil {
		t.Fatal(err)
	}
	if len(showcase.Questions) != 40 || showcase.Meta.Files != 15 || showcase.Meta.Dimension != 8 {
		t.Fatalf("неполная выгрузка: meta=%+v questions=%d", showcase.Meta, len(showcase.Questions))
	}
	documents, manifest, manifestSHA, err := loadCorpus("corpus")
	if err != nil {
		t.Fatal(err)
	}
	questions, questionsSHA, err := loadQuestions(filepath.Join("eval", "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := readComparison("compare.json")
	if err != nil {
		t.Fatal(err)
	}
	fixed, err := readIndex(filepath.Join("index", "fixed.json"))
	if err != nil {
		t.Fatal(err)
	}
	structure, err := readIndex(filepath.Join("index", "structure.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(embedded) != len(questions) {
		t.Fatalf("в Ollama ушло %d текстов, вопросов %d", len(embedded), len(questions))
	}
	for i, question := range questions {
		if embedded[i] != question.Question {
			t.Fatalf("%s: в Ollama ушёл текст %q, а не текст вопроса", question.ID, embedded[i])
		}
	}
	assertShowcaseSources(t, showcase, "abc1234", documents, manifest, manifestSHA, questions, questionsSHA, comparison, fixed, structure)
	vector := []float64{1, 0, 0, 0, 0, 0, 0, 0}
	for i, question := range questions {
		for name, pair := range map[string]struct {
			got   ShowcaseQuestionResult
			index Index
		}{"fixed": {showcase.Questions[i].Fixed, fixed}, "structure": {showcase.Questions[i].Structure, structure}} {
			expected, err := rankChunks(pair.index, vector, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(pair.got.Top10) != len(expected) {
				t.Fatalf("%s %s: top_10=%d, ожидалось %d", question.ID, name, len(pair.got.Top10), len(expected))
			}
			for rank := range expected {
				wantScore := math.Round(expected[rank].Similarity*10000) / 10000
				wantHit := containsEvidence(expected[rank].Chunk.Text, question.Evidence)
				got := pair.got.Top10[rank]
				if got.ChunkID != expected[rank].Chunk.ChunkID || got.Score != wantScore || got.Hit != wantHit {
					t.Errorf("%s %s rank %d: got=%+v want chunk=%s score=%v hit=%t", question.ID, name, rank+1, got, expected[rank].Chunk.ChunkID, wantScore, wantHit)
				}
			}
		}
	}
}

func TestDumpModeErrorsBeforeCallingOllama(t *testing.T) {
	t.Run("missing indexes", func(t *testing.T) {
		setupDumpFixture(t, false, false)
		var stdout, stderr bytes.Buffer
		code := run([]string{"-dump"}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "-index") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})
	t.Run("missing compare", func(t *testing.T) {
		setupDumpFixture(t, true, false)
		var stdout, stderr bytes.Buffer
		code := run([]string{"-dump"}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "go run ./day-21 -compare") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})
	for _, strategy := range []string{"fixed", "structure"} {
		t.Run("stale "+strategy+" index", func(t *testing.T) {
			setupDumpFixture(t, true, true)
			path := filepath.Join("index", strategy+".json")
			index, err := readIndex(path)
			if err != nil {
				t.Fatal(err)
			}
			if strategy == "fixed" {
				index.Header.Parameters.Size++
			} else {
				index.Header.Parameters.MaxRunes++
			}
			if err := writeIndexAtomic(path, index); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := run([]string{"-dump"}, &stdout, &stderr)
			if code != 1 || !strings.Contains(stderr.String(), "-index") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
	t.Run("unknown commit", func(t *testing.T) {
		setupDumpFixture(t, true, true)
		originalRevision := showcaseRevision
		showcaseRevision = func() string { return "unknown" }
		defer func() { showcaseRevision = originalRevision }()
		var stdout, stderr bytes.Buffer
		code := run([]string{"-dump"}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "коммит A9") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})
}

func TestDumpModeWithoutOllamaSuggestsServe(t *testing.T) {
	setupDumpFixture(t, true, true)
	originalClient := httpClientNoFixedTimeout
	originalRevision := showcaseRevision
	showcaseRevision = func() string { return "abc1234" }
	httpClientNoFixedTimeout = http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("connection refused")
	})}
	defer func() {
		httpClientNoFixedTimeout = originalClient
		showcaseRevision = originalRevision
	}()
	var stdout, stderr bytes.Buffer
	code := run([]string{"-dump", "-ollama", "http://127.0.0.1:1", "-timeout", "2s"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "ollama serve") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestShowcaseFirstHitsMatchComparison(t *testing.T) {
	showcase := readCommittedShowcase(t)
	comparison, err := readComparison("compare.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(showcase.Questions) != len(comparison.Questions) {
		t.Fatalf("showcase=%d compare=%d", len(showcase.Questions), len(comparison.Questions))
	}
	for i, question := range showcase.Questions {
		measured := comparison.Questions[i]
		if question.ID != measured.ID || nullableRank(question.Fixed.FirstHit) != measured.FixedRank || nullableRank(question.Structure.FirstHit) != measured.StructureRank ||
			(measured.FixedRank == 0) != (question.Fixed.FirstHit == nil) || (measured.StructureRank == 0) != (question.Structure.FirstHit == nil) {
			t.Errorf("%s: showcase fixed=%d structure=%d; compare %s fixed=%d structure=%d", question.ID, nullableRank(question.Fixed.FirstHit), nullableRank(question.Structure.FirstHit), measured.ID, measured.FixedRank, measured.StructureRank)
		}
	}
}

func TestShowcaseMetadataCorpusAndProjectionsMatchSources(t *testing.T) {
	showcase := readCommittedShowcase(t)
	documents, manifest, manifestSHA, err := loadCorpus("corpus")
	if err != nil {
		t.Fatal(err)
	}
	questions, questionsSHA, err := loadQuestions(filepath.Join("eval", "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := readComparison("compare.json")
	if err != nil {
		t.Fatal(err)
	}
	assertCommittedShowcaseSources(t, showcase, documents, manifest, manifestSHA, questions, questionsSHA, comparison)
}

func TestShowcaseResultRowsMatchResultsMarkdown(t *testing.T) {
	showcase := readCommittedShowcase(t)
	raw, err := os.ReadFile("RESULTS.md")
	if err != nil {
		t.Fatal(err)
	}
	markdownTables := extractMarkdownTables(string(raw))
	if len(markdownTables) < 3 {
		t.Fatalf("в RESULTS.md найдено %d таблиц", len(markdownTables))
	}
	showcaseTables := [][][]string{showcase.Results.Chunks, showcase.Results.Search, showcase.Results.McNemar}
	for i, rows := range showcaseTables {
		if len(rows) != len(markdownTables[i]) {
			t.Errorf("таблица %d: showcase=%d RESULTS=%d", i, len(rows), len(markdownTables[i]))
			continue
		}
		for j, row := range rows {
			line := markdownTableRow(row)
			if line != markdownTables[i][j] || !strings.Contains(string(raw), line) {
				t.Errorf("таблица %d строка %d не совпала:\nshowcase: %s\nRESULTS:  %s", i, j, line, markdownTables[i][j])
			}
		}
	}
}

func TestShowcaseChunksMatchMeasuredBoundaries(t *testing.T) {
	showcase := readCommittedShowcase(t)
	documents := make(map[string][]rune, len(showcase.Corpus))
	for _, document := range showcase.Corpus {
		documents[document.Source] = []rune(document.Text)
	}
	cases := []struct {
		name       string
		chunks     []ShowcaseChunk
		wantChunks int
		wantInside int
	}{
		{"fixed", showcase.Strategies.Fixed.Chunks, 170, 134},
		{"structure", showcase.Strategies.Structure.Chunks, 218, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.chunks) != tc.wantChunks {
				t.Fatalf("чанков=%d, ожидалось %d", len(tc.chunks), tc.wantChunks)
			}
			inside := 0
			for _, chunk := range tc.chunks {
				text, ok := documents[chunk.Source]
				if !ok || chunk.Start < 0 || chunk.Start >= chunk.End || chunk.End > len(text) {
					t.Errorf("неверный диапазон %+v при длине %d", chunk, len(text))
				}
				if len(chunk.EmbeddingHead) != 8 {
					t.Errorf("%s: embedding_head=%d", chunk.ChunkID, len(chunk.EmbeddingHead))
				}
				if chunk.InsideParagraph {
					inside++
				}
			}
			if inside != tc.wantInside {
				t.Fatalf("inside_paragraph=%d, ожидалось %d", inside, tc.wantInside)
			}
		})
	}

	raw, err := os.ReadFile("showcase.json")
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		Strategies map[string]struct {
			Chunks []map[string]json.RawMessage `json:"chunks"`
		} `json:"strategies"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	for strategy, data := range shape.Strategies {
		for _, chunk := range data.Chunks {
			if _, ok := chunk["text"]; ok {
				t.Errorf("%s-чанк дублирует text", strategy)
			}
			if _, ok := chunk["embedding"]; ok {
				t.Errorf("%s-чанк содержит полный embedding", strategy)
			}
		}
	}
}

func TestShowcaseMatchesTheCopyTheShowcaseChecksAgainst(t *testing.T) {
	committed, err := os.ReadFile("showcase.json")
	if os.IsNotExist(err) {
		t.Skip("showcase.json ещё не выгружен")
	}
	if err != nil {
		t.Fatal(err)
	}
	const copyPath = "../../uchebnik-ai-advent/challeng/public/day-21/showcase.json"
	copy, err := os.ReadFile(copyPath)
	if err != nil {
		t.Skipf("копии витрины нет рядом (%v)", err)
	}
	if !bytes.Equal(committed, copy) {
		t.Errorf("выгрузка разошлась с копией витрины %s", copyPath)
	}
}

func readCommittedShowcase(t *testing.T) Showcase {
	t.Helper()
	raw, err := os.ReadFile("showcase.json")
	if os.IsNotExist(err) {
		t.Skip("showcase.json ещё не выгружен")
	}
	if err != nil {
		t.Fatal(err)
	}
	var showcase Showcase
	if err := json.Unmarshal(raw, &showcase); err != nil {
		t.Fatal(err)
	}
	return showcase
}

func assertShowcaseSources(t *testing.T, showcase Showcase, expectedCommit string, documents []Document, manifest Manifest, manifestSHA string, questions []Question, questionsSHA string, comparison Comparison, fixed, structure Index) {
	t.Helper()
	totalRunes := 0
	for _, entry := range manifest.Files {
		totalRunes += entry.Runes
	}
	if showcase.Meta.Commit != expectedCommit || showcase.Meta.Revision != comparison.Revision || showcase.Meta.Started != comparison.Started ||
		showcase.Meta.Model != comparison.Model || showcase.Meta.Dimension != fixed.Header.Dimension ||
		showcase.Meta.ManifestSHA256 != manifestSHA || showcase.Meta.QuestionsSHA256 != questionsSHA ||
		showcase.Meta.Files != len(manifest.Files) || showcase.Meta.Runes != totalRunes || showcase.Meta.Pages != (totalRunes+1799)/1800 {
		t.Errorf("meta не соответствует источникам: %+v", showcase.Meta)
	}
	if len(showcase.Corpus) != len(documents) {
		t.Fatalf("corpus=%d, ожидалось %d", len(showcase.Corpus), len(documents))
	}
	documentBySource := make(map[string]Document, len(documents))
	for i, document := range documents {
		documentBySource[document.Source] = document
		got := showcase.Corpus[i]
		if got.Source != document.Source || got.Title != document.Title || got.Text != document.Text {
			t.Errorf("corpus[%d] не совпадает с %s", i, document.Source)
		}
	}
	if showcase.Strategies.Fixed.Parameters != fixed.Header.Parameters || showcase.Strategies.Structure.Parameters != structure.Header.Parameters {
		t.Errorf("параметры стратегий не совпадают с индексами")
	}
	assertChunkProjection(t, "fixed", showcase.Strategies.Fixed.Chunks, fixed.Chunks, documentBySource)
	assertChunkProjection(t, "structure", showcase.Strategies.Structure.Chunks, structure.Chunks, documentBySource)

	if len(showcase.Questions) != len(questions) || len(comparison.Questions) != len(questions) {
		t.Fatalf("questions: showcase=%d compare=%d source=%d", len(showcase.Questions), len(comparison.Questions), len(questions))
	}
	indexes := map[string]Index{"fixed": fixed, "structure": structure}
	for i, question := range questions {
		gotQuestion := showcase.Questions[i]
		if gotQuestion.ID != question.ID || gotQuestion.Question != question.Question || gotQuestion.Source != question.Source || gotQuestion.Evidence != question.Evidence {
			t.Errorf("question[%d] не совпадает с %s", i, question.ID)
		}
		results := map[string]ShowcaseQuestionResult{"fixed": gotQuestion.Fixed, "structure": gotQuestion.Structure}
		for name, got := range results {
			chunks := make(map[string]Chunk, len(indexes[name].Chunks))
			for _, chunk := range indexes[name].Chunks {
				chunks[chunk.ChunkID] = chunk
			}
			wantLength := min(10, len(chunks))
			if len(got.Top10) != wantLength {
				t.Errorf("%s %s: top_10=%d, ожидалось %d", question.ID, name, len(got.Top10), wantLength)
			}
			firstHit := 0
			for rank, result := range got.Top10 {
				chunk, ok := chunks[result.ChunkID]
				if !ok {
					t.Errorf("%s %s rank %d: неизвестный chunk_id %s", question.ID, name, rank+1, result.ChunkID)
					continue
				}
				if result.Score != math.Round(result.Score*10000)/10000 {
					t.Errorf("%s %s rank %d: score %v не округлён до 4 знаков", question.ID, name, rank+1, result.Score)
				}
				if rank > 0 && result.Score > got.Top10[rank-1].Score {
					t.Errorf("%s %s: score вырос на rank %d", question.ID, name, rank+1)
				}
				wantHit := containsEvidence(chunk.Text, question.Evidence)
				if result.Hit != wantHit {
					t.Errorf("%s %s rank %d: hit=%t, ожидалось %t", question.ID, name, rank+1, result.Hit, wantHit)
				}
				if result.Hit && firstHit == 0 {
					firstHit = rank + 1
				}
			}
			if nullableRank(got.FirstHit) != firstHit || (firstHit == 0) != (got.FirstHit == nil) {
				t.Errorf("%s %s: first_hit=%d, первый hit в top_10=%d", question.ID, name, nullableRank(got.FirstHit), firstHit)
			}
		}
	}
}

func assertCommittedShowcaseSources(t *testing.T, showcase Showcase, documents []Document, manifest Manifest, manifestSHA string, questions []Question, questionsSHA string, comparison Comparison) {
	t.Helper()
	totalRunes := 0
	for _, entry := range manifest.Files {
		totalRunes += entry.Runes
	}
	if !looksLikeGitRevision(showcase.Meta.Commit) || showcase.Meta.Revision != comparison.Revision || showcase.Meta.Started != comparison.Started ||
		showcase.Meta.Model != comparison.Model || showcase.Meta.Dimension != 1024 ||
		showcase.Meta.ManifestSHA256 != manifestSHA || showcase.Meta.QuestionsSHA256 != questionsSHA ||
		showcase.Meta.Files != len(manifest.Files) || showcase.Meta.Runes != totalRunes || showcase.Meta.Pages != (totalRunes+1799)/1800 {
		t.Errorf("meta не соответствует закоммиченным источникам: %+v", showcase.Meta)
	}
	if len(showcase.Corpus) != len(documents) {
		t.Fatalf("corpus=%d, ожидалось %d", len(showcase.Corpus), len(documents))
	}
	documentBySource := make(map[string]Document, len(documents))
	for i, document := range documents {
		documentBySource[document.Source] = document
		got := showcase.Corpus[i]
		if got.Source != document.Source || got.Title != document.Title || got.Text != document.Text {
			t.Errorf("corpus[%d] не совпадает с %s", i, document.Source)
		}
	}
	wantFixedParameters := StrategyParameters{Size: fixedSize, Overlap: fixedOverlap, Rewind: fixedRewind}
	wantStructureParameters := StrategyParameters{MaxRunes: structureMaxSize, Rewind: fixedRewind}
	if showcase.Strategies.Fixed.Parameters != wantFixedParameters || showcase.Strategies.Structure.Parameters != wantStructureParameters {
		t.Errorf("параметры стратегий не совпадают с кодом")
	}
	fixedText := assertExportedChunks(t, "fixed", showcase.Strategies.Fixed.Chunks, documentBySource)
	structureText := assertExportedChunks(t, "structure", showcase.Strategies.Structure.Chunks, documentBySource)

	if len(showcase.Questions) != len(questions) || len(comparison.Questions) != len(questions) {
		t.Fatalf("questions: showcase=%d compare=%d source=%d", len(showcase.Questions), len(comparison.Questions), len(questions))
	}
	chunkTexts := map[string]map[string]string{"fixed": fixedText, "structure": structureText}
	for i, question := range questions {
		gotQuestion := showcase.Questions[i]
		if gotQuestion.ID != question.ID || gotQuestion.Question != question.Question || gotQuestion.Source != question.Source || gotQuestion.Evidence != question.Evidence {
			t.Errorf("question[%d] не совпадает с %s", i, question.ID)
		}
		for name, got := range map[string]ShowcaseQuestionResult{"fixed": gotQuestion.Fixed, "structure": gotQuestion.Structure} {
			if len(got.Top10) != 10 {
				t.Errorf("%s %s: top_10=%d, ожидалось 10", question.ID, name, len(got.Top10))
			}
			firstHit := 0
			for rank, result := range got.Top10 {
				text, ok := chunkTexts[name][result.ChunkID]
				if !ok {
					t.Errorf("%s %s rank %d: неизвестный chunk_id %s", question.ID, name, rank+1, result.ChunkID)
					continue
				}
				if result.Score != math.Round(result.Score*10000)/10000 {
					t.Errorf("%s %s rank %d: score %v не округлён до 4 знаков", question.ID, name, rank+1, result.Score)
				}
				if rank > 0 && result.Score > got.Top10[rank-1].Score {
					t.Errorf("%s %s: score вырос на rank %d", question.ID, name, rank+1)
				}
				wantHit := containsEvidence(text, question.Evidence)
				if result.Hit != wantHit {
					t.Errorf("%s %s rank %d: hit=%t, ожидалось %t", question.ID, name, rank+1, result.Hit, wantHit)
				}
				if result.Hit && firstHit == 0 {
					firstHit = rank + 1
				}
			}
			if nullableRank(got.FirstHit) != firstHit || (firstHit == 0) != (got.FirstHit == nil) {
				t.Errorf("%s %s: first_hit=%d, первый hit в top_10=%d", question.ID, name, nullableRank(got.FirstHit), firstHit)
			}
		}
	}
}

func assertExportedChunks(t *testing.T, strategy string, chunks []ShowcaseChunk, documents map[string]Document) map[string]string {
	t.Helper()
	texts := make(map[string]string, len(chunks))
	for _, chunk := range chunks {
		document, ok := documents[chunk.Source]
		if !ok || chunk.Start < 0 || chunk.Start >= chunk.End || chunk.End > len(document.Runes) {
			t.Errorf("%s: неверный диапазон %+v", strategy, chunk)
			continue
		}
		if _, exists := texts[chunk.ChunkID]; chunk.ChunkID == "" || exists || !strings.HasPrefix(chunk.ChunkID, strategy+":"+chunk.Source+":") {
			t.Errorf("%s: пустой, повторный или неверный chunk_id %q", strategy, chunk.ChunkID)
		}
		if chunk.Runes != chunk.End-chunk.Start || chunk.Spans < 1 || chunk.Part < 0 || chunk.Tokens <= 0 ||
			chunk.InsideParagraph != boundaryInsideParagraph(document.Runes, chunk.End) || len(chunk.EmbeddingHead) != 8 {
			t.Errorf("%s: неверные метаданные %+v", chunk.ChunkID, chunk)
		}
		texts[chunk.ChunkID] = string(document.Runes[chunk.Start:chunk.End])
	}
	return texts
}

func looksLikeGitRevision(revision string) bool {
	if len(revision) < 7 || len(revision) > 40 {
		return false
	}
	for _, r := range revision {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func assertChunkProjection(t *testing.T, strategy string, got []ShowcaseChunk, want []Chunk, documents map[string]Document) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: showcase chunks=%d, index chunks=%d", strategy, len(got), len(want))
	}
	for i, chunk := range want {
		exported := got[i]
		document, ok := documents[chunk.Source]
		if !ok {
			t.Fatalf("%s: файла %s нет в корпусе", chunk.ChunkID, chunk.Source)
		}
		if exported.ChunkID != chunk.ChunkID || exported.Source != chunk.Source || exported.Section != chunk.Section ||
			exported.Spans != chunk.Spans || exported.Part != chunk.Part || exported.Start != chunk.Start || exported.End != chunk.End ||
			exported.Runes != chunk.Runes || exported.Tokens != chunk.Tokens ||
			exported.InsideParagraph != boundaryInsideParagraph(document.Runes, chunk.End) {
			t.Errorf("%s: метаданные чанка не совпадают\ngot=%+v\nwant=%+v", chunk.ChunkID, exported, chunk)
		}
		if len(exported.EmbeddingHead) != 8 {
			t.Errorf("%s: embedding_head=%d", chunk.ChunkID, len(exported.EmbeddingHead))
			continue
		}
		for j := range exported.EmbeddingHead {
			wantValue := math.Round(chunk.Embedding[j]*10000) / 10000
			if exported.EmbeddingHead[j] != wantValue {
				t.Errorf("%s embedding_head[%d]=%v, ожидалось %v", chunk.ChunkID, j, exported.EmbeddingHead[j], wantValue)
			}
		}
	}
}

func nullableRank(rank *int) int {
	if rank == nil {
		return 0
	}
	return *rank
}

func extractMarkdownTables(text string) [][]string {
	var tables [][]string
	var current []string
	flush := func() {
		if len(current) > 0 {
			tables = append(tables, current)
			current = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "|") {
			flush()
			continue
		}
		if strings.Trim(line, "|:- ") == "" {
			continue
		}
		current = append(current, line)
	}
	flush()
	return tables
}

func setupDumpFixture(t *testing.T, withIndexes, withComparison bool) {
	t.Helper()
	t.Chdir(t.TempDir())
	questions, documents := questionFixture(15)
	if err := os.MkdirAll("corpus", 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SourceCommit: "test", Files: make([]ManifestFile, len(documents))}
	for i, document := range documents {
		raw := []byte(document.Text)
		digest := sha256.Sum256(raw)
		manifest.Files[i] = ManifestFile{Path: document.Source, Runes: len(document.Runes), SHA256: hex.EncodeToString(digest[:])}
		if err := os.WriteFile(filepath.Join("corpus", document.Source), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("corpus", manifestName), manifestRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	if !withIndexes {
		return
	}
	manifestDigest := sha256.Sum256(manifestRaw)
	manifestSHA := hex.EncodeToString(manifestDigest[:])
	if err := os.MkdirAll("eval", 0o755); err != nil {
		t.Fatal(err)
	}
	questionsRaw, err := json.Marshal(questions)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("eval", "questions.json"), questionsRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	questionDigest := sha256.Sum256(questionsRaw)
	questionsSHA := hex.EncodeToString(questionDigest[:])

	fixed := fixtureIndex("fixed", fixedChunks(documents), manifestSHA)
	structure := fixtureIndex("structure", structureChunks(documents), manifestSHA)
	if err := writeIndexAtomic(filepath.Join("index", "fixed.json"), fixed); err != nil {
		t.Fatal(err)
	}
	if err := writeIndexAtomic(filepath.Join("index", "structure.json"), structure); err != nil {
		t.Fatal(err)
	}
	if !withComparison {
		return
	}
	vectors := make([][]float64, len(questions))
	for i := range vectors {
		vectors[i] = []float64{1, 0, 0, 0, 0, 0, 0, 0}
	}
	fixedMetrics, err := evaluate(fixed, questions, vectors)
	if err != nil {
		t.Fatal(err)
	}
	structureMetrics, err := evaluate(structure, questions, vectors)
	if err != nil {
		t.Fatal(err)
	}
	comparison := Comparison{
		Revision: "fixture", Started: "2026-09-29T00:00:00+04:00", Model: defaultModel,
		ManifestSHA256: manifestSHA, QuestionsSHA256: questionsSHA, Questions: make([]QuestionResult, len(questions)),
	}
	for i, question := range questions {
		comparison.Questions[i] = QuestionResult{ID: question.ID, Source: question.Source, FixedRank: fixedMetrics.Ranks[i], StructureRank: structureMetrics.Ranks[i]}
	}
	if err := writeJSONAtomic("compare.json", comparison); err != nil {
		t.Fatal(err)
	}
}

func fixtureIndex(strategy string, chunks []Chunk, manifestSHA string) Index {
	parameters := StrategyParameters{Size: fixedSize, Overlap: fixedOverlap, Rewind: fixedRewind}
	if strategy == "structure" {
		parameters = StrategyParameters{MaxRunes: structureMaxSize, Rewind: fixedRewind}
	}
	for i := range chunks {
		x := 1 - float64(i)*0.001
		chunks[i].Tokens = 10
		chunks[i].Embedding = []float64{x, math.Sqrt(1 - x*x), 0, 0, 0, 0, 0, 0}
	}
	return Index{Header: IndexHeader{
		Strategy: strategy, Model: defaultModel, Dimension: 8, BuiltAt: "2026-09-29T00:00:00+04:00",
		ManifestSHA256: manifestSHA, Parameters: parameters, ChunkCount: len(chunks), TotalTokens: len(chunks) * 10,
	}, Chunks: chunks}
}

func TestFailedShowcaseWritePreservesPreviousFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "showcase.json")
	if err := os.WriteFile(path, []byte("previous complete showcase"), 0o644); err != nil {
		t.Fatal(err)
	}
	document := testDocument("x.md", "# X\nответ для проверки атомарной записи")
	question := Question{ID: "q1", Question: "вопрос", Source: document.Source, Evidence: "ответ для проверки атомарной записи"}
	fixed := fixtureIndex("fixed", fixedChunks([]Document{document}), "manifest")
	structure := fixtureIndex("structure", structureChunks([]Document{document}), "manifest")
	fixed.Chunks[0].Embedding[0] = math.NaN()
	comparison := Comparison{
		Revision: "measure", Started: "2026-09-29T00:00:00+04:00", Model: defaultModel,
		ManifestSHA256: "manifest", QuestionsSHA256: "questions",
		Questions: []QuestionResult{{ID: question.ID, Source: question.Source, FixedRank: 1, StructureRank: 1}},
	}
	originalRevision := showcaseRevision
	showcaseRevision = func() string { return "abc1234" }
	defer func() { showcaseRevision = originalRevision }()
	err := writeShowcase(context.Background(), path, defaultModel, "manifest", "questions", Manifest{Files: []ManifestFile{{Path: document.Source, Runes: len(document.Runes)}}}, []Document{document}, []Question{question}, fixed, structure, comparison, "fixed.json", "structure.json", staticEmbedder{vector: []float64{1, 0, 0, 0, 0, 0, 0, 0}}, io.Discard)
	if err == nil {
		t.Fatal("JSON с NaN неожиданно записан")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "previous complete showcase" {
		t.Fatalf("неудачная запись повредила старую выгрузку: %q", raw)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "showcase.json" {
		t.Fatalf("после ошибки остались временные файлы: %v", entries)
	}
}

func TestBuildShowcaseRejectsRankDrift(t *testing.T) {
	questions, documents := questionFixture(15)
	fixed := fixtureIndex("fixed", fixedChunks(documents), "manifest")
	structure := fixtureIndex("structure", structureChunks(documents), "manifest")
	comparison := Comparison{
		Revision: "fixture", Started: "2026-09-29T00:00:00+04:00", Model: defaultModel,
		ManifestSHA256: "manifest", QuestionsSHA256: "questions", Questions: make([]QuestionResult, len(questions)),
	}
	for i, question := range questions {
		comparison.Questions[i] = QuestionResult{ID: question.ID, Source: question.Source}
	}
	comparison.Questions[0].FixedRank = 10
	_, err := buildShowcase(context.Background(), "abc1234", defaultModel, "manifest", "questions", Manifest{Files: make([]ManifestFile, len(documents))}, documents, questions, fixed, structure, comparison, "fixed.json", "structure.json", staticEmbedder{vector: []float64{1, 0, 0, 0, 0, 0, 0, 0}}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "расходится с compare.json") {
		t.Fatalf("ошибка=%v", err)
	}
}
