package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCLIAskNonDefaultThresholdsReachSelectionAndHeader(t *testing.T) {
	answer := Answer{Answer: "Ответ второго чанка.", Sources: []Source{{Source: "s2.md", Section: "S2", ChunkID: "c2", Quote: "Текст второго чанка."}}}
	raw, _ := encodeJSON(answer)
	client, bodies := testModel(t, []string{"query", `{"scores":[{"id":1,"score":2},{"id":2,"score":3},{"id":3,"score":3}]}`, string(raw)})
	t.Setenv("DEEPSEEK_API_KEY_DAY24", "test")
	t.Setenv("DEEPSEEK_API_URL", client.URL)
	t.Chdir(t.TempDir())
	pool := []Candidate{
		{Rank: 1, Similarity: .9, Source: "s1.md", Section: "S1", ChunkID: "c1", Text: "Текст первого чанка."},
		{Rank: 2, Similarity: .8, Source: "s2.md", Section: "S2", ChunkID: "c2", Text: "Текст второго чанка."},
		{Rank: 3, Similarity: .7, Source: "s3.md", Section: "S3", ChunkID: "c3", Text: "Текст третьего чанка."},
		{Rank: 4, Similarity: .59, Source: "s4.md", Section: "S4", ChunkID: "c4", Text: "Ниже нового порога."},
		{Rank: 5, Similarity: .5, Source: "s5.md", Section: "S5", ChunkID: "c5", Text: "Ниже нового порога тоже."},
		{Rank: 6, Similarity: .99, Source: "s6.md", Section: "S6", ChunkID: "c6", Text: "За пределами нового пула."},
	}
	search := &searchStub{pool: pool}
	original := openSearcherFn
	openSearcherFn = func(string) (Searcher, string, error) { return search, "test", nil }
	t.Cleanup(func() { openSearcherFn = original })
	var out, errout bytes.Buffer
	args := []string{"-ask", "Вопрос?", "-k-before", "5", "-cos", "0.6", "-min-score", "3", "-k-after", "1"}
	if code := runCLI(args, &out, &errout); code != 0 {
		t.Fatalf("exit=%d: %s", code, errout.String())
	}
	if search.k != 5 || search.query != "query" {
		t.Fatalf("search params k=%d query=%q", search.k, search.query)
	}
	if len(*bodies) != 3 {
		t.Fatalf("model calls=%d", len(*bodies))
	}
	rerankMessages := (*bodies)[1]["messages"].([]any)
	rerankContext := rerankMessages[1].(map[string]any)["content"].(string)
	if strings.Count(rerankContext, "source: ") != 3 || strings.Contains(rerankContext, "s4.md") || strings.Contains(rerankContext, "s6.md") {
		t.Fatalf("cos/pool filters not applied: %s", rerankContext)
	}
	messages := (*bodies)[2]["messages"].([]any)
	answerContext := messages[1].(map[string]any)["content"].(string)
	contextJSON := strings.TrimSuffix(strings.TrimPrefix(answerContext, "Фрагменты (JSON):\n"), "\nВопрос: Вопрос?")
	var context []struct {
		ChunkID string `json:"chunk_id"`
	}
	if e := json.Unmarshal([]byte(contextJSON), &context); e != nil {
		t.Fatal(e)
	}
	if len(context) != 1 || context[0].ChunkID != "c2" {
		t.Fatalf("rerank/top-K flags not applied: %s", contextJSON)
	}
	for _, part := range []string{
		"Top-5 → cosine ≥ 0.60 → rerank ≥ 3 → top-1",
		"1. cos=0.9000 score=2 score",
		"2. cos=0.8000 score=3 контекст #1",
		"3. cos=0.7000 score=3 top_k",
		"4. cos=0.5900 score=— cos",
	} {
		if !strings.Contains(out.String(), part) {
			t.Fatalf("missing %q in:\n%s", part, out.String())
		}
	}
	if strings.Contains(out.String(), "s6.md") {
		t.Fatal("outside-pool chunk printed")
	}
}

func TestPrintQuestionUnknownClarificationAndDistinctReason(t *testing.T) {
	for _, reason := range []string{"empty_context", "model_unknown"} {
		t.Run(reason, func(t *testing.T) {
			q := QuestionRun{Question: Question{Question: "Вопрос?", Kind: "in_base"}, Answer: Answer{Answer: "Не знаю.", Unknown: true, Clarification: "Уточните\x1b термин\u202e?", Sources: []Source{}}}
			q.Checks = checkAnswer(q.Question, q.Answer, reason)
			var out bytes.Buffer
			printQuestion(&out, q, evalParams)
			text := out.String()
			for _, part := range []string{"Ответ: Не знаю.", "Уточнение: Уточните? термин??", "Причина отказа: " + reason, "источники=not_applicable", "цитаты=not_applicable"} {
				if !strings.Contains(text, part) {
					t.Fatalf("missing %q in:\n%s", part, text)
				}
			}
			if strings.ContainsAny(text, "\x1b\u202e") {
				t.Fatal("untrusted clarification terminal control leaked")
			}
		})
	}
}
