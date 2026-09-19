package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

// The day-13 CLI is where a person drives the machine by hand. Its parsing gets the
// same coverage the day-11 and day-12 commands have, because a command that silently
// misreads its arguments moves the state to somewhere nobody asked for.

func stateCLIAgent(t *testing.T, dir string, cfg *agent.TaskConfig) *agent.Agent {
	t.Helper()
	t.Setenv("DEEPSEEK_API_KEY", "test-no-network")
	a, err := agent.FromEnv(agent.Config{
		Memory: &agent.MemoryConfig{Dir: dir, User: "u", Session: "s"},
		Task:   cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		if err := a.StartTask("сервис"); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func TestStateCommandsRefuseWhenTheMachineIsOff(t *testing.T) {
	a := stateCLIAgent(t, t.TempDir(), nil)
	for _, line := range []string{"/state", "/plan шаг", "/step done", "/go execution", "/pause", "/resume"} {
		handled, err := handleStateCommand(a, line)
		if !handled {
			t.Fatalf("%q не распознана как команда состояния", line)
		}
		if !errors.Is(err, agent.ErrTaskStateOff) {
			t.Fatalf("%q: ожидался ErrTaskStateOff, получено %v", line, err)
		}
	}
}

func TestStateCommandsLeaveOtherLinesAlone(t *testing.T) {
	a := stateCLIAgent(t, t.TempDir(), &agent.TaskConfig{Inject: true})
	for _, line := range []string{"", "обычный вопрос", "/task new X", "/profile show", "/statement"} {
		if handled, _ := handleStateCommand(a, line); handled {
			t.Fatalf("%q перехвачена командой состояния", line)
		}
	}
}

func TestParsePlanSplitsOnSemicolons(t *testing.T) {
	steps, err := parsePlan("/plan  JWT module ; Token validation ;; Refresh ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"JWT module", "Token validation", "Refresh"}
	if len(steps) != len(want) {
		t.Fatalf("шагов %d, ожидалось %d: %v", len(steps), len(want), steps)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("шаг %d = %q, ожидался %q", i+1, steps[i], want[i])
		}
	}
	// A step is a phrase, so spaces inside it survive and only ';' separates.
	if steps, err := parsePlan("/plan один шаг из нескольких слов"); err != nil || len(steps) != 1 {
		t.Fatalf("однословный разбор: %v %v", steps, err)
	}
	for _, bad := range []string{"/plan", "/plan   ", "/plan ; ; ;"} {
		if _, err := parsePlan(bad); err == nil {
			t.Fatalf("%q принято как план", bad)
		}
	}
}

func TestParseGoReadsTheStageAndTheOptionalCarry(t *testing.T) {
	for _, tc := range []struct {
		line      string
		wantStage agent.TaskStage
		wantCarry string
	}{
		{"/go execution", agent.StageExecution, ""},
		{"/go  execution ", agent.StageExecution, ""},
		{"/go execution = решили: свой JWT", agent.StageExecution, "решили: свой JWT"},
		{"/go execution=итог", agent.StageExecution, "итог"},
	} {
		stage, carry, err := parseGo(tc.line)
		if err != nil {
			t.Fatalf("%q: %v", tc.line, err)
		}
		if stage != tc.wantStage || carry != tc.wantCarry {
			t.Fatalf("%q → стадия %q, перенос %q", tc.line, stage, carry)
		}
	}
	for _, bad := range []string{"/go", "/go   ", "/go = только итог"} {
		if _, _, err := parseGo(bad); err == nil {
			t.Fatalf("%q принято", bad)
		}
	}
}

func TestPlanAndGoRunThroughTheCommandSurface(t *testing.T) {
	dir := t.TempDir()
	a := stateCLIAgent(t, dir, &agent.TaskConfig{Inject: true})

	if handled, err := handleStateCommand(a, "/plan JWT module; Token validation"); !handled || err != nil {
		t.Fatalf("/plan: %v %v", handled, err)
	}
	if v := a.TaskState(); v.Total != 2 || v.Current != "JWT module" {
		t.Fatalf("после /plan: %d шагов, текущий %q", v.Total, v.Current)
	}
	// Day 15: /plan writes a draft, /approve is what opens the edge to execution.
	if v := a.TaskState(); v.PlanApproved {
		t.Fatal("/plan сам утвердил план")
	}
	if handled, err := handleStateCommand(a, "/approve"); !handled || err != nil {
		t.Fatalf("/approve: %v %v", handled, err)
	}
	if handled, err := handleStateCommand(a, "/go execution = план утверждён"); !handled || err != nil {
		t.Fatalf("/go: %v %v", handled, err)
	}
	if v := a.TaskState(); v.State != agent.StageExecution {
		t.Fatalf("стадия %q", v.State)
	}
	if handled, err := handleStateCommand(a, "/step done"); !handled || err != nil {
		t.Fatalf("/step done: %v %v", handled, err)
	}
	if v := a.TaskState(); v.Step != 2 {
		t.Fatalf("шаг %d", v.Step)
	}

	// The refusal reaches the person through the command surface too, not just the API.
	_, err := handleStateCommand(a, "/go done")
	if !errors.Is(err, agent.ErrTransition) {
		t.Fatalf("незаконный переход через CLI: %v", err)
	}
	if v := a.TaskState(); v.State != agent.StageExecution {
		t.Fatalf("отклонённый переход всё же случился: %q", v.State)
	}
}

func TestStepDoneRejectsAnythingButDone(t *testing.T) {
	a := stateCLIAgent(t, t.TempDir(), &agent.TaskConfig{Inject: true})
	for _, line := range []string{"/step", "/step next", "/step done now"} {
		_, err := handleStateCommand(a, line)
		if err == nil {
			t.Fatalf("%q принято", line)
		}
		if strings.Contains(err.Error(), "план") {
			t.Fatalf("%q: разобрано как валидная команда и ушло дальше: %v", line, err)
		}
	}
}

func TestPauseAndResumeSpeakSlideTwentyTwo(t *testing.T) {
	dir := t.TempDir()
	a := stateCLIAgent(t, dir, &agent.TaskConfig{Inject: true})
	if _, err := handleStateCommand(a, "/plan JWT module; Token validation"); err != nil {
		t.Fatal(err)
	}
	if _, err := handleStateCommand(a, "/approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := handleStateCommand(a, "/go execution"); err != nil {
		t.Fatal(err)
	}
	if _, err := handleStateCommand(a, "/step done"); err != nil {
		t.Fatal(err)
	}
	if _, err := handleStateCommand(a, "/pause"); err != nil {
		t.Fatal(err)
	}
	if !a.TaskState().Paused {
		t.Fatal("/pause не отметил паузу")
	}
	if _, err := handleStateCommand(a, "/resume"); err != nil {
		t.Fatal(err)
	}
	v := a.TaskState()
	if v.Paused {
		t.Fatal("/resume не снял паузу")
	}
	if v.State != agent.StageExecution || v.Step != 2 || v.Current != "Token validation" {
		t.Fatalf("после /resume: %s, шаг %d/%d — %q", v.State, v.Step, v.Total, v.Current)
	}
}
