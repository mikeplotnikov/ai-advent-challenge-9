package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

func syntheticRun(t *testing.T, questionsPath string) Run {
	t.Helper()
	questions, sha, err := loadQuestions(questionsPath)
	if err != nil {
		t.Fatal(err)
	}
	run := Run{Meta: RunHeader{Commit: "abc", StartedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), RunPath: "day-22/run.json", RequestedModel: modelName, ResponseModels: []string{modelName}, Temperature: 0, MaxTokens: 600, K: 5, Repeats: 3, QuestionsSHA256: sha, IndexSHA256: "index", ManifestSHA256: "manifest", PromptsSHA256: promptsSHA256()}}
	for _, q := range questions {
		item := QuestionRun{Question: q}
		if q.Kind == "in_base" {
			item.Chunks = []FoundChunk{{Rank: 1, Source: q.Source, ChunkID: "c", Text: q.Evidence, EvidenceHit: true, SourceHit: true}}
		}
		for repeat := 1; repeat <= 3; repeat++ {
			no := Call{Mode: "norag", Repeat: repeat, Messages: noRAGMessages(q.Question), Model: modelName, Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}, CostKnown: true, CostUSD: 0.001, DurationMS: int64(10 + repeat)}
			ragCall := Call{Mode: "rag", Repeat: repeat, Messages: ragMessages(q.Question, item.Chunks), Model: modelName, Usage: llm.Usage{PromptTokens: 20, CompletionTokens: 3, TotalTokens: 23}, CostKnown: true, CostUSD: 0.002, DurationMS: int64(20 + repeat)}
			if q.Kind == "out_of_base" {
				no.Score = Score{Outcome: "declined", Success: true}
				ragCall.Score = Score{Outcome: "declined", Success: true}
			} else {
				no.Score = Score{Outcome: "wrong"}
				ragCall.Score = Score{Outcome: "correct", Success: true}
			}
			item.NoRAG = append(item.NoRAG, no)
			item.RAG = append(item.RAG, ragCall)
		}
		run.Questions = append(run.Questions, item)
	}
	return run
}

func TestReportIsDeterministicAndSharesTableRows(t *testing.T) {
	dir := t.TempDir()
	questionsPath := "eval/questions.json"
	runPath := filepath.Join(dir, "run.json")
	resultsPath := filepath.Join(dir, "RESULTS.md")
	showcasePath := filepath.Join(dir, "showcase.json")
	if err := writeRun(runPath, syntheticRun(t, questionsPath)); err != nil {
		t.Fatal(err)
	}
	if err := writeReport(runPath, questionsPath, resultsPath, showcasePath); err != nil {
		t.Fatal(err)
	}
	firstResults, _ := os.ReadFile(resultsPath)
	firstShowcase, _ := os.ReadFile(showcasePath)
	if err := writeReport(runPath, questionsPath, resultsPath, showcasePath); err != nil {
		t.Fatal(err)
	}
	secondResults, _ := os.ReadFile(resultsPath)
	secondShowcase, _ := os.ReadFile(showcasePath)
	if !bytes.Equal(firstResults, secondResults) || !bytes.Equal(firstShowcase, secondShowcase) {
		t.Fatal("report output is not byte-reproducible")
	}
	var showcase Showcase
	if err := json.Unmarshal(firstShowcase, &showcase); err != nil {
		t.Fatal(err)
	}
	for name, rows := range showcase.Results {
		if !strings.Contains(string(firstResults), "## "+name+"\n\n"+renderTable(rows)) {
			t.Fatalf("RESULTS.md does not use showcase rows for %s", name)
		}
	}
}

