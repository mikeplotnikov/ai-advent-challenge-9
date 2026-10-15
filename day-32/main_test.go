package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

func init() { retryDelays = []time.Duration{0} }

// gitRepo makes a repository with a base commit and a head commit on top of it.
func gitRepo(t *testing.T, base, head map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(files map[string]string) {
		for name, text := range files {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if text == "" {
				os.Remove(path)
				continue
			}
			os.MkdirAll(filepath.Dir(path), 0o755)
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	run("init", "-q", "-b", "main")
	write(base)
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	run("tag", "base")
	write(head)
	run("add", "-A")
	run("commit", "-q", "-m", "head")
	return dir
}

func TestCollectReadsEveryKindOfChange(t *testing.T) {
	long := strings.Repeat("x", maxFileRunes+1) + "\n"
	repo := gitRepo(t,
		map[string]string{"a.go": "package a\n\nfunc A() {}\n", "gone.txt": "bye\n", "old.md": "same text that survives the rename\nline two\nline three\n", "big.txt": "small\n"},
		map[string]string{"a.go": "package a\n\nfunc A() {}\n\nfunc B() {}\n", "gone.txt": "", "old.md": "", "new.md": "same text that survives the rename\nline two\nline three\n", "img.bin": "\x00\x01\x02", "big.txt": long})
	pr, err := collect(repo, "base", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Base) != 40 || len(pr.Head) != 40 {
		t.Fatalf("refs not resolved to SHAs: %q %q", pr.Base, pr.Head)
	}
	byPath := map[string]change{}
	for _, c := range pr.Changes {
		byPath[c.Path] = c
	}
	if c := byPath["a.go"]; c.Status != "M" || c.Lines != 5 || !strings.Contains(c.Diff, "    5 +func B() {}") || !strings.Contains(c.Content, "    5| func B() {}") {
		t.Fatalf("modified file: %+v", c)
	}
	if c := byPath["gone.txt"]; c.Status != "D" || c.Content != "" || !strings.Contains(c.Diff, "-bye") {
		t.Fatalf("deleted file: %+v", c)
	}
	if c := byPath["new.md"]; c.Status != "R" || c.OldPath != "old.md" {
		t.Fatalf("renamed file: %+v", c)
	}
	if c := byPath["img.bin"]; !c.Binary || c.Diff != "" {
		t.Fatalf("binary file: %+v", c)
	}
	if c := byPath["big.txt"]; c.Content != "" || c.Diff == "" {
		t.Fatalf("over-long file must keep its diff and drop its content: %+v", c)
	}
	omitted := strings.Join(pr.Omitted, "\n")
	for _, want := range []string{"img.bin: двоичный", "big.txt: файл целиком не передан"} {
		if !strings.Contains(omitted, want) {
			t.Fatalf("Omitted lacks %q:\n%s", want, omitted)
		}
	}
}

func TestCollectRejectsAnEmptyRangeAndABadRef(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.txt": "a\n"}, map[string]string{"b.txt": "b\n"})
	if _, err := collect(repo, "HEAD", "HEAD"); err == nil || !strings.Contains(err.Error(), "нет изменений") {
		t.Fatalf("empty range: %v", err)
	}
	if _, err := collect(repo, "--output=/tmp/x", "HEAD"); err == nil {
		t.Fatal("an option-looking ref must be rejected")
	}
}

func TestAnnotateNumbersNewLinesOnly(t *testing.T) {
	got := annotate("@@ -10,3 +20,3 @@ func x() {\n ctx\n-old\n+new\n ctx2\n")
	want := "@@ -10,3 +20,3 @@ func x() {\n   20  ctx\n      -old\n   21 +new\n   22  ctx2\n"
	if got != want {
		t.Fatalf("annotate:\n%q\nwant\n%q", got, want)
	}
	if hunkStart("@@ -1 +7 @@") != 7 || hunkStart("garbage") != 0 {
		t.Fatal("hunkStart")
	}
}

func TestChunkGoMakesOneChunkPerDeclarationWithoutImports(t *testing.T) {
	src := "package p\n\nimport \"fmt\"\n\n// Hello greets.\nfunc Hello() { fmt.Println(1) }\n\ntype T struct{}\n\nfunc (t *T) M() {}\n"
	chunks, err := chunkGo("p/p.go", src)
	if err != nil {
		t.Fatal(err)
	}
	var sections []string
	for _, c := range chunks {
		sections = append(sections, c.Section)
	}
	if got := strings.Join(sections, "|"); got != "package p · func Hello|package p · type T|package p · func (*T) M" {
		t.Fatalf("sections: %s", got)
	}
	if !strings.HasPrefix(chunks[0].Text, "// Hello greets.") || chunks[0].Start != 5 || chunks[0].End != 6 {
		t.Fatalf("doc comment and lines: %+v", chunks[0])
	}
}

func TestChunkGoSplitsLongDeclarationsAndWindowsBrokenFiles(t *testing.T) {
	body := "package p\n\nfunc Long() {\n" + strings.Repeat("\tx := 1 // "+strings.Repeat("y", 60)+"\n", 80) + "}\n"
	chunks, err := chunkGo("p/l.go", body)
	if err != nil || len(chunks) < 2 {
		t.Fatalf("long declaration: %d chunks, %v", len(chunks), err)
	}
	for _, c := range chunks {
		if c.Runes > maxCodeRunes {
			t.Fatalf("part of %d runes exceeds %d", c.Runes, maxCodeRunes)
		}
	}
	if chunks[1].Start != chunks[0].End+1 {
		t.Fatalf("parts must be contiguous: %d..%d then %d", chunks[0].Start, chunks[0].End, chunks[1].Start)
	}
	broken, err := chunkGo("p/b.go", strings.Repeat("not go\n", 130))
	if err == nil || len(broken) != 3 || broken[2].Start != 121 {
		t.Fatalf("broken file: %d chunks, %v", len(broken), err)
	}
}

func TestCodeSourcesTakesInternalAndTouchedPackagesButNotChangedFiles(t *testing.T) {
	repo := t.TempDir()
	for _, f := range []string{"internal/a/a.go", "internal/a/a_test.go", "internal/b/deep/b.go", "day-40/main.go", "day-40/sub/x.go", "day-41/main.go"} {
		os.MkdirAll(filepath.Join(repo, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(repo, f), []byte("package x\n"), 0o644)
	}
	os.WriteFile(filepath.Join(repo, "day-40", "util.go"), []byte("package main\n"), 0o644)
	os.Symlink(filepath.Join(repo, "internal/a/a.go"), filepath.Join(repo, "internal/a/link.go"))
	got, err := codeSources(repo, pullRequest{Changes: []change{{Status: "M", Path: "day-40/main.go"}, {Status: "M", Path: "internal/a/a.go"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "day-40/util.go,internal/b/deep/b.go" {
		t.Fatalf("sources: %v", got)
	}
}

type fakeEmbedder struct {
	calls int
	err   error
}

// Embed maps text onto three axes by keyword, so similarity is predictable.
func (f *fakeEmbedder) Embed(_ context.Context, _ string, input string) ([]float64, int, bool, error) {
	f.calls++
	if f.err != nil {
		return nil, 0, false, f.err
	}
	v := []float64{0.01, 0.01, 0.01}
	for i, word := range []string{"правила", "cache", "parser"} {
		if strings.Contains(strings.ToLower(input), word) {
			v[i] = 1
		}
	}
	return v, 1, false, nil
}

func TestEmbeddingCacheServesRepeatsAndForgetsWhatIsNoLongerUsed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c", "emb.json")
	pieces := func(texts ...string) []piece {
		var out []piece
		for _, text := range texts {
			out = append(out, piece{Chunk: rag.Chunk{ChunkID: text, Text: text}})
		}
		return out
	}
	emb := &fakeEmbedder{}
	cache, _ := loadCache(path, "m")
	if s, err := embedAll(context.Background(), emb, cache, pieces("one", "two")); err != nil || s.Embedded != 2 {
		t.Fatalf("first run: %+v %v", s, err)
	}
	if err := cache.save(); err != nil {
		t.Fatal(err)
	}
	cache, _ = loadCache(path, "m")
	s, err := embedAll(context.Background(), emb, cache, pieces("two", "three"))
	if err != nil || s.FromCache != 1 || s.Embedded != 1 || emb.calls != 3 {
		t.Fatalf("second run: %+v calls=%d %v", s, emb.calls, err)
	}
	cache.save()
	cache, _ = loadCache(path, "m")
	if len(cache.vectors) != 2 {
		t.Fatalf("unused vector must be pruned, have %d", len(cache.vectors))
	}
	os.WriteFile(path, []byte("{broken"), 0o600)
	if cache, err := loadCache(path, "m"); err != nil || len(cache.vectors) != 0 {
		t.Fatalf("broken cache must read as empty: %v", err)
	}
}

func TestRetrieveKeepsPinnedRulesAndCapsTotals(t *testing.T) {
	var pieces []piece
	pieces = append(pieces, piece{Kind: kindDoc, Chunk: rag.Chunk{ChunkID: "rules", Source: "docs/review-guide.md", Text: "правила"}})
	for i := 0; i < 10; i++ {
		pieces = append(pieces, piece{Kind: kindDoc, Chunk: rag.Chunk{ChunkID: "doc" + string(rune('a'+i)), Text: "cache"}})
		pieces = append(pieces, piece{Kind: kindCode, Chunk: rag.Chunk{ChunkID: "code" + string(rune('a'+i)), Text: "cache"}})
	}
	emb := &fakeEmbedder{}
	if _, err := embedAll(context.Background(), emb, &embeddingCache{vectors: map[string][]float64{}, used: map[string]bool{}}, pieces); err != nil {
		t.Fatal(err)
	}
	qs := []query{{Label: "rules", Text: "правила", Docs: 1, Pinned: true}}
	for i := 0; i < 5; i++ {
		qs = append(qs, query{Label: "f", Text: "cache " + string(rune('a'+i)), Docs: 2, Code: 3})
	}
	got, err := retrieve(context.Background(), vectorScorer(emb, pieces), pieces, qs)
	if err != nil {
		t.Fatal(err)
	}
	docs, code := 0, 0
	for _, s := range got {
		if s.Kind == kindDoc {
			docs++
		} else {
			code++
		}
	}
	if got[0].ID != "D1" || got[0].Chunk.ChunkID != "rules" {
		t.Fatalf("pinned rules must come first: %+v", got[0])
	}
	if docs > maxDocs || code > maxCode {
		t.Fatalf("caps exceeded: docs=%d code=%d", docs, code)
	}
}

func TestLexicalScorerFindsTheIdentifierTheDiffNames(t *testing.T) {
	pieces := []piece{
		{Kind: kindCode, Chunk: rag.Chunk{ChunkID: "x", Text: "func unrelated() { return }"}},
		{Kind: kindCode, Chunk: rag.Chunk{ChunkID: "y", Text: "func loadCache(path string) error"}},
	}
	ranked, err := lexicalScorer(pieces)(context.Background(), query{Text: "+ c, err := loadCache(p)"})
	if err != nil || pieces[ranked[0].Index].Chunk.ChunkID != "y" || ranked[1].Score != 0 {
		t.Fatalf("lexical: %+v %v", ranked, err)
	}
}

// scriptedModel answers from a list; each entry is a reply or an error.
type scriptedModel struct {
	replies []any
	got     [][]llm.Message
}

func (m *scriptedModel) AskWith(_ context.Context, messages []llm.Message, opts llm.Options) (llm.Answer, error) {
	m.got = append(m.got, messages)
	if opts.ResponseFormat != "json_object" || opts.Temperature == nil {
		return llm.Answer{}, errors.New("request lost its format or temperature")
	}
	next := m.replies[0]
	m.replies = m.replies[1:]
	if err, ok := next.(error); ok {
		return llm.Answer{Model: "deepseek-flash", Usage: llm.Usage{PromptTokens: 5}}, err
	}
	return llm.Answer{Content: next.(string), Model: "deepseek-flash", Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 10}}, nil
}

var testPR = pullRequest{Changes: []change{{Status: "M", Path: "a.go", Lines: 10}, {Status: "D", Path: "gone.go"}}}
var testSources = []source{{ID: "D1", Kind: kindDoc}, {ID: "C1", Kind: kindCode}}

const goodAnswer = `{"summary":"ok","bugs":[{"file":"a.go","line":3,"title":"t","detail":"d","sources":["diff","C1"]}],"architecture":[],"recommendations":[{"file":"gone.go","line":0,"title":"r","detail":"","sources":["D1"]}]}`

func TestReviewAcceptsAValidAnswerInOneCall(t *testing.T) {
	m := &scriptedModel{replies: []any{goodAnswer}}
	v, stats, err := review(context.Background(), m, testPR, testSources)
	if err != nil || stats.Calls != 1 || stats.Repaired || len(v.Bugs) != 1 || len(v.Recommendations) != 1 {
		t.Fatalf("v=%+v stats=%+v err=%v", v, stats, err)
	}
	if !stats.Priced || stats.CostUSD <= 0 {
		t.Fatalf("cost must be priced: %+v", stats)
	}
}

func TestReviewRepairsOnceThenDropsWhatStillFails(t *testing.T) {
	bad := `{"summary":"s","bugs":[{"file":"a.go","line":99,"title":"t","detail":"d","sources":["diff"]},{"file":"a.go","line":1,"title":"ok","detail":"","sources":["diff"]}],"architecture":[{"file":"elsewhere.go","line":1,"title":"t","sources":["diff"]}],"recommendations":[{"file":"a.go","line":1,"title":"t","sources":["C9"]}]}`
	m := &scriptedModel{replies: []any{bad, bad}}
	v, stats, err := review(context.Background(), m, testPR, testSources)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Repaired || stats.Calls != 2 || len(v.Bugs) != 1 || v.Bugs[0].Title != "ok" || len(v.Architecture) != 0 || len(v.Recommendations) != 0 {
		t.Fatalf("v=%+v stats=%+v", v, stats)
	}
	dropped := strings.Join(stats.Dropped, "\n")
	for _, want := range []string{"строки 99", `"elsewhere.go" не входит`, `"C9" не было`} {
		if !strings.Contains(dropped, want) {
			t.Fatalf("Dropped lacks %q:\n%s", want, dropped)
		}
	}
	repairTurn := m.got[1][len(m.got[1])-1].Content
	if !strings.Contains(repairTurn, "строки 99") {
		t.Fatalf("repair turn must list the problems: %s", repairTurn)
	}
}

func TestReviewFailsWhenTheAnswerNeverParses(t *testing.T) {
	m := &scriptedModel{replies: []any{"not json", llm.ErrEmptyContent}}
	if _, _, err := review(context.Background(), m, testPR, testSources); err == nil || !strings.Contains(err.Error(), "дважды") {
		t.Fatalf("err=%v", err)
	}
}

func TestAskRetriesTransientFailuresButNotARejectedRequest(t *testing.T) {
	m := &scriptedModel{replies: []any{errors.New("dial tcp: timeout"), errors.New("API вернул 429 Too Many Requests: slow down"), goodAnswer}}
	_, stats, err := review(context.Background(), m, testPR, testSources)
	if err != nil || stats.Calls != 3 || stats.Retries != 2 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	m = &scriptedModel{replies: []any{errors.New("API вернул 401 Unauthorized: bad key"), goodAnswer}}
	_, stats, err = review(context.Background(), m, testPR, testSources)
	if err == nil || stats.Calls != 1 {
		t.Fatalf("401 must not be retried: stats=%+v err=%v", stats, err)
	}
	m = &scriptedModel{replies: []any{errors.New("API вернул 500"), errors.New("API вернул 502"), errors.New("API вернул 503")}}
	if _, stats, err = review(context.Background(), m, testPR, testSources); err == nil || stats.Calls != maxAttempts {
		t.Fatalf("must give up after %d: stats=%+v err=%v", maxAttempts, stats, err)
	}
}

func TestRenderNeutralisesModelText(t *testing.T) {
	v := verdict{Summary: "ping @owner <!-- day-32-ai-review -->", Bugs: []finding{{File: "a.go", Line: 2, Title: "multi\nline", Detail: "x", Sources: []string{"diff"}}}}
	body := renderReview(testPR, v, nil, metrics{Started: time.Now(), Degraded: "Ollama недоступна"})
	if strings.Count(body, marker) != 1 {
		t.Fatalf("model text forged the marker:\n%s", body)
	}
	if strings.Contains(body, "@owner") || !strings.Contains(body, "`a.go:2` — **multi line**") {
		t.Fatalf("mention or line break survived:\n%s", body)
	}
	if !strings.Contains(body, "Деградация") || !strings.Contains(body, "Архитектурные проблемы\n\nНе найдено.") {
		t.Fatalf("degradation banner or empty section missing:\n%s", body)
	}
}

func TestReviewPRFallsBackToLexicalSearchWhenOllamaIsDown(t *testing.T) {
	repo := gitRepo(t,
		map[string]string{"README.md": "# Проект\n\n## Правила\n\nправила ревью\n", "internal/x/x.go": "package x\n\nfunc LoadCache() {}\n"},
		map[string]string{"day-50/main.go": "package main\n\nfunc main() { LoadCache() }\n"})
	cfg := config{repo: repo, base: "base", head: "HEAD", cache: filepath.Join(t.TempDir(), "e.json")}
	m := &scriptedModel{replies: []any{`{"summary":"s","bugs":[],"architecture":[],"recommendations":[]}`}}
	body, err := reviewPR(context.Background(), cfg, m, &fakeEmbedder{err: errors.New("Ollama недоступна: запустите `ollama serve`")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Деградация") || !strings.Contains(body, "поиск по словам") || !strings.Contains(body, "internal/x/x.go") {
		t.Fatalf("degraded review:\n%s", body)
	}
}

func TestReviewPRTurnsAModelFailureIntoAFailureComment(t *testing.T) {
	repo := gitRepo(t, map[string]string{"README.md": "# P\n"}, map[string]string{"a.go": "package a\n"})
	cfg := config{repo: repo, base: "base", head: "HEAD", cache: filepath.Join(t.TempDir(), "e.json")}
	m := &scriptedModel{replies: []any{errors.New("API вернул 402 Payment Required: balance")}}
	body, err := reviewPR(context.Background(), cfg, m, &fakeEmbedder{})
	if err == nil || !strings.HasPrefix(body, marker) || !strings.Contains(body, "Ревью не выполнено") || !strings.Contains(body, "402") {
		t.Fatalf("err=%v body:\n%s", err, body)
	}
}

// fakeGitHub keeps issue comments in memory and answers like the REST API.
func fakeGitHub(t *testing.T, comments []issueComment, saveBody func(issueComment) issueComment) (*httptest.Server, *[]string) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(comments)
		case http.MethodPost, http.MethodPatch:
			raw, _ := io.ReadAll(r.Body)
			var in struct{ Body string }
			json.Unmarshal(raw, &in)
			json.NewEncoder(w).Encode(saveBody(issueComment{ID: 77, Body: in.Body, HTMLURL: "https://github.com/o/r/pull/5#issuecomment-77"}))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestUpsertEditsOnlyItsOwnComment(t *testing.T) {
	own := issueComment{ID: 42, Body: marker + "\nold"}
	own.User.Login = botLogin
	forged := issueComment{ID: 13, Body: marker + "\nby a person"}
	forged.User.Login = "someone"
	echo := func(c issueComment) issueComment { return c }

	srv, calls := fakeGitHub(t, []issueComment{forged, own}, echo)
	url, err := github{API: srv.URL, Token: "tok", Repo: "o/r"}.upsert(context.Background(), 5, marker+"\nnew")
	if err != nil || url == "" || (*calls)[1] != "PATCH /repos/o/r/issues/comments/42" {
		t.Fatalf("url=%q err=%v calls=%v", url, err, *calls)
	}

	srv, calls = fakeGitHub(t, []issueComment{forged}, echo)
	if _, err := (github{API: srv.URL, Token: "tok", Repo: "o/r"}).upsert(context.Background(), 5, marker+"\nnew"); err != nil || (*calls)[1] != "POST /repos/o/r/issues/5/comments" {
		t.Fatalf("err=%v calls=%v", err, *calls)
	}
}

func TestUpsertTrustsTheResponseBodyNotTheStatus(t *testing.T) {
	srv, _ := fakeGitHub(t, nil, func(issueComment) issueComment { return issueComment{} })
	if _, err := (github{API: srv.URL, Token: "tok", Repo: "o/r"}).upsert(context.Background(), 5, marker); err == nil || !strings.Contains(err.Error(), "не подтвердил") {
		t.Fatalf("an empty 200 must not count as posted: %v", err)
	}
	srv, _ = fakeGitHub(t, nil, func(c issueComment) issueComment { return c })
	if _, err := (github{API: srv.URL, Token: "bad", Repo: "o/r"}).upsert(context.Background(), 5, marker); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("401 must surface: %v", err)
	}
}

func TestRunRefusesToPostWithoutTheRunnerEnvironment(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	err := run(context.Background(), config{post: true})
	if err == nil || !strings.Contains(err.Error(), "PR_NUMBER") {
		t.Fatalf("err=%v", err)
	}
}

func TestUserMessageCarriesEverythingTheModelReviews(t *testing.T) {
	pr := pullRequest{
		Changes: []change{
			{Status: "M", Path: "a.go", Diff: "@@ -1 +1 @@\n    1 +new line\n", Content: "    1| new line\n", Lines: 1},
			{Status: "R", Path: "new.md", OldPath: "old.md"},
			{Status: "A", Path: "img.bin", Binary: true},
		},
		Omitted: []string{"img.bin: двоичный файл"},
	}
	sources := []source{
		{ID: "D1", Kind: kindDoc, Chunk: rag.Chunk{Source: "docs/review-guide.md", Section: "Правила", Text: "rule text"}},
		{ID: "C1", Kind: kindCode, Chunk: rag.Chunk{Source: "internal/x/x.go", Section: "func X", Start: 3, End: 9, Text: "func X() {}"}},
	}
	msg := userMessage(pr, sources)
	for _, want := range []string{
		"- M a.go", "- R new.md (было old.md)", "- A img.bin — двоичный",
		"Не передано целиком:\n- img.bin: двоичный файл",
		"### [D1] docs/review-guide.md — Правила\n```\nrule text\n```",
		"### [C1] internal/x/x.go:3-9 — func X\n```\nfunc X() {}\n```",
		"### a.go\n```diff\n@@ -1 +1 @@\n    1 +new line\n```",
		"### a.go\n```\n    1| new line\n```",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("user message lacks %q:\n%s", want, msg)
		}
	}
}

func TestReviewKeepsTheRepairedAnswerAndShowsTheModelItsOwnReply(t *testing.T) {
	bad := `{"summary":"s","bugs":[{"file":"a.go","line":99,"title":"t","detail":"d","sources":["diff"]}],"architecture":[],"recommendations":[]}`
	fixed := `{"summary":"s2","bugs":[{"file":"a.go","line":10,"title":"fixed","detail":"d","sources":["diff"]}],"architecture":[],"recommendations":[]}`
	m := &scriptedModel{replies: []any{bad, fixed}}
	v, stats, err := review(context.Background(), m, testPR, testSources)
	if err != nil || !stats.Repaired || len(stats.Dropped) != 0 || v.Summary != "s2" || len(v.Bugs) != 1 || v.Bugs[0].Title != "fixed" {
		t.Fatalf("v=%+v stats=%+v err=%v", v, stats, err)
	}
	turns := m.got[1]
	if len(turns) != 4 || turns[2].Role != "assistant" || turns[2].Content != bad || turns[3].Role != "user" {
		t.Fatalf("repair conversation: %+v", turns)
	}
}

func TestCheckFindingBoundaries(t *testing.T) {
	known := map[string]bool{"diff": true}
	ok := finding{File: "a.go", Line: 10, Title: "t", Sources: []string{"diff"}}
	if p := checkFinding(ok, testPR, known); p != "" {
		t.Fatalf("the last line must be accepted: %s", p)
	}
	for name, f := range map[string]finding{
		"past end":   {File: "a.go", Line: 11, Title: "t", Sources: []string{"diff"}},
		"negative":   {File: "a.go", Line: -1, Title: "t", Sources: []string{"diff"}},
		"no title":   {File: "a.go", Line: 1, Title: " ", Sources: []string{"diff"}},
		"no sources": {File: "a.go", Line: 1, Title: "t"},
		"deleted":    {File: "gone.go", Line: 1, Title: "t", Sources: []string{"diff"}},
	} {
		if checkFinding(f, testPR, known) == "" {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

func TestCollectCountsALastLineWithoutNewlineAndNonASCIIPaths(t *testing.T) {
	repo := gitRepo(t, map[string]string{"док/файл.go": "a\n"}, map[string]string{"док/файл.go": "a\nb\nc"})
	pr, err := collect(repo, "base", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	c := pr.Changes[0]
	if c.Path != "док/файл.go" || c.Lines != 3 || !strings.Contains(c.Diff, "    3 +c") || len(pr.Omitted) != 0 {
		t.Fatalf("change: %+v omitted: %v", c, pr.Omitted)
	}
}

func TestCollectSpendsTheTotalBudgetsAndSkipsRenamedBinaries(t *testing.T) {
	base := map[string]string{"pic.bin": "\x00\x01\x02\x03"}
	head := map[string]string{"pic.bin": "", "art/pic.bin": "\x00\x01\x02\x03"}
	for i := 0; i < 4; i++ {
		head[string(rune('a'+i))+".txt"] = strings.Repeat("строка\n", 2500) // 17500 runes each
	}
	repo := gitRepo(t, base, head)
	pr, err := collect(repo, "base", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	withContent := 0
	for _, c := range pr.Changes {
		if c.Path == "art/pic.bin" && (!c.Binary || c.Content != "") {
			t.Fatalf("renamed binary: %+v", c)
		}
		if c.Content != "" {
			withContent++
		}
	}
	if withContent != 3 {
		t.Fatalf("content budget %d must fit three files of 17500, got %d", maxContentRunes, withContent)
	}
	omitted := strings.Join(pr.Omitted, "\n")
	if !strings.Contains(omitted, "d.txt: файл целиком не передан — превышен общий лимит") || !strings.Contains(omitted, "d.txt: diff не передан") {
		t.Fatalf("budget notes:\n%s", omitted)
	}
}

func TestQueriesAlwaysAskForTheRulesFirst(t *testing.T) {
	qs := queries(pullRequest{Changes: []change{
		{Path: "a.go", Diff: "@@ -1 +1 @@\n      -old\n    1 +" + strings.Repeat("x", 3000) + "\n"},
		{Path: "b.bin", Binary: true},
	}})
	if len(qs) != 2 || !qs[0].Pinned || qs[0].Text != rulesQuery || qs[0].Code != 0 {
		t.Fatalf("queries: %+v", qs)
	}
	q := qs[1]
	if q.Pinned || q.Docs != docsPerQuery || q.Code != codePerQuery || !strings.Contains(q.Text, "-old") || len([]rune(q.Text)) != queryRunes {
		t.Fatalf("file query: pinned=%v docs=%d code=%d len=%d", q.Pinned, q.Docs, q.Code, len([]rune(q.Text)))
	}
}

func TestRetrieveNumbersSourcesAndHonoursPerQueryLimits(t *testing.T) {
	var pieces []piece
	for i := 0; i < 5; i++ {
		pieces = append(pieces, piece{Kind: kindCode, Chunk: rag.Chunk{ChunkID: "c" + string(rune('0'+i))}})
		pieces = append(pieces, piece{Kind: kindDoc, Chunk: rag.Chunk{ChunkID: "d" + string(rune('0'+i))}})
	}
	// Every piece scores the same for every query, so only the limits decide.
	flat := func(context.Context, query) ([]scored, error) {
		out := make([]scored, len(pieces))
		for i := range pieces {
			out[i] = scored{Index: i, Score: float64(len(pieces) - i)}
		}
		return out, nil
	}
	got, err := retrieve(context.Background(), flat, pieces, []query{{Docs: 1, Code: 2}})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID+"="+s.Chunk.ChunkID)
	}
	if strings.Join(ids, ",") != "C1=c0,D1=d0,C2=c1" {
		t.Fatalf("ids: %v", ids)
	}
}

// flakyEmbedder works for the first n calls, then fails like a stopped Ollama.
type flakyEmbedder struct {
	fakeEmbedder
	n int
}

func (f *flakyEmbedder) Embed(ctx context.Context, id string, input string) ([]float64, int, bool, error) {
	if f.n == 0 {
		return nil, 0, false, errors.New("Ollama недоступна")
	}
	f.n--
	return f.fakeEmbedder.Embed(ctx, id, input)
}

func TestReviewPRFallsBackWhenOllamaDiesAfterIndexing(t *testing.T) {
	repo := gitRepo(t,
		map[string]string{"README.md": "# P\n\n## Правила\n\nправила ревью\n", "internal/x/x.go": "package x\n\nfunc LoadCache() {}\n"},
		map[string]string{"day-50/main.go": "package main\n\nfunc main() { LoadCache() }\n"})
	cfg := config{repo: repo, base: "base", head: "HEAD", cache: filepath.Join(t.TempDir(), "e.json")}
	m := &scriptedModel{replies: []any{`{"summary":"s","bugs":[],"architecture":[],"recommendations":[]}`}}
	// README makes one or two chunks and x.go one: three embeddings, then the queries fail.
	corpus, _ := buildCorpus(repo, pullRequest{Changes: []change{{Path: "day-50/main.go"}}})
	body, err := reviewPR(context.Background(), cfg, m, &flakyEmbedder{n: len(corpus.Pieces)})
	if err != nil || !strings.Contains(body, "Деградация") {
		t.Fatalf("err=%v body:\n%s", err, body)
	}
}

func TestRunPostsTheFailureAndAppendsTheSummary(t *testing.T) {
	repo := gitRepo(t, map[string]string{"README.md": "# P\n"}, map[string]string{"a.go": "package a\n"})
	deepseek := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no balance", http.StatusPaymentRequired)
	}))
	defer deepseek.Close()
	var posted string
	gh, _ := fakeGitHub(t, nil, func(c issueComment) issueComment { posted = c.Body; return c })
	summary := filepath.Join(t.TempDir(), "summary.md")
	os.WriteFile(summary, []byte("earlier step\n"), 0o644)
	out := filepath.Join(t.TempDir(), "review.md")
	t.Setenv("DEEPSEEK_API_URL", deepseek.URL)
	t.Setenv(keyEnv, "test-key")
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	t.Setenv("PR_NUMBER", "5")
	t.Setenv("GITHUB_API_URL", gh.URL)
	err := run(context.Background(), config{repo: repo, base: "base", head: "HEAD", out: out, summary: summary,
		post: true, ollama: "http://127.0.0.1:1", cache: filepath.Join(t.TempDir(), "e.json")})
	if err == nil || !strings.Contains(err.Error(), "402") {
		t.Fatalf("a failed review must fail the job: %v", err)
	}
	if !strings.Contains(posted, "Ревью не выполнено") {
		t.Fatalf("failure comment not posted: %q", posted)
	}
	file, _ := os.ReadFile(out)
	sum, _ := os.ReadFile(summary)
	if string(file) != posted || !strings.HasPrefix(string(sum), "earlier step\n") || !strings.Contains(string(sum), "Ревью не выполнено") {
		t.Fatalf("outputs: file=%q summary=%q", file, sum)
	}
}

func TestRunPostsAFailureWhenTheKeyIsMissing(t *testing.T) {
	var posted string
	gh, _ := fakeGitHub(t, nil, func(c issueComment) issueComment { posted = c.Body; return c })
	t.Setenv(keyEnv, "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	t.Setenv("PR_NUMBER", "5")
	t.Setenv("GITHUB_API_URL", gh.URL)
	err := run(context.Background(), config{repo: t.TempDir(), base: "b", head: "h", out: filepath.Join(t.TempDir(), "r.md"), post: true})
	if err == nil || !strings.Contains(posted, "нет ключа DeepSeek") {
		t.Fatalf("err=%v posted=%q", err, posted)
	}
}

func TestFindOwnReadsEveryPageAndNeedsTheMarker(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		var list []issueComment
		if r.URL.Query().Get("page") == "1" {
			for i := 0; i < 100; i++ {
				c := issueComment{ID: int64(i + 1), Body: "other bot note"}
				c.User.Login = botLogin
				list = append(list, c)
			}
		} else {
			c := issueComment{ID: 500, Body: marker + "\nreview"}
			c.User.Login = botLogin
			list = append(list, c)
		}
		json.NewEncoder(w).Encode(list)
	}))
	defer srv.Close()
	id, err := github{API: srv.URL, Token: "tok", Repo: "o/r"}.findOwn(context.Background(), 5)
	if err != nil || id != 500 || strings.Join(pages, ",") != "1,2" {
		t.Fatalf("id=%d pages=%v err=%v", id, pages, err)
	}
}

func TestGitHubRetriesAServerErrorButNotAClientError(t *testing.T) {
	calls := 0
	status := http.StatusBadGateway
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(status)
			return
		}
		w.Write([]byte("[]"))
	}))
	defer srv.Close()
	g := github{API: srv.URL, Token: "tok", Repo: "o/r"}
	if _, err := g.findOwn(context.Background(), 5); err != nil || calls != 2 {
		t.Fatalf("502 must be retried: calls=%d err=%v", calls, err)
	}
	calls, status = 0, http.StatusNotFound
	if _, err := g.findOwn(context.Background(), 5); err == nil || calls != 1 {
		t.Fatalf("404 must not be retried: calls=%d err=%v", calls, err)
	}
}

