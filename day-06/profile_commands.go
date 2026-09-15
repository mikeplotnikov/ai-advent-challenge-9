package main

// Day 12's half of the interface: the commands with which a user configures the agent
// for themselves. The host's requirement is exactly that — "надо чтобы условный
// пользователь вашего сервиса мог настроить себе агента под себя" (chat #2896) — so
// the profile is edited from the same prompt the user asks questions from, not by
// handing them a JSON file to fill in.

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

// handleProfileCommand runs /profile. The bool reports whether the line was it.
func handleProfileCommand(a *agent.Agent, in *bufio.Scanner, line string) (bool, error) {
	parts := strings.Fields(line)
	if len(parts) == 0 || parts[0] != "/profile" {
		return false, nil
	}
	if !a.ProfileState().Enabled {
		return true, fmt.Errorf("%w — запусти с -layers", agent.ErrProfileOff)
	}
	if len(parts) == 1 {
		printProfile(a)
		return true, nil
	}
	switch parts[1] {
	case "show":
		printProfile(a)
		return true, nil
	case "list":
		names, err := a.Profiles()
		if err != nil {
			return true, err
		}
		if len(names) == 0 {
			fmt.Fprintln(os.Stderr, "профилей ещё нет — /profile init заведёт первый")
			return true, nil
		}
		active := a.ProfileState().Name
		for _, name := range names {
			mark := "  "
			if name == active {
				mark = "* "
			}
			fmt.Fprintf(os.Stderr, "%s%s\n", mark, name)
		}
		return true, nil
	case "use":
		if len(parts) != 3 {
			return true, errors.New("формат: /profile use ИМЯ")
		}
		if err := a.UseProfile(parts[2]); err != nil {
			return true, err
		}
		fmt.Fprintf(os.Stderr, "активный профиль: %s\n", a.ProfileState().Name)
		return true, nil
	case "set":
		block, key, value, err := parseProfileSet(line)
		if err != nil {
			return true, err
		}
		if err := a.SetPreference(block, key, value); err != nil {
			return true, err
		}
		fmt.Fprintf(os.Stderr, "сохранено: профиль %s, блок %s (%s = %s)\n", a.ProfileState().Name, block, key, value)
		return true, nil
	case "drop":
		if len(parts) != 4 {
			return true, errors.New("формат: /profile drop style|constraints|context КЛЮЧ")
		}
		block, err := agent.ParseProfileBlock(parts[2])
		if err != nil {
			return true, err
		}
		if err := a.DropPreference(block, parts[3]); err != nil {
			return true, err
		}
		fmt.Fprintf(os.Stderr, "удалено: профиль %s, блок %s, %s\n", a.ProfileState().Name, block, parts[3])
		return true, nil
	case "pipeline":
		if len(parts) != 3 {
			return true, errors.New("формат: /profile pipeline direct|plan-answer")
		}
		pipeline, err := agent.ParseProfilePipeline(parts[2])
		if err != nil {
			return true, err
		}
		if err := a.SetPipeline(pipeline); err != nil {
			return true, err
		}
		fmt.Fprintf(os.Stderr, "конвейер профиля %s: %s\n", a.ProfileState().Name, pipeline)
		return true, nil
	case "route":
		if len(parts) < 4 {
			return true, errors.New("формат: /profile route ПОДСТРОКА = ИМЯ, или /profile route default ИМЯ")
		}
		if parts[2] == "default" && len(parts) == 4 {
			if err := a.SetDefaultProfile(parts[3]); err != nil {
				return true, err
			}
			fmt.Fprintf(os.Stderr, "профиль по умолчанию: %s\n", parts[3])
			return true, nil
		}
		rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/profile route"))
		match, use, ok := strings.Cut(rest, "=")
		if !ok {
			return true, errors.New("формат: /profile route ПОДСТРОКА = ИМЯ")
		}
		if err := a.SetRoute(strings.TrimSpace(match), strings.TrimSpace(use)); err != nil {
			return true, err
		}
		fmt.Fprintf(os.Stderr, "правило: «%s» → %s\n", strings.TrimSpace(match), strings.TrimSpace(use))
		return true, nil
	case "init":
		return true, runProfileInterview(a, in)
	}
	return true, errors.New("формат: /profile [show|list|use|set|drop|pipeline|route|init]")
}

