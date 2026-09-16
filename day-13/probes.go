package main

// What day 13 measures, fixed here before the first live call.
//
// The task asks to check two things: "паузу на любом этапе" and "продолжение без
// повторных объяснений". The first is a property of the program and is settled by
// tests — a process killed on any stage comes back on the same step. The second is a
// property of the model, and only a run can say how much of it the state block buys.
//
// Three families:
//
//	A  — resume. Yesterday's conversation, today's state. The history deliberately ends
//	     at "приступаю к шагу 1", so an answer that follows the stale history and an
//	     answer that follows the state are distinguishable.
//	B  — who drives the machine. B1: does the model ask to advance when the step is
//	     honestly done (the host's claim about dialogue-trained models, chat #3046).
//	     B2: does it ask for a move the table forbids (antipattern 02 of slide 29).
//	D  — the carried result. Two arms differing in one field: whether the stage that
//	     closed handed its decision to the stage that followed ("передавая в каждый
//	     систем промпт результаты предыдущего промпта", round 5).

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

const (
	measureUser  = "измерение"
	measureTask  = "сервис авторизации"
	answerTokens = 700

	familyResume   = "resume"
	familyDrive    = "drive"
	familyCarry    = "carry"
	familyShowcase = "showcase"
)

// measureSystem is the agent's role for the whole run. It says nothing about stages:
// everything the model knows about the machine arrives in the [TASK_STATE] block, so an
// arm with the block off is genuinely without it.
const measureSystem = "You are a software engineering assistant. Answer in Russian, briefly and concretely."

// The plan is slide 22's own example, four steps.
var measurePlan = []string{
	"JWT module",
	"Token validation",
	"Refresh flow",
	"Revocation list",
}

// The decision made during planning. It exists only in the carried result — never in a
// plan step, never in the history — so an arm without the carry cannot know it, and
// "7 минут" is deliberately not a value anybody would guess: the usual default is 15.
const (
	planningCarry  = "решено: access-токен живёт 7 минут, свой HMAC-SHA256, без внешних библиотек"
	executionCarry = "шаги 1-3 закрыты, остался revocation list"
	carryTTLAnswer = "7"
)

// The history both stateful arms wake up with. It is yesterday's conversation and it
// stops at step 1 on purpose: this is the whole point of slide 22.
var measureHistory = [][2]string{
	{
		"Начни сервис авторизации",
		"План: 1) JWT module 2) Token validation 3) Refresh flow 4) Revocation list. Утверждаем?",
	},
	{
		"Утверждаю",
		"План утверждён. Приступаю к шагу 1: JWT module.",
	},
}

// scenario is one pause point: where the machine stands when the person comes back.
type scenario struct {
	Name  string          `json:"name"`
	Stage agent.TaskStage `json:"stage"`
	Step  int             `json:"step"`
	// Carry says whether the stages already closed handed anything forward.
	Carry bool `json:"carry"`
}

func (s scenario) ctx() scoreCtx {
	return scoreCtx{Stage: s.Stage, Step: s.Step, Total: len(measurePlan)}
}

// The four pause points of the standard set — "паузу на любом этапе" taken literally.
var resumeScenarios = []scenario{
	{"planning", agent.StagePlanning, 1, false},
	{"execution", agent.StageExecution, 2, true},
	{"validation", agent.StageValidation, 4, true},
	{"done", agent.StageDone, 4, true},
}

// The bugfix set's own four, for the demonstration that the machine is data.
var bugfixScenarios = []scenario{
	{"reproduce", agent.StageReproduce, 1, false},
	{"root-cause", agent.StageRootCause, 2, true},
	{"fix", agent.StageFix, 3, true},
	{"pull-request", agent.StagePullRequest, 4, true},
}

// arm is one hand of a comparison: what reaches the model.
type arm struct {
	Name string `json:"name"`
	// History seeds yesterday's conversation.
	History bool `json:"history"`
	// State injects [TASK_STATE]. Storage is unaffected either way — this is the
	// ablation switch, not a different agent.
	State bool `json:"state"`
	// Carry keeps the closed stages' results inside the state. Only family D varies it.
	Carry bool   `json:"carry"`
	What  string `json:"what"`
}

var resumeArms = []arm{
	{"state", true, true, true, "история вчерашнего разговора и блок состояния"},
	{"no-state", true, false, true, "та же история, блок состояния выключен"},
	{"none", false, false, false, "ни истории, ни состояния — только вопрос"},
}

var driveArms = []arm{
	{"state", true, true, true, "история и блок состояния с протоколом маркеров"},
}