func TestReportStatisticsFromSyntheticRun(t *testing.T) {
	run := syntheticRun(t, "eval/questions.json")
	report := buildReportData(run).Sections
	wantMain := [][]string{
		{"Срез", "Без RAG", "С RAG"},
		{"Все вопросы", "2/10 (0.06–0.51)", "10/10 (0.72–1.00)"},
		{"in_base", "0/7 (0.00–0.35)", "7/7 (0.65–1.00)"},
		{"general", "0/1 (0.00–0.79)", "1/1 (0.21–1.00)"},
		{"out_of_base", "2/2 (0.34–1.00)", "2/2 (0.34–1.00)"},
	}
	if got := report["Успех по вопросам"]; !equalRows(got, wantMain) {
		t.Fatalf("main table=%v want=%v", got, wantMain)
	}
	if got := report["Макнемар"][1]; got[1] != "8" || got[2] != "0" || got[3] != "0.0078" || got[4] != "показано при α = 0,05" {
		t.Fatalf("McNemar row=%v", got)
	}
	wantOutcomes := [][]string{
		{"Исход", "Без RAG", "С RAG"}, {"correct", "0", "24"}, {"partial", "0", "0"},
		{"unknown", "0", "0"}, {"wrong", "24", "0"}, {"declined", "6", "6"},
		{"fabricated", "0", "0"}, {"empty", "0", "0"}, {"marker_with_facts", "0", "0"},
	}
	if got := report["Исходы ответов (N = 30 на режим)"]; !equalRows(got, wantOutcomes) {
		t.Fatalf("outcomes=%v", got)
	}
	search := report["Поиск"]
	if len(search) != 9 || !equalRows(search[len(search)-1:], [][]string{{"Итого (N = 7)", "evidence hit@5 7/7", "7/7", "7/7"}}) {
		t.Fatalf("search=%v", search)
	}
	wantEconomy := [][]string{{"Режим", "Вход", "Выход", "Кэш", "Цена", "Медиана времени"}, {"norag", "300", "60", "0", "$0.030000", "12 ms"}, {"rag", "600", "90", "0", "$0.060000", "22 ms"}}
	if got := report["Токены, цена и время"]; !equalRows(got, wantEconomy) {
		t.Fatalf("economy=%v", got)
	}
	wantCitations := [][]string{{"Метрика", "Значение"}, {"Все ссылки ведут на найденные чанки", "0/21"}, {"Есть ссылка на ожидаемый источник", "0/21"}, {"Факты без ссылок (uncited)", "0"}}
	if got := report["Ссылки RAG (7 in_base)"]; !equalRows(got, wantCitations) {
		t.Fatalf("citations=%v", got)
	}
	if got := report["По вопросам"]; len(got) != 11 || got[1][4] != "1" || got[1][5] != "wrong, wrong, wrong" || got[1][6] != "correct, correct, correct" {
		t.Fatalf("per-question table=%v", got)
	}
}

