package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
)

//go:embed index.html app.js style.css
var assets embed.FS

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type identity struct {
	Model        string `json:"model"`
	Endpoint     string `json:"endpoint"`
	Digest       string `json:"digest"`
	Size         int64  `json:"size_bytes"`
	Quantization string `json:"quantization_level"`
}
type source struct {
	Source  string `json:"source"`
	Section string `json:"section"`
	ChunkID string `json:"chunk_id"`
	Quote   string `json:"quote"`
}
type answer struct {
	Answer        string   `json:"answer"`
	Unknown       bool     `json:"unknown"`
	Clarification string   `json:"clarification"`
	Sources       []source `json:"sources"`
}
type fragment struct {
	Source     string  `json:"source"`
	Section    string  `json:"section"`
	ChunkID    string  `json:"chunk_id"`
	Text       string  `json:"text"`
	Similarity float64 `json:"similarity"`
}
type attempt struct {
	Request    any             `json:"request"`
	Response   json.RawMessage `json:"response,omitempty"`
	StartedAt  string          `json:"started_at"`
	DurationMS int64           `json:"duration_ms"`
	Error      string          `json:"error,omitempty"`
	Resources  resourceTrace   `json:"resources"`
}
type result struct {
	Question     string        `json:"question"`
	StartedAt    string        `json:"started_at"`
	Context      []fragment    `json:"context"`
	Embedding    *attempt      `json:"embedding,omitempty"`
	Attempts     []attempt     `json:"attempts"`
	Answer       *answer       `json:"answer,omitempty"`
	Error        string        `json:"error,omitempty"`
	ErrorKind    string        `json:"error_kind,omitempty"`
	RetrievalMS  int64         `json:"retrieval_ms"`
	GenerationMS int64         `json:"generation_ms"`
	TotalMS      int64         `json:"total_ms"`
	Reason       string        `json:"reason,omitempty"`
	Resources    resourceTrace `json:"resources"`
}
type ollamaRequest struct {
	Model    string         `json:"model"`
	Messages []message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Think    bool           `json:"think"`
	Format   any            `json:"format"`
	Options  map[string]any `json:"options"`
	Truncate bool           `json:"truncate"`
	Shift    bool           `json:"shift"`
}
type ollamaResponse struct {
	Model      string  `json:"model"`
	Message    message `json:"message"`
	Done       bool    `json:"done"`
	DoneReason string  `json:"done_reason"`
	Error      string  `json:"error"`
}
type app struct {
	client                *http.Client
	endpoint, model, host string
	index                 rag.Index
	indexSHA              string
	busy                  chan struct{}
	capturePath           string
	profile               profile
	collectResources      bool
}

