package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/docrag"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

const (
	kindDoc  = "doc"
	kindCode = "code"
	// maxCodeRunes splits a long declaration into parts; bge-m3 takes far more, but a
	// whole 300-line function as one vector matches everything a little and nothing well.
	maxCodeRunes = 2400
	codeWindow   = 60
)

// piece is one retrievable fragment of the knowledge base: a documentation section or
// a Go declaration.
type piece struct {
	Kind   string
	Chunk  rag.Chunk
	Vector []float64
}

// corpus is the knowledge base for one review.
type corpus struct {
	Pieces      []piece
	DocFiles    int
	CodeFiles   int
	CodeSkipped []string
}

// buildCorpus reads the documentation (README + docs/, as on day 31) and the Go code:
// every package under internal/ plus the packages the pull request touches. The changed
// files themselves stay out — the model gets them whole, and a stale copy of a line the
// PR rewrites would only argue with the diff.
func buildCorpus(repo string, pr pullRequest) (corpus, error) {
	var c corpus
	files, texts, err := docrag.Manifest(repo)
	if err != nil {
		return c, err
	}
	for _, f := range files {
		for _, chunk := range docrag.Chunk(f.Path, texts[f.Path]) {
			c.Pieces = append(c.Pieces, piece{Kind: kindDoc, Chunk: chunk})
		}
	}
	c.DocFiles = len(files)

	sources, err := codeSources(repo, pr)
	if err != nil {
		return c, err
	}
	for _, source := range sources {
		raw, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(source)))
		if err != nil {
			return c, err
		}
		chunks, err := chunkGo(source, string(raw))
		if err != nil {
			c.CodeSkipped = append(c.CodeSkipped, source+": "+err.Error())
		}
		for _, chunk := range chunks {
			c.Pieces = append(c.Pieces, piece{Kind: kindCode, Chunk: chunk})
		}
	}
	c.CodeFiles = len(sources)
	return c, nil
}

func codeSources(repo string, pr pullRequest) ([]string, error) {
	changed := map[string]bool{}
	dirs := map[string]bool{"internal": true}
	for _, ch := range pr.Changes {
		changed[ch.Path] = true
		if strings.HasSuffix(ch.Path, ".go") {
			dirs[path.Dir(ch.Path)] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for dir := range dirs {
		root := filepath.Join(repo, filepath.FromSlash(dir))
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		recursive := dir == "internal"
		err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if p != root && !recursive {
					return fs.SkipDir
				}
				return nil
			}
			// Regular files only: a symlink could pull a gitignored secret into the prompt.
			if !entry.Type().IsRegular() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repo, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !changed[rel] && !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// chunkGo makes one chunk per top-level declaration, doc comment included, so a hit
// names a function or a type rather than a line range. A file that does not parse is
// cut into fixed line windows and the parse error is returned alongside.
func chunkGo(source, text string) ([]rag.Chunk, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, source, text, parser.ParseComments)
	if err != nil {
		return windows(source, text), fmt.Errorf("не разобран как Go, нарезан окнами по %d строк: %w", codeWindow, err)
	}
	pkg := "package " + file.Name.Name
	var out []rag.Chunk
	for _, decl := range file.Decls {
		// Import blocks are skipped: they name packages, not behaviour, and their lists
		// of common words outscore real code against almost any diff.
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.IMPORT {
			continue
		}
		start := decl.Pos()
		var doc *ast.CommentGroup
		switch d := decl.(type) {
		case *ast.FuncDecl:
			doc = d.Doc
		case *ast.GenDecl:
			doc = d.Doc
		}
		if doc != nil {
			start = doc.Pos()
		}
		from, to := fset.Position(start), fset.Position(decl.End())
		body := text[from.Offset:to.Offset]
		section := pkg + " · " + declName(decl)
		out = append(out, splitDecl(source, section, body, from.Line)...)
	}
	return out, nil
}

func declName(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil && len(d.Recv.List) > 0 {
			return "func (" + exprName(d.Recv.List[0].Type) + ") " + d.Name.Name
		}
		return "func " + d.Name.Name
	case *ast.GenDecl:
		var names []string
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				names = append(names, s.Name.Name)
			case *ast.ValueSpec:
				for _, n := range s.Names {
					names = append(names, n.Name)
				}
			}
		}
		if len(names) > 4 {
			names = append(names[:4], "…")
		}
		return d.Tok.String() + " " + strings.Join(names, ", ")
	}
	return "decl"
}

