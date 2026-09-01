package main

import "github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"

// The stop marker travels twice: the prompt asks the model to end with it, and
// the API is told to cut on it. Those are two separate levers on purpose —
// turning the API one off is what proves which of them removes the marker.
const stopMarker = "<<<END>>>"

// Run A asks for nothing beyond an answer: no shape, no length, no ending.
// Anything else here would make "without constraints" a lie.
const systemPlain = "Ты ассистент, работающий на модели DeepSeek через официальный API DeepSeek. " +
	"Отвечай на русском языке."

const blockFormat = "\n\nФормат ответа: ровно три пункта, пронумерованных «1.», «2.», «3.», " +
	"каждый с новой строки. Никакого вступления и никакого заключения — только эти три пункта.\n" +
	"Длина: не больше 20 слов в пункте."

const blockMarker = "\n\nЗакончи ответ отдельной последней строкой " + stopMarker +
	" и не пиши после неё ничего."

// DeepSeek requires JSON to be asked for in the prompt as well as in the field;
// response_format alone can fail generation.
const blockJSON = "\n\nОтвечай строго одним объектом JSON, без markdown и без пояснений вокруг. Схема:\n" +
	`{"тема": "строка", "шаги": ["строка", "строка", "строка"], "итог": "строка"}` + "\n" +
	"В «шаги» ровно три элемента, каждый не длиннее 20 слов."

// levers are the five independent controls this day is about: two that live in
// the prompt and three that live in the request body.
type levers struct {
	Format    bool // describe the shape in the system prompt
	Marker    bool // ask the model to end with the stop marker
	Stop      bool // send that marker in the API's stop field
	JSON      bool // send response_format=json_object (and the schema in the prompt)
	MaxTokens int  // 0 means the field is not sent at all
}

type mode struct {
	key     string
	title   string
	control string
	system  string
	opts    llm.Options
}

// build turns a set of levers into the prompt and the request options together,
// so the CLI and the web showcase can never drift apart on what a lever means.
func (l levers) build(key, title, control string) mode {
	system := systemPlain
	if l.JSON {
		system += blockJSON
	} else if l.Format {
		system += blockFormat
	}
	if l.Marker {
		system += blockMarker
	}

	opts := llm.Options{MaxTokens: l.MaxTokens}
	if l.Stop {
		opts.Stop = []string{stopMarker}
	}
	if l.JSON {
		opts.ResponseFormat = "json_object"
	}
	return mode{key: key, title: title, control: control, system: system, opts: opts}
}

func defaultModes() []mode {
	return []mode{
		levers{}.build("A", "Без ограничений",
			"ничего не задано: ни формата, ни длины, ни условия завершения"),
		levers{Format: true, Marker: true, Stop: true, MaxTokens: 400}.build(
			"B", "Ограничения: промпт + API",
			"формат и длина описаны в system, завершение — маркер в промпте и в stop"),
		levers{JSON: true, MaxTokens: 400}.build(
			"C", "Формат средствами API",
			"response_format=json_object: структуру задаёт не просьба, а поле запроса"),
		levers{MaxTokens: 60}.build(
			"D", "Длина средствами API",
			"тот же промпт, что в A, но max_tokens=60 — жёсткий потолок генерации"),
	}
}
