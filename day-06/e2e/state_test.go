package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Day 13 through the real binary. The unit tests call handleStateCommand directly and
// therefore prove nothing about the stdin loop that routes a typed line to it: a change
// in dispatch order, or another handler starting to shadow a state command, would leave
// every one of them green. The project already proves memory and profile commands this
// way; the state commands were the gap the first review wave named.
func TestStateCommandsDriveTheMachineThroughTheCLI(t *testing.T) {
	p := &provider{}
	srv := p.start(t)
	bin := build(t)
	work := t.TempDir()
	mem := filepath.Join(work, "mem")
	base := []string{"-layers", "-task-state", "-memory-dir", mem, "-user", "михаил", "-session", "s1"}

	out := repl(t, bin, srv.URL, work,
		"/task new сервис авторизации\n"+
			"/plan JWT module; Token validation; Refresh flow\n"+
			"/go execution = решено: свой JWT на HMAC-SHA256\n"+
			"/step done\n"+
			"/go done\n"+ // запрещён из execution — отказ должен дойти до человека
			"/pause\n/exit\n",
		base...)

	for _, want := range []string{
		"состояние задачи: /state",              // подсказка перечисляет команды дня 13
		"задача \"сервис авторизации\" создана", // имя из двух слов принято
		"план утверждён, шагов: 3",
		"переход planning → execution",
		"итог стадии planning: решено: свой JWT на HMAC-SHA256",
		"шаг закрыт, теперь 2/3 — Token validation",
		"переход запрещён: execution → done",
		"из execution разрешено: validation, planning",
		"пауза: execution, шаг 2/3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в выводе CLI нет %q:\n%s", want, out)
		}
	}

	// The state is on disk under the task's own name, beside its working memory.
	statePath := filepath.Join(mem, "михаил", "tasks", "сервис-авторизации.state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("файла состояния нет: %v", err)
	}
	// Файл записан с отступами, поэтому сравниваем по разобранному JSON, а не по строке:
	// проверка подстроки здесь падала бы от смены форматирования, а не от смены смысла.
	var ctx struct {
		State  string   `json:"state"`
		Step   int      `json:"step"`
		Paused bool     `json:"paused"`
		Plan   []string `json:"plan"`
	}
	if err := json.Unmarshal(raw, &ctx); err != nil {
		t.Fatalf("файл состояния не разобран: %v\n%s", err, raw)
	}
	if ctx.State != "execution" || ctx.Step != 2 || !ctx.Paused || len(ctx.Plan) != 3 {
		t.Errorf("на диске: стадия %q, шаг %d, пауза %v, шагов %d", ctx.State, ctx.Step, ctx.Paused, len(ctx.Plan))
	}

	// A second process picks the task up where the first left it — slide 22, end to end.
	out = repl(t, bin, srv.URL, work, "/resume\n/exit\n",
		append(base, "-task", "сервис авторизации")...)
	if !strings.Contains(out, "State: execution, шаг 2/3. Продолжаю: Token validation.") {
		t.Fatalf("второй процесс не поднял задачу на том же шаге:\n%s", out)
	}
}

// Without -task-state the day-13 commands refuse, and — the part that matters — the
// request is byte-for-byte the one days 6-12 sent.
func TestWithoutTheFlagTheStateCommandsRefuseAndTheRequestIsUnchanged(t *testing.T) {
	p := &provider{}
	srv := p.start(t)
	bin := build(t)
	work := t.TempDir()
	mem := filepath.Join(work, "mem")

	out := repl(t, bin, srv.URL, work, "/state\nвопрос\n/exit\n",
		"-layers", "-memory-dir", mem, "-user", "михаил", "-session", "s1")
	if !strings.Contains(out, "состояние задачи выключено") {
		t.Fatalf("/state без флага не отказал:\n%s", out)
	}
	withoutState := contentsOf(p.requests[len(p.requests)-1])

	p2 := &provider{}
	srv2 := p2.start(t)
	work2 := t.TempDir()
	out = repl(t, bin, srv2.URL, work2, "вопрос\n/exit\n",
		"-layers", "-memory-dir", filepath.Join(work2, "mem"), "-user", "михаил", "-session", "s1",
		"-task-state")
	withState := contentsOf(p2.requests[len(p2.requests)-1])
	if withoutState != withState {
		t.Fatalf("включённый флаг без задачи изменил запрос:\n%q\n%q", withoutState, withState)
	}
}
