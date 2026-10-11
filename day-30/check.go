package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type step struct {
	Name   string `json:"name"`
	Expect string `json:"expect"`
	Got    string `json:"got"`
	OK     bool   `json:"ok"`
}

type checker struct {
	base, token string
	client      *http.Client
	steps       []step
}

func (c *checker) do(method, path string, token string, body any) (int, map[string]any, http.Header) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, map[string]any{"error": err.Error()}, nil
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func (c *checker) chat(msgs []message) (int, map[string]any, http.Header) {
	return c.do("POST", "/api/chat", c.token, map[string]any{"messages": msgs})
}

func (c *checker) add(name, expect, got string, ok bool) {
	c.steps = append(c.steps, step{name, expect, got, ok})
	mark := "PASS"
	if !ok {
		mark = "FAIL"
	}
	log.Printf("%s %s: %s", mark, name, got)
}

func num(m map[string]any, k string) int {
	v, _ := m[k].(float64)
	return int(v)
}

// runCheck drives the public URL the way outside clients would and writes the
// observed results to day-30/check.json for the showcase.
func runCheck(base, token string) error {
	c := &checker{base: base, token: token, client: &http.Client{Timeout: 5 * time.Minute}}

	code, _, _ := c.do("GET", "/health", "", nil)
	c.add("Сервис доступен из интернета", "GET /health → 200", fmt.Sprintf("HTTP %d", code), code == 200)

	code, _, _ = c.do("GET", "/api/status", "", nil)
	c.add("Без токена не пускает", "без Authorization → 401", fmt.Sprintf("HTTP %d", code), code == 401)

	code, st, _ := c.do("GET", "/api/status", token, nil)
	limits, _ := st["limits"].(map[string]any)
	if code != 200 || limits == nil {
		return fmt.Errorf("status with token: HTTP %d", code)
	}
	rate, maxChars, numCtx, numPredict := num(limits, "rate_per_minute"), num(limits, "max_chars"), num(limits, "num_ctx"), num(limits, "num_predict")

	code, out, _ := c.chat([]message{{"user", strings.Repeat("я", maxChars+1)}})
	c.add("Слишком длинный контекст", fmt.Sprintf("%d символов → 413 до модели", maxChars+1), fmt.Sprintf("HTTP %d: %v", code, out["error"]), code == 413)

	question := "\n\nОтветь одним словом: текст прочитан?"
	filler := strings.Repeat("Локальная модель работает на домашнем сервере и отвечает клиентам через туннель. ", maxChars/60+1)
	filler = string([]rune(filler)[:maxChars-utf8.RuneCountInString(question)])
	code, out, _ = c.chat([]message{{"user", filler + question}})
	pt := num(out, "prompt_tokens")
	c.add("Контекст ровно на лимите", fmt.Sprintf("%d символов → 200, токенов промпта + %d ответа ≤ num_ctx %d", maxChars, numPredict, numCtx),
		fmt.Sprintf("HTTP %d, %d токенов промпта", code, pt), code == 200 && pt > 0 && pt+numPredict <= numCtx)

	// Two independent clients, each holding its own two-turn dialogue, at the same time.
	dialogs := [][2]string{
		{"Меня зовут Аня. Запомни это.", "Как меня зовут? Ответь одним словом."},
		{"Мой город — Казань. Запомни это.", "Какой мой город? Ответь одним словом."},
	}
	expectWord := []string{"Ан", "Казан"}
	var wg sync.WaitGroup
	results := make([]string, len(dialogs))
	oks := make([]bool, len(dialogs))
	for i, d := range dialogs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := []message{{"user", d[0]}}
			code1, o1, _ := c.chat(h)
			reply1, _ := o1["reply"].(string)
			h = append(h, message{"assistant", reply1}, message{"user", d[1]})
			code2, o2, _ := c.chat(h)
			reply2, _ := o2["reply"].(string)
			results[i] = fmt.Sprintf("клиент %d: HTTP %d/%d, в очереди перед ним %d и %d, ответ «%s»", i+1, code1, code2, num(o1, "ahead"), num(o2, "ahead"), strings.TrimSpace(reply2))
			oks[i] = code1 == 200 && code2 == 200 && strings.Contains(reply2, expectWord[i])
		}()
	}
	wg.Wait()
	c.add("Два клиента одновременно ведут диалог", "оба получают 200 и модель помнит историю своего диалога", strings.Join(results, "; "), oks[0] && oks[1])

	log.Printf("waiting 61s for the rate-limit window to reset")
	time.Sleep(61 * time.Second)
	burst := rate + 2
	codes := make([]int, burst)
	retry := make([]string, burst)
	for i := range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, h := c.chat([]message{{"user", fmt.Sprintf("Ответь одним словом: %d плюс 1?", i)}})
			codes[i] = code
			if h != nil {
				retry[i] = h.Get("Retry-After")
			}
		}()
	}
	wg.Wait()
	n200, n429, other, withRetry := 0, 0, 0, 0
	for i, code := range codes {
		switch code {
		case 200:
			n200++
		case 429:
			n429++
			if retry[i] != "" {
				withRetry++
			}
		default:
			other++
		}
	}
	c.add("Rate limit под залпом", fmt.Sprintf("%d запросов разом → %d × 200 и %d × 429 с Retry-After, без сбоев", burst, rate, burst-rate),
		fmt.Sprintf("200: %d, 429: %d (с Retry-After: %d), прочие: %d", n200, n429, withRetry, other), n200 == rate && n429 == burst-rate && withRetry == n429 && other == 0)

	code, _, _ = c.do("GET", "/health", "", nil)
	c.add("Сервис жив после нагрузки", "GET /health → 200", fmt.Sprintf("HTTP %d", code), code == 200)

	all := true
	for _, s := range c.steps {
		all = all && s.OK
	}
	u, _ := url.Parse(base)
	report := map[string]any{"checked_at": time.Now().UTC().Format(time.RFC3339), "via": u.Host, "model": st["model"], "limits": limits, "steps": c.steps, "all_ok": all}
	b, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile("day-30/check.json", append(b, '\n'), 0o644); err != nil {
		return err
	}
	log.Printf("all_ok=%v, written day-30/check.json", all)
	return nil
}
