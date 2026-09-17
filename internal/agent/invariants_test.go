package agent

// Day 14's tests. Two things they are built around.
//
// First, every detector gets both controls. A check that cannot return "violated" on
// an injected case proves nothing when it returns "clean", and a check that fires on
// an innocent answer manufactures the very numbers the day is supposed to measure.
//
// Second, the trap this file exists to keep shut: a substring search for "java" finds
// one inside "JavaScript". Day 12's review found exactly this class of defect in a
// detector after it had already produced numbers, and those numbers had to be
// recomputed. The boundary tests below are that defect, written down.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// invCaller returns scripted answers in order and records what it was sent.
type invCaller struct {
	sent    [][]llm.Message
	replies []string
	errs    []error
	n       int
}

func (c *invCaller) AskWith(_ context.Context, messages []llm.Message, _ llm.Options) (llm.Answer, error) {
	c.sent = append(c.sent, append([]llm.Message(nil), messages...))
	i := c.n
	c.n++
	if i < len(c.errs) && c.errs[i] != nil {
		return llm.Answer{}, c.errs[i]
	}
	if i < len(c.replies) {
		return llm.Answer{Content: c.replies[i], Model: llm.DefaultModel}, nil
	}
	return llm.Answer{Content: "ответ", Model: llm.DefaultModel}, nil
}

func invConfig(dir string, on func(*InvariantConfig)) *InvariantConfig {
	c := &InvariantConfig{Dir: dir, User: "михаил", Inject: true, Check: true}
	if on != nil {
		on(c)
	}
	return c
}

