// Command day-32 reviews a pull request: it reads the diff and the changed files from
// git, finds the relevant documentation and Go code of the project (RAG over local
// bge-m3 embeddings, with a lexical fallback), asks DeepSeek over REST for potential
// bugs, architectural problems and recommendations, and posts the review as a PR
// comment. The GitHub Actions workflow day-32-review.yml runs it on every PR.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

const (
	embedModel = "bge-m3"
	keyEnv     = "DEEPSEEK_API_KEY_DAY32"
)

type config struct {
	repo, base, head, out, summary, ollama, cache string
	post                                          bool
}

func main() {
	var cfg config
	flag.StringVar(&cfg.repo, "repo", "", "репозиторий (по умолчанию корень текущего git-репозитория)")
	flag.StringVar(&cfg.base, "base", "", "коммит базовой ветки PR (обязателен)")
	flag.StringVar(&cfg.head, "head", "", "последний коммит PR (обязателен)")
	flag.StringVar(&cfg.out, "out", "", "куда записать текст ревью (Markdown); по умолчанию stdout")
	flag.StringVar(&cfg.summary, "summary", os.Getenv("GITHUB_STEP_SUMMARY"), "сводка job (на раннере — GITHUB_STEP_SUMMARY)")
	flag.StringVar(&cfg.ollama, "ollama", "http://127.0.0.1:11434", "адрес Ollama для эмбеддингов")
	flag.StringVar(&cfg.cache, "cache", defaultCache(), "кэш эмбеддингов между запусками")
	flag.BoolVar(&cfg.post, "post", false, "опубликовать ревью комментарием в PR (GITHUB_TOKEN, GITHUB_REPOSITORY, PR_NUMBER)")
	flag.Parse()
	if cfg.base == "" || cfg.head == "" {
		fail(errors.New("нужны -base и -head: коммиты PR, например -base origin/main -head HEAD"))
	}
	if cfg.repo == "" {
		top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			fail(errors.New("не нашёл git-репозиторий: запустите из него или задайте -repo"))
		}
		cfg.repo = strings.TrimSpace(string(top))
	}
	llm.LoadDotEnv(".env")
	fail(run(context.Background(), cfg))
}

func defaultCache() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(".cache", "day-32", "embeddings.json")
	}
	return filepath.Join(dir, "ai-advent-day-32", "embeddings.json")
}

func run(ctx context.Context, cfg config) error {
	var gh *github
	var prNumber int
	if cfg.post {
		token, repo := os.Getenv("GITHUB_TOKEN"), os.Getenv("GITHUB_REPOSITORY")
		n, err := strconv.Atoi(os.Getenv("PR_NUMBER"))
		if token == "" || repo == "" || err != nil || n <= 0 {
			return errors.New("для -post нужны GITHUB_TOKEN, GITHUB_REPOSITORY и PR_NUMBER")
		}
		api := os.Getenv("GITHUB_API_URL")
		if api == "" {
			api = "https://api.github.com"
		}
		gh, prNumber = &github{API: api, Token: token, Repo: repo}, n
	}
	var body string
	var reviewErr error
	if model, err := llm.NewWithKeyEnv(keyEnv); err != nil {
		reviewErr = err
		body = renderFailure(pullRequest{Base: cfg.base, Head: cfg.head}, "нет ключа DeepSeek: "+err.Error(), metrics{Started: time.Now()})
	} else {
		body, reviewErr = reviewPR(ctx, cfg, model, &rag.OllamaClient{BaseURL: cfg.ollama, Model: embedModel})
	}
	// The comment goes out first: a local file that cannot be written must not cost
	// the PR its review.
	var postErr error
	if gh != nil {
		var url string
		if url, postErr = gh.upsert(ctx, prNumber, body); postErr == nil {
			fmt.Fprintln(os.Stderr, "Комментарий:", url)
		} else {
			postErr = fmt.Errorf("ревью не опубликовано: %w", postErr)
		}
	}
	writeErr := writeOutputs(cfg, body)
	// A failed review still leaves a comment that says so; the red job is the second
	// signal to a human that this PR has not been looked at.
	return errors.Join(reviewErr, postErr, writeErr)
}

// reviewPR runs the pipeline and always returns a comment body: the review, or a
// failure notice with the reason.
func reviewPR(ctx context.Context, cfg config, model chatModel, embedder rag.Embedder) (string, error) {
	m := metrics{Started: time.Now()}
	pr, err := collect(cfg.repo, cfg.base, cfg.head)
	m.Collect = time.Since(m.Started)
	if err != nil {
		return renderFailure(pullRequest{Base: cfg.base, Head: cfg.head}, "diff не прочитан: "+err.Error(), m), err
	}
	stage := time.Now()
	c, err := buildCorpus(cfg.repo, pr)
	if err != nil {
		return renderFailure(pr, "не собрать базу документации и кода: "+err.Error(), m), err
	}
	m.Corpus = c
	score, embed, degraded := pickScorer(ctx, embedder, cfg.cache, c.Pieces)
	m.Index, m.Embed, m.Degraded = time.Since(stage), embed, degraded

	stage = time.Now()
	qs := queries(pr)
	sources, err := retrieve(ctx, score, c.Pieces, qs)
	if err != nil && m.Degraded == "" {
		// Ollama died between indexing and the queries: the same fallback applies.
		m.Degraded = err.Error()
		sources, err = retrieve(ctx, lexicalScorer(c.Pieces), c.Pieces, qs)
	}
	m.Retrieve = time.Since(stage)
	if err != nil {
		return renderFailure(pr, "поиск по базе не удался: "+err.Error(), m), err
	}

	stage = time.Now()
	v, calls, err := review(ctx, model, pr, sources)
	m.Model, m.Calls = time.Since(stage), calls
	if err != nil {
		return renderFailure(pr, err.Error(), m), err
	}
	return renderReview(pr, v, sources, m), nil
}

// pickScorer embeds the corpus and returns vector search; if Ollama fails it returns
// lexical search and the reason, which the comment then shows.
func pickScorer(ctx context.Context, embedder rag.Embedder, cachePath string, pieces []piece) (scorer, embedStats, string) {
	cache, err := loadCache(cachePath, embedModel)
	if err != nil {
		return lexicalScorer(pieces), embedStats{}, "кэш эмбеддингов не прочитан: " + err.Error()
	}
	stats, err := embedAll(ctx, embedder, cache, pieces)
	if err != nil {
		return lexicalScorer(pieces), stats, err.Error()
	}
	if err := cache.save(); err != nil {
		fmt.Fprintln(os.Stderr, "кэш эмбеддингов не сохранён:", err)
	}
	return vectorScorer(embedder, pieces), stats, ""
}

func writeOutputs(cfg config, body string) error {
	if cfg.out == "" {
		// The body quotes the model and the PR: a terminal gets it without control codes.
		fmt.Print(rag.TerminalSafeMultiline(body, len(body)))
	} else if err := os.WriteFile(cfg.out, []byte(body), 0o644); err != nil {
		return err
	}
	if cfg.summary != "" {
		f, err := os.OpenFile(cfg.summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, werr := f.WriteString(body + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return werr
	}
	return nil
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", rag.TerminalSafeMultiline(err.Error(), 4000))
		os.Exit(1)
	}
}