func equalRows(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

func TestReportRejectsQuestionHashDivergence(t *testing.T) {
	dir := t.TempDir()
	original, err := os.ReadFile("eval/questions.json")
	if err != nil {
		t.Fatal(err)
	}
	questionsPath := filepath.Join(dir, "questions.json")
	if err := os.WriteFile(questionsPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	run := syntheticRun(t, "eval/questions.json")
	runPath := filepath.Join(dir, "run.json")
	if err := writeRun(runPath, run); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(questionsPath, append(original, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	err = writeReport(runPath, questionsPath, filepath.Join(dir, "RESULTS.md"), filepath.Join(dir, "showcase.json"))
	if err == nil || !strings.Contains(err.Error(), "questions.json") {
		t.Fatalf("err=%v", err)
	}
}

func TestReportRejectsPromptHashDivergence(t *testing.T) {
	dir := t.TempDir()
	run := syntheticRun(t, "eval/questions.json")
	run.Meta.PromptsSHA256 = "wrong"
	runPath := filepath.Join(dir, "run.json")
	if err := writeRun(runPath, run); err != nil {
		t.Fatal(err)
	}
	err := writeReport(runPath, "eval/questions.json", filepath.Join(dir, "RESULTS.md"), filepath.Join(dir, "showcase.json"))
	if err == nil || !strings.Contains(err.Error(), "промптов") {
		t.Fatalf("err=%v", err)
	}
}

func TestReportHeaderUsesRunMetadata(t *testing.T) {
	dir := t.TempDir()
	run := syntheticRun(t, "eval/questions.json")
	run.Meta.Commit = "from-run"
	run.Meta.ResponseModels = []string{"provider-model"}
	run.Meta.RequestedModel = "requested-model"
	run.Meta.Temperature = 0.25
	run.Meta.MaxTokens = 321
	run.Meta.K = 4
	run.Meta.Repeats = 9
	run.Meta.IndexSHA256 = "index-from-run"
	run.Meta.ManifestSHA256 = "manifest-from-run"
	run.Meta.RunPath = "frozen/path/run.json"
	runPath := filepath.Join(dir, "run.json")
	resultsPath := filepath.Join(dir, "RESULTS.md")
	if err := writeRun(runPath, run); err != nil {
		t.Fatal(err)
	}
	if err := writeReport(runPath, "eval/questions.json", resultsPath, filepath.Join(dir, "showcase.json")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(resultsPath)
	for _, want := range []string{"`from-run`", "`provider-model`", "`requested-model`", "Температура: 0.25", "max_tokens: 321", "k: 4", "R: 9", "`index-from-run`", "`manifest-from-run`", "`frozen/path/run.json`"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("header lacks %q:\n%s", want, raw)
		}
	}
	if !strings.Contains(string(raw), "`from-run`") {
		t.Fatalf("header=%s", raw)
	}
}

func TestReportModeDoesNotNeedKeyOrOllama(t *testing.T) {
	dir := t.TempDir()
	runPath := filepath.Join(dir, "run.json")
	resultsPath := filepath.Join(dir, "RESULTS.md")
	showcasePath := filepath.Join(dir, "showcase.json")
	if err := writeRun(runPath, syntheticRun(t, "eval/questions.json")); err != nil {
		t.Fatal(err)
	}
	oldRun, oldQuestions, oldResults, oldShowcase := defaultRun, defaultQuestions, defaultResults, defaultShowcase
	defaultRun, defaultQuestions, defaultResults, defaultShowcase = runPath, "eval/questions.json", resultsPath, showcasePath
	defer func() {
		defaultRun, defaultQuestions, defaultResults, defaultShowcase = oldRun, oldQuestions, oldResults, oldShowcase
	}()
	t.Setenv("DEEPSEEK_API_KEY_DAY22", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-report"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestReportPairRollsBackWhenSecondRenameFails(t *testing.T) {
	dir := t.TempDir()
	resultsPath := filepath.Join(dir, "RESULTS.md")
	if err := os.WriteFile(resultsPath, []byte("old results"), 0600); err != nil {
		t.Fatal(err)
	}
	showcasePath := filepath.Join(dir, "showcase.json")
	if err := os.Mkdir(showcasePath, 0700); err != nil {
		t.Fatal(err)
	}
	err := writeReportPairAtomic(resultsPath, []byte("new results"), showcasePath, []byte("new showcase"))
	if err == nil {
		t.Fatal("expected the showcase rename to fail")
	}
	raw, readErr := os.ReadFile(resultsPath)
	if readErr != nil || string(raw) != "old results" {
		t.Fatalf("RESULTS.md was not rolled back: %q, %v", raw, readErr)
	}
}

func TestWriteReportRollsBackWhenShowcaseReplacementFails(t *testing.T) {
	dir := t.TempDir()
	runPath := filepath.Join(dir, "run.json")
	resultsPath := filepath.Join(dir, "RESULTS.md")
	showcasePath := filepath.Join(dir, "showcase.json")
	if err := writeRun(runPath, syntheticRun(t, "eval/questions.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultsPath, []byte("old results"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(showcasePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeReport(runPath, "eval/questions.json", resultsPath, showcasePath); err == nil {
		t.Fatal("expected writeReport to fail")
	}
	raw, err := os.ReadFile(resultsPath)
	if err != nil || string(raw) != "old results" {
		t.Fatalf("RESULTS.md was not rolled back: %q, %v", raw, err)
	}
}