// invAgent builds an agent with memory, an open task and invariants on.
func invAgent(t *testing.T, c *invCaller, dir string, tune func(*InvariantConfig)) *Agent {
	t.Helper()
	cfg := Config{
		Memory:     memoryConfig(dir, "михаил", ""),
		Task:       taskConfig(),
		Invariants: invConfig(dir, tune),
	}
	a, err := New(c, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.StartTask("авторизация"); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	return a
}

func stackOnlyKotlin() Invariant {
	return Invariant{
		Name: "stack", About: "Разрешены только Kotlin и Ktor.",
		Scope: ScopeTask, Kind: KindStackOnly, Values: []string{"Kotlin", "Ktor"},
	}
}

func mustAdd(t *testing.T, a *Agent, i Invariant) {
	t.Helper()
	if err := a.AddInvariant(i); err != nil {
		t.Fatalf("AddInvariant %s: %v", i.Name, err)
	}
}

// --- the matcher ------------------------------------------------------------

func TestTheMatcherDoesNotFindJavaInsideJavaScript(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		// The negative control of the whole file: the word that contains the banned
		// word must not count as the banned word.
		{"javascript is not java", "Возьмём JavaScript и Node.js.", []string{"JavaScript", "Node.js"}},
		// The positive control that proves the detector can still fire.
		{"java on its own", "Сделаем на Java 21.", []string{"Java"}},
		{"java with punctuation", "Быстрее будет на Java.", []string{"Java"}},
		{"multi-word alias", "Возьми Spring Boot, так проще.", []string{"Spring"}},
		{"case is ignored", "kotlin и KTOR", []string{"Kotlin", "Ktor"}},
		{"cyrillic spelling", "Пиши на джава, привычнее.", []string{"Java"}},
		// Glued to a Cyrillic ending the term is not counted. This under-cuts rather
		// than over-cuts, which is the rule for an approximate matcher.
		{"glued to a longer identifier", "пакет kotlinx.coroutines", nil},
		{"nothing at all", "Опиши архитектуру словами.", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findTerms(tc.text, stackVocabulary)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("findTerms(%q) = %v, ожидалось %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestEachMachineCheckHasBothControls(t *testing.T) {
	cases := []struct {
		name      string
		inv       Invariant
		clean     string
		violating string
	}{
		{
			"stack-only", stackOnlyKotlin(),
			"Сделаем на Kotlin с Ktor.",
			"Быстрее будет на Java со Spring Boot.",
		},
		{
			"no-banned",
			Invariant{Name: "no-orm", About: "ORM не используем.", Scope: ScopeTask,
				Kind: KindNoBanned, Values: []string{"Hibernate", "JPA", "Exposed"}},
			"Запросы пишем руками через JDBC.",
			"Возьмём Hibernate, он сам всё смапит.",
		},
		{
			"max-deps",
			Invariant{Name: "deps", About: "Не больше трёх зависимостей.", Scope: ScopeTask,
				Kind: KindMaxDeps, Limit: 3},
			"Хватит Kotlin, Ktor и PostgreSQL.",
			"Возьмём Kotlin, Ktor, PostgreSQL, Redis, Kafka и Keycloak.",
		},
		{
			"arch-only",
			Invariant{Name: "arch", About: "Архитектура только гексагональная.", Scope: ScopeTask,
				Kind: KindArchOnly, Values: []string{"hexagonal"}},
			"Оставляем hexagonal: порты и адаптеры.",
			"Проще разложить по MVC.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if v := checkMachine([]Invariant{tc.inv}, tc.clean); len(v) != 0 {
				t.Fatalf("чистый ответ признан нарушением: %+v", v)
			}
			v := checkMachine([]Invariant{tc.inv}, tc.violating)
			if len(v) != 1 {
				t.Fatalf("нарушение не найдено, получено %d", len(v))
			}
			if v[0].Enforce != EnforceMachine {
				t.Fatalf("нарушение помечено %q, ожидалось %q", v[0].Enforce, EnforceMachine)
			}
			if v[0].Detail == "" {
				t.Fatal("нарушение без детали — по такому отказу нельзя понять, что именно не так")
			}
		})
	}
}

// A correct refusal names the thing it refuses, and the check must not read that as
// proposing it. The exemption is per sentence, so an answer that refuses and then
// complies anyway is still caught — which is the only reason the exemption is safe.
func TestARefusalIsNotItselfAViolation(t *testing.T) {
	inv := stackOnlyKotlin()
	cases := []struct {
		name string
		text string
		bad  bool
	}{
		{
			"a clean refusal naming what it refuses",
			"Java использовать нельзя: разрешены только Kotlin и Ktor. Предлагаю решение на Ktor.",
			false,
		},
		{
			"a refusal that then complies anyway",
			"Я не могу предложить Java. Вот пример на Java со Spring Boot:",
			true,
		},
		{
			"plain compliance",
			"Возьмём Java и Spring Boot.",
			true,
		},
		{
			"a bulleted list where only one item breaks the rule",
			"Что нужно:\n- Ktor для HTTP\n- Spring Security для входа",
			true,
		},
		{
			"an answer within the rules",
			"Берём Kotlin и Ktor, больше ничего не нужно.",
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := len(checkMachine([]Invariant{inv}, tc.text)) > 0
			if got != tc.bad {
				t.Fatalf("нарушение = %v, ожидалось %v для %q", got, tc.bad, tc.text)
			}
			// MentionsForbidden answers the other question and must say yes in every
			// case where the banned name appears at all, refusal or not.
			mentions := MentionsForbidden([]Invariant{inv}, tc.text)
			wantMention := strings.Contains(strings.ToLower(tc.text), "java") ||
				strings.Contains(strings.ToLower(tc.text), "spring")
			if mentions != wantMention {
				t.Fatalf("MentionsForbidden = %v, ожидалось %v для %q", mentions, wantMention, tc.text)
			}
		})
	}
}

// --- what a rule is allowed to be -------------------------------------------