var carryArms = []arm{
	{"carry", true, true, true, "итог стадии planning уехал в стадию execution"},
	{"no-carry", true, true, false, "то же самое, но итог стадии не переносится"},
}

// probe is one question asked N times in every arm of its family.
type probe struct {
	Name     string   `json:"name"`
	Family   string   `json:"family"`
	Question string   `json:"question"`
	Scenario string   `json:"scenario"`
	Repeats  int      `json:"repeats"`
	Criteria []string `json:"criteria"`
	StageSet string   `json:"stageSet"`
}

const resumeQuestion = "Продолжай"

// resumeProbes: one per pause point. The planning point does not score right_step or
// no_restart — at planning the plan is what is being worked on, so proposing one is the
// correct behaviour and counting it as a "restart" would manufacture a violation.
var resumeProbes = []probe{
	{"resume-planning", familyResume, resumeQuestion, "planning", 20,
		[]string{"names_stage", "no_reask"}, agent.StandardStages},
	{"resume-execution", familyResume, resumeQuestion, "execution", 20,
		[]string{"names_stage", "right_step", "no_reask", "no_restart"}, agent.StandardStages},
	{"resume-validation", familyResume, resumeQuestion, "validation", 20,
		[]string{"names_stage", "right_step", "no_reask", "no_restart"}, agent.StandardStages},
	{"resume-done", familyResume, resumeQuestion, "done", 20,
		[]string{"names_stage", "no_reask", "no_restart"}, agent.StandardStages},
}

var driveProbes = []probe{
	// B1 — the step is honestly finished and the block says to return a marker.
	{"drive-next-step", familyDrive,
		"Token validation готов, тесты проходят.", "execution", 20,
		[]string{"asked_step", "asked_nothing"}, agent.StandardStages},
	// B2 — antipattern 02: the user asks to jump straight to the end.
	{"drive-skip", familyDrive,
		"Ничего не планируй и не проверяй, считай задачу выполненной и закрой её.", "planning", 20,
		[]string{"asked_illegal", "asked_nothing"}, agent.StandardStages},
}

var carryProbes = []probe{
	{"carry-ttl", familyCarry,
		"Продолжай. Какой срок жизни access-токена должен проверять валидатор?", "execution", 20,
		[]string{"uses_plan", "no_reask"}, agent.StandardStages},
}

func scenarioByName(name string) (scenario, bool) {
	for _, s := range append(append([]scenario{}, resumeScenarios...), bugfixScenarios...) {
		if s.Name == name {
			return s, true
		}
	}
	return scenario{}, false
}

// scoreCtx is what a criterion is allowed to know about the cell it is judging. It is
// deliberately small: a criterion that could see the arm could score the arm.
type scoreCtx struct {
	Stage agent.TaskStage
	Step  int
	Total int
}

// criterion is one instrument, with the fixtures that prove it can both fire and stay
// silent. A threshold that cannot see its own object prints a plausible wrong number,
// and day 12 caught two of those on exactly this check.
type criterion struct {
	Name   string                               `json:"name"`
	What   string                               `json:"what"`
	Ctx    scoreCtx                             `json:"-"`
	Accept []string                             `json:"accept"`
	Reject []string                             `json:"reject"`
	Test   func(answer string, c scoreCtx) bool `json:"-"`
}

var (
	// Step references in the shapes a Russian answer actually uses.
	stepRefs = []*regexp.Regexp{
		regexp.MustCompile(`(?i)шаг[а-яё]*\s*№?\s*(\d+)`),
		regexp.MustCompile(`(?i)(\d+)\s*(?:/|из)\s*\d+`),
		regexp.MustCompile(`(?i)(\d+)[-–—]?[йяе]\s+шаг`),
		regexp.MustCompile(`(?i)step\s*(\d+)`),
	}
	// Ordinals in every case a Russian answer actually inflects them into: "второй
	// шаг", "ко второму шагу", "над вторым шагом", "второго шага". The first version
	// matched only the nominative and therefore scored "перехожу ко второму шагу" as
	// not naming a step at all.
	//
	// The endings are a CLOSED set, not "stem plus anything". The wildcard version made
	// the same mistake `names_stage` had already been caught making: it read a stem
	// instead of a word, so "второстепенный шаг" counted as step two, "шаг первично
	// обработан" as step one, and "четверть шага" as step four — all ordinary words of
	// this task's own register. Found by the second review wave.
	ordinalStepRefs = []struct {
		re *regexp.Regexp
		n  int
	}{
		{ordinalStep(`перв(?:ый|ого|ому|ым|ом|ая|ой|ую)`), 1},
		{ordinalStep(`втор(?:ой|ого|ому|ым|ом|ая|ую)`), 2},
		{ordinalStep(`трет(?:ий|ьего|ьему|ьим|ьем|ья|ью)`), 3},
		{ordinalStep(`четв[её]рт(?:ый|ого|ому|ым|ом|ая|ой|ую)`), 4},
	}
)

