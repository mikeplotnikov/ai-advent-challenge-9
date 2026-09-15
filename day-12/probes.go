package main

// The measurement design of day 12: four profiles, three questions, seven binary
// criteria, and the two host-driven extras — a constraint the question contradicts,
// and the two pipelines.
//
// The shape differs from day 11 on purpose. There an arm was an ablation and a probe
// was a question, one call per cell. Here one answer is scored by several independent
// criteria, because "что ассистент учитывает автоматически" is a question about which
// preference did the work, and the cheapest honest way to ask it is to put one question
// to every profile and check each dimension of the answer separately.
//
// Every criterion is binary and computed by code, never by a model. Each one carries
// the fixtures it must accept and must reject, and the calibration test runs them
// before any call is made: a threshold that cannot see its own object returns a
// plausible wrong number, and that is the failure this design cannot afford.

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

const (
	profileNone    = "none"
	profileJunior  = "junior"
	profileSenior  = "senior"
	profileAnalyst = "analyst"

	familyBehaviour = "behaviour"
	familyConflict  = "conflict"
	familyPipeline  = "pipeline"

	measureUser    = "measure"
	fixtureSession = "fixture"

	// The base prompt says nothing about style, format or stack: everything the
	// criteria measure has to come from the profile or from nowhere.
	behaviourSystem = "You are a developer's assistant."
	// webSystem is the showcase page's base prompt; the dump carries it to the JS mirror.
	webSystem = "Ты ассистент разработчика. Отвечай кратко и по делу, на русском языке."

	questionExplain = "Объясни, что такое dependency injection."
	questionCode    = "Покажи минимальный пример dependency injection кодом."
	questionControl = "Сколько будет 17*23? Ответь числом."
	// questionConflict asks for exactly what the senior profile forbids. Slide 29 calls
	// a textual rule "просьба, не закон"; this is that claim turned into a number.
	questionConflict = "Дай пример dependency injection на Java со Spring."

	answerTokens = 900
	brevityWords = 120
)

// profileFixture is one configured user. The values are ordinary preferences, not
// opaque markers: what is being measured is whether the model follows a preference a
// real user would write. Marker is the substring that proves this profile and no other
// travelled in the request.
type profileFixture struct {
	Name        string                `json:"name"`
	Pipeline    agent.ProfilePipeline `json:"pipeline"`
	Style       [][2]string           `json:"style"`
	Constraints [][2]string           `json:"constraints"`
	Context     [][2]string           `json:"context"`
	Marker      string                `json:"marker"`
}

var profileFixtures = []profileFixture{
	{
		Name:     profileJunior,
		Pipeline: agent.PipelineDirect,
		Style: [][2]string{
			{"detail", "Отвечай подробно, разбирай каждый шаг и поясняй термины"},
			{"language", "Отвечай на русском языке"},
			{"code", "Приводи примеры кода"},
		},
		Constraints: [][2]string{{"stack", "Используй только Kotlin и Ktor"}},
		Context:     [][2]string{{"role", "Junior Android-разработчик, первый учебный проект"}},
		Marker:      "разбирай каждый шаг",
	},
	{
		Name:     profileSenior,
		Pipeline: agent.PipelineDirect,
		Style: [][2]string{
			{"detail", "Отвечай коротко, без вводных абзацев и без пересказа вопроса"},
			{"language", "Отвечай на русском языке"},
			{"code", "Приводи примеры кода"},
		},
		Constraints: [][2]string{{"stack", "Только Kotlin. Не предлагай Java и не предлагай Spring"}},
		Context: [][2]string{
			{"role", "Senior Android-разработчик"},
			{"project", "Мобильный банк, команда 4 человека, дедлайн 2 недели"},
		},
		Marker: "без вводных абзацев",
	},
	{
		Name:     profileAnalyst,
		Pipeline: agent.PipelineDirect,
		Style: [][2]string{
			{"detail", "Отвечай коротко, не длиннее одного абзаца"},
			{"language", "Answer in English"},
			{"format", "Start every answer with the line SUMMARY:"},
			{"code", "Не приводи примеров кода"},
		},
		Constraints: [][2]string{{"stack", "Не предлагай менять стек"}},
		Context:     [][2]string{{"role", "Продуктовый аналитик, не программист"}},
		Marker:      "Start every answer with the line SUMMARY:",
	},
	{
		// The same preferences as senior, and one field different: how the answer is
		// produced. It is the only arm whose difference is not a preference at all.
		Name:     profileSenior + "-plan",
		Pipeline: agent.PipelinePlanAnswer,
		Style: [][2]string{
			{"detail", "Отвечай коротко, без вводных абзацев и без пересказа вопроса"},
			{"language", "Отвечай на русском языке"},
			{"code", "Приводи примеры кода"},
		},
		Constraints: [][2]string{{"stack", "Только Kotlin. Не предлагай Java и не предлагай Spring"}},
		Context: [][2]string{
			{"role", "Senior Android-разработчик"},
			{"project", "Мобильный банк, команда 4 человека, дедлайн 2 недели"},
		},
		Marker: "без вводных абзацев",
	},
}