func sha(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (a *app) config(ctx context.Context) (map[string]any, error) {
	p := a.profile
	if p.ID == "" {
		p, _ = findProfile("baseline")
	}
	gen, e := a.pinned(ctx, p)
	if e != nil {
		return nil, e
	}
	emb, e := a.modelInfo(ctx, "bge-m3", "embedding")
	if e != nil {
		return nil, e
	}
	if emb.Digest != "7907646426070047a77226ac3e684fbbe8410524f7b4a74d02837e43f2146bab" {
		return nil, fmt.Errorf("digest эмбеддера изменился")
	}
	var version map[string]any
	if _, e = a.call(ctx, "/api/version", nil, &version); e != nil {
		return nil, e
	}
	return map[string]any{"mode": "live", "generation": gen, "embedding": emb, "ollama": version, "index_sha256": a.indexSHA, "chunks": len(a.index.Chunks), "dimension": a.index.Header.Dimension, "cos_threshold": 0.45, "top_k": 3, "options": a.options(), "profiles": profiles(), "winner": a.winner()}, nil
}
func generationOptions() map[string]any {
	return map[string]any{"temperature": 0, "seed": 28, "num_predict": 2048, "num_ctx": 16384}
}
func strictObject(text string, keys ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(strings.NewReader(text))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("ожидался JSON-объект")
	}
	m := map[string]json.RawMessage{}
	for d.More() {
		tok, e := d.Token()
		if e != nil {
			return nil, e
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("ключ не строка")
		}
		if _, ok = m[key]; ok {
			return nil, fmt.Errorf("повторное поле %s", key)
		}
		var raw json.RawMessage
		if e = d.Decode(&raw); e != nil {
			return nil, e
		}
		m[key] = raw
	}
	if _, e = d.Token(); e != nil {
		return nil, e
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return nil, fmt.Errorf("лишние данные после JSON")
	}
	if len(m) != len(keys) {
		return nil, fmt.Errorf("неверный набор полей")
	}
	for _, k := range keys {
		v, ok := m[k]
		if !ok || string(v) == "null" {
			return nil, fmt.Errorf("нет обязательного поля %s", k)
		}
	}
	return m, nil
}
func parseAnswer(text string, chunks []fragment) (answer, error) {
	var out answer
	fields, e := strictObject(text, "answer", "unknown", "clarification", "sources")
	if e != nil {
		return out, e
	}
	if len(fields["sources"]) == 0 || fields["sources"][0] != '[' {
		return out, fmt.Errorf("sources должен быть массивом")
	}
	var ss []json.RawMessage
	if e = json.Unmarshal(fields["sources"], &ss); e != nil {
		return out, e
	}
	for _, s := range ss {
		if _, e = strictObject(string(s), "source", "section", "chunk_id", "quote"); e != nil {
			return out, e
		}
	}
	if e = json.Unmarshal([]byte(text), &out); e != nil {
		return out, e
	}
	if strings.TrimSpace(out.Answer) == "" {
		return out, fmt.Errorf("пустой ответ")
	}
	if out.Unknown {
		if out.Answer != "Не знаю." || strings.TrimSpace(out.Clarification) == "" || len(out.Sources) != 0 {
			return out, fmt.Errorf("неверный отказ")
		}
		return out, nil
	}
	if out.Clarification != "" || len(out.Sources) == 0 || len(out.Sources) > len(chunks) {
		return out, fmt.Errorf("нет источников или неверное уточнение")
	}
	for _, s := range out.Sources {
		valid := false
		for _, c := range chunks {
			if s.Source == c.Source && s.Section == c.Section && s.ChunkID == c.ChunkID && strings.TrimSpace(s.Quote) != "" && strings.Contains(c.Text, s.Quote) {
				valid = true
			}
		}
		if !valid {
			return out, fmt.Errorf("источник или цитата не совпадают с контекстом: %s", s.ChunkID)
		}
	}
	return out, nil
}
func messagesFor(question string, chunks []fragment) []message {
	type textChunk struct {
		Source  string `json:"source"`
		Section string `json:"section"`
		ChunkID string `json:"chunk_id"`
		Text    string `json:"text"`
	}
	cc := make([]textChunk, len(chunks))
	for i, c := range chunks {
		cc[i] = textChunk{c.Source, c.Section, c.ChunkID, c.Text}
	}
	var encoded strings.Builder
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(cc)
	b := []byte(strings.TrimSuffix(encoded.String(), "\n"))
	return []message{{"system", AnswerSystemPrompt}, {"user", "Фрагменты (JSON):\n" + string(b) + "\nВопрос: " + question}}
}

// Constrain citation copying, not answer facts: every offered quote is a literal substring.
func quoteChoices(text string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, parts := range [][]string{strings.Split(text, "\n\n"), strings.Split(text, "\n")} {
		for _, q := range parts {
			q = strings.TrimSpace(q)
			if q != "" && !seen[q] {
				seen[q] = true
				out = append(out, q)
			}
		}
	}
	return out
}

func answerSchema(chunks []fragment) map[string]any {
	alternatives := []any{}
	for _, c := range chunks {
		props := map[string]any{"source": map[string]any{"type": "string", "enum": []string{c.Source}}, "section": map[string]any{"type": "string", "enum": []string{c.Section}}, "chunk_id": map[string]any{"type": "string", "enum": []string{c.ChunkID}}, "quote": map[string]any{"type": "string", "enum": quoteChoices(c.Text)}}
		alternatives = append(alternatives, map[string]any{"type": "object", "properties": props, "required": []string{"source", "section", "chunk_id", "quote"}, "additionalProperties": false})
	}
	return map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}, "unknown": map[string]any{"type": "boolean"}, "clarification": map[string]any{"type": "string"}, "sources": map[string]any{"type": "array", "items": map[string]any{"anyOf": alternatives}, "maxItems": len(chunks)}}, "required": []string{"answer", "unknown", "clarification", "sources"}, "additionalProperties": false}
}

