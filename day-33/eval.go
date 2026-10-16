package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// scenario is one support question with checks a machine can decide: every group in
// Must needs at least one of its phrases in the answer, no MustNot phrase may appear.
type scenario struct {
	Name      string     `json:"name"`
	Purpose   string     `json:"purpose"`
	UserID    string     `json:"user_id"`
	TicketID  string     `json:"ticket_id"`
	Question  string     `json:"question"`
	NoContext bool       `json:"no_context,omitempty"`
	Must      [][]string `json:"must,omitempty"`
	MustNot   []string   `json:"must_not,omitempty"`
	MustCall  []string   `json:"must_call,omitempty"`
	MaxRunes  int        `json:"max_runes,omitempty"`
}

type outcome struct {
	Scenario scenario `json:"scenario"`
	Pass     bool     `json:"pass"`
	Failures []string `json:"failures,omitempty"`
	Reply    reply    `json:"reply"`
	Error    string   `json:"error,omitempty"`
}

// check returns the failed conditions; an empty result is a pass. Matching ignores case.
func check(s scenario, r reply) []string {
	answer := strings.ToLower(r.Answer)
	var failures []string
	for _, group := range s.Must {
		if !slices.ContainsFunc(group, func(p string) bool { return strings.Contains(answer, strings.ToLower(p)) }) {
			failures = append(failures, "нет ни одного из "+strings.Join(group, " | "))
		}
	}
	for _, p := range s.MustNot {
		if strings.Contains(answer, strings.ToLower(p)) {
			failures = append(failures, "есть запрещённое «"+p+"»")
		}
	}
	for _, tool := range s.MustCall {
		if !slices.Contains(r.ToolsUsed, tool) {
			failures = append(failures, "модель не вызвала "+tool)
		}
	}
	if s.MaxRunes > 0 && utf8.RuneCountInString(r.Answer) > s.MaxRunes {
		failures = append(failures, fmt.Sprintf("ответ длиннее %d символов", s.MaxRunes))
	}
	return failures
}

func runEval(ctx context.Context, a *assistant, path, outPath string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var scenarios []scenario
	if err := json.Unmarshal(raw, &scenarios); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	started := time.Now()
	var outcomes []outcome
	passed, cost := 0, 0.0
	for _, s := range scenarios {
		r, err := a.reply(ctx, request{UserID: s.UserID, TicketID: s.TicketID, Message: s.Question, NoContext: s.NoContext})
		o := outcome{Scenario: s, Reply: r}
		if err != nil {
			o.Error = err.Error()
			o.Failures = []string{"ошибка: " + err.Error()}
		} else {
			o.Failures = check(s, r)
		}
		o.Pass = len(o.Failures) == 0
		if o.Pass {
			passed++
		}
		if r.Cost != nil {
			cost += *r.Cost
		}
		outcomes = append(outcomes, o)
		mark := "PASS"
		if !o.Pass {
			mark = "FAIL"
		}
		fmt.Printf("%s  %-18s инструменты модели: %-28s %.1f с  %s\n", mark, s.Name, strings.Join(r.ToolsUsed, ","), r.Seconds, strings.Join(o.Failures, "; "))
	}
	fmt.Printf("\nИтого: %d из %d · $%.5f · %.0f с\n", passed, len(scenarios), cost, time.Since(started).Seconds())
	data, err := json.MarshalIndent(map[string]any{"run_at": started.Format(time.RFC3339), "model": a.modelName, "passed": passed, "total": len(scenarios), "cost_usd": cost, "outcomes": outcomes}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Println("Ответы и шаги → " + outPath)
	fmt.Print("\nТаблица для RESULTS.md:\n\n" + table(outcomes))
	if passed != len(scenarios) {
		return fmt.Errorf("не прошли %d сценариев", len(scenarios)-passed)
	}
	return nil
}

// table renders the outcomes as the Markdown table of RESULTS.md, so the numbers there
// come from a run, not from typing.
func table(outcomes []outcome) string {
	var b strings.Builder
	b.WriteString("| Сценарий | Что проверяет | Итог | Модель сама вызвала | Вход (кэш) / выход, токены | Цена | Секунды |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, o := range outcomes {
		mark := "PASS"
		if !o.Pass {
			mark = "FAIL"
		}
		tools := strings.Join(o.Reply.ToolsUsed, ", ")
		if tools == "" {
			tools = "—"
		}
		cost := "—"
		if o.Reply.Cost != nil {
			cost = fmt.Sprintf("$%.5f", *o.Reply.Cost)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %d (%d) / %d | %s | %.1f |\n", o.Scenario.Name, o.Scenario.Purpose, mark, tools,
			o.Reply.Prompt, o.Reply.Cached, o.Reply.Output, cost, o.Reply.Seconds)
	}
	return b.String()
}
