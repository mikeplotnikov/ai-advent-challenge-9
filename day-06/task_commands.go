package main

// Day 13's half of the interface: the commands with which a person drives the task's
// state machine. They exist because the transitions belong to the program — "мы можем
// жёстко задать транзишены детерминированно в программе, в коде" (lesson 3) — so the
// user asks for a move the same way the model does, and the same table answers both.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

// handleStateCommand runs /state, /plan, /step, /go, /pause and /resume. The bool
// reports whether the line was one of them.
func handleStateCommand(a *agent.Agent, line string) (bool, error) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false, nil
	}
	switch parts[0] {
	case "/state", "/plan", "/step", "/go", "/pause", "/resume",
		// Day 15: approving a plan, recording a verdict and reading the trail are the
		// three acts the preconditions are about. They are commands and not something
		// the model may do, which is the point — the machine's readiness is the
		// person's to declare.
		"/approve", "/validate", "/trail":
	default:
		return false, nil
	}
	if !a.TaskState().Enabled {
		return true, fmt.Errorf("%w — запусти с -layers -task-state", agent.ErrTaskStateOff)
	}

	switch parts[0] {
	case "/state":
		printTaskState(a)
		return true, nil

	case "/plan":
		steps, err := parsePlan(line)
		if err != nil {
			return true, err
		}
		if err := a.PlanTask(steps); err != nil {
			return true, err
		}
		// Day 15: this writes a DRAFT. Day 13's message here said "план утверждён",
		// and that wording was the whole confusion the day is about — a plan that
		// exists is not a plan somebody agreed to.
		fmt.Fprintf(os.Stderr, "план записан черновиком, шагов: %d — утвердить: /approve\n", len(steps))
		printTaskState(a)
		return true, nil

	case "/approve":
		if err := a.ApprovePlan(); err != nil {
			return true, err
		}
		fmt.Fprintln(os.Stderr, "план утверждён")
		printTaskState(a)
		return true, nil

	case "/validate":
		pass, note, err := parseVerdict(line)
		if err != nil {
			return true, err
		}
		if err := a.RecordVerdict(pass, note); err != nil {
			return true, err
		}
		if pass {
			fmt.Fprintln(os.Stderr, "вердикт: валидация пройдена")
		} else {
			fmt.Fprintln(os.Stderr, "вердикт: валидация НЕ пройдена — финал закрыт, путь назад открыт")
		}
		printTaskState(a)
		return true, nil

	case "/trail":
		printTrail(a)
		return true, nil

	case "/step":
		if len(parts) != 2 || parts[1] != "done" {
			return true, errors.New("формат: /step done")
		}
		if err := a.StepDone(); err != nil {
			return true, err
		}
		v := a.TaskState()
		fmt.Fprintf(os.Stderr, "шаг закрыт, теперь %d/%d — %s\n", v.Step, v.Total, v.Current)
		return true, nil

	case "/go":
		target, carry, err := parseGo(line)
		if err != nil {
			return true, err
		}
		from := a.TaskState().State
		if err := a.TaskGo(target, carry); err != nil {
			return true, err
		}
		fmt.Fprintf(os.Stderr, "переход %s → %s\n", from, a.TaskState().State)
		printTaskState(a)
		return true, nil

	case "/pause":
		if err := a.PauseTask(); err != nil {
			return true, err
		}
		v := a.TaskState()
		// What is printed is the promise being made: the process may die now, and
		// this is where it will come back.
		fmt.Fprintf(os.Stderr, "пауза: %s, шаг %s. Состояние в %s\n",
			v.State, stepLabel(v), v.Path)
		return true, nil

	case "/resume":
		v, err := a.ResumeTask()
		if err != nil {
			return true, err
		}
		// Slide 22, in the agent's own voice: state, step, and the step's own words.
		fmt.Fprintf(os.Stderr, "State: %s, шаг %s.%s\n", v.State, stepLabel(v), continueLabel(v))
		return true, nil
	}
	return true, nil
}

// parsePlan reads "/plan шаг; шаг; шаг". The separator is ';' rather than whitespace
// because a step is a phrase, not a word.
func parsePlan(line string) ([]string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/plan"))
	if rest == "" {
		return nil, errors.New("формат: /plan первый шаг; второй шаг; третий шаг")
	}
	var steps []string
	for _, part := range strings.Split(rest, ";") {
		if step := strings.TrimSpace(part); step != "" {
			steps = append(steps, step)
		}
	}
	if len(steps) == 0 {
		return nil, errors.New("в плане нет ни одного шага")
	}
	return steps, nil
}

// parseGo reads "/go СТАДИЯ" and the optional "/go СТАДИЯ = итог стадии". Without the
// tail the caller passes the last answer, which is the round-5 mechanism: the result of
// the previous prompt travels into the next stage.
func parseGo(line string) (agent.TaskStage, string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/go"))
	if rest == "" {
		return "", "", errors.New("формат: /go СТАДИЯ  или  /go СТАДИЯ = итог стадии")
	}
	stage, carry := rest, ""
	if i := strings.Index(rest, "="); i >= 0 {
		stage, carry = strings.TrimSpace(rest[:i]), strings.TrimSpace(rest[i+1:])
	}
	if stage == "" {
		return "", "", errors.New("не названа стадия: /go СТАДИЯ")
	}
	return agent.TaskStage(stage), carry, nil
}