func (a *app) generate(ctx context.Context, msgs []message, chunks []fragment) (*answer, []attempt, error) {
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
	}
	if total > 64000 {
		return nil, []attempt{}, fmt.Errorf("контекст превышает лимит 64000 байт")
	}
	attempts := []attempt{}
	for i := 0; i < 2; i++ {
		req := ollamaRequest{Model: a.model, Messages: msgs, Stream: false, Think: false, Format: answerSchema(chunks), Options: a.options(), Truncate: false}
		start := time.Now()
		var res ollamaResponse
		finish := a.startResources(ctx)
		raw, e := a.call(ctx, "/api/chat", req, &res)
		trace := finish()
		tr := attempt{Request: req, Response: raw, StartedAt: start.UTC().Format(time.RFC3339Nano), DurationMS: time.Since(start).Milliseconds(), Resources: trace}
		if e == nil && (res.Error != "" || !res.Done || res.DoneReason != "stop" || res.Model != a.model || res.Message.Role != "assistant") {
			e = fmt.Errorf("ответ модели незавершён или некорректен: %s %s", res.DoneReason, res.Error)
		}
		var parsed answer
		completion := e == nil
		if e == nil {
			parsed, e = parseAnswer(res.Message.Content, chunks)
		}
		if e != nil {
			tr.Error = e.Error()
		}
		attempts = append(attempts, tr)
		if e == nil {
			return &parsed, attempts, nil
		}
		if !completion || i == 1 {
			return nil, attempts, e
		}
		msgs = append(append([]message{}, msgs...), message{"assistant", res.Message.Content}, message{"user", "Исправь только JSON и дословные цитаты, не добавляя фактов. Ошибка проверки: " + e.Error()})
	}
	return nil, attempts, fmt.Errorf("генерация не завершена")
}
func errorKind(attempts []attempt, model string) string {
	if len(attempts) > 0 {
		var last ollamaResponse
		if json.Unmarshal(attempts[len(attempts)-1].Response, &last) == nil {
			if !last.Done || last.DoneReason == "length" {
				return "incomplete"
			}
			if last.Done && last.DoneReason == "stop" && last.Error == "" && last.Message.Role == "assistant" && last.Model == model {
				return "contract"
			}
		}
	}
	return "technical"
}

