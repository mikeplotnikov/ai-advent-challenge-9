package main

import "github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"

// Every method ends the same way, and that is the point: the only thing this day
// varies is how the model reasons. Where the answer is written down says nothing
// about reasoning, so it stays identical everywhere — otherwise the comparison
// would measure our own inconsistency.
const outputRule = "В последней строке ответа напиши ANSWER: <число>."

const russian = "Отвечай на русском языке. "

// A asks for nothing beyond an answer. It does not forbid reasoning either:
// "without additional instructions" means no instructions, and if the model
// reasons on its own initiative, that is a fact about the model worth showing.
const systemDirect = russian + outputRule

const systemSteps = russian +
	"Решай задачу пошагово: разбей её на шаги, выполняй каждый шаг явно " +
	"и проверяй промежуточные результаты, прежде чем дать окончательный ответ. " +
	outputRule

// C, call one. The model writes the prompt; it must not answer the task here,
// or the second call would only be copying an answer it already has.
const systemPromptWriter = "Ты инженер по промптам. По условию задачи составь промпт, " +
	"который поможет языковой модели решить эту задачу максимально точно. " +
	"Верни только текст промпта, без пояснений, без примеров ответа " +
	"и не решай саму задачу."

// D puts the experts in the system prompt — the host's own recommendation in the
// challenge chat on 2026-09-02: "консилиум лучше в систем промпт закидывать".
const systemCouncil = russian +
	"Ты ведёшь консилиум из трёх экспертов, у каждого свой угол зрения:\n" +
	"— Аналитик: разбирает условие, выделяет все ограничения и считает.\n" +
	"— Инженер: предлагает механический способ пересчёта и перепроверяет вычисления.\n" +
	"— Критик: ищет ошибки в решениях коллег и проверяет граничные случаи.\n" +
	"Каждый эксперт даёт собственное решение под своим заголовком. " +
	"После этого сведи их мнения и назови итог. " + outputRule

// tokenCap is the same for every method, including the one that reasons inside
// the model: reasoning tokens are billed as output and would hit the ceiling
// first, and a different cap for one method would add a second variable.
const tokenCap = 8000

type method struct {
	key    string
	title  string
	idea   string
	system string
	opts   llm.Options
	// twoStep marks the meta-prompt method: call one writes the prompt, call two
	// runs it. The model remembers nothing between them — "нейронка никогда не
	// помнит прошлых промптов" [чат 02.09] — so we carry the prompt across.
	twoStep bool
}

func methods() []method {
	return []method{
		{
			key: "A", title: "Прямой ответ",
			idea:   "условие задачи и больше ничего",
			system: systemDirect,
			opts:   llm.Options{MaxTokens: tokenCap},
		},
		{
			key: "B", title: "Пошагово",
			idea:   "та же задача плюс инструкция рассуждать по шагам",
			system: systemSteps,
			opts:   llm.Options{MaxTokens: tokenCap},
		},
		{
			key: "C", title: "Мета-промпт",
			idea:    "модель сначала пишет промпт для задачи, потом решает по нему",
			system:  systemPromptWriter,
			opts:    llm.Options{MaxTokens: tokenCap},
			twoStep: true,
		},
		{
			key: "D", title: "Консилиум экспертов",
			idea:   "аналитик, инженер и критик в одном системном промпте",
			system: systemCouncil,
			opts:   llm.Options{MaxTokens: tokenCap},
		},
		{
			key: "E", title: "Встроенный reasoning",
			idea:   "промпт как в A, но рассуждает сама модель: thinking=enabled",
			system: systemDirect,
			opts: llm.Options{
				MaxTokens:       tokenCap,
				Thinking:        "enabled",
				ReasoningEffort: "high",
			},
		},
	}
}