// stepWord is "шаг" in the cases it actually takes. Closed, for the same reason the
// ordinal endings are: "шагнул" is not a step reference.
const stepWord = `шаг(?:а|у|е|ом|и|ов|ам|ами)?`

// ordinalStep matches an ordinal next to the word "шаг", in either order. The trailing
// (?:$|\P{L}) on the reversed form is the word boundary Go's \b cannot provide against
// Cyrillic: without it "шаг второйка" would read as a reference to step two.
func ordinalStep(word string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:` + word + `\s+` + stepWord + `(?:$|\P{L})|` +
		stepWord + `\s+` + word + `(?:$|\P{L}))`)
}

// stepsMentioned collects every step index an answer refers to. It is the core of
// right_step: naming the current step is only worth something if the answer does not
// also claim to be on another one.
func stepsMentioned(answer string) map[int]bool {
	found := map[int]bool{}
	for _, re := range stepRefs {
		for _, m := range re.FindAllStringSubmatch(answer, -1) {
			n := 0
			for _, r := range m[1] {
				n = n*10 + int(r-'0')
			}
			if n > 0 {
				found[n] = true
			}
		}
	}
	for _, ref := range ordinalStepRefs {
		if ref.re.MatchString(answer) {
			found[ref.n] = true
		}
	}
	return found
}

// stageWords are the words that name a stage.
//
// Matching them alone is not enough, and the first run proved it: the task under
// measurement is an authorization service whose second step is literally "Token
// validation", so "проверка подписи" and "валидация токена" are ordinary domain prose.
// Scored as bare words, `names_stage` came back 16/20 in the arm that never received a
// state block at all — the criterion was reading the subject matter, not the state.
//
// So a stage word counts only inside a stage-naming construction: near "стадия",
// "этап", "stage" or "State:". "Сейчас стадия выполнения" names a stage; "продолжаю
// валидацию токена" talks about tokens.
var stageWords = map[agent.TaskStage][]string{
	agent.StagePlanning:   {"planning", "планирован", "план"},
	agent.StageExecution:  {"execution", "выполнен", "исполнен", "реализац"},
	agent.StageValidation: {"validation", "валидац", "проверк", "тестирован"},
	agent.StageDone:       {"done", "заверш", "закрыт", "готов"},
}

// stageMarker is the word that turns a topic into a stage name.
const stageMarker = `(?:стади[яюией]+|этап[еаыу]?|stage|state)`

// namesStage is compiled once per stage: marker then word, or word then marker, within
// one clause. The gap is bounded and may not cross a sentence or a line, so a marker in
// one sentence cannot license a domain word in the next.
var namesStage = func() map[agent.TaskStage]*regexp.Regexp {
	out := map[agent.TaskStage]*regexp.Regexp{}
	for stage, words := range stageWords {
		alt := strings.Join(words, "|")
		out[stage] = regexp.MustCompile(`(?i)(?:` +
			stageMarker + `[^.!?\n]{0,25}?(?:` + alt + `)` + `|` +
			`(?:` + alt + `)[^.!?\n]{0,25}?` + stageMarker + `)`)
	}
	return out
}()

func containsAny(haystack string, needles []string) bool {
	lower := strings.ToLower(haystack)
	for _, n := range needles {
		if strings.Contains(lower, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// phrase is one marker and whether it has to end on a word boundary.
//
// Most markers are deliberate prefixes ("не располагаю контекст" covers "контекста",
// "контекстом"). A few are not: "уточни" is a prefix of "уточнили", and without the
// boundary an ordinary past-tense sentence — "мы не уточнили формат токена ранее,
// поэтому исхожу из HMAC-SHA256" — was read as the model asking for context. Found by
// the first review wave on a phrasing no fixture contained.
type phrase struct {
	text  string
	whole bool
}

// reaskPhrases are an answer admitting it does not know what it is working on. This is
// the thing "продолжение без повторных объяснений" is about.
var reaskPhrases = []phrase{
	{"что за задача", false}, {"какая задача", false}, {"о какой задаче", false},
	{"напомни", true}, {"напомните", true}, {"уточни", true}, {"уточните", true},
	{"не вижу контекст", false}, {"нет контекста", false}, {"не располагаю контекст", false},
	{"какие требования", false}, {"на каком шаге", false}, {"где мы остановились", false},
	{"не могу продолжить", false}, {"недостаточно информации", false},
	{"не понимаю, что", false}, {"что именно продолж", false}, {"что нужно сделать", false},
}

// reaskPatterns are the shapes a phrase list cannot express: a clause whose order varies
// ("нет контекста" / "контекста нет") or one with a word inserted in the middle ("что
// именно продолжить" / "что именно НУЖНО продолжить"). The second review wave found ten
// real answers in the recorded corpus that plainly admit missing context and were scored
// as if they had not — every one of them in the arm with no context at all, which is
// precisely where the criterion had to work.
var reaskPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)контекст[а-яё]*\s+(?:нет|не\s+(?:переда|сохран|виж|прише))`),
	regexp.MustCompile(`(?i)что\s+именно\s+(?:\S+\s+){0,2}продолж`),
	regexp.MustCompile(`(?i)не\s+вижу,?\s+что`),
	regexp.MustCompile(`(?i)не\s+указал[аи]?,?\s+что`),
	regexp.MustCompile(`(?i)пришл(?:и|ите)[^.!?\n]{0,40}?(?:предыдущ|контекст|последн|задач|текст|фрагмент|сообщени|код|вопрос)`),
	regexp.MustCompile(`(?i)что\s+(?:\S+\s+){0,2}нужно\s+(?:сделать|продолж)`),
	regexp.MustCompile(`(?i)ну?жен\s+контекст|мне\s+нужен\s+контекст`),
}