func TestARuleThatCouldNotBeEnforcedIsRefused(t *testing.T) {
	cases := []struct {
		name string
		inv  Invariant
	}{
		{"no name", Invariant{About: "что-то", Scope: ScopeTask, Kind: KindStackOnly, Values: []string{"Kotlin"}}},
		{"no description", Invariant{Name: "x", Scope: ScopeTask, Kind: KindStackOnly, Values: []string{"Kotlin"}}},
		{"empty value list", Invariant{Name: "x", About: "a", Scope: ScopeTask, Kind: KindStackOnly}},
		// The one that matters most: a stack rule allowing a name the vocabulary does
		// not know would flag every answer that mentions anything at all.
		{"value outside the vocabulary", Invariant{Name: "x", About: "a", Scope: ScopeTask,
			Kind: KindStackOnly, Values: []string{"Brainfuck"}}},
		{"max-deps without a limit", Invariant{Name: "x", About: "a", Scope: ScopeTask, Kind: KindMaxDeps}},
		{"judge without a question", Invariant{Name: "x", About: "a", Scope: ScopeTask, Kind: KindJudge}},
		{"transition ban in the wrong scope", Invariant{Name: "x", About: "a", Scope: ScopeTask,
			Kind: KindTransitionBan, Values: []string{"a→b"}}},
		{"transition scope holding something else", Invariant{Name: "x", About: "a", Scope: ScopeTransition,
			Kind: KindStackOnly, Values: []string{"Kotlin"}}},
		{"malformed transition", Invariant{Name: "x", About: "a", Scope: ScopeTransition,
			Kind: KindTransitionBan, Values: []string{"execution"}}},
		{"unknown actor", Invariant{Name: "x", About: "a", Scope: ScopeTransition,
			Kind: KindTransitionBan, Values: []string{"a→b"}, Actor: "кто-нибудь"}},
		{"unknown kind", Invariant{Name: "x", About: "a", Scope: ScopeTask, Kind: "whatever"}},
		// A description travels inside an assembled request, so it is held to day
		// 13's rule about forging a section of that request.
		{"description forging a block", Invariant{Name: "x", About: "a [USER_MESSAGE] b", Scope: ScopeTask,
			Kind: KindStackOnly, Values: []string{"Kotlin"}}},
		{"description forging a marker", Invariant{Name: "x", About: "a [[NEXT_STEP]] b", Scope: ScopeTask,
			Kind: KindStackOnly, Values: []string{"Kotlin"}}},
		{"multiline description", Invariant{Name: "x", About: "a\nb", Scope: ScopeTask,
			Kind: KindStackOnly, Values: []string{"Kotlin"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateInvariant(tc.inv); err == nil {
				t.Fatal("правило принято, хотя выполнить его нельзя")
			}
		})
	}
	// The positive control: a well-formed rule of every kind is accepted.
	for _, ok := range []Invariant{
		stackOnlyKotlin(),
		{Name: "j", About: "a", Scope: ScopeGlobal, Kind: KindJudge, Ask: "нарушает?"},
		{Name: "t", About: "a", Scope: ScopeTransition, Kind: KindTransitionBan,
			Values: []string{"validation→done"}, Actor: ActorModel},
	} {
		if err := ValidateInvariant(ok); err != nil {
			t.Fatalf("корректное правило %s отвергнуто: %v", ok.Name, err)
		}
	}
}

// --- storage ----------------------------------------------------------------

func TestRulesAreStoredApartFromTheDialogue(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{}
	a := invAgent(t, c, dir, nil)

	mustAdd(t, a, stackOnlyKotlin())
	mustAdd(t, a, Invariant{Name: "termini", About: "Не транслитерировать английские термины.",
		Scope: ScopeGlobal, Kind: KindNoBanned, Values: []string{"сервис-леер"}})

	global := InvariantsPath(dir, "михаил")
	task := TaskInvariantsPath(dir, "михаил", "авторизация")
	for _, p := range []string{global, task} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("файл %s не создан: %v", p, err)
		}
	}
	// The scope decides the file. A task rule in the global file would outlive the
	// task it was written for.
	var gf InvariantFile
	readJSON(t, global, &gf)
	if len(gf.Invariants) != 1 || gf.Invariants[0].Name != "termini" {
		t.Fatalf("в глобальном файле %+v", gf.Invariants)
	}
	if gf.Task != "" {
		t.Fatalf("глобальный файл помечен задачей %q", gf.Task)
	}
	var tf InvariantFile
	readJSON(t, task, &tf)
	if len(tf.Invariants) != 1 || tf.Invariants[0].Name != "stack" {
		t.Fatalf("в файле задачи %+v", tf.Invariants)
	}

	// Storage survives a dialogue that is thrown away: that is what "отдельно от
	// диалога" has to mean to be worth anything.
	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := len(a.InvariantState().Invariants); got != 2 {
		t.Fatalf("после сброса диалога осталось %d правил, ожидалось 2", got)
	}

	if err := a.RemoveInvariant("stack"); err != nil {
		t.Fatalf("RemoveInvariant: %v", err)
	}
	if got := len(a.InvariantState().Invariants); got != 1 {
		t.Fatalf("после удаления осталось %d правил", got)
	}
}

