package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
	"os"
	"strings"
	"time"
)

type question struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Question    string `json:"question"`
	Expectation string `json:"expectation"`
	Facts       []struct {
		Name     string   `json:"name"`
		Patterns []string `json:"patterns"`
	} `json:"facts"`
}
type measured struct {
	ID      string `json:"id"`
	Repeat  int    `json:"repeat"`
	Profile string `json:"profile"`
	Result  result `json:"result"`
}
type control struct {
	Profile    string          `json:"profile"`
	Kind       string          `json:"kind"`
	Request    any             `json:"request"`
	Response   json.RawMessage `json:"response"`
	Error      string          `json:"error,omitempty"`
	DurationMS int64           `json:"duration_ms"`
}
type capture struct {
	Meta       map[string]any `json:"meta"`
	Profiles   []profile      `json:"profiles"`
	Questions  []question     `json:"questions"`
	Retrievals []measured     `json:"retrievals"`
	Runs       []measured     `json:"runs"`
	Controls   []control      `json:"controls"`
}

func (a *app) evaluate(path string, diagnostic bool, ids []string) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	meta, e := a.config(context.Background())
	if e != nil {
		return e
	}
	qr, e := os.ReadFile(rag.ProjectPath("day-22/eval/questions.json"))
	if e != nil {
		return e
	}
	var questions []question
	if e = json.Unmarshal(qr, &questions); e != nil {
		return e
	}
	if diagnostic {
		filtered := []question{}
		for _, q := range questions {
			if q.ID == "q05" || q.ID == "q15" || q.ID == "g01" || q.ID == "x02" {
				filtered = append(filtered, q)
			}
		}
		questions = filtered
	}
	selected := []profile{}
	identities := map[string]identity{}
	models := map[string]any{}
	for _, id := range ids {
		p, e := findProfile(id)
		if e != nil {
			return e
		}
		selected = append(selected, p)
		if _, ok := identities[p.Model]; !ok {
			identity, e := a.pinned(context.Background(), p)
			if e != nil {
				return e
			}
			identities[p.Model] = identity
			var info map[string]any
			if _, e = a.call(context.Background(), "/api/show", map[string]string{"model": p.Model}, &info); e != nil {
				return e
			}
			models[p.Model] = info
		}
	}
	meta["profile_order"] = strings.Join(ids, ",")
	meta["started_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	meta["questions_sha256"] = sha(qr)
	meta["diagnostic"] = diagnostic
	meta["identities"] = identities
	meta["model_details"] = models
	meta["resource_sampling_ms"] = 250
	meta["timing_protocol"] = "Shared retrieval once per question. Total is retrieval_ms + sequential generation_ms. Profiles rotate by repeat, warm control before each block. Sampled runner RSS includes all Ollama runners; selected generator size is from /api/ps. Cold is separate."
	c := capture{meta, selected, questions, []measured{}, []measured{}, []control{}}
	save := func() error {
		data, e := json.MarshalIndent(c, "", "  ")
		if e != nil {
			return e
		}
		if _, e = f.Seek(0, 0); e != nil {
			return e
		}
		if e = f.Truncate(0); e != nil {
			return e
		}
		if _, e = f.Write(data); e != nil {
			return e
		}
		return f.Sync()
	}
	if e = save(); e != nil {
		return e
	}
	for _, q := range questions {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		r := a.retrieve(ctx, q.Question, func(string, any) {})
		cancel()
		if r.Error != "" {
			return fmt.Errorf("retrieval %s: %s", q.ID, r.Error)
		}
		c.Retrievals = append(c.Retrievals, measured{q.ID, 0, "retrieval", r})
		if e = save(); e != nil {
			return e
		}
	}
	repeats := 3
	if diagnostic {
		repeats = 1
	}
	for n := 1; n <= repeats; n++ {
		for j := range selected {
			p := selected[(j+n-1)%len(selected)]
			b := a.configured(p)
			// Remove every other generator; embedding stays shared. Unload selected model to reset the same context before warmup.
			for model := range identities {
				var out map[string]any
				req := map[string]any{"model": model, "keep_alive": 0}
				_, e = a.call(context.Background(), "/api/generate", req, &out)
				if e != nil {
					return e
				}
			}
			warm := map[string]any{"model": p.Model, "messages": []message{{"user", "Ответь одним словом: готов."}}, "think": false, "truncate": false, "shift": false, "stream": false, "options": b.options()}
			var out map[string]any
			t := time.Now()
			raw, e := a.call(context.Background(), "/api/chat", warm, &out)
			ctrl := control{p.ID, "warmup", warm, raw, "", time.Since(t).Milliseconds()}
			if e != nil {
				ctrl.Error = e.Error()
			}
			c.Controls = append(c.Controls, ctrl)
			if e = save(); e != nil {
				return e
			}
			if ctrl.Error != "" {
				return fmt.Errorf("warmup %s: %s", p.ID, ctrl.Error)
			}
			for i, q := range questions {
				fmt.Printf("%s repeat%d %s\n", p.ID, n, q.ID)
				ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
				r := a.generateRetrieved(ctx, c.Retrievals[i].Result, p)
				cancel()
				c.Runs = append(c.Runs, measured{q.ID, n, p.ID, r})
				if e = save(); e != nil {
					return e
				}
				fmt.Printf("%dms error=%s\n", r.GenerationMS, r.Error)
			}
		}
	}
	meta["completed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	meta["profile_order"] = strings.Join(ids, ",")
	return save()
}
