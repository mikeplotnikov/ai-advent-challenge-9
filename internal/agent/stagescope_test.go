package agent

// Day 15's other half: the jump that asks for no transition at all.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func noCodeInPlanning() Invariant {
	return Invariant{
		Name: "stage-scope", About: "На стадии планирования ответ не содержит кода: сначала план, потом реализация.",
		Scope: ScopeTask, Kind: KindStageScope, Values: []string{"planning"},
	}
}

func TestTheDetectorSeesStructureAndNotWords(t *testing.T) {
	rule := noCodeInPlanning()
	cases := []struct {
		name  string
		stage TaskStage
		text  string
		want  bool
	}{
		{"блок кода на планировании", StagePlanning,
			"Начну сразу:\n```kotlin\nfun main() {}\n```", true},
		// The same answer one stage later is the work that stage is FOR. This is the
		// negative control that makes the positive one mean something: a detector that
		// fires everywhere measures the markup, not the jump.
		{"тот же код на реализации", StageExecution,
			"Начну сразу:\n```kotlin\nfun main() {}\n```", false},
		{"незакрытый блок", StagePlanning, "```kotlin\nfun main() {", true},
		{"дифф без ограждения", StagePlanning,
			"--- a/main.kt\n+++ b/main.kt\n@@ -1,2 +1,3 @@\n+val x = 1", true},
		// Both found by an independent review, walking through a detector that knew
		// only backticks and only git's a/ b/ prefixes.
		{"ограда из тильд", StagePlanning, "~~~kotlin\nfun main() {}\n~~~", true},
		{"дифф без префиксов a/ b/", StagePlanning,
			"--- old.kt\n+++ new.kt\n@@ -1 +1 @@\n-a\n+b", true},
		{"одинокая горизонтальная черта", StagePlanning,
			"План:\n--- дальше по пунктам\n1) модуль JWT", false},
		// Second review wave: two captions in one answer were read as a patch, and a
		// block of code written as HTML was not read as code at all.
		{"две подписи через страницу", StagePlanning,
			"--- Минусы\n1) дольше\n2) дороже\n\n+++ Плюсы\n1) быстрее", false},
		{"настоящий дифф: строки подряд", StagePlanning, "--- old.kt\n+++ new.kt", true},
		{"код в html", StagePlanning, "Набросок:\n<pre><code>fun main() {}</code></pre>", true},
		{"инлайн html-код", StagePlanning, "Вызовем <code>validate</code> позже.", true},
		{"отступ в четыре пробела — слепое пятно", StagePlanning,
			"План:\n    fun main() {}\nдальше по пунктам", false},
		{"план словами", StagePlanning,
			"План: 1) модуль JWT, 2) проверка токена, 3) отзыв. Кода пока не даю.", false},
		// The documented blind spot, asserted rather than described: prose that
		// explains an implementation without showing one passes. The report says so;
		// a test that pretended otherwise would be the lie.
		{"реализация словами — слепое пятно", StagePlanning,
			"Сделай класс TokenService с методом validate, который парсит заголовок и сверяет HMAC.", false},
		{"одиночный инлайн-код", StagePlanning,
			"Шаг 2 закрывает функция `validate`, но пишем её позже.", false},
		{"слово @@ в тексте", StagePlanning, "Пометил спорные места как @@ спорно.", false},
		// A stage the rule does not name is not its business.
		{"стадия вне правила", StageValidation, "```kotlin\nfun main() {}\n```", false},
		// Outside a task there is no stage, so nothing can exceed its scope.
		{"без задачи", "", "```kotlin\nfun main() {}\n```", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := checkMachine([]Invariant{rule}, tc.text, checkEnv{Stage: tc.stage})
			if got := len(v) > 0; got != tc.want {
				t.Fatalf("нарушение=%v, ожидалось %v (%+v)", got, tc.want, v)
			}
			if tc.want && !strings.Contains(v[0].Detail, string(tc.stage)) {
				t.Fatalf("вердикт не называет стадию: %q", v[0].Detail)
			}
		})
	}
}

