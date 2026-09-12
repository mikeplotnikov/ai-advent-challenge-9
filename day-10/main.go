// Day 10 runs one requirements-gathering scenario through three context strategies.
// It writes the provider's answers and usage as JSONL; the human report is generated
// from that file, so no measured number is typed into documentation by hand.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

type requirement struct {
	Prompt string
}

type branchScenario struct {
	Name      string
	Facts     []requirement
	Expected  expectedSpec
	Forbidden []string
}

type expectedSpec struct {
	Product       string
	Deadline      string
	Budget        string
	Roles         []string
	Integration   string
	Auth          string
	Notifications string
	Rollout       string
}

type answerCheck struct {
	Branch     string   `json:"branch"`
	Attempt    int      `json:"attempt"`
	Answer     string   `json:"answer"`
	Expected   []string `json:"expected"`
	Forbidden  []string `json:"forbidden"`
	Passed     int      `json:"passed"`
	Total      int      `json:"total"`
	Correct    bool     `json:"correct"`
	ParseError string   `json:"parseError,omitempty"`
}

type usability struct {
	EnteredMessages  int `json:"enteredMessages"`
	ReplayedMessages int `json:"replayedMessages"`
	ControlActions   int `json:"controlActions"`
	ProviderCalls    int `json:"providerCalls"`
}

type report struct {
	Run         string                `json:"run"`
	Strategy    agent.ContextStrategy `json:"strategy"`
	Window      int                   `json:"windowMessages"`
	Checks      []answerCheck         `json:"checks"`
	Passed      int                   `json:"passed"`
	TotalChecks int                   `json:"totalChecks"`
	Stable      int                   `json:"stableAnswers"`
	Answers     int                   `json:"answers"`
	Spend       agent.Totals          `json:"spend"`
	FactSpend   agent.Totals          `json:"factSpend"`
	Usability   usability             `json:"usability"`
}

var common = []requirement{
	{"Продукт называется ATLAS-17. Зафиксируй это как устойчивое требование."},
	{"Срок запуска — 2026-10-21. Зафиксируй точно."},
	{"Бюджет — 450000 RUB. Зафиксируй точное значение и валюту."},
	{"Роли пользователей — employee и admin. Зафиксируй обе."},
	{"Обязательная интеграция — Google Calendar. Зафиксируй."},
}

var branches = []branchScenario{
	{
		Name: "variant-a",
		Facts: []requirement{
			{"В варианте A авторизация — SSO. Зафиксируй для этой ветки."},
			{"В варианте A уведомления идут в Telegram. Зафиксируй."},
			{"В варианте A пилот рассчитан на 20 employees. Зафиксируй."},
		},
		Expected: expectedSpec{
			Product: "ATLAS-17", Deadline: "2026-10-21", Budget: "450000 RUB", Roles: []string{"employee", "admin"},
			Integration: "Google Calendar", Auth: "SSO", Notifications: "Telegram", Rollout: "20 employees",
		},
		Forbidden: []string{"email/password", "email notifications", "60 employees"},
	},
	{
		Name: "variant-b",
		Facts: []requirement{
			{"В варианте B авторизация — email/password. Зафиксируй для этой ветки."},
			{"В варианте B уведомления — email notifications. Зафиксируй."},
			{"В варианте B запуск рассчитан на 60 employees. Зафиксируй."},
		},
		Expected: expectedSpec{
			Product: "ATLAS-17", Deadline: "2026-10-21", Budget: "450000 RUB", Roles: []string{"employee", "admin"},
			Integration: "Google Calendar", Auth: "email/password", Notifications: "email notifications", Rollout: "60 employees",
		},
		Forbidden: []string{"SSO", "Telegram", "20 employees"},
	},
}

const finalQuestion = "Верни итоговое ТЗ активного варианта одним JSON-объектом с полями product, deadline, budget, roles, integration, auth, notifications, rollout. Значения перенеси точно."