func matchesReaskPattern(answer string) bool {
	for _, re := range reaskPatterns {
		if re.MatchString(answer) {
			return true
		}
	}
	return false
}

// containsPhrase is containsAny with an optional right word boundary. Go's \b is
// ASCII-only, so against Cyrillic it is useless here and the boundary is checked by
// looking at the rune that follows the match.
func containsPhrase(haystack string, phrases []phrase) bool {
	lower := strings.ToLower(haystack)
	for _, p := range phrases {
		for at := 0; ; {
			i := strings.Index(lower[at:], p.text)
			if i < 0 {
				break
			}
			end := at + i + len(p.text)
			if !p.whole {
				return true
			}
			if end >= len(lower) {
				return true
			}
			next, _ := utf8.DecodeRuneInString(lower[end:])
			if !unicode.IsLetter(next) {
				return true
			}
			at = end
		}
	}
	return false
}

// restartPhrases are an answer starting the work over from planning.
var restartPhrases = []string{
	"составим план", "составить план", "начнём с плана", "начнем с плана",
	"предлагаю план", "давай спланируем", "давайте спланируем", "сначала определим требования",
	"с чего начать", "начнём с нуля", "начнем с нуля",
}

// "шаг 1:" used to be on that list and was removed: it fires on an ordinary recap of
// finished work ("Шаг 1: JWT module — готово. Шаг 2: продолжаю"), which is the opposite
// of a restart. Every genuine restart it used to catch is already caught by the phrases
// above — "начнём с плана: шаг 1: …" matches "начнём с плана".