func TestUsageAndAttemptsAreCountedAcrossRetriesAndRepair(t *testing.T) {
	bad := `{"summary":"s","bugs":[{"file":"nope.go","line":1,"title":"t","sources":["diff"]}],"architecture":[],"recommendations":[]}`
	m := &scriptedModel{replies: []any{errors.New("API вернул 503"), bad, errors.New("dial tcp: reset"), errors.New("dial tcp: reset"), errors.New("dial tcp: reset")}}
	_, stats, err := review(context.Background(), m, testPR, testSources)
	if err == nil || !strings.Contains(err.Error(), "попыток: 3") {
		t.Fatalf("the repair call's own attempts must be reported: %v", err)
	}
	if stats.Calls != 5 || stats.Usage.PromptTokens != 5+100+5+5+5 {
		t.Fatalf("billed usage must be summed over every call: %+v", stats)
	}
}

func TestEmbeddingCacheKeyDependsOnTheModel(t *testing.T) {
	a := &embeddingCache{model: "bge-m3"}
	b := &embeddingCache{model: "other"}
	if a.key("x") == b.key("x") {
		t.Fatal("a new embedding model must not reuse old vectors")
	}
}

func TestRunStillPostsWhenALocalFileCannotBeWritten(t *testing.T) {
	var posted string
	gh, _ := fakeGitHub(t, nil, func(c issueComment) issueComment { posted = c.Body; return c })
	t.Setenv(keyEnv, "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	t.Setenv("PR_NUMBER", "5")
	t.Setenv("GITHUB_API_URL", gh.URL)
	out := filepath.Join(t.TempDir(), "missing-dir", "r.md")
	err := run(context.Background(), config{repo: t.TempDir(), base: "b", head: "h", out: out, post: true})
	if posted == "" || err == nil || !strings.Contains(err.Error(), "missing-dir") || !strings.Contains(err.Error(), "не задан") {
		t.Fatalf("posted=%q err=%v", posted, err)
	}
}

func TestRunFailsTheJobWhenThePostFails(t *testing.T) {
	gh, _ := fakeGitHub(t, nil, func(issueComment) issueComment { return issueComment{} })
	t.Setenv(keyEnv, "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	t.Setenv("PR_NUMBER", "5")
	t.Setenv("GITHUB_API_URL", gh.URL)
	err := run(context.Background(), config{repo: t.TempDir(), base: "b", head: "h", out: filepath.Join(t.TempDir(), "r.md"), post: true})
	if err == nil || !strings.Contains(err.Error(), "ревью не опубликовано") {
		t.Fatalf("err=%v", err)
	}
}

func TestReviewPRTurnsAnUnreadableDiffIntoAFailureComment(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.txt": "a\n"}, map[string]string{"b.txt": "b\n"})
	cfg := config{repo: repo, base: "no-such-ref", head: "HEAD", cache: filepath.Join(t.TempDir(), "e.json")}
	body, err := reviewPR(context.Background(), cfg, &scriptedModel{}, &fakeEmbedder{})
	if err == nil || !strings.HasPrefix(body, marker) || !strings.Contains(body, "diff не прочитан") {
		t.Fatalf("err=%v body:\n%s", err, body)
	}
}

func TestRenderFailureBoundsTheReason(t *testing.T) {
	body := renderFailure(pullRequest{Head: "abc"}, strings.Repeat("Ω", 5000), metrics{Started: time.Now()})
	if strings.Count(body, "Ω") != 800 {
		t.Fatalf("reason must be cut to 800 runes, got %d", strings.Count(body, "Ω"))
	}
}

func TestGitHubRetriesATransportFailureButNeverRepeatsAPost(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.Write([]byte(`{"id":1,"html_url":"u","body":"` + marker + `"}`))
	}))
	defer srv.Close()
	g := github{API: srv.URL, Token: "tok", Repo: "o/r"}
	if _, err := g.do(context.Background(), http.MethodPatch, srv.URL+"/x", []byte(`{}`)); err != nil || calls != 2 {
		t.Fatalf("a dropped PATCH must be retried: calls=%d err=%v", calls, err)
	}
	calls = 0
	if _, err := g.do(context.Background(), http.MethodPost, srv.URL+"/x", []byte(`{}`)); err == nil || calls != 1 {
		t.Fatalf("a POST must not be repeated: calls=%d err=%v", calls, err)
	}
	dead := 0
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dead++
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	defer down.Close()
	if _, err := g.do(context.Background(), http.MethodGet, down.URL+"/x", nil); err == nil || dead != maxAttempts {
		t.Fatalf("must give up after %d attempts: calls=%d err=%v", maxAttempts, dead, err)
	}
}