func main() {
	out := flag.String("out", "", "обязательный путь для трёх JSONL-отчётов")
	model := flag.String("model", "", "модель; пусто — DEEPSEEK_MODEL или дефолт клиента")
	window := flag.Int("window-messages", 10, "чётный размер окна sliding и facts")
	flag.Parse()
	if strings.TrimSpace(*out) == "" {
		fail(errors.New("-out обязателен, например: -out day-10/strategies.jsonl"))
	}
	if *window < 2 || *window%2 != 0 {
		fail(fmt.Errorf("-window-messages=%d: нужны минимум два и чётное число", *window))
	}

	run := fmt.Sprintf("day10-%d", time.Now().UnixNano())
	reports := make([]report, 0, 3)
	for _, strategy := range []agent.ContextStrategy{agent.ContextSliding, agent.ContextFacts, agent.ContextBranching} {
		rep, err := runStrategy(context.Background(), run, strategy, *model, *window)
		if err != nil {
			fail(fmt.Errorf("%s: %w", strategy, err))
		}
		reports = append(reports, rep)
		fmt.Printf("%s: качество %d/%d · стабильных ответов %d/%d · %s\n",
			strategy, rep.Passed, rep.TotalChecks, rep.Stable, rep.Answers, rep.Spend)
	}
	if err := writeReports(*out, reports); err != nil {
		fail(err)
	}
}

func runStrategy(ctx context.Context, run string, strategy agent.ContextStrategy, model string, window int) (report, error) {
	rep := report{Run: run, Strategy: strategy, Window: window}
	if strategy == agent.ContextBranching {
		return runBranching(ctx, rep, model)
	}

	// Sliding and facts cannot return to a checkpoint. To produce both requested
	// variants from the same shared requirements, the user must start a second
	// conversation and replay the five common messages. Those calls are part of the
	// token cost and the user-effort comparison, not erased from the measurement.
	for branchIndex, branch := range branches {
		a, err := build(rep.Run, strategy, model, window)
		if err != nil {
			return rep, err
		}
		for _, item := range append(append([]requirement{}, common...), branch.Facts...) {
			if _, err := a.Ask(ctx, item.Prompt); err != nil {
				return rep, err
			}
			rep.Usability.EnteredMessages++
		}
		if branchIndex > 0 {
			rep.Usability.ReplayedMessages += len(common)
		}
		if err := runFinalChecks(ctx, a, branch, &rep); err != nil {
			return rep, err
		}
		rep.Spend = addTotals(rep.Spend, a.Totals())
		rep.FactSpend = addTotals(rep.FactSpend, a.StrategyState().FactSpend)
	}
	rep.Usability.ProviderCalls = rep.Spend.Calls
	return rep, nil
}

func runBranching(ctx context.Context, rep report, model string) (report, error) {
	a, err := build(rep.Run, agent.ContextBranching, model, 0)
	if err != nil {
		return rep, err
	}
	for _, item := range common {
		if _, err := a.Ask(ctx, item.Prompt); err != nil {
			return rep, err
		}
		rep.Usability.EnteredMessages++
	}
	if err := a.Checkpoint("shared"); err != nil {
		return rep, err
	}
	rep.Usability.ControlActions++
	for _, branch := range branches {
		if err := a.Fork(branch.Name, "shared"); err != nil {
			return rep, err
		}
		rep.Usability.ControlActions++
	}
	for _, branch := range branches {
		if err := a.Switch(branch.Name); err != nil {
			return rep, err
		}
		rep.Usability.ControlActions++
		for _, item := range branch.Facts {
			if _, err := a.Ask(ctx, item.Prompt); err != nil {
				return rep, err
			}
			rep.Usability.EnteredMessages++
		}
		if err := runFinalChecks(ctx, a, branch, &rep); err != nil {
			return rep, err
		}
	}
	rep.Spend = a.Totals()
	rep.Usability.ProviderCalls = rep.Spend.Calls
	return rep, nil
}

func runFinalChecks(ctx context.Context, a *agent.Agent, branch branchScenario, rep *report) error {
	for attempt := 1; attempt <= 2; attempt++ {
		reply, err := a.Ask(ctx, finalQuestion)
		if err != nil {
			return err
		}
		rep.Usability.EnteredMessages++
		check := score(branch, attempt, reply.Text)
		rep.Checks = append(rep.Checks, check)
		rep.Passed += check.Passed
		rep.TotalChecks += check.Total
		rep.Answers++
		if check.Correct {
			rep.Stable++
		}
	}
	return nil
}

