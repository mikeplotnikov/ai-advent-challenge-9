package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"io"
	"regexp"
	"strings"
)

func strictObject(raw string, fields ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(strings.NewReader(raw))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("expected JSON object")
	}
	m := map[string]json.RawMessage{}
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return nil, e
		}
		key := k.(string)
		if _, ok := m[key]; ok {
			return nil, fmt.Errorf("duplicate field %s", key)
		}
		var v json.RawMessage
		if e = d.Decode(&v); e != nil {
			return nil, e
		}
		m[key] = v
	}
	if _, e = d.Token(); e != nil {
		return nil, e
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	if len(m) != len(fields) {
		return nil, fmt.Errorf("wrong fields")
	}
	for _, f := range fields {
		if v, ok := m[f]; !ok || string(v) == "null" {
			return nil, fmt.Errorf("missing/null field %s", f)
		}
	}
	return m, nil
}
func parseAnswer(raw string, chunks []Candidate) (Answer, error) {
	m, e := strictObject(raw, "answer", "unknown", "clarification", "sources")
	if e != nil {
		return Answer{}, e
	}
	var a Answer
	for key, target := range map[string]any{"answer": &a.Answer, "unknown": &a.Unknown, "clarification": &a.Clarification} {
		if e = json.Unmarshal(m[key], target); e != nil {
			return a, e
		}
	}
	var sources []json.RawMessage
	if e = json.Unmarshal(m["sources"], &sources); e != nil {
		return a, e
	}
	a.Sources = []Source{}
	for _, raw := range sources {
		if _, e = strictObject(string(raw), "source", "section", "chunk_id", "quote"); e != nil {
			return a, e
		}
		var s Source
		if e = json.Unmarshal(raw, &s); e != nil {
			return a, e
		}
		a.Sources = append(a.Sources, s)
	}
	if strings.TrimSpace(a.Answer) == "" {
		return a, fmt.Errorf("empty answer")
	}
	if a.Unknown {
		if a.Answer != "Не знаю." || strings.TrimSpace(a.Clarification) == "" || len(a.Sources) != 0 {
			return a, fmt.Errorf("unknown requires не знаю, clarification and empty sources")
		}
		return a, nil
	}
	if a.Clarification != "" || len(a.Sources) == 0 {
		return a, fmt.Errorf("substantive answer requires sources and empty clarification")
	}
	for _, s := range a.Sources {
		found := false
		for _, c := range chunks {
			if s.Source == c.Source && s.Section == c.Section && s.ChunkID == c.ChunkID && strings.TrimSpace(s.Quote) != "" && strings.Contains(c.Text, s.Quote) {
				found = true
				break
			}
		}
		if !found {
			return a, fmt.Errorf("invalid source/section/chunk/quote for %s (%s); quote must retain Markdown and line breaks", s.Source, s.ChunkID)
		}
	}
	return a, nil
}
func answerQuestion(ctx context.Context, client *llm.Client, q *QuestionRun) error {
	chunks := q.Context()
	if len(chunks) == 0 {
		q.Answer = Answer{"Не знаю.", true, "Уточните термин или задачу, о которой спрашиваете.", []Source{}}
		q.Checks = checkAnswer(q.Question, q.Answer, "empty_context")
		return nil
	}
	var parsed Answer
	call, e := callModel(ctx, client, "answer", answerMessages(q.Question.Question, chunks), answerOptions(), func(raw string) error { var err error; parsed, err = parseAnswer(raw, chunks); return err })
	q.AnswerCall = &call
	if e != nil {
		return e
	}
	q.Answer = parsed
	reason := ""
	if parsed.Unknown {
		reason = "model_unknown"
	}
	q.Checks = checkAnswer(q.Question, parsed, reason)
	return nil
}
func checkAnswer(q Question, a Answer, reason string) Checks {
	c := Checks{Schema: true, Sources: "pass", Quotes: "pass", FactOutcome: "wrong", MatchedFacts: []string{}, RefusalReason: reason}
	if q.Kind == "chat" {
		c.FactOutcome = "not_assessed"
	}
	if a.Unknown {
		c.Sources = "not_applicable"
		c.Quotes = "not_applicable"
		c.FactOutcome = "unknown"
		if q.Kind == "out_of_base" {
			c.FactOutcome = "declined"
		}
		return c
	}
	for _, f := range q.Facts {
		for _, p := range f.Patterns {
			re, e := regexp.Compile("(?i)" + p)
			if e == nil && re.MatchString(a.Answer) {
				c.MatchedFacts = append(c.MatchedFacts, f.Name)
				break
			}
		}
	}
	if len(c.MatchedFacts) > 0 {
		c.FactOutcome = "partial"
		if len(c.MatchedFacts) == len(q.Facts) {
			c.FactOutcome = "correct"
		}
	}
	return c
}
func containsString(v []string, w string) bool {
	for _, s := range v {
		if s == w {
			return true
		}
	}
	return false
}