func TestUpsertChecksEveryFieldOfTheConfirmation(t *testing.T) {
	for name, reply := range map[string]issueComment{
		"no marker": {ID: 1, HTMLURL: "u", Body: "text"},
		"no id":     {HTMLURL: "u", Body: marker},
		"no url":    {ID: 1, Body: marker},
	} {
		srv, _ := fakeGitHub(t, nil, func(issueComment) issueComment { return reply })
		if _, err := (github{API: srv.URL, Token: "tok", Repo: "o/r"}).upsert(context.Background(), 5, marker); err == nil {
			t.Fatalf("%s must not count as posted", name)
		}
	}
}

func TestFindOwnIgnoresABotCommentThatOnlyQuotesTheMarker(t *testing.T) {
	quoting := issueComment{ID: 9, Body: "see " + marker}
	quoting.User.Login = botLogin
	srv, calls := fakeGitHub(t, []issueComment{quoting}, func(c issueComment) issueComment { return c })
	if _, err := (github{API: srv.URL, Token: "tok", Repo: "o/r"}).upsert(context.Background(), 5, marker+"\nnew"); err != nil || (*calls)[1] != "POST /repos/o/r/issues/5/comments" {
		t.Fatalf("err=%v calls=%v", err, *calls)
	}
}

func TestEveryUsageFieldAndTheCostAreSummed(t *testing.T) {
	var stats callStats
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	one := llm.Answer{Model: "deepseek-flash", Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, PromptCacheHitTokens: 64, PromptCacheMissTokens: 36}}
	addUsage(&stats, one, at)
	single := stats.CostUSD
	addUsage(&stats, one, at)
	u := stats.Usage
	if u.PromptTokens != 200 || u.CompletionTokens != 20 || u.TotalTokens != 220 || u.PromptCacheHitTokens != 128 || u.PromptCacheMissTokens != 72 {
		t.Fatalf("usage: %+v", u)
	}
	if single <= 0 || stats.CostUSD != 2*single {
		t.Fatalf("cost must add up: one=%v two=%v", single, stats.CostUSD)
	}
}

func TestWriteOutputsStripsControlCodesOnTheTerminal(t *testing.T) {
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err := writeOutputs(config{}, "title \x1b[2Jcleared\n")
	w.Close()
	os.Stdout = stdout
	got, _ := io.ReadAll(r)
	if err != nil || strings.Contains(string(got), "\x1b") || !strings.Contains(string(got), "cleared\n") {
		t.Fatalf("stdout=%q err=%v", got, err)
	}
}

func TestCollectNotesADiffItCouldNotMatch(t *testing.T) {
	repo := gitRepo(t, map[string]string{`q"uote.go`: "a\n"}, map[string]string{`q"uote.go`: "a\nb\n"})
	pr, err := collect(repo, "base", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(pr.Omitted, "\n"), `q"uote.go: diff не разобран`) {
		t.Fatalf("change=%+v omitted=%v", pr.Changes, pr.Omitted)
	}
}

func TestLoadCacheKeysByTheModelItIsGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.json")
	a, _ := loadCache(path, "bge-m3")
	b, _ := loadCache(path, "other")
	if a.key("x") == b.key("x") {
		t.Fatal("loadCache must keep the model in the key")
	}
}