func build(run string, strategy agent.ContextStrategy, model string, window int) (*agent.Agent, error) {
	zero := 0.0
	cfg := agent.Config{
		Name: "day-10-" + string(strategy),
		SystemPrompt: "Метка прогона " + run + "-" + string(strategy) + ". Ты собираешь техническое задание. " +
			"На сообщение с требованием отвечай JSON {\"status\":\"accepted\"}. На просьбу вернуть ТЗ отвечай только запрошенным JSON без пояснений.",
		Model:           model,
		Temperature:     &zero,
		MaxTokens:       256,
		ResponseFormat:  "json_object",
		Thinking:        "disabled",
		ContextStrategy: strategy,
		WindowMessages:  window,
	}
	if strategy == agent.ContextBranching {
		cfg.WindowMessages = 0
	}
	// Both non-branching sessions intentionally share the same prefix. That makes
	// replay cost observable without defeating the provider's real prefix cache.
	return agent.FromEnv(cfg)
}

func score(branch branchScenario, attempt int, answer string) answerCheck {
	expected := []string{
		branch.Expected.Product, branch.Expected.Deadline, branch.Expected.Budget,
		branch.Expected.Roles[0], branch.Expected.Roles[1], branch.Expected.Integration,
		branch.Expected.Auth, branch.Expected.Notifications, branch.Expected.Rollout,
	}
	check := answerCheck{Branch: branch.Name, Attempt: attempt, Answer: answer,
		Expected: expected, Forbidden: append([]string(nil), branch.Forbidden...), Total: len(expected) + len(branch.Forbidden)}
	var got struct {
		Product       string   `json:"product"`
		Deadline      string   `json:"deadline"`
		Budget        string   `json:"budget"`
		Roles         []string `json:"roles"`
		Integration   string   `json:"integration"`
		Auth          string   `json:"auth"`
		Notifications string   `json:"notifications"`
		Rollout       string   `json:"rollout"`
	}
	if err := json.Unmarshal([]byte(answer), &got); err != nil {
		check.ParseError = err.Error()
		return check
	}
	for _, pair := range [][2]string{
		{got.Product, branch.Expected.Product}, {got.Deadline, branch.Expected.Deadline},
		{got.Budget, branch.Expected.Budget}, {got.Integration, branch.Expected.Integration},
		{got.Auth, branch.Expected.Auth}, {got.Notifications, branch.Expected.Notifications},
		{got.Rollout, branch.Expected.Rollout},
	} {
		if pair[0] == pair[1] {
			check.Passed++
		}
	}
	for _, role := range branch.Expected.Roles {
		// Each expected role earns its point only while the returned list is not
		// larger than the contract. Otherwise an answer that invents superadmin
		// would receive the same perfect score as the exact two-role answer.
		if len(got.Roles) <= len(branch.Expected.Roles) && containsExact(got.Roles, role) {
			check.Passed++
		}
	}
	haystack := strings.ToLower(answer)
	for _, marker := range branch.Forbidden {
		if !strings.Contains(haystack, strings.ToLower(marker)) {
			check.Passed++
		}
	}
	check.Correct = check.Passed == check.Total
	return check
}

func containsExact(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func addTotals(a, b agent.Totals) agent.Totals {
	a.Calls += b.Calls
	a.Failed += b.Failed
	a.PromptTokens += b.PromptTokens
	a.CompletionTokens += b.CompletionTokens
	a.ReasoningTokens += b.ReasoningTokens
	a.CachedTokens += b.CachedTokens
	a.MissedTokens += b.MissedTokens
	a.Cost += b.Cost
	a.Unpriced += b.Unpriced
	return a
}

func writeReports(path string, reports []report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("каталог отчёта: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".day10-*.tmp")
	if err != nil {
		return fmt.Errorf("временный отчёт: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(tmpName)
		}
	}()
	enc := json.NewEncoder(tmp)
	enc.SetEscapeHTML(false)
	for _, row := range reports {
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("JSONL: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("закрытие отчёта: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("публикация отчёта: %w", err)
	}
	ok = true
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
