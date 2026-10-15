package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

const (
	docsPerQuery = 2
	codePerQuery = 3
	maxDocs      = 6
	maxCode      = 8
	queryRunes   = 1500
	// rulesQuery is asked on every review: the lesson's pipeline fetches "how a PR in this
	// project must look" from RAG, and that has to arrive whatever the diff is about.
	rulesQuery = "Правила ревью PR в этом репозитории: что обязательно должно быть в изменении и чего быть не должно"
)

// source is a retrieved fragment with the id the model cites it by (D1, C3).
type source struct {
	ID    string
	Kind  string
	Score float64
	Chunk rag.Chunk
}

// query is one search: the rules question, or a changed file with its diff.
type query struct {
	Label  string
	Text   string
	Docs   int
	Code   int
	Pinned bool // its hits are kept whatever their score against the other queries
}

func queries(pr pullRequest) []query {
	out := []query{{Label: "правила ревью", Text: rulesQuery, Docs: docsPerQuery, Pinned: true}}
	for _, c := range pr.Changes {
		if c.Binary {
			continue
		}
		text := "Файл: " + c.Path + "\n" + changedLines(c.Diff)
		out = append(out, query{Label: c.Path, Text: truncateRunes(text, queryRunes), Docs: docsPerQuery, Code: codePerQuery})
	}
	return out
}

// changedLines keeps the added and removed lines of an annotated diff, without numbers.
func changedLines(diff string) string {
	var out strings.Builder
	for _, line := range strings.Split(diff, "\n") {
		if len(line) > 6 && (line[6] == '+' || line[6] == '-') {
			out.WriteString(line[6:])
			out.WriteString("\n")
		}
	}
	return out.String()
}

// scorer ranks every piece of the corpus against one query, best first.
type scorer func(ctx context.Context, q query) ([]scored, error)

type scored struct {
	Index int
	Score float64
}

func vectorScorer(embedder rag.Embedder, pieces []piece) scorer {
	return func(ctx context.Context, q query) ([]scored, error) {
		vector, _, _, err := embedder.Embed(ctx, "query:"+q.Label, q.Text)
		if err != nil {
			return nil, err
		}
		out := make([]scored, len(pieces))
		for i, p := range pieces {
			if len(p.Vector) != len(vector) {
				return nil, fmt.Errorf("размерность %s: %d, у запроса %d", p.Chunk.ChunkID, len(p.Vector), len(vector))
			}
			sum := 0.0
			for j, v := range vector {
				sum += v * p.Vector[j]
			}
			out[i] = scored{Index: i, Score: sum}
		}
		sortScored(out)
		return out, nil
	}
}

// lexicalScorer is the fallback when Ollama is unavailable: it counts the query's words
// in each fragment, weighting rare words up (a TF-IDF without the vectors). It finds
// identifiers the diff names well and paraphrases badly — which is why it is a fallback.
func lexicalScorer(pieces []piece) scorer {
	docs := make([]map[string]int, len(pieces))
	df := map[string]int{}
	for i, p := range pieces {
		docs[i] = map[string]int{}
		for _, w := range words(p.Chunk.Source + " " + p.Chunk.Section + " " + p.Chunk.Text) {
			if docs[i][w] == 0 {
				df[w]++
			}
			docs[i][w]++
		}
	}
	n := float64(len(pieces))
	return func(_ context.Context, q query) ([]scored, error) {
		terms := map[string]bool{}
		for _, w := range words(q.Text) {
			terms[w] = true
		}
		out := make([]scored, len(pieces))
		for i := range pieces {
			sum := 0.0
			for w := range terms {
				if tf := docs[i][w]; tf > 0 {
					sum += (1 + math.Log(float64(tf))) * math.Log(1+n/float64(df[w]))
				}
			}
			out[i] = scored{Index: i, Score: sum}
		}
		sortScored(out)
		return out, nil
	}
}

func sortScored(s []scored) {
	sort.SliceStable(s, func(i, j int) bool { return s[i].Score > s[j].Score })
}

// words splits text into lowercase words of three or more letters or digits; an
// identifier like loadCache stays one word.
func words(text string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		if utf8.RuneCountInString(w) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

// retrieve runs every query and keeps, per query, the best documentation and code
// fragments, then caps the totals, pinned hits first. A fragment found by several
// queries is kept once.
func retrieve(ctx context.Context, score scorer, pieces []piece, qs []query) ([]source, error) {
	best := map[int]float64{}
	pinned := map[int]bool{}
	for _, q := range qs {
		ranked, err := score(ctx, q)
		if err != nil {
			return nil, err
		}
		docs, code := 0, 0
		for _, r := range ranked {
			if r.Score <= 0 || docs >= q.Docs && code >= q.Code {
				break
			}
			kind := pieces[r.Index].Kind
			if kind == kindDoc && docs < q.Docs {
				docs++
			} else if kind == kindCode && code < q.Code {
				code++
			} else {
				continue
			}
			if r.Score > best[r.Index] {
				best[r.Index] = r.Score
			}
			if q.Pinned {
				pinned[r.Index] = true
			}
		}
	}
	var picked []scored
	for i, s := range best {
		picked = append(picked, scored{Index: i, Score: s})
	}
	sort.Slice(picked, func(i, j int) bool {
		if pinned[picked[i].Index] != pinned[picked[j].Index] {
			return pinned[picked[i].Index]
		}
		if picked[i].Score == picked[j].Score {
			return picked[i].Index < picked[j].Index
		}
		return picked[i].Score > picked[j].Score
	})
	var out []source
	docs, code := 0, 0
	for _, p := range picked {
		pc := pieces[p.Index]
		switch {
		case pc.Kind == kindDoc && docs < maxDocs:
			docs++
			out = append(out, source{ID: fmt.Sprintf("D%d", docs), Kind: kindDoc, Score: p.Score, Chunk: pc.Chunk})
		case pc.Kind == kindCode && code < maxCode:
			code++
			out = append(out, source{ID: fmt.Sprintf("C%d", code), Kind: kindCode, Score: p.Score, Chunk: pc.Chunk})
		}
	}
	return out, nil
}

func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// location names a fragment for the prompt and the comment. Documentation sections are
// named by heading: docrag counts their position in characters, not lines.
func (s source) location() string {
	if s.Kind == kindCode {
		return fmt.Sprintf("%s:%d-%d — %s", s.Chunk.Source, s.Chunk.Start, s.Chunk.End, s.Chunk.Section)
	}
	return s.Chunk.Source + " — " + s.Chunk.Section
}