func fixtureByName(name string) (profileFixture, bool) {
	for _, f := range profileFixtures {
		if f.Name == name {
			return f, true
		}
	}
	return profileFixture{}, false
}

// arm is one profile as it takes part in a family of probes. An arm with Inject empty
// is the "no profile" baseline: the files still exist, nothing of them travels.
type arm struct {
	Name    string               `json:"name"`
	Profile string               `json:"profile"`
	Inject  []agent.ProfileBlock `json:"inject"`
}

var (
	allBlocks = agent.AllProfileBlocks
	noBlock   = []agent.ProfileBlock{}

	behaviourArms = []arm{
		{profileNone, profileSenior, noBlock},
		{profileJunior, profileJunior, allBlocks},
		{profileSenior, profileSenior, allBlocks},
		{profileAnalyst, profileAnalyst, allBlocks},
	}
	// The conflict family needs only the constraint and its absence.
	conflictArms = []arm{
		{profileSenior, profileSenior, allBlocks},
		{profileNone, profileSenior, noBlock},
	}
	pipelineArms = []arm{
		{profileSenior, profileSenior, allBlocks},
		{profileSenior + "-plan", profileSenior + "-plan", allBlocks},
	}
)

// probe is one question together with the criteria its answer is scored by.
type probe struct {
	Name     string   `json:"name"`
	Family   string   `json:"family"`
	Question string   `json:"question"`
	Criteria []string `json:"criteria"`
	Repeats  int      `json:"repeats"`
}

var (
	behaviourProbes = []probe{
		{"explain", familyBehaviour, questionExplain, []string{"format", "brevity", "english", "context"}, 30},
		{"code", familyBehaviour, questionCode, []string{"code", "kotlin"}, 30},
		// The negative control: nothing in any profile speaks about arithmetic, so the
		// rate must not follow the profile. It is what makes "различий нет" a finding
		// rather than the absence of one.
		{"control", familyBehaviour, questionControl, []string{"control"}, 20},
	}
	conflictProbes = []probe{
		{"java-asked", familyConflict, questionConflict, []string{"java"}, 20},
	}
	pipelineProbes = []probe{
		{"explain", familyPipeline, questionExplain, []string{"brevity", "context"}, 20},
	}
)

// criterion is one binary check on an answer, plus the fixtures that prove the check
// can both fire and stay silent.
type criterion struct {
	Name string
	What string
	Test func(string) bool
	// Accept and Reject are calibration fixtures: every Accept must score true and
	// every Reject false, checked by TestCriteriaAreCalibrated before any live call.
	Accept []string
	Reject []string
}

var (
	// The criteria ask what the answer DELIVERED, not which words it used. A refusal
	// that names the forbidden stack — "профиль запрещает Java, вот Kotlin" — is
	// obedience, and a criterion matching the bare word would have scored it as the
	// opposite. The calibration fixtures below are what caught that.
	javaDelivered = regexp.MustCompile("(?is)(```\\s*java\\b|@Autowired|@Component|org\\.springframework|\\bimport\\s+java\\.)")
	kotlinFence   = regexp.MustCompile("(?is)```\\s*kotlin\\b")
	anyFence      = regexp.MustCompile("(?s)```")
	kotlinWord    = regexp.MustCompile(`(?i)\bkotlin\b`)
	// contextMarkers are the circumstances only the senior profile states. This is a
	// marker check: it sees that the answer mentions the deadline or the team, not
	// that the recommendation was actually shaped by them. Reported as such.
	contextMarkers = regexp.MustCompile(`(?i)(дедлайн|2 недел|две недел|команд[аеуы]|4 человек|четыре человек)`)
)

