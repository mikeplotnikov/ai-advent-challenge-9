package main

// Day 11 commands: the interface names a target, the agent decides the layer.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

// parseInject turns "-inject short,working,long" into the agent's layer list. An
// empty flag value means no layer travels, which is a legitimate control.
func parseInject(value string) ([]agent.MemoryLayer, error) {
	out := []agent.MemoryLayer{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		layer := agent.MemoryLayer(part)
		switch layer {
		case agent.LayerShort, agent.LayerWorking, agent.LayerLong:
		default:
			return nil, fmt.Errorf("-inject: слой %q неизвестен, допустимы short, working, long", part)
		}
		out = append(out, layer)
	}
	return out, nil
}

// handleMemoryCommand runs /remember, /drop, /task and /memory. The bool reports
// whether the line was one of them.
func handleMemoryCommand(a *agent.Agent, line string) (bool, error) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false, nil
	}
	switch parts[0] {
	case "/remember", "/drop", "/task", "/memory":
	default:
		return false, nil
	}
	if !a.MemoryState().Enabled {
		return true, fmt.Errorf("%w — запусти с -layers", agent.ErrMemoryOff)
	}
	switch parts[0] {
	case "/memory":
		printMemory(a)
		return true, nil
	case "/remember":
		target, key, value, err := parseRemember(line)
		if err != nil {
			return true, err
		}
		if err := a.Remember(target, key, value); err != nil {
			return true, err
		}
		layer, _ := target.Layer()
		fmt.Fprintf(os.Stderr, "сохранено: %s → %s слой (%s = %s)\n", target, layerName(layer), key, value)
		return true, nil
	case "/drop":
		if len(parts) != 3 {
			return true, errors.New("формат: /drop task|profile|decision|knowledge КЛЮЧ")
		}
		target := agent.MemoryTarget(parts[1])
		if err := a.Drop(target, parts[2]); err != nil {
			return true, err
		}
		fmt.Fprintf(os.Stderr, "удалено: %s %s\n", target, parts[2])
		return true, nil
	default: // /task
		return true, handleTask(a, parts[1:])
	}
}

// parseRemember reads "/remember TARGET KEY = VALUE". The value is everything after
// the first " = ", so it may contain spaces and further equals signs.
func parseRemember(line string) (agent.MemoryTarget, string, string, error) {
	const usage = "формат: /remember task|profile|decision|knowledge КЛЮЧ = ЗНАЧЕНИЕ"
	rest := strings.TrimSpace(strings.TrimPrefix(line, "/remember"))
	target, rest, ok := strings.Cut(rest, " ")
	if !ok {
		return "", "", "", errors.New(usage)
	}
	key, value, ok := strings.Cut(rest, "=")
	if !ok {
		return "", "", "", errors.New(usage)
	}
	return agent.MemoryTarget(target), strings.TrimSpace(key), strings.TrimSpace(value), nil
}

func handleTask(a *agent.Agent, args []string) error {
	if len(args) == 0 {
		task := a.MemoryState().Task
		if task == "" {
			task = "нет"
		}
		fmt.Fprintln(os.Stderr, "активная задача:", task)
		return nil
	}
	switch {
	case args[0] == "new" && len(args) == 2:
		if err := a.StartTask(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "задача %q создана и активна\n", args[1])
	case args[0] == "use" && len(args) == 2:
		if err := a.UseTask(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "активная задача: %s\n", args[1])
	case args[0] == "done" && len(args) == 1:
		task := a.MemoryState().Task
		if err := a.FinishTask(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "задача %q завершена: её рабочая память удалена, долговременная не тронута\n", task)
	default:
		return errors.New("формат: /task · /task new ИМЯ · /task use ИМЯ · /task done")
	}
	return nil
}

func layerName(layer agent.MemoryLayer) string {
	switch layer {
	case agent.LayerShort:
		return "краткосрочный"
	case agent.LayerWorking:
		return "рабочий"
	default:
		return "долговременный"
	}
}

// printMemory shows the three layers side by side: where each lives, what is in it,
// and whether it travels in the next request.
func printMemory(a *agent.Agent) {
	m := a.MemoryState()
	sent := map[agent.MemoryLayer]bool{}
	for _, layer := range m.Inject {
		sent[layer] = true
	}
	mark := func(layer agent.MemoryLayer) string {
		if sent[layer] {
			return "уходит в запрос"
		}
		return "хранится, в запрос не уходит"
	}
	fmt.Fprintf(os.Stderr, "пользователь %s · сессия %s\n", m.User, m.Session)
	fmt.Fprintf(os.Stderr, "краткосрочный: сообщений %d · %s\n", m.ShortMessages, mark(agent.LayerShort))
	if m.Task == "" {
		fmt.Fprintf(os.Stderr, "рабочий: нет активной задачи (/task new ИМЯ) · %s\n", mark(agent.LayerWorking))
	} else {
		fmt.Fprintf(os.Stderr, "рабочий: задача %s · %s · ≈%d токенов · %s\n", m.Task, m.WorkingPath, m.WorkingTokens, mark(agent.LayerWorking))
		printEntries("  ", m.Working)
	}
	fmt.Fprintf(os.Stderr, "долговременный: %s · ≈%d токенов · %s\n", m.LongTermPath, m.LongTermTokens, mark(agent.LayerLong))
	for _, section := range []struct {
		name    string
		entries []agent.MemoryEntry
	}{{"profile", m.Profile}, {"decision", m.Decisions}, {"knowledge", m.Knowledge}} {
		if len(section.entries) > 0 {
			fmt.Fprintf(os.Stderr, "  %s:\n", section.name)
			printEntries("    ", section.entries)
		}
	}
}

func printEntries(indent string, entries []agent.MemoryEntry) {
	for _, e := range entries {
		fmt.Fprintf(os.Stderr, "%s%s = %s\n", indent, e.Key, e.Value)
	}
}

// memorySpend is the part of the spend line that says which layers travelled.
func memorySpend(reply agent.Reply) string {
	m := reply.Memory
	if m == (agent.MemorySent{}) {
		return ""
	}
	return fmt.Sprintf(" · слои: история %d сообщ., рабочая %d зап. ≈%d ток., долговременная %d зап. ≈%d ток.",
		m.ShortMessages, m.WorkingEntries, m.WorkingTokens, m.LongEntries, m.LongTermTokens)
}