func TestAFileWithARuleInTheWrongPlaceIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := InvariantsPath(dir, "михаил")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, err := json.Marshal(InvariantFile{
		Version: InvariantsVersion, User: "михаил",
		Invariants: []Invariant{stackOnlyKotlin()}, // task-scoped, in the global file
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = New(&invCaller{}, Config{
		Memory:     memoryConfig(dir, "михаил", ""),
		Task:       taskConfig(),
		Invariants: invConfig(dir, nil),
	})
	if err == nil {
		t.Fatal("файл с правилом задачи в глобальном файле принят")
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
}

// --- the request ------------------------------------------------------------

// The compatibility claim of days 11-13, continued: invariants that are off, or on
// with nothing stored, cost nothing at all. Not "almost nothing" — the same bytes.
func TestAbsentInvariantsChangeTheRequestByNotOneByte(t *testing.T) {
	ask := func(t *testing.T, withInvariants bool) []llm.Message {
		t.Helper()
		dir := t.TempDir()
		c := &invCaller{}
		cfg := Config{Memory: memoryConfig(dir, "михаил", ""), Task: taskConfig()}
		if withInvariants {
			cfg.Invariants = invConfig(dir, nil)
		}
		a, err := New(c, cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := a.StartTask("авторизация"); err != nil {
			t.Fatalf("StartTask: %v", err)
		}
		if _, err := a.Ask(context.Background(), "вопрос"); err != nil {
			t.Fatalf("Ask: %v", err)
		}
		return c.sent[0]
	}
	off := ask(t, false)
	on := ask(t, true)
	if len(off) != len(on) {
		t.Fatalf("сообщений без инвариантов %d, с пустыми инвариантами %d", len(off), len(on))
	}
	for i := range off {
		if off[i] != on[i] {
			t.Fatalf("сообщение %d разошлось:\nбез: %q\nс:   %q", i, off[i].Content, on[i].Content)
		}
	}
}

func TestTheRulesRideAtTheFrontOfTheRequest(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{}
	a := invAgent(t, c, dir, nil)
	mustAdd(t, a, stackOnlyKotlin())
	if _, err := a.Ask(context.Background(), "как сделать авторизацию"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	sent := c.sent[0]
	if sent[0].Role != "system" {
		t.Fatalf("первое сообщение — %s, ожидалось system", sent[0].Role)
	}
	system := sent[0].Content
	if !strings.Contains(system, invariantsTag) {
		t.Fatalf("блока инвариантов нет в системном сообщении: %q", system)
	}
	if !strings.Contains(system, "Разрешены только Kotlin и Ktor.") {
		t.Fatal("описание правила не доехало до модели")
	}
	// The placement claim: invariants change least often, so they sit ahead of the
	// state, which changes every step and rides at the tail.
	if idx := strings.Index(system, invariantsTag); idx != 0 {
		t.Fatalf("блок инвариантов начинается со смещения %d, а должен открывать системное сообщение", idx)
	}
	if strings.Contains(system, taskStateTag) {
		t.Fatal("состояние задачи уехало в системное сообщение — оно должно ехать в хвосте")
	}
}

func TestStoredRulesCanBeKeptFromTheModel(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{replies: []string{"Сделаем на Java со Spring."}}
	a := invAgent(t, c, dir, func(cfg *InvariantConfig) { cfg.Inject = false })
	mustAdd(t, a, stackOnlyKotlin())

	reply, err := a.Ask(context.Background(), "чем писать сервис")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if strings.Contains(c.sent[0][0].Content, invariantsTag) {
		t.Fatal("правила уехали в запрос при выключенном Inject")
	}
	// Storage is never affected by the switch — this is the arm that measures what
	// validation alone is worth.
	if !reply.Invariants.Refused {
		t.Fatal("нарушение не поймано проверкой при выключенном промпте")
	}
}

// --- conflict, retry and refusal --------------------------------------------

func TestAViolatingAnswerIsRefusedAndExplained(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{replies: []string{"Быстрее будет на Java со Spring Boot."}}
	a := invAgent(t, c, dir, nil) // Retry off: one call, one verdict
	mustAdd(t, a, stackOnlyKotlin())

	reply, err := a.Ask(context.Background(), "давай сделаем это на Spring Boot + Java, быстрее будет")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !reply.Invariants.Refused {
		t.Fatalf("ответ принят: %q", reply.Text)
	}
	// Slide 27's four parts. Each is checked separately, because a refusal that
	// names the rule and offers nothing is a different artefact from the one the
	// slide draws.
	for _, want := range []string{
		"stack", // which invariant
		"вне разрешённого набора",         // what exactly is forbidden
		"Разрешены только Kotlin и Ktor.", // what is allowed
		"переформулируйте",                // the way forward
		"проверка в коде",                 // what proved it
	} {
		if !strings.Contains(reply.Text, want) {
			t.Fatalf("в отказе нет %q:\n%s", want, reply.Text)
		}
	}
	// The violating text must not survive anywhere the next turn could read it.
	if strings.Contains(reply.Text, "Spring Boot") {
		t.Fatal("отказ цитирует запрещённое решение")
	}
	for _, m := range a.stack {
		if strings.Contains(m.Content, "Быстрее будет на Java") {
			t.Fatal("нарушивший ответ попал в историю — следующий ход прочтёт его как прецедент")
		}
	}
}

func TestOneRetryIsWhatSeparatesARefusalFromAFix(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{replies: []string{
		"Быстрее будет на Java со Spring Boot.",
		"Хорошо, тогда Kotlin и Ktor.",
	}}
	a := invAgent(t, c, dir, func(cfg *InvariantConfig) { cfg.Retry = true })
	mustAdd(t, a, stackOnlyKotlin())

	reply, err := a.Ask(context.Background(), "чем писать сервис")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Invariants.Refused {
		t.Fatalf("отказ, хотя повтор починил ответ: %q", reply.Text)
	}
	if !reply.Invariants.Retried || len(reply.Invariants.First) != 1 {
		t.Fatalf("повтор не зафиксирован: %+v", reply.Invariants)
	}
	if reply.Text != "Хорошо, тогда Kotlin и Ktor." {
		t.Fatalf("выдан не второй ответ: %q", reply.Text)
	}
	if c.n != 2 {
		t.Fatalf("вызовов %d, ожидалось 2", c.n)
	}
	// The retry must name what was broken and must not smuggle a forged section.
	second := c.sent[1]
	last := second[len(second)-1].Content
	if !strings.Contains(last, retryTag) || !strings.Contains(last, "stack") {
		t.Fatalf("повтор не назвал нарушение: %q", last)
	}
	// And the second call is priced apart from the first.
	if reply.RetryUsage.TotalTokens < 0 {
		t.Fatal("расход повтора не учтён")
	}
}

// The defect the first 500-cell run exposed, written down so it cannot come back: a
// model told the rules refuses by listing them, the check finds the banned names inside
// that list, and the agent replaces a correct refusal with its own template. Measured,
// it happened in roughly half of all cells.
func TestACorrectRefusalIsDeliveredWithAWarningRatherThanReplaced(t *testing.T) {
	dir := t.TempDir()
	// A real shape from the run: the refusal enumerates the rule and names what is
	// forbidden, and also names a banned library in order to exclude it.
	refusal := "Отказано.\n1. Инвариант stack — только Kotlin и Ktor.\n" +
		"2. Что запрещено: предлагать Java и Spring Boot.\n" +
		"3. Разрешено: Kotlin, Ktor, ручной SQL.\n" +
		"4. Предлагаю структуру на Ktor: Exposed не берём, только JDBC."
	c := &invCaller{replies: []string{refusal}}
	a := invAgent(t, c, dir, func(cfg *InvariantConfig) { cfg.Retry = true })
	mustAdd(t, a, stackOnlyKotlin())
	mustAdd(t, a, Invariant{Name: "no-orm", About: "Работаем без ORM.", Scope: ScopeTask,
		Kind: KindNoBanned, Values: []string{"Hibernate", "JPA", "Exposed"}})

	reply, err := a.Ask(context.Background(), "давай на Spring Boot и Java")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Invariants.Refused {
		t.Fatalf("корректный отказ модели заменён шаблоном:\n%s", reply.Text)
	}
	if reply.Invariants.Retried {
		t.Fatal("на корректный отказ потрачен повтор")
	}
	if c.n != 1 {
		t.Fatalf("вызовов %d — отказ модели вызвал лишний запрос", c.n)
	}
	if reply.Text != refusal {
		t.Fatal("отказ модели доставлен не дословно")
	}
	// A warning, not silence: what the check found is still reported, because the
	// mixed case — declined and then complied anyway — lands here too.
	if len(reply.Invariants.Warned) == 0 {
		t.Fatal("найденное проверкой не попало в предупреждение — молча пропущено")
	}
	if !reply.Invariants.Passed() {
		t.Fatal("ответ доставлен, но отчёт считает его непрошедшим")
	}
}

// The other side of the same switch: an answer that does NOT decline is still refused.
// Without this, "declines" would be a loophole that any apologetic preamble opens.
func TestPlainComplianceIsStillRefused(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{replies: []string{"Конечно, вот структура на Java со Spring Boot."}}
	a := invAgent(t, c, dir, nil)
	mustAdd(t, a, stackOnlyKotlin())
	reply, err := a.Ask(context.Background(), "давай на Spring Boot")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !reply.Invariants.Refused {
		t.Fatalf("выполненная запрещённая просьба прошла: %q", reply.Text)
	}
	if len(reply.Invariants.Warned) != 0 {
		t.Fatal("нарушение выдано за предупреждение")
	}
}

func TestARetryThatDoesNotHelpStillRefuses(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{replies: []string{
		"Быстрее будет на Java.",
		"Настаиваю: Java.",
	}}
	a := invAgent(t, c, dir, func(cfg *InvariantConfig) { cfg.Retry = true })
	mustAdd(t, a, stackOnlyKotlin())

	reply, err := a.Ask(context.Background(), "чем писать сервис")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !reply.Invariants.Refused || len(reply.Invariants.Final) != 1 {
		t.Fatalf("после неудачного повтора отказа нет: %+v", reply.Invariants)
	}
}

func TestARefusedAnswerMovesNothing(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{replies: []string{"Делаем на Java, шаг закрыт.\n" + markerNextStep}}
	a := invAgent(t, c, dir, nil)
	mustAdd(t, a, stackOnlyKotlin())
	planOf(t, a, "первый", "второй")

	before := a.TaskState().Step
	reply, err := a.Ask(context.Background(), "погнали")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !reply.Invariants.Refused {
		t.Fatal("ожидался отказ")
	}
	if reply.Move.StepApplied {
		t.Fatal("нарушивший ответ закрыл шаг")
	}
	if after := a.TaskState().Step; after != before {
		t.Fatalf("шаг сдвинулся с %d на %d при отказе", before, after)
	}
}

// --- the state machine's own invariant --------------------------------------

func TestAModelMayNotCloseTheTaskWithoutTheUser(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{}
	a := invAgent(t, c, dir, nil)
	mustAdd(t, a, Invariant{
		Name: "no-silent-done", About: "Задача не закрывается без явного согласия пользователя.",
		Scope: ScopeTransition, Kind: KindTransitionBan,
		Values: []string{"validation→done"}, Actor: ActorModel,
	})
	planOf(t, a, "шаг")
	if err := a.TaskGo(StageExecution, ""); err != nil {
		t.Fatalf("TaskGo execution: %v", err)
	}
	if err := a.TaskGo(StageValidation, ""); err != nil {
		t.Fatalf("TaskGo validation: %v", err)
	}

	// The model asks for the move the table allows and the invariant does not.
	move := a.applyTaskMove("готово", false, StageDone)
	if move.StageApplied {
		t.Fatal("модель закрыла задачу сама")
	}
	if !move.Blocked {
		t.Fatalf("переход не помечен запрещённым инвариантом: %+v", move)
	}
	if move.Illegal {
		t.Fatal("запрет инварианта выдан за отсутствующий переход — это разные находки")
	}
	if got := a.TaskState().State; got != StageValidation {
		t.Fatalf("стадия стала %q, ожидалась %q", got, StageValidation)
	}

	// The user's own command IS the consent the invariant asks for.
	if err := a.TaskGo(StageDone, ""); err != nil {
		t.Fatalf("пользователь не смог закрыть задачу: %v", err)
	}
	if got := a.TaskState().State; got != StageDone {
		t.Fatalf("после команды пользователя стадия %q", got)
	}
}

func TestAnActorAnyBanBindsTheUserToo(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{}
	a := invAgent(t, c, dir, nil)
	mustAdd(t, a, Invariant{
		Name: "no-done", About: "Задачу нельзя закрывать в этом проекте.",
		Scope: ScopeTransition, Kind: KindTransitionBan,
		Values: []string{"validation→done"}, Actor: ActorAny,
	})
	planOf(t, a, "шаг")
	mustGo(t, a, StageExecution)
	mustGo(t, a, StageValidation)
	if err := a.TaskGo(StageDone, ""); err == nil {
		t.Fatal("запрет с actor=any не связал пользователя")
	}
}

func mustGo(t *testing.T, a *Agent, s TaskStage) {
	t.Helper()
	if err := a.TaskGo(s, ""); err != nil {
		t.Fatalf("TaskGo %s: %v", s, err)
	}
}

// --- the judge --------------------------------------------------------------

func TestTheJudgeIsAnOpinionAndIsLabelledAsOne(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{replies: []string{
		"Возьмите платный Auth0, будет быстрее.",
		"budget: VIOLATION — предлагает платный сервис",
	}}
	a := invAgent(t, c, dir, func(cfg *InvariantConfig) { cfg.Judge = true })
	mustAdd(t, a, Invariant{
		Name: "budget", About: "Только бесплатные сервисы: бюджет проекта нулевой.",
		Scope: ScopeTask, Kind: KindJudge,
		Ask: "Предлагает ли ответ решение, требующее платного стороннего сервиса?",
	})

	reply, err := a.Ask(context.Background(), "как сделать вход через соцсети")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !reply.Invariants.Refused {
		t.Fatalf("судья нашёл нарушение, но отказа нет: %+v", reply.Invariants)
	}
	if reply.Invariants.JudgeCalls != 1 {
		t.Fatalf("вызовов судьи %d, ожидался 1", reply.Invariants.JudgeCalls)
	}
	if reply.Invariants.Final[0].Enforce != EnforceJudge {
		t.Fatal("вердикт судьи выдан за машинную проверку")
	}
	// #3162: "Но тоже не гарантия". The refusal has to say so, or it presents an
	// opinion of another model as a decided fact.
	if !strings.Contains(reply.Text, "не гарантия") {
		t.Fatalf("отказ по судье не оговаривает отсутствие гарантии:\n%s", reply.Text)
	}
}

func TestAJudgeThatSaysOKIsNotAViolation(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{replies: []string{"Возьмите Keycloak, он свободный.", "budget: OK"}}
	a := invAgent(t, c, dir, func(cfg *InvariantConfig) { cfg.Judge = true })
	mustAdd(t, a, Invariant{
		Name: "budget", About: "Только бесплатные сервисы.", Scope: ScopeTask,
		Kind: KindJudge, Ask: "Требует ли ответ платного сервиса?",
	})
	reply, err := a.Ask(context.Background(), "чем закрыть OAuth")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Invariants.Refused {
		t.Fatalf("оправдательный вердикт превращён в отказ: %+v", reply.Invariants)
	}
}

func TestAJudgeThatDidNotAnswerIsNotAPass(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{
		replies: []string{"Возьмите платный Auth0.", ""},
		errs:    []error{nil, context.DeadlineExceeded},
	}
	a := invAgent(t, c, dir, func(cfg *InvariantConfig) { cfg.Judge = true })
	mustAdd(t, a, Invariant{
		Name: "budget", About: "Только бесплатные сервисы.", Scope: ScopeTask,
		Kind: KindJudge, Ask: "Требует ли ответ платного сервиса?",
	})
	reply, err := a.Ask(context.Background(), "чем закрыть OAuth")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	// Silence is not a verdict: the rule is reported as unchecked, loudly.
	if reply.Invariants.JudgeError == "" {
		t.Fatal("судья не ответил, а отчёт молчит — правило осталось непроверенным незаметно")
	}
}

func TestTheJudgeIsNotCalledWhenItIsOff(t *testing.T) {
	dir := t.TempDir()
	c := &invCaller{replies: []string{"Возьмите платный Auth0."}}
	a := invAgent(t, c, dir, nil) // Judge off
	mustAdd(t, a, Invariant{
		Name: "budget", About: "Только бесплатные сервисы.", Scope: ScopeTask,
		Kind: KindJudge, Ask: "Требует ли ответ платного сервиса?",
	})
	reply, err := a.Ask(context.Background(), "чем закрыть OAuth")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if c.n != 1 {
		t.Fatalf("вызовов %d — судья звонил при выключенном Judge", c.n)
	}
	// A rule nobody ran is not counted as a rule that passed.
	if reply.Invariants.Checked != 0 {
		t.Fatalf("проверено правил %d, а машинных среди них нет", reply.Invariants.Checked)
	}
}

func TestJudgeVerdictsAreParsedConservatively(t *testing.T) {
	rules := []Invariant{
		{Name: "budget", About: "бюджет", Scope: ScopeTask, Kind: KindJudge, Ask: "?"},
		{Name: "tone", About: "тон", Scope: ScopeGlobal, Kind: KindJudge, Ask: "?"},
	}
	got := parseJudgeVerdicts(rules, "budget: VIOLATION — предлагает платный Auth0\ntone: OK")
	if len(got) != 1 || got[0].Name != "budget" {
		t.Fatalf("разобрано %+v", got)
	}
	if got[0].Detail != "предлагает платный Auth0" {
		t.Fatalf("деталь разобрана как %q", got[0].Detail)
	}
	// A rule the judge did not mention, and a line that makes no sense, leave the
	// answer alone: an unreadable verdict is our failure, not the answer's.
	if v := parseJudgeVerdicts(rules, "непонятно что"); len(v) != 0 {
		t.Fatalf("мусор разобран как нарушения: %+v", v)
	}
	if v := parseJudgeVerdicts(rules, "unknown: VIOLATION — что-то"); len(v) != 0 {
		t.Fatalf("вердикт про неизвестное правило принят: %+v", v)
	}
}

// --- configuration ----------------------------------------------------------

func TestASwitchThatCouldNeverFireIsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		tune func(*InvariantConfig)
	}{
		{"retry without check", func(c *InvariantConfig) { c.Check = false; c.Retry = true }},
		{"judge without check", func(c *InvariantConfig) { c.Check = false; c.Judge = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(&invCaller{}, Config{
				Memory:     memoryConfig(dir, "михаил", ""),
				Task:       taskConfig(),
				Invariants: invConfig(dir, tc.tune),
			})
			if err == nil {
				t.Fatal("конфигурация принята, хотя переключатель никогда не сработает")
			}
		})
	}
	// And the one that must be refused for a different reason: rules and memory
	// pointing at different users would judge one person's answers by another's laws.
	_, err := New(&invCaller{}, Config{
		Memory:     memoryConfig(dir, "михаил", ""),
		Invariants: &InvariantConfig{Dir: dir, User: "другой", Check: true},
	})
	if err == nil {
		t.Fatal("инварианты одного пользователя приняты поверх памяти другого")
	}
}
