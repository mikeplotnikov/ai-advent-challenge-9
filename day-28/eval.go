package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/rag"
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
	ID     string `json:"id"`
	Repeat int    `json:"repeat"`
	Result result `json:"result"`
}
type comparison struct {
	ID       string          `json:"id"`
	Question string          `json:"question"`
	Local    result          `json:"local"`
	Cloud    json.RawMessage `json:"cloud"`
	Context  []fragment      `json:"context"`
	Messages []message       `json:"messages"`
}

func (a *app) evaluate(path string) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	meta, e := a.config(context.Background())
	if e != nil {
		return e
	}
	qraw, e := os.ReadFile(rag.ProjectPath("day-22/eval/questions.json"))
	if e != nil {
		return e
	}
	var questions []question
	if e = json.Unmarshal(qraw, &questions); e != nil {
		return e
	}
	craw, e := os.ReadFile(rag.ProjectPath("day-24/showcase.json"))
	if e != nil {
		return e
	}
	var archive struct {
		Meta struct {
			StartedAt string            `json:"started_at"`
			Hashes    map[string]string `json:"hashes"`
		} `json:"meta"`
		Questions []json.RawMessage `json:"questions"`
	}
	if e = json.Unmarshal(craw, &archive); e != nil {
		return e
	}
	if archive.Meta.Hashes["index"] != a.indexSHA {
		return fmt.Errorf("архив и текущий индекс различаются")
	}
	meta["started_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	meta["questions_sha256"] = sha(qraw)
	meta["cloud_sha256"] = sha(craw)
	meta["cloud_date"] = archive.Meta.StartedAt
	capture := struct {
		Meta        map[string]any `json:"meta"`
		Questions   []question     `json:"questions"`
		Runs        []measured     `json:"runs"`
		Comparisons []comparison   `json:"comparisons"`
	}{meta, questions, []measured{}, []comparison{}}
	save := func() error {
		if _, e = f.Seek(0, 0); e != nil {
			return e
		}
		if e = f.Truncate(0); e != nil {
			return e
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if e = enc.Encode(capture); e != nil {
			return e
		}
		return f.Sync()
	}
	if e = save(); e != nil {
		return e
	}
	for repeat := 1; repeat <= 3; repeat++ {
		for _, q := range questions {
			fmt.Printf("local %s repeat %d\n", q.ID, repeat)
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			r := a.runQuestion(ctx, q.Question, func(string, any) {})
			cancel()
			capture.Runs = append(capture.Runs, measured{q.ID, repeat, r})
			if e = save(); e != nil {
				return e
			}
			fmt.Printf("%dms error=%s\n", r.TotalMS, r.Error)
		}
	}
	for _, raw := range archive.Questions {
		var q struct {
			Question   question `json:"question"`
			Candidates []struct {
				fragment
				Position int `json:"position"`
			} `json:"candidates"`
			AnswerCall *struct {
				Messages []message `json:"messages"`
			} `json:"answer_call"`
		}
		if e = json.Unmarshal(raw, &q); e != nil {
			return e
		}
		chunks := []fragment{}
		for position := 1; position <= 3; position++ {
			for _, c := range q.Candidates {
				if c.Position == position {
					chunks = append(chunks, c.fragment)
				}
			}
		}
		cmp := comparison{ID: q.Question.ID, Question: q.Question.Question, Cloud: raw, Context: chunks, Messages: []message{}}
		if q.AnswerCall != nil {
			cmp.Messages = q.AnswerCall.Messages
			fmt.Printf("fixed-context %s\n", cmp.ID)
			start := time.Now()
			r := result{Question: cmp.Question, StartedAt: start.UTC().Format(time.RFC3339Nano), Context: chunks, Attempts: []attempt{}}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			var err error
			r.Answer, r.Attempts, err = a.generate(ctx, cmp.Messages, chunks)
			cancel()
			r.GenerationMS = time.Since(start).Milliseconds()
			r.TotalMS = r.GenerationMS
			if err != nil {
				r.Error = err.Error()
			}
			cmp.Local = r
		} else {
			cmp.Local = result{Question: cmp.Question, Context: chunks, Attempts: []attempt{}, Reason: "no_archived_generation"}
		}
		capture.Comparisons = append(capture.Comparisons, cmp)
		if e = save(); e != nil {
			return e
		}
	}
	return nil
}