// parseVerdict reads "/validate ok" and "/validate fail = что именно не так".
func parseVerdict(line string) (bool, string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/validate"))
	if rest == "" {
		return false, "", errors.New("формат: /validate ok  или  /validate fail = что именно не так")
	}
	verdict, note := rest, ""
	if i := strings.Index(rest, "="); i >= 0 {
		verdict, note = strings.TrimSpace(rest[:i]), strings.TrimSpace(rest[i+1:])
	}
	switch strings.ToLower(verdict) {
	case "ok", "pass":
		return true, note, nil
	case "fail", "no":
		return false, note, nil
	default:
		return false, "", fmt.Errorf("вердикт %q не понят: ok или fail", verdict)
	}
}

// printTrail shows both halves of the record: the moves that happened, which live in
// the state file and survive a pause, and the moves this session was refused, which do
// not — a refused attempt may not change the state, so it is not written into it.
func printTrail(a *agent.Agent) {
	v := a.TaskState()
	if v.Task == "" {
		fmt.Fprintln(os.Stderr, "активной задачи нет — /task new ИМЯ")
		return
	}
	if len(v.Trail) == 0 {
		fmt.Fprintln(os.Stderr, "переходов ещё не было")
	}
	for _, e := range v.Trail {
		mark := "→"
		if e.Back {
			mark = "↩"
		}
		line := fmt.Sprintf("  %s %s %s %s (%s)", e.At.Local().Format("15:04:05"), e.From, mark, e.To, e.Actor)
		if e.Reason != "" {
			line += ": " + e.Reason
		}
		fmt.Fprintln(os.Stderr, line)
	}
	if len(v.Refused) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, "отклонено в этой сессии (в файл не пишется — отказ не меняет состояние):")
	for _, r := range v.Refused {
		fmt.Fprintf(os.Stderr, "  %s %s ⨯ %s (%s, %s): %s\n",
			r.At.Local().Format("15:04:05"), r.From, r.To, r.Actor, r.Kind, r.Reason)
	}
}

func stepLabel(v agent.TaskStateView) string {
	if v.Total == 0 {
		return "плана ещё нет"
	}
	return fmt.Sprintf("%d/%d", v.Step, v.Total)
}

func approvalLabel(v agent.TaskStateView) string {
	switch {
	case v.Total == 0:
		return "нет"
	case v.PlanApproved:
		return "утверждён"
	default:
		return "черновик (/approve)"
	}
}

func verdictLabel(v agent.TaskStateView) string {
	if v.Validated {
		return "пройдена"
	}
	return "нет вердикта (/validate ok|fail)"
}

func continueLabel(v agent.TaskStateView) string {
	if v.Current == "" {
		return ""
	}
	return " Продолжаю: " + v.Current + "."
}

func printTaskState(a *agent.Agent) {
	v := a.TaskState()
	if v.Task == "" {
		fmt.Fprintln(os.Stderr, "активной задачи нет — /task new ИМЯ")
		return
	}
	fmt.Fprintf(os.Stderr, "задача: %s (набор стадий %s)\n", v.Task, v.StageSet)
	fmt.Fprintf(os.Stderr, "стадия: %s из %s\n", v.State, joinStages(v.Stages))
	fmt.Fprintf(os.Stderr, "шаг: %s", stepLabel(v))
	if v.Current != "" {
		fmt.Fprintf(os.Stderr, " — %s", v.Current)
	}
	fmt.Fprintln(os.Stderr)
	for i, step := range v.Done {
		fmt.Fprintf(os.Stderr, "  сделано %d. %s\n", i+1, step)
	}
	for _, e := range v.Carry {
		fmt.Fprintf(os.Stderr, "  итог стадии %s: %s\n", e.Key, e.Value)
	}
	fmt.Fprintf(os.Stderr, "ожидается: %s\n", v.Expect)
	// Day 15: readiness, and then what it closes. A list of allowed transitions that
	// does not say which of them are shut right now is the list day 13 printed, and it
	// is exactly the thing a person then walks into.
	fmt.Fprintf(os.Stderr, "план: %s · валидация: %s\n", approvalLabel(v), verdictLabel(v))
	for _, b := range v.Blocked {
		fmt.Fprintf(os.Stderr, "закрыт переход в %s — %s (сейчас: %s)\n", b.To, b.About, b.Detail)
	}
	if len(v.Allowed) > 0 {
		// Запятая, а не стрелка: это список вариантов, а не последовательность.
		// Стрелка здесь читалась бы как «сначала validation, потом planning».
		fmt.Fprintf(os.Stderr, "разрешённые переходы: %s\n", listStages(v.Allowed))
	} else {
		fmt.Fprintln(os.Stderr, "переходов нет — это конечная стадия")
	}
	if v.Paused {
		fmt.Fprintln(os.Stderr, "задача на паузе — /resume")
	}
	if v.Control != agent.ControlGuards {
		// An arm of the measurement running in an interactive session says so out
		// loud: a weaker control that looked like the default would be a demo of a
		// guarantee the build is not making.
		fmt.Fprintf(os.Stderr, "контроль переходов: %s (не строгий; по умолчанию %s)\n", v.Control, agent.ControlGuards)
	}
	if !v.Inject {
		fmt.Fprintln(os.Stderr, "состояние НЕ уходит в запрос (-inject без state); хранится всё")
	}
	fmt.Fprintf(os.Stderr, "в запросе: %d токенов по локальной оценке\n", v.Tokens)
}

func joinStages(stages []agent.TaskStage) string { return joinStagesWith(stages, " → ") }

func listStages(stages []agent.TaskStage) string { return joinStagesWith(stages, ", ") }

func joinStagesWith(stages []agent.TaskStage, sep string) string {
	parts := make([]string, 0, len(stages))
	for _, s := range stages {
		parts = append(parts, string(s))
	}
	return strings.Join(parts, sep)
}
