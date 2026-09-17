package main

// Day 14's half of the interface: showing the rules, loading a set from a file and
// lifting a rule. There is deliberately no command that writes a rule from a typed
// line — the day's requirement is that invariants live apart from the dialogue, and a
// rule composed inside the dialogue would be the opposite of that. A set arrives as a
// file; the conversation can only look at it, or drop one.

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

// parseInvariantMode reads "inject,check,retry,judge".
func parseInvariantMode(spec string) (*agent.InvariantConfig, error) {
	cfg := &agent.InvariantConfig{}
	for _, part := range strings.Split(spec, ",") {
		switch strings.TrimSpace(part) {
		case "":
		case "inject":
			cfg.Inject = true
		case "check":
			cfg.Check = true
		case "retry":
			cfg.Retry = true
		case "judge":
			cfg.Judge = true
		default:
			return nil, fmt.Errorf("-inv: %q не из набора inject, check, retry, judge", strings.TrimSpace(part))
		}
	}
	if !cfg.Inject && !cfg.Check {
		return nil, errors.New("-inv: без inject и без check инварианты не делают ничего")
	}
	return cfg, nil
}

func loadInvariantFile(a *agent.Agent, path string) error {
	n, err := a.LoadInvariantFile(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "инвариантов загружено: %d из %s\n", n, path)
	return nil
}

// handleInvariantCommand runs /inv. The bool reports whether the line was it.
func handleInvariantCommand(a *agent.Agent, line string) (bool, error) {
	parts := strings.Fields(line)
	if len(parts) == 0 || parts[0] != "/inv" {
		return false, nil
	}
	if !a.InvariantsEnabled() {
		return true, fmt.Errorf("%w — запусти с -layers -invariants", agent.ErrInvariantsOff)
	}
	switch {
	case len(parts) == 1:
		printInvariants(a)
		return true, nil

	case parts[1] == "load" && len(parts) == 3:
		if err := loadInvariantFile(a, parts[2]); err != nil {
			return true, err
		}
		printInvariants(a)
		return true, nil

	case parts[1] == "rm" && len(parts) == 3:
		if err := a.RemoveInvariant(parts[2]); err != nil {
			return true, err
		}
		// What is printed is the promise being withdrawn: from here on the agent may
		// propose what it just refused, and the person needs to see that happen.
		fmt.Fprintf(os.Stderr, "инвариант %q снят\n", parts[2])
		printInvariants(a)
		return true, nil
	}
	return true, errors.New("формат: /inv  ·  /inv load ФАЙЛ  ·  /inv rm ИМЯ")
}

func printInvariants(a *agent.Agent) {
	v := a.InvariantState()
	on := []string{}
	for _, s := range []struct {
		name string
		set  bool
	}{{"inject", v.Inject}, {"check", v.Check}, {"retry", v.Retry}, {"judge", v.Judge}} {
		if s.set {
			on = append(on, s.name)
		}
	}
	fmt.Fprintf(os.Stderr, "инварианты: %s; в запросе %d токенов по оценке\n", strings.Join(on, ", "), v.Tokens)
	fmt.Fprintf(os.Stderr, "  глобальные: %s\n", v.GlobalPath)
	if v.TaskPath != "" {
		fmt.Fprintf(os.Stderr, "  задачи %s: %s\n", v.Task, v.TaskPath)
	}
	if len(v.Invariants) == 0 {
		fmt.Fprintln(os.Stderr, "  правил нет")
		return
	}
	rules := append([]agent.Invariant(nil), v.Invariants...)
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Scope < rules[j].Scope })
	for _, i := range rules {
		// The enforcement is printed with every rule, because a judged rule is an
		// opinion of another model and the person has to know which of their laws
		// are guaranteed and which are not.
		how := "проверка в коде"
		if i.Enforce() == agent.EnforceJudge {
			how = "внешний судья, без гарантии"
		}
		fmt.Fprintf(os.Stderr, "  [%s] %s — %s (%s)\n", i.Scope, i.Name, i.About, how)
	}
}