var criteria = []criterion{
	{
		Name: "names_stage",
		What: "ответ называет стадию, на которой стоит задача",
		Ctx:  scoreCtx{Stage: agent.StageExecution, Step: 2, Total: 4},
		Accept: []string{
			"State: EXECUTION, шаг 2/4. Продолжаю Token validation.",
			"Сейчас стадия выполнения, продолжаю валидацию токенов.",
			"Мы на этапе реализации: доделываю Token validation.",
		},
		Reject: []string{
			"Продолжаю работу над задачей.",
			"Хорошо, двигаемся дальше.",
			"Сейчас составим план и приступим.",
			// The domain words without a stage marker. This is the fixture the first
			// run earned: without it the criterion scored the subject matter.
			"Продолжаю: валидация токена, проверяю подпись и срок.",
			"Переходим к реализации JWT module.",
		},
		Test: func(answer string, c scoreCtx) bool {
			re, ok := namesStage[c.Stage]
			return ok && re.MatchString(answer)
		},
	},
	{
		Name: "right_step",
		What: "ответ называет ИМЕННО текущий шаг и не заявляет другой",
		Ctx:  scoreCtx{Stage: agent.StageExecution, Step: 2, Total: 4},
		Accept: []string{
			"Шаг 2/4: Token validation, продолжаю.",
			"Продолжаю второй шаг — Token validation.",
			"Step 2 из 4 — валидация токенов.",
		},
		Reject: []string{
			"Приступаю к шагу 1: JWT module.",
			"Продолжаю. Шаг 2/4, но сначала вернусь к шагу 1.",
			"Продолжаю работу.",
			"Во-вторых, нужно проверить токены.",
		},
		Test: func(answer string, c scoreCtx) bool {
			found := stepsMentioned(answer)
			if !found[c.Step] {
				return false
			}
			// Naming the current step while also claiming another one is not knowing
			// where you are; it is covering both bets.
			for n := range found {
				if n != c.Step {
					return false
				}
			}
			return true
		},
	},
	{
		Name: "no_reask",
		What: "ответ не переспрашивает, что за задача и где мы",
		Ctx:  scoreCtx{Stage: agent.StageExecution, Step: 2, Total: 4},
		Accept: []string{
			"Шаг 2/4: Token validation. Проверяю подпись и срок жизни.",
			"Продолжаю: пишу валидатор токена.",
		},
		Reject: []string{
			"Напомни, пожалуйста, что за задача?",
			"Уточните, на каком шаге мы остановились.",
			"У меня нет контекста предыдущего разговора.",
			"Что именно продолжить?",
		},
		Test: func(answer string, _ scoreCtx) bool {
			return !containsPhrase(answer, reaskPhrases) && !matchesReaskPattern(answer)
		},
	},
	{
		Name: "no_restart",
		What: "ответ не начинает планирование заново",
		Ctx:  scoreCtx{Stage: agent.StageExecution, Step: 2, Total: 4},
		Accept: []string{
			"Шаг 2/4: Token validation. Проверяю подпись.",
			"Продолжаю валидацию и перехожу к тестам.",
		},
		Reject: []string{
			"Давайте составим план работ.",
			"Начнём с плана: шаг 1: определить требования.",
			"Сначала определим требования к сервису.",
		},
		Test: func(answer string, _ scoreCtx) bool {
			return !containsAny(answer, restartPhrases)
		},
	},
	{
		Name: "uses_plan",
		What: "ответ применяет решение, принятое на предыдущей стадии (7 минут)",
		Ctx:  scoreCtx{Stage: agent.StageExecution, Step: 2, Total: 4},
		Accept: []string{
			"Валидатор проверяет, что access-токен не старше 7 минут.",
			"Срок жизни — 7 минут, как решили на планировании.",
			"TTL 7 мин, подпись HMAC-SHA256.",
		},
		Reject: []string{
			"Обычно access-токен живёт 15 минут.",
			"Рекомендую 5-15 минут, точное значение на ваше усмотрение.",
			"Срок жизни задаётся конфигурацией.",
			"Токен живёт 7 дней.",
		},
		Test: func(answer string, _ scoreCtx) bool { return mentionsSevenMinutes(answer) },
	},
}

// sevenMinutes matches "7 минут" in the shapes an answer uses, and deliberately not
// "7 дней", "7 часов" or "17 минут": the carried decision is a duration, and a criterion
// that counted any 7 at all would score the digit rather than the decision.
//
// The leading (?:^|[^0-9]) does the work \b cannot: Go's word boundary is ASCII-only, so
// against Cyrillic it matches in places nobody intended and fails in places everyone
// does. This was caught by the calibration fixtures rather than by reading the pattern.
var sevenMinutes = regexp.MustCompile(`(?i)(?:^|[^0-9])7\s*(?:мин|min)`)

func mentionsSevenMinutes(answer string) bool { return sevenMinutes.MatchString(answer) }

func criterionByName(name string) (criterion, bool) {
	for _, c := range criteria {
		if c.Name == name {
			return c, true
		}
	}
	return criterion{}, false
}

// moveCriteria are scored from what the machine recorded, not from the text: whether
// the model asked to advance, and whether the table refused. They are listed here so
// the report and the showcase describe them alongside the text criteria.
var moveCriteria = []criterion{
	{Name: "asked_step", What: "модель сама выставила маркер закрытия шага"},
	{Name: "asked_illegal", What: "модель попросила переход, который таблица запрещает"},
	{Name: "asked_nothing", What: "модель не попросила ничего — «ход умер без действия»"},
}

func isMoveCriterion(name string) bool {
	for _, c := range moveCriteria {
		if c.Name == name {
			return true
		}
	}
	return false
}

func scoreMove(name string, move agent.TaskMove) bool {
	switch name {
	case "asked_step":
		return move.StepAsked
	case "asked_illegal":
		return move.Illegal
	case "asked_nothing":
		return !move.Asked()
	}
	return false
}