func exprName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return "*" + exprName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return exprName(e.X)
	case *ast.IndexListExpr:
		return exprName(e.X)
	}
	return "?"
}

// splitDecl keeps a declaration whole when it fits and otherwise cuts it at line
// boundaries; every part keeps the declaration's name as its section.
func splitDecl(source, section, body string, firstLine int) []rag.Chunk {
	lines := strings.SplitAfter(body, "\n")
	var out []rag.Chunk
	var part strings.Builder
	partStart := firstLine
	emit := func(end int) {
		if strings.TrimSpace(part.String()) == "" {
			part.Reset()
			return
		}
		text := part.String()
		out = append(out, rag.Chunk{
			ChunkID: fmt.Sprintf("%s#L%d", source, partStart),
			Source:  source, Section: section, Part: len(out) + 1,
			Start: partStart, End: end, Runes: utf8.RuneCountInString(text), Text: text,
		})
		part.Reset()
	}
	for i, line := range lines {
		if part.Len() > 0 && utf8.RuneCountInString(part.String())+utf8.RuneCountInString(line) > maxCodeRunes {
			emit(firstLine + i - 1)
			partStart = firstLine + i
		}
		part.WriteString(line)
	}
	emit(firstLine + len(lines) - 1)
	return out
}

func windows(source, text string) []rag.Chunk {
	lines := strings.SplitAfter(text, "\n")
	var out []rag.Chunk
	for start := 0; start < len(lines); start += codeWindow {
		end := min(start+codeWindow, len(lines))
		body := strings.Join(lines[start:end], "")
		out = append(out, splitDecl(source, "строки", body, start+1)...)
	}
	return out
}

// embeddingCache maps sha256(model, input) to its vector, so a rerun on the next push
// embeds only the fragments that changed.
type embeddingCache struct {
	path    string
	model   string
	vectors map[string][]float64
	used    map[string]bool
	dirty   bool
}

func loadCache(path, model string) (*embeddingCache, error) {
	c := &embeddingCache{path: path, model: model, vectors: map[string][]float64{}, used: map[string]bool{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &c.vectors); err != nil {
		// A broken cache costs one full re-embedding, not a failed review.
		c.vectors = map[string][]float64{}
	}
	return c, nil
}

func (c *embeddingCache) key(input string) string {
	sum := sha256.Sum256([]byte(c.model + "\x00" + input))
	return hex.EncodeToString(sum[:])
}

// save keeps only the vectors this run used, so the file tracks the code as it is
// rather than every version of it ever reviewed.
func (c *embeddingCache) save() error {
	for key := range c.vectors {
		if !c.used[key] {
			delete(c.vectors, key)
			c.dirty = true
		}
	}
	if !c.dirty {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(c.vectors)
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

type embedStats struct {
	FromCache, Embedded int
}

// embedAll fills every piece's vector, from the cache where it can. The first error
// stops it: the caller then reviews on lexical search instead.
func embedAll(ctx context.Context, embedder rag.Embedder, cache *embeddingCache, pieces []piece) (embedStats, error) {
	var stats embedStats
	for i := range pieces {
		input := docrag.EmbeddingInput(pieces[i].Chunk)
		key := cache.key(input)
		cache.used[key] = true
		if vector, ok := cache.vectors[key]; ok {
			pieces[i].Vector = vector
			stats.FromCache++
			continue
		}
		vector, _, _, err := embedder.Embed(ctx, pieces[i].Chunk.ChunkID, input)
		if err != nil {
			return stats, err
		}
		pieces[i].Vector = vector
		cache.vectors[key] = vector
		cache.dirty = true
		stats.Embedded++
	}
	return stats, nil
}