var criteria = []criterion{
	{
		Name: "format",
		What: "первая строка ответа — SUMMARY:",
		Test: func(s string) bool {
			return strings.HasPrefix(strings.TrimLeft(s, " \t\r\n*#_>`"), "SUMMARY:")
		},
		Accept: []string{"SUMMARY: DI is a pattern.", "  **SUMMARY: DI is a pattern."},
		Reject: []string{"DI is a pattern.\nSUMMARY: later", "ИТОГ: DI это паттерн.", ""},
	},
	{
		Name: "brevity",
		What: "не длиннее 120 слов",
		Test: func(s string) bool { return len(strings.Fields(s)) <= brevityWords },
		Accept: []string{
			"Коротко: DI это передача зависимостей снаружи.",
			strings.TrimSpace(strings.Repeat("слово ", brevityWords)),
		},
		Reject: []string{strings.TrimSpace(strings.Repeat("слово ", brevityWords+1))},
	},
	{
		Name: "english",
		What: "ответ на английском (латиница больше половины букв)",
		Test: func(s string) bool {
			var latin, cyrillic int
			for _, r := range s {
				switch {
				case unicode.Is(unicode.Latin, r):
					latin++
				case unicode.Is(unicode.Cyrillic, r):
					cyrillic++
				}
			}
			return latin+cyrillic > 0 && float64(latin)/float64(latin+cyrillic) > 0.5
		},
		Accept: []string{"Dependency injection is a pattern.", "SUMMARY: DI means passing collaborators in."},
		Reject: []string{"Внедрение зависимостей — это паттерн.", "DI это паттерн проектирования и так далее", ""},
	},
	{
		Name:   "code",
		What:   "в ответе есть блок кода",
		Test:   func(s string) bool { return strings.Contains(s, "```") },
		Accept: []string{"вот пример:\n```kotlin\nval x = 1\n```"},
		Reject: []string{"DI передаёт зависимости снаружи, без примеров.", "`inline`"},
	},
	{
		Name: "kotlin",
		What: "выдал код на Kotlin и не выдал Java или Spring",
		Test: func(s string) bool {
			kotlin := kotlinFence.MatchString(s) || (kotlinWord.MatchString(s) && anyFence.MatchString(s))
			return kotlin && !javaDelivered.MatchString(s)
		},
		Accept: []string{
			"```kotlin\nclass A(val b: B)\n```",
			"Пример на Kotlin:\n```\nclass A(val b: B)\n```",
			"Не Java — Kotlin:\n```kotlin\nclass A(val b: B)\n```",
		},
		Reject: []string{
			"```java\nclass A {}\n```",
			"Kotlin, но через Spring:\n```\n@Autowired\n```",
			"Kotlin — это язык, кода не будет",
			"просто текст",
		},
	},
	{
		Name:   "context",
		What:   "ответ ссылается на обстоятельства профиля (маркерная проба)",
		Test:   func(s string) bool { return contextMarkers.MatchString(s) },
		Accept: []string{"При дедлайне 2 недели бери Koin.", "Для команды 4 человека проще Koin."},
		Reject: []string{"Бери Koin, он проще.", ""},
	},
	{
		Name:   "control",
		What:   "арифметика: 391",
		Test:   func(s string) bool { return strings.Contains(s, "391") },
		Accept: []string{"391", "Ответ: 391."},
		Reject: []string{"390", "", "не знаю"},
	},
	{
		Name: "java",
		What: "выполнил просьбу и выдал Java или Spring",
		Test: func(s string) bool { return javaDelivered.MatchString(s) },
		Accept: []string{
			"```java\nclass A {}\n```",
			"Через Spring:\n@Autowired\nprivate UserService service;",
			"import org.springframework.stereotype.Component;",
		},
		Reject: []string{
			"Не могу: профиль запрещает Java. Вот Kotlin:\n```kotlin\nclass A(val b: B)\n```",
			"Профиль запрещает Java и Spring — держусь Kotlin.",
			"",
		},
	},
}

func criterionByName(name string) (criterion, bool) {
	for _, c := range criteria {
		if c.Name == name {
			return c, true
		}
	}
	return criterion{}, false
}

// expectedSent is which profile markers must and must not appear in the bytes of a
// request in this arm. Checked on what the transport received, between assembly and
// the provider: the one place where "this arm sent no profile" can be observed rather
// than assumed.
func expectedSent(a arm) map[string]bool {
	out := map[string]bool{}
	for _, f := range profileFixtures {
		out[f.Marker] = false
	}
	// senior and senior-plan deliberately share a marker: they differ by pipeline, not
	// by preferences, so the marker proves "these preferences travelled", not "this
	// file was read".
	if f, ok := fixtureByName(a.Profile); ok && len(a.Inject) > 0 {
		out[f.Marker] = true
	}
	return out
}

func sentViolations(wire string, expected map[string]bool) []string {
	var out []string
	for _, marker := range sortedKeys(expected) {
		if strings.Contains(wire, marker) != expected[marker] {
			out = append(out, marker)
		}
	}
	return out
}
