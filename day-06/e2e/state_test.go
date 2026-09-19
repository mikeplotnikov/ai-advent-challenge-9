package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Days 13 and 15 through the real binary. The unit tests call handleStateCommand
// directly and therefore prove nothing about the stdin loop that routes a typed line to
// it: a change in dispatch order, or another handler starting to shadow a state command,
// would leave every one of them green.
//
// This test was day 13's and stayed day 13's while day 15 split /plan from /approve — it
// went red on the pushed revision and `go test` without -count=1 answered "ok (cached)",
// because the cache does not know the test builds the binary itself. An external review
// found it. Hence the scenario below walks the WHOLE day-15 surface: the draft plan, the
// approval, the verdict, the rollback and the trail.
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
			"/go execution\n"+ // план ещё черновик — предусловие обязано отказать
			"/approve\n"+
			"/go execution = решено: свой JWT на HMAC-SHA256\n"+
			"/step done\n"+
			"/go done\n"+ // запрещён из execution — отказ должен дойти до человека
			"/go validation\n"+ // план не пройден — второе предусловие
			"/step done\n"+
			"/go validation = сделано, отдаём на проверку\n"+
			"/go done\n"+ // вердикта нет — третье предусловие
			"/validate fail = на истёкшем токене 500 вместо 401\n"+
			"/go execution = вернули на доработку\n"+
			"/trail\n"+
			"/pause\n/exit\n",
		base...)

	for _, want := range []string{
		"состояние задачи: /state",              // подсказка перечисляет команды дня 15
		"задача \"сервис авторизации\" создана", // имя из двух слов принято
		"план записан черновиком, шагов: 3 — утвердить: /approve",
		"переход пока закрыт: planning → execution",
		"план утверждён",
		"переход planning → execution",
		"итог стадии planning: решено: свой JWT на HMAC-SHA256",
		"шаг закрыт, теперь 2/3 — Token validation",
		"переход запрещён: execution → done",
		"из execution разрешено: validation, planning",
		"переход пока закрыт: execution → validation",
		"машина на шаге 2 из 3",
		"переход execution → validation",
		"переход пока закрыт: validation → done",
		"вердикт валидации не записан",
		"вердикт: валидация НЕ пройдена",
		"переход validation → execution",
		"отклонено в этой сессии (в файл не пишется",
		"пауза: execution, шаг 3/3",
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
		State     string   `json:"state"`
		Step      int      `json:"step"`
		Paused    bool     `json:"paused"`
		Plan      []string `json:"plan"`
		Approved  bool     `json:"plan_approved"`
		Validated bool     `json:"validated"`
		Trail     []struct {
			From, To, Reason string
			Back             bool
		} `json:"trail"`
	}
	if err := json.Unmarshal(raw, &ctx); err != nil {
		t.Fatalf("файл состояния не разобран: %v\n%s", err, raw)
	}
	if ctx.State != "execution" || ctx.Step != 3 || !ctx.Paused || len(ctx.Plan) != 3 {
		t.Errorf("на диске: стадия %q, шаг %d, пауза %v, шагов %d", ctx.State, ctx.Step, ctx.Paused, len(ctx.Plan))
	}
	// Возврат на доработку снимает ВЕРДИКТ и сохраняет утверждение плана: план не
	// переписывают, переделывают работу по нему. Утверждение снял бы только откат в
	// стадию, где план и утверждается, — это планирование.
	if !ctx.Approved {
		t.Error("возврат на доработку снял утверждение плана, хотя план не менялся")
	}
	if ctx.Validated {
		t.Error("вердикт пережил возврат на доработку")
	}
	// Журнал хранит ПРИМЕНЁННЫЕ ходы; четыре отказа в него не попали.
	if len(ctx.Trail) != 3 {
		t.Fatalf("в журнале %d ходов, ожидались три применённых: %+v", len(ctx.Trail), ctx.Trail)
	}
	if last := ctx.Trail[2]; !last.Back || last.From != "validation" || last.To != "execution" {
		t.Errorf("последний ход журнала: %+v", last)
	}

	// A second process picks the task up where the first left it — slide 22, end to end.
	out = repl(t, bin, srv.URL, work, "/resume\n/exit\n",
		append(base, "-task", "сервис авторизации")...)
	if !strings.Contains(out, "State: execution, шаг 3/3. Продолжаю: Refresh flow.") {
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
