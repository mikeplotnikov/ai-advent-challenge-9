package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type request struct {
	Model    string         `json:"model"`
	Messages []message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Think    bool           `json:"think"`
	Options  map[string]any `json:"options"`
	Format   any            `json:"format,omitempty"`
}
type response struct {
	Model           string  `json:"model"`
	Message         message `json:"message"`
	Done            bool    `json:"done"`
	DoneReason      string  `json:"done_reason"`
	TotalDuration   int64   `json:"total_duration"`
	LoadDuration    int64   `json:"load_duration"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	EvalCount       int     `json:"eval_count"`
	EvalDuration    int64   `json:"eval_duration"`
	Error           string  `json:"error,omitempty"`
}
type modelInfo struct {
	RemoteHost   string         `json:"remote_host,omitempty"`
	RemoteModel  string         `json:"remote_model,omitempty"`
	Capabilities []string       `json:"capabilities"`
	Details      map[string]any `json:"details"`
}
type result struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Difficulty string          `json:"difficulty"`
	Request    request         `json:"request"`
	Response   json.RawMessage `json:"response"`
	WallMS     int64           `json:"wall_ms"`
	Passed     bool            `json:"passed"`
	Check      string          `json:"check"`
}
type report struct {
	CreatedAt string    `json:"created_at"`
	Endpoint  string    `json:"endpoint"`
	Model     string    `json:"model"`
	Digest    string    `json:"digest"`
	Version   string    `json:"ollama_version"`
	Platform  string    `json:"platform"`
	Metadata  modelInfo `json:"model_metadata"`
	Results   []result  `json:"results"`
}

func localClient(endpoint string) (*http.Client, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, "", err
	}
	host := u.Hostname()
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, "", fmt.Errorf("only an HTTP loopback endpoint without credentials, path or query is allowed")
	}
	if u.Port() != "" {
		u.Host = net.JoinHostPort(host, u.Port())
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	u.Path = ""
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // A proxy would break the loopback-only contract.
	return &http.Client{Transport: transport, Timeout: 3 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("redirects are not allowed") }}, u.String(), nil
}

func call(client *http.Client, base, path string, payload any, target any) (json.RawMessage, error) {
	method := http.MethodGet
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
		method = http.MethodPost
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Ollama unavailable: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ollama %s: HTTP %d: %s", path, res.StatusCode, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, target); err != nil {
		return nil, fmt.Errorf("Ollama %s invalid JSON: %w", path, err)
	}
	return data, nil
}

func preflight(c *http.Client, base, model string) (report, error) {
	r := report{CreatedAt: time.Now().UTC().Format(time.RFC3339), Endpoint: base, Model: model, Platform: runtime.GOOS + "/" + runtime.GOARCH}
	var version struct {
		Version string `json:"version"`
	}
	if _, err := call(c, base, "/api/version", nil, &version); err != nil {
		return r, err
	}
	r.Version = version.Version
	var tags struct {
		Models []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if _, err := call(c, base, "/api/tags", nil, &tags); err != nil {
		return r, err
	}
	for _, m := range tags.Models {
		if m.Name == model {
			r.Digest = m.Digest
		}
	}
	if r.Digest == "" {
		return r, fmt.Errorf("model %q is not installed; run ollama pull %s", model, model)
	}
	if _, err := call(c, base, "/api/show", map[string]string{"model": model}, &r.Metadata); err != nil {
		return r, err
	}
	if r.Metadata.RemoteHost != "" || r.Metadata.RemoteModel != "" || strings.Contains(model, "cloud") {
		return r, fmt.Errorf("remote/cloud models are not allowed")
	}
	for _, capability := range r.Metadata.Capabilities {
		if capability == "completion" {
			return r, nil
		}
	}
	return r, fmt.Errorf("model lacks completion capability")
}

type workshop struct {
	ID                 string
	Cost, Hours, Score int
}

var workshops = []workshop{{"A", 2000, 2, 6}, {"B", 3000, 3, 10}, {"C", 1500, 2, 5}, {"D", 2500, 1, 8}}

func bestScore() int {
	best := 0
	for mask := 0; mask < 1<<len(workshops); mask++ {
		cost, hours, score := 0, 0, 0
		for i, w := range workshops {
			if mask&(1<<i) != 0 {
				cost += w.Cost
				hours += w.Hours
				score += w.Score
			}
		}
		if cost <= 5000 && hours <= 4 && score > best {
			best = score
		}
	}
	return best
}

func verify(id, text string) (bool, string) {
	switch id {
	case "arithmetic":
		return strings.TrimSpace(text) == "42", "Ожидается ровно 42: 17 + 25."
	case "extraction":
		var got map[string]any
		err := json.Unmarshal([]byte(text), &got)
		return err == nil && len(got) == 3 && got["city"] == "Казань" && got["date"] == "2026-10-12" && got["participants"] == float64(12), "Три поля JSON совпадают с исходным текстом."
	case "optimization":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(text), &fields); err != nil || len(fields) != 4 {
			return false, "Ожидается JSON ровно с четырьмя полями."
		}
		var got struct {
			Selected []string `json:"selected"`
			Cost     int      `json:"cost"`
			Hours    int      `json:"hours"`
			Score    int      `json:"score"`
		}
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			return false, "Ответ не является JSON: " + err.Error()
		}
		seen := map[string]bool{}
		cost, hours, score := 0, 0, 0
		for _, id := range got.Selected {
			if seen[id] {
				return false, "Повтор выбранного занятия."
			}
			seen[id] = true
			found := false
			for _, w := range workshops {
				if w.ID == id {
					cost += w.Cost
					hours += w.Hours
					score += w.Score
					found = true
				}
			}
			if !found {
				return false, "Неизвестное занятие."
			}
		}
		return cost <= 5000 && hours <= 4 && score == bestScore() && cost == got.Cost && hours == got.Hours && score == got.Score, fmt.Sprintf("Суммы пересчитаны; ограничения ≤5000 ₽ и ≤4 ч; оптимальная полезность %d проверена перебором всех наборов.", bestScore())
	}
	return false, "Неизвестная проверка."
}

func cases(model string) []result {
	items := []result{
		{ID: "arithmetic", Title: "Простой расчёт", Difficulty: "Простой", Request: request{Messages: []message{{"user", "Сколько будет 17 + 25? Ответь только одним числом, без пояснений."}}}},
		{ID: "extraction", Title: "Извлечение данных", Difficulty: "Средний", Request: request{Format: "json", Messages: []message{{"user", "Извлеки данные из текста. Ответ — только JSON с тремя ключами city (строка), date (YYYY-MM-DD), participants (целое число). Текст: Встреча пройдёт в Казани 12 октября 2026 года. Подтвердили участие 12 человек. Название города в ответе: Казань."}}}},
		{ID: "optimization", Title: "Выбор с ограничениями", Difficulty: "Сложный", Request: request{Format: "json"}},
	}
	var prompt strings.Builder
	prompt.WriteString("Выбери набор занятий с максимальной суммарной полезностью. Каждое занятие можно взять не более одного раза. Бюджет не более 5000 рублей, время не более 4 часов. Занятия независимы, стоимости и часы складываются.\n")
	for _, w := range workshops {
		fmt.Fprintf(&prompt, "%s: цена %d рублей, время %d ч, полезность %d.\n", w.ID, w.Cost, w.Hours, w.Score)
	}
	prompt.WriteString("Ответь только JSON: selected (массив выбранных букв), cost (суммарная цена), hours (суммарные часы), score (суммарная полезность).")
	items[2].Request.Messages = []message{{"user", prompt.String()}}
	for i := range items {
		items[i].Request.Model = model
		items[i].Request.Messages = append([]message{{"system", "Ты выполняешь учебные задания. Отвечай по-русски. Соблюдай указанный формат ответа."}}, items[i].Request.Messages...)
		items[i].Request.Options = map[string]any{"temperature": 0, "seed": 26, "num_ctx": 4096, "num_predict": 512}
	}
	return items
}

func run(c *http.Client, base, model string, output io.Writer) (report, error) {
	r, err := preflight(c, base, model)
	if err != nil {
		return r, err
	}
	for _, item := range cases(model) {
		start := time.Now()
		var reply response
		data, err := call(c, base, "/api/chat", item.Request, &reply)
		if err != nil {
			return r, err
		}
		if reply.Error != "" || !reply.Done || reply.DoneReason != "stop" || reply.Message.Role != "assistant" || strings.TrimSpace(reply.Message.Content) == "" {
			return r, fmt.Errorf("%s: incomplete, empty or failed response (reason=%q error=%q)", item.ID, reply.DoneReason, reply.Error)
		}
		item.Response = data
		item.WallMS = time.Since(start).Milliseconds()
		item.Passed, item.Check = verify(item.ID, reply.Message.Content)
		r.Results = append(r.Results, item)
		fmt.Fprintf(output, "\n%s · %s\nВопрос: %s\nОтвет: %s\nПроверка: %t · Критерий: %s · %d мс\n", item.Difficulty, item.Title, item.Request.Messages[1].Content, reply.Message.Content, item.Passed, item.Check, item.WallMS)
	}
	return r, nil
}

func saveCapture(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("capture not written; choose a new -out path: %w", err)
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func main() {
	model := flag.String("model", "qwen3:4b", "Installed local model name")
	endpoint := flag.String("endpoint", "http://127.0.0.1:11434", "Ollama loopback URL")
	out := flag.String("out", filepath.Join(os.TempDir(), fmt.Sprintf("day26-%d.json", time.Now().UnixNano())), "Capture file (contains only these public synthetic tasks)")
	flag.Parse()
	c, base, err := localClient(*endpoint)
	if err == nil {
		var r report
		r, err = run(c, base, *model, os.Stdout)
		if err == nil {
			var data []byte
			data, err = json.MarshalIndent(r, "", "  ")
			if err == nil {
				err = saveCapture(*out, append(data, '\n'))
			}
			if err == nil {
				fmt.Printf("\nСохранён реальный прогон: %s\n", *out)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