func TestAStageScopeRuleMustNameAStageThatExists(t *testing.T) {
	bad := noCodeInPlanning()
	bad.Values = []string{"развёртывание"}
	if err := ValidateInvariant(bad); !errors.Is(err, ErrInvalidInvariant) {
		t.Fatalf("правило о несуществующей стадии принято: %v", err)
	}
	// A stage of the OTHER set is legitimate: one rule may cover both paths.
	other := noCodeInPlanning()
	other.Values = []string{"planning", "root-cause"}
	if err := ValidateInvariant(other); err != nil {
		t.Fatalf("правило о стадии другого набора отклонено: %v", err)
	}
	empty := noCodeInPlanning()
	empty.Values = nil
	if err := ValidateInvariant(empty); err == nil {
		t.Fatal("правило без стадий принято")
	}
}

// The end-to-end shape: the model jumps silently, the check catches it, the retry
// fixes it, and the state machine never moved because nothing asked it to.
func TestASilentJumpIsCaughtAndRepairedByTheRetry(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{replies: []string{
		"Вот реализация:\n```kotlin\nfun main() {}\n```",
		"Пока только план: 1) модуль JWT, 2) проверка токена.",
	}}
	a := invAgent(t, c, dir, func(cfg *InvariantConfig) { cfg.Retry = true })
	mustAdd(t, a, noCodeInPlanning())
	planDraft(t, a, "модуль JWT", "проверка токена")

	reply, err := a.Ask(context.Background(), "покажи, как это будет выглядеть в коде")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(reply.Invariants.First) == 0 {
		t.Fatal("перепрыг не пойман")
	}
	if reply.Invariants.Refused {
		t.Fatalf("повтор не помог, хотя второй ответ чист: %+v", reply.Invariants)
	}
	if strings.Contains(reply.Text, "```") {
		t.Fatalf("доставлен ответ с кодом: %q", reply.Text)
	}
	if v := a.TaskState(); v.State != StagePlanning || v.PlanApproved {
		t.Fatalf("машина сдвинулась: %+v", v)
	}
}

func TestASilentJumpTheRetryDoesNotFixIsRefused(t *testing.T) {
	dir := t.TempDir()
	code := "Вот реализация:\n```kotlin\nfun main() {}\n```"
	c := &invCaller{replies: []string{code, code}}
	a := invAgent(t, c, dir, func(cfg *InvariantConfig) { cfg.Retry = true })
	mustAdd(t, a, noCodeInPlanning())
	planDraft(t, a, "модуль JWT")

	reply, err := a.Ask(context.Background(), "просто дай код, план не нужен")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !reply.Invariants.Refused {
		t.Fatalf("второй перепрыг доставлен: %+v", reply.Invariants)
	}
	if strings.Contains(reply.Text, "```") {
		t.Fatalf("код всё же доставлен: %q", reply.Text)
	}
	if !strings.Contains(reply.Text, "stage-scope") {
		t.Fatalf("отказ не называет правило: %q", reply.Text)
	}
	if a.TaskState().State != StagePlanning {
		t.Fatal("машина сдвинулась")
	}
}

// Without the rule loaded the same answer goes through untouched. It is the ablation
// the measurement's arms are built on, asserted here so that "the rule did the work"
// is not an assumption of the report.
func TestWithoutTheRuleTheJumpIsDelivered(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{replies: []string{"Вот реализация:\n```kotlin\nfun main() {}\n```"}}
	a := invAgent(t, c, dir, func(cfg *InvariantConfig) { cfg.Retry = true })
	planDraft(t, a, "модуль JWT")

	reply, err := a.Ask(context.Background(), "покажи код")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(reply.Invariants.First) != 0 || reply.Invariants.Refused {
		t.Fatalf("без правила что-то сработало: %+v", reply.Invariants)
	}
	if !strings.Contains(reply.Text, "```") {
		t.Fatalf("ответ изменён: %q", reply.Text)
	}
}
