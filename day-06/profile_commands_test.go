package main

import (
	"bufio"
	"errors"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

// The day-12 CLI is the surface the host's requirement lands on — "надо чтобы условный
// пользователь вашего сервиса мог настроить себе агента под себя". Its parsing gets the
// same coverage the day-11 memory commands already have.

func profileAgent(t *testing.T, dir string) *agent.Agent {
	t.Helper()
	t.Setenv("DEEPSEEK_API_KEY", "test-no-network")
	a, err := agent.FromEnv(agent.Config{
		Profile: &agent.ProfileConfig{Dir: dir, User: "u", Name: agent.DefaultProfileName},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestParseProfileSetKeepsTheValueWhole(t *testing.T) {
	block, key, value, err := parseProfileSet("/profile set style detail = коротко, без вводных = и без пересказа")
	if err != nil || block != agent.BlockStyle || key != "detail" || value != "коротко, без вводных = и без пересказа" {
		t.Fatalf("parseProfileSet = %q %q %q %v", block, key, value, err)
	}
	if _, _, _, err := parseProfileSet("/profile set CONSTRAINTS stack = Kotlin"); err != nil {
		t.Fatalf("имя блока должно разбираться без учёта регистра: %v", err)
	}
	for _, line := range []string{
		"/profile set",
		"/profile set style",
		"/profile set style detail коротко",
		"/profile set invariants detail = коротко",
	} {
		if _, _, _, err := parseProfileSet(line); err == nil {
			t.Errorf("%q принято", line)
		}
	}
}

// One verb, two shapes: a routing rule and the fallback profile. Picking the wrong
// branch would silently write the wrong thing into the router.
func TestProfileRouteTellsARuleFromTheDefault(t *testing.T) {
	dir := t.TempDir()
	a := profileAgent(t, dir)

	if handled, err := handleProfileCommand(a, nil, "/profile route отчёт = analyst"); !handled || err != nil {
		t.Fatalf("правило не записано: handled=%v err=%v", handled, err)
	}
	if handled, err := handleProfileCommand(a, nil, "/profile route default senior"); !handled || err != nil {
		t.Fatalf("профиль по умолчанию не записан: handled=%v err=%v", handled, err)
	}
	state := a.ProfileState()
	if len(state.Rules) != 1 || state.Rules[0].Match != "отчёт" || state.Rules[0].Use != "analyst" {
		t.Fatalf("правила роутера = %+v", state.Rules)
	}
	// "default" as a rule condition, not as the keyword: the shape with "=" wins.
	if handled, err := handleProfileCommand(a, nil, "/profile route default = junior"); !handled || err != nil {
		t.Fatalf("правило со словом default не записано: handled=%v err=%v", handled, err)
	}
	if rules := a.ProfileState().Rules; len(rules) != 2 || rules[1].Match != "default" || rules[1].Use != "junior" {
		t.Fatalf("после правила «default = junior» правила = %+v", rules)
	}
	chosen, err := a.SelectProfile("нужен отчёт по выручке")
	if err != nil || chosen != "analyst" {
		t.Fatalf("роутер выбрал %q (%v)", chosen, err)
	}
	if chosen, _ := a.SelectProfile("ничего похожего"); chosen != "senior" {
		t.Fatalf("профиль по умолчанию не применился: %q", chosen)
	}
}

func TestProfileCommandRefusesMalformedVerbs(t *testing.T) {
	dir := t.TempDir()
	a := profileAgent(t, dir)
	for _, line := range []string{
		"/profile use",
		"/profile use a b",
		"/profile drop style",
		"/profile drop style k extra",
		"/profile drop invariants k",
		"/profile pipeline",
		"/profile pipeline skills",
		"/profile pipeline direct extra",
		"/profile route",
		"/profile route только-одно-слово",
		"/profile неизвестно",
	} {
		handled, err := handleProfileCommand(a, nil, line)
		if !handled {
			t.Errorf("%q не распознано как команда профиля", line)
		}
		if err == nil {
			t.Errorf("%q принято без ошибки", line)
		}
	}
	// Lines that are not /profile are left to the other handlers.
	for _, line := range []string{"/memory", "/remember profile k = v", "", "обычный вопрос"} {
		if handled, _ := handleProfileCommand(a, nil, line); handled {
			t.Errorf("%q перехвачено обработчиком профиля", line)
		}
	}
	// /profile init needs a reader; without one it must say so rather than panic.
	if handled, err := handleProfileCommand(a, nil, "/profile init"); !handled || err == nil {
		t.Errorf("/profile init без ввода: handled=%v err=%v", handled, err)
	}
}

// The interview writes what was answered and skips what was not, without a model call.
func TestProfileInitWritesTheAnsweredQuestionsOnly(t *testing.T) {
	dir := t.TempDir()
	a := profileAgent(t, dir)
	answers := []string{"аналитик", "", "русский", "", "", "", "не предлагать смену стека"}
	in := bufio.NewScanner(strings.NewReader(strings.Join(answers, "\n") + "\n"))
	if handled, err := handleProfileCommand(a, in, "/profile init"); !handled || err != nil {
		t.Fatalf("интервью: handled=%v err=%v", handled, err)
	}
	state := a.ProfileState()
	if len(state.Context) != 1 || state.Context[0].Value != "аналитик" {
		t.Fatalf("context = %+v", state.Context)
	}
	if len(state.Style) != 1 || state.Style[0].Key != "language" {
		t.Fatalf("style = %+v", state.Style)
	}
	if len(state.Constraints) != 1 || state.Constraints[0].Key != "avoid" {
		t.Fatalf("constraints = %+v", state.Constraints)
	}
	// An interview cut short must fail rather than half-write silently.
	short := bufio.NewScanner(strings.NewReader("роль\n"))
	if _, err := handleProfileCommand(a, short, "/profile init"); err == nil {
		t.Fatal("оборванное интервью принято")
	}
}

func TestProfileCommandSaysWhenProfileIsOff(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-no-network")
	a, err := agent.FromEnv(agent.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"/profile", "/profile show", "/profile set style k = v", "/profile init"} {
		handled, err := handleProfileCommand(a, nil, line)
		if !handled || !errors.Is(err, agent.ErrProfileOff) {
			t.Errorf("%q: handled=%v err=%v", line, handled, err)
		}
		if err != nil && !strings.Contains(err.Error(), "-layers") {
			t.Errorf("%q: ошибка не подсказывает флаг: %v", line, err)
		}
	}
}