// parseProfileSet reads "/profile set BLOCK KEY = VALUE". As with /remember, the value
// is everything after the first "=", so it may contain spaces and further equals signs.
func parseProfileSet(line string) (agent.ProfileBlock, string, string, error) {
	const usage = "формат: /profile set style|constraints|context КЛЮЧ = ЗНАЧЕНИЕ"
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/profile set"))
	blockName, rest, ok := strings.Cut(rest, " ")
	if !ok {
		return "", "", "", errors.New(usage)
	}
	block, err := agent.ParseProfileBlock(blockName)
	if err != nil {
		return "", "", "", err
	}
	key, value, ok := strings.Cut(rest, "=")
	if !ok {
		return "", "", "", errors.New(usage)
	}
	return block, strings.TrimSpace(key), strings.TrimSpace(value), nil
}

// runProfileInterview walks the fixed question list and writes the answers. It costs
// no model call: the questions are constants and the answers go straight into the
// blocks. An empty answer skips the question rather than saving a preference that
// says nothing.
func runProfileInterview(a *agent.Agent, in *bufio.Scanner) error {
	if in == nil {
		return errors.New("/profile init доступен только в диалоге")
	}
	fmt.Fprintf(os.Stderr, "интервью профиля %s — Enter пропускает вопрос\n", a.ProfileState().Name)
	answers := map[string]string{}
	for _, q := range agent.ProfileInterview() {
		fmt.Fprintf(os.Stderr, "[%s] %s (%s)\n> ", q.Block, q.Ask, q.Hint)
		if !in.Scan() {
			return errors.New("интервью прервано")
		}
		answers[q.Key] = in.Text()
	}
	written, err := a.InitProfile(answers)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "профиль %s: записано %d предпочтений\n", a.ProfileState().Name, written)
	return nil
}

func printProfile(a *agent.Agent) {
	p := a.ProfileState()
	sent := map[agent.ProfileBlock]bool{}
	for _, b := range p.Inject {
		sent[b] = true
	}
	mark := func(b agent.ProfileBlock) string {
		if sent[b] {
			return "уходит в запрос"
		}
		return "хранится, в запрос не уходит"
	}
	fmt.Fprintf(os.Stderr, "профиль %s · %s · ≈%d токенов · конвейер %s\n", p.Name, p.Path, p.Tokens, p.Pipeline)
	for _, group := range []struct {
		block   agent.ProfileBlock
		entries []agent.MemoryEntry
	}{{agent.BlockStyle, p.Style}, {agent.BlockConstraints, p.Constraints}, {agent.BlockContext, p.Context}} {
		fmt.Fprintf(os.Stderr, "  %s: %s\n", group.block, mark(group.block))
		printEntries("    ", group.entries)
	}
	if len(p.Available) > 1 {
		fmt.Fprintf(os.Stderr, "  другие профили: %s\n", strings.Join(p.Available, ", "))
	}
	if len(p.Rules) > 0 {
		fmt.Fprintf(os.Stderr, "  роутер (%s): %s\n", routeMode(p.Route), p.RouterPath)
		for _, rule := range p.Rules {
			fmt.Fprintf(os.Stderr, "    «%s» → %s\n", rule.Match, rule.Use)
		}
	}
}

func routeMode(on bool) string {
	if on {
		return "выбирает профиль на каждый вопрос"
	}
	return "выключен, -profile-route включает"
}

// profileSpend is the part of the spend line that says what personalization cost.
func profileSpend(reply agent.Reply) string {
	if reply.Profile == "" {
		return ""
	}
	if !reply.PlanAttempted {
		return fmt.Sprintf(" · профиль: %s", reply.Profile)
	}
	return fmt.Sprintf(" · профиль: %s · план: +%d вход, +%d выход",
		reply.Profile, reply.PlanUsage.PromptTokens, reply.PlanUsage.CompletionTokens)
}