func (a *app) retrieve(ctx context.Context, question string, emit func(string, any)) (r result) {
	start := time.Now()
	r = result{Question: question, StartedAt: start.UTC().Format(time.RFC3339Nano), Context: []fragment{}, Attempts: []attempt{}}
	defer func() { r.TotalMS = time.Since(start).Milliseconds() }()
	emit("embedding", nil)
	req := map[string]any{"model": "bge-m3", "input": question, "truncate": false}
	var resp struct {
		Embeddings [][]float64 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	t := time.Now()
	raw, e := a.call(ctx, "/api/embed", req, &resp)
	tr := attempt{Request: req, Response: raw, StartedAt: t.UTC().Format(time.RFC3339Nano), DurationMS: time.Since(t).Milliseconds()}
	r.Embedding = &tr
	if e == nil && (resp.Error != "" || len(resp.Embeddings) != 1) {
		e = fmt.Errorf("неверный embedding: %s", resp.Error)
	}
	if e != nil {
		tr.Error = e.Error()
		r.Embedding = &tr
		r.Error = e.Error()
		return
	}
	v := resp.Embeddings[0]
	norm := 0.0
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			r.Error = "нечисловой embedding"
			return
		}
		norm += x * x
	}
	if norm == 0 || len(v) != a.index.Header.Dimension {
		r.Error = "пустой embedding или несовместимая размерность"
		return
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] /= norm
	}
	emit("retrieval", nil)
	ranked, e := rag.RankChunks(a.index, v, 10)
	if e != nil {
		r.Error = e.Error()
		return
	}
	for _, x := range ranked {
		if x.Similarity >= 0.45 && len(r.Context) < 3 {
			c := x.Chunk
			r.Context = append(r.Context, fragment{c.Source, c.Section, c.ChunkID, c.Text, x.Similarity})
		}
	}
	r.RetrievalMS = time.Since(start).Milliseconds()
	emit("context", r.Context)
	if len(r.Context) == 0 {
		r.Answer = &answer{"Не знаю.", true, "Уточните термин или задачу: подходящих фрагментов в базе не найдено.", []source{}}
		r.Reason = "empty_context"
		return
	}
	return
}
func (a *app) generateRetrieved(ctx context.Context, retrieval result, p profile) (r result) {
	r = retrieval
	r.Attempts = []attempt{}
	r.Answer = nil
	r.Resources = resourceTrace{Samples: []resourceSample{}}
	if r.Error != "" {
		return
	}
	if len(r.Context) == 0 {
		r.Answer = &answer{"Не знаю.", true, "Уточните термин или задачу: подходящих фрагментов в базе не найдено.", []source{}}
		r.Reason = "empty_context"
		return
	}
	b := a.configured(p)
	t := time.Now()
	var e error
	r.Answer, r.Attempts, e = b.generate(ctx, b.messages(r.Question, r.Context), r.Context)
	r.GenerationMS = time.Since(t).Milliseconds()
	r.TotalMS = r.RetrievalMS + r.GenerationMS
	r.Resources = combineResources(r.Attempts)
	if e != nil {
		r.Error = e.Error()
		r.ErrorKind = errorKind(r.Attempts, p.Model)
	}
	return
}
func (a *app) runQuestion(ctx context.Context, q string, emit func(string, any)) result {
	r := a.retrieve(ctx, q, emit)
	emit("generation", nil)
	p := a.profile
	if p.ID == "" {
		p, _ = findProfile("baseline")
	}
	return a.generateRetrieved(ctx, r, p)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, e error) {
	writeJSON(w, status, map[string]string{"error": e.Error()})
}
func loadApp(endpoint, model, indexPath string) (*app, error) {
	client, base, e := localClient(endpoint)
	if e != nil {
		return nil, e
	}
	idx, e := rag.ReadIndex(indexPath)
	if e != nil {
		return nil, e
	}
	manifest, e := rag.ManifestSHA256(rag.ProjectPath("day-21/corpus"))
	if e != nil {
		return nil, e
	}
	if e = rag.ValidateIndex(idx, "structure", "bge-m3", manifest, 1024); e != nil {
		return nil, e
	}
	for _, c := range idx.Chunks {
		if c.Text == "" || len(c.Text) > 16000 {
			return nil, fmt.Errorf("пустой или слишком большой фрагмент %s", c.ChunkID)
		}
		norm := 0.0
		for _, v := range c.Embedding {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("нечисловой индекс")
			}
			norm += v * v
		}
		if math.Abs(norm-1) > 0.01 {
			return nil, fmt.Errorf("вектор индекса не нормализован")
		}
	}
	data, e := os.ReadFile(indexPath)
	if e != nil {
		return nil, e
	}
	return &app{client: client, endpoint: base, model: model, index: idx, indexSHA: sha(data), busy: make(chan struct{}, 1), capturePath: rag.ProjectPath("day-29/showcase.json")}, nil
}
func validateListen(address string) error {
	host, _, e := net.SplitHostPort(address)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("listen must be a literal loopback IP:port")
	}
	return nil
}
func run() error {
	listen := flag.String("listen", "127.0.0.1:8029", "loopback IP:port")
	endpoint := flag.String("ollama", "http://127.0.0.1:11434", "local Ollama URL")
	eval := flag.String("eval-out", "", "new evaluation path")
	cold := flag.String("cold-out", "", "one separate cold control to new file")
	diagnostic := flag.Bool("diagnostic", false, "screen profiles on diagnostic questions")
	selected := flag.String("profiles", "baseline,temperature,limits,prompt,combined,q8", "comma separated profile IDs")
	flag.Parse()
	if e := validateListen(*listen); e != nil {
		return e
	}
	a, e := loadApp(*endpoint, "qwen3:4b", rag.ProjectPath("day-21/index/structure.json"))
	if e != nil {
		return e
	}
	a.host = *listen
	a.collectResources = true
	if *cold != "" {
		return a.cold(*cold, strings.Split(*selected, ",")[0])
	}
	if *eval != "" {
		return a.evaluate(*eval, *diagnostic, strings.Split(*selected, ","))
	}
	if _, e = a.config(context.Background()); e != nil {
		return e
	}
	fmt.Printf("Живая витрина: http://%s/?record=1\n", *listen)
	return (&http.Server{Addr: *listen, Handler: a, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Minute, IdleTimeout: 30 * time.Second}).ListenAndServe()
}
func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
