package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// The classes. The host allowed reusing what earlier days already tested —
// "можно те же задачи брать логические которые вы уже тестили" [чат #1801] — and
// that is also the better option: T1 and T2 below are calibrated. On flash the
// counting task landed at 53-60% correct across 30 runs on day 4, which is the
// band where a difference between models can actually show. A class every rung
// solves, or none does, measures nothing about the ladder.
//
// A participant put the same risk from the other side on the day the task
// dropped: "на простых вещах qwen 27b и claude opus 5/5.6 sol работают
// одинаково" [чат #1841]. Hence the pilot: classes earn their place in the grid
// by separating the rungs, and the ones that do not are dropped before the paid
// run.

// kind decides which numbers may be printed about a class at all.
type kind int

const (
	exact  kind = iota // a machine-checkable answer
	schema             // no ground truth for the value; the shape is checkable
	open               // no ground truth at all: raw answers only
)

type task struct {
	key   string
	title string
	kind  kind
	// note says what this class is for, so a report explains its own columns.
	note      string
	system    string
	prompt    string
	maxTokens int
	// truth is the expected answer, computed rather than typed in.
	truth string
	// check reads the raw answer and says whether it is right. The second value
	// separates "did not produce an answer in the required form" from "produced a
	// wrong one" — two different defects that must never be added together.
	check func(string) (correct bool, parsed bool)
}

// outputRule carries no slot a model can copy. The first pilot run had
// qwen3:1.7b answer "…Всего: 3 + 12 + 4 = 19 шт. ANSWER:" and then the literal
// placeholder — it had done the arithmetic and reproduced the slot instead of
// substituting into it. The checker correctly read that as "no answer in the
// required form", and anything merging the two defects would have published it as
// an arithmetic failure. The wording came from day 4, which only ran it on flash.
//
// Changing it costs exact comparability with day 4's numbers for T1 and T2. That
// is the right trade: this day compares rungs against each other under identical
// conditions, and day 4's 53-60% was only ever cited as evidence that the class
// sits mid-range, which this day's own pilot re-establishes directly.
const (
	russian    = "Отвечай на русском языке. "
	outputRule = "Заверши ответ последней строкой, которая начинается со слова ANSWER, " +
		"затем двоеточие, затем только сам ответ и ничего больше. "
)

// tokenCap is identical for every rung. A cap that cut some runs short would
// show up as lost accuracy attributed to the model, so truncation is counted and
// reported on its own instead: the runner reads finish_reason.
const tokenCap = 4000

// --- T0: extraction, the easy end of the ladder -----------------------------
//
// This is the job the host named as the practical use of the whole day: a cheap
// model works out what kind of task this is, and only then does the expensive
// configuration run [чат #1796].
//
// It was written expecting a tie — every rung solving it, proving the expensive
// one is not needed here. The pilot said otherwise: qwen3:1.7b answered
// "3 + 12 + 4 = 20 ANSWER: 20", in perfect form and wrong, five times out of five,
// against 5/5 for both DeepSeek rungs. A 1.7B model does not reliably add three
// small numbers. The class stays, with its expectation corrected rather than its
// data: the tie turned up in T3 instead.

var order = []struct {
	item string
	qty  int
}{
	{"эспрессо", 3},
	{"капучино", 12},
	{"чизкейк", 4},
}

// orderText and orderTotal are built from the same slice, so the question and
// its answer cannot drift apart when someone edits the numbers.
func orderText() string {
	parts := make([]string, 0, len(order))
	for _, o := range order {
		parts = append(parts, fmt.Sprintf("%s — %d шт.", o.item, o.qty))
	}
	return strings.Join(parts, ", ")
}

func orderTotal() int {
	total := 0
	for _, o := range order {
		total += o.qty
	}
	return total
}

// --- T1: the counting task, carried over from days 3 and 4 ------------------
//
// Same constants and the same brute force, and the wording is built from them so
// the two cannot drift apart.

const (
	rangeEnd       = 400
	divisibleBy    = 4
	digitSumBy     = 3
	forbiddenDigit = '8'
)

func countingTruth() int {
	found := 0
	for n := 1; n <= rangeEnd; n++ {
		if n%divisibleBy != 0 || digitSum(n)%digitSumBy != 0 ||
			strings.ContainsRune(strconv.Itoa(n), forbiddenDigit) {
			continue
		}
		found++
	}
	return found
}

func digitSum(n int) int {
	sum := 0
	for ; n > 0; n /= 10 {
		sum += n % 10
	}
	return sum
}

// --- T2: the letter task ----------------------------------------------------
//
// Out of the challenge chat, where it is offered as a "базовая проверка для
// нейронок" [чат #1669]. A different failure mode from T1: no arithmetic, just
// counting positions in a word, and Cyrillic is two bytes per letter so the
// oracle walks runes rather than bytes.

const (
	letterWord     = "делопроизводство"
	letterPosition = 7
)

func letterFromEnd(word string, position int) (string, bool) {
	runes := []rune(word)
	i := len(runes) - position
	if position < 1 || i < 0 {
		return "", false
	}
	return string(runes[i]), true
}

func ordinalFromEnd(n int) string {
	words := map[int]string{5: "пятом", 6: "шестом", 7: "седьмом", 8: "восьмом"}
	if w, ok := words[n]; ok {
		return w
	}
	return strconv.Itoa(n) + "-м"
}

// --- T3: obeying a schema ---------------------------------------------------
//
// The smoke run made this axis visible before it was even a class: asked to
// answer with nothing but a number, qwen3:1.7b wrote 177 characters of working,
// qwen3:4b wrote 1064, and 8b, flash and pro wrote "391". Whether a model does
// what it was told about the shape of its output is a quality of the answer, and
// it is the quality that decides whether a model can sit inside an agent.
//
// response_format is deliberately NOT sent. The platform guarantee is narrower
// than its name — json_object buys valid JSON, never your schema — so sending it
// would measure the provider's post-processing instead of the model's obedience.
// What is scored here is the shape only: there is no ground truth for the value,
// and pretending otherwise would smuggle an unverifiable claim into a hit rate.

var schemaKeys = []string{"город", "население", "страна"}

// checkSchema is deterministic and asks three things: does the whole answer parse
// as one JSON object, does it carry exactly the required keys, and is the numeric
// field actually a number rather than a string that looks like one.
func checkSchema(raw string) (bool, bool) {
	stripped, _ := stripThinking(raw)
	text := strings.TrimSpace(stripped)
	// A fenced block is still an answer that obeyed the schema; the fence is
	// formatting, and stripping it is not the same as forgiving wrong keys.
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	text = strings.TrimSpace(text)

	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		return false, false // never produced a JSON object at all
	}
	// A literal "null" unmarshals into a nil map without error here, while
	// JSON.parse gives null and the JS mirror rejects it. Without this the two
	// implementations disagree about whether "null" counts as having produced an
	// object — a divergence with no visible symptom until it matters.
	if got == nil {
		return false, false
	}
	if len(got) != len(schemaKeys) {
		return false, true
	}
	for _, k := range schemaKeys {
		v, ok := got[k]
		if !ok {
			return false, true
		}
		if k == "население" {
			if _, isNumber := v.(float64); !isNumber {
				return false, true
			}
		}
	}
	return true, true
}

// --- the model thinking inside its own answer ----------------------------------

// stripThinking separates a model's deliberation from its answer.
//
// Measured on this day's own grid: 88 of qwen3:4b's 150 calls came back with the
// thinking inside `content`, closed by a `</think>` tag and followed by the real
// answer. The opening tag never appears — the chat template consumes it. Every other
// rung: zero. So `reasoning_effort: "none"` stops the reasoning being split into its
// own field on that model without stopping the model reasoning.
//
// That broke two things at once. The guard built to catch "the switch did not take"
// inspects the separate field, so it reported all 88 as reasoning-off — a false
// negative in exactly the check meant to prevent this. And the schema class, which
// requires the whole answer to be the JSON, scored 0 of 30 on a model whose JSON was
// correct, because prose preceded it.
//
// Hence both halves: the deliberation is cut off before anything is scored, so what
// is measured is the answer; and the fact that it was there is returned, so the
// report can say the cell is not comparable. Stripping without saying so would hide
// the confound the whole day is built to avoid.
//
// Cutting before parsing also closes a subtler hole: a model reasoning out loud can
// write an "ANSWER:" line inside its thinking, and the parser would have taken it.
func stripThinking(raw string) (answer string, thought bool) {
	const closing = "</think>"
	if i := strings.LastIndex(raw, closing); i >= 0 {
		return strings.TrimSpace(raw[i+len(closing):]), true
	}
	if strings.Contains(raw, "<think>") {
		// Opened and never closed: the model was still thinking when it ran out of
		// budget, so there is no answer in there at all.
		return "", true
	}
	return raw, false
}

// --- answer extraction ------------------------------------------------------

// The output rule asks for a last line that begins with ANSWER, a colon, and then
// "только сам ответ и ничего больше". So the whole tail of that line is read and
// then required to BE the answer — not searched for an answer inside.
//
// The difference is not academic. The first version captured the first number
// after the colon, so "ANSWER: 3 + 12 + 4 = 19" scored as the wrong answer 3 on a
// class whose truth is 19, and "ANSWER: ве" scored as a correct "в". One of those
// deflates a weak rung's accuracy and the other inflates it, and both then travel
// into the Fisher p-values and the Holm correction. Writing working on the answer
// line is not an edge case for a small model — it is the behaviour this day exists
// to measure — so it has to land in the "did not answer in the required form"
// column, which is counted separately and never added to wrong answers.
var answerLine = regexp.MustCompile(`(?im)\*{0,2}ANSWER\*{0,2}\s*:\s*(.*)$`)

const (
	// Decoration models wrap the answer in. Stripped because where the answer is
	// written down says nothing about the model's ability, and a strict reading
	// would score "**24**" as no answer at all.
	emphasis    = "*`_"
	quoteMarks  = "«»\"'“”„"
	sentenceEnd = ".!?;,: "
)

// answerTail returns what followed the last ANSWER: on its own line, stripped of
// decoration. The second value is false when there is no such line at all.
//
// The whitespace after the colon is allowed to span a line break: a model that
// writes the answer on the next line has added layout, not content, and the rule
// this enforces is about content. "ANSWER:" with nothing after it still fails,
// which is what the placeholder echo from the pilot looked like once the slot
// itself was gone.
func answerTail(text string) (string, bool) {
	text, _ = stripThinking(text)
	found := answerLine.FindAllStringSubmatch(text, -1)
	if len(found) == 0 {
		return "", false
	}
	// The last one, not the first: a model that works out loud writes intermediate
	// ANSWER lines, and the one it ends on is what it commits to.
	tail := strings.TrimSpace(found[len(found)-1][1])
	// Repeated until it stops changing, because decoration nests in any order:
	// "**«19».**" needs emphasis, then a quote, then a full stop, then emphasis
	// again. One pass leaves "19»" — a correct answer scored as no answer. It
	// terminates because every pass either shortens the string or ends the loop.
	for {
		before := tail
		tail = strings.TrimSpace(strings.Trim(tail, emphasis))
		tail = strings.TrimSpace(strings.Trim(tail, quoteMarks))
		tail = strings.TrimSpace(strings.TrimRight(tail, sentenceEnd))
		if tail == before {
			return tail, true
		}
	}
}

var wholeNumber = regexp.MustCompile(`^-?\d+$`)

// numericAnswer requires the tail to be a number and nothing else.
func numericAnswer(text string) (string, bool) {
	tail, ok := answerTail(text)
	if !ok || !wholeNumber.MatchString(tail) {
		return "", false
	}
	return tail, true
}

// letterAnswer requires the tail to be exactly one Cyrillic letter. A Latin "o"
// looks identical to a Cyrillic "о" and is a different letter, so the class is
// checked rather than the shape.
func letterAnswer(text string) (string, bool) {
	tail, ok := answerTail(text)
	if !ok {
		return "", false
	}
	runes := []rune(tail)
	if len(runes) != 1 || !unicode.Is(unicode.Cyrillic, runes[0]) {
		return "", false
	}
	return strings.ToLower(tail), true
}

// exactCheck builds a checker that compares a parsed answer against a computed
// truth. The two return values keep "wrote nothing in the required form" apart
// from "wrote the wrong answer": the weak rungs fail both ways, and merging them
// would hide which.
func exactCheck(parse func(string) (string, bool), truth string) func(string) (bool, bool) {
	return func(raw string) (bool, bool) {
		got, ok := parse(raw)
		if !ok {
			return false, false
		}
		return got == truth, true
	}
}

func tasks() []task {
	letter, ok := letterFromEnd(letterWord, letterPosition)
	if !ok {
		// Unreachable with the constants above, and a panic is the right answer if
		// someone edits them into nonsense: a class with no computable truth must
		// not silently score every answer as wrong.
		panic("буквенная задача: позиция вне слова")
	}
	total := strconv.Itoa(orderTotal())
	counting := strconv.Itoa(countingTruth())

	return []task{
		{
			key: "T0", title: "Извлечение из текста",
			kind:      exact,
			note:      "самая простая арифметика: пилот показал, что 1.7B ошибается и в ней — 3+12+4=20",
			system:    russian + outputRule + "Ответ — целое число.",
			prompt:    fmt.Sprintf("Заказ: %s. Сколько всего единиц товара в заказе?", orderText()),
			maxTokens: tokenCap,
			truth:     total,
			check:     exactCheck(numericAnswer, total),
		},
		{
			key: "T1", title: "Счётная задача",
			kind:   exact,
			note:   "переносится из дней 3–4, но с изменённым правилом вывода: числа тех дней здесь не эталон, а лишь причина считать класс срединным",
			system: russian + outputRule + "Ответ — целое число.",
			prompt: fmt.Sprintf(
				"Сколько существует целых чисел от 1 до %d включительно, "+
					"которые делятся на %d, сумма цифр которых делится на %d "+
					"и в записи которых нет цифры %c?",
				rangeEnd, divisibleBy, digitSumBy, forbiddenDigit),
			maxTokens: tokenCap,
			truth:     counting,
			check:     exactCheck(numericAnswer, counting),
		},
		{
			key: "T2", title: "Буквенная задача",
			kind:   exact,
			note:   "другой режим отказа: не арифметика, а счёт позиций в слове",
			system: russian + outputRule + "Ответ — одна буква.",
			prompt: fmt.Sprintf("Какая буква стоит на %s месте с конца в слове «%s»?",
				ordinalFromEnd(letterPosition), letterWord),
			maxTokens: tokenCap,
			truth:     letter,
			check:     exactCheck(letterAnswer, letter),
		},
		{
			key: "T3", title: "Соблюдение схемы",
			kind:   schema,
			note:   "проверяется форма, не значение: эталона у населения нет, и выдавать его за точность нельзя",
			system: russian + "Верни только JSON-объект, без текста вокруг и без markdown-обрамления.",
			prompt: fmt.Sprintf(
				"Верни JSON-объект с ровно тремя полями: %q (строка), %q (целое число), %q (строка) — для города Саратов.",
				schemaKeys[0], schemaKeys[1], schemaKeys[2]),
			maxTokens: tokenCap,
			check:     checkSchema,
		},
		{
			key: "T4", title: "Открытая задача",
			kind:      open,
			note:      "эталона нет — не оценивается вообще, только сырые ответы на экран",
			system:    russian + "Отвечай одной короткой строкой, без пояснений и без кавычек.",
			prompt:    "Придумай одно название для кофейни в старом трамвайном депо.",
			maxTokens: 200,
			check:     func(string) (bool, bool) { return false, true },
		},
	}
}

func pickTasks(all []task, only string) ([]task, error) {
	if strings.TrimSpace(only) == "" {
		return all, nil
	}
	wanted := map[string]bool{}
	for _, part := range strings.Split(only, ",") {
		if part = strings.ToUpper(strings.TrimSpace(part)); part != "" {
			wanted[part] = true
		}
	}
	var out []task
	for _, t := range all {
		if wanted[t.key] {
			out = append(out, t)
			delete(wanted, t.key)
		}
	}
	if len(wanted) > 0 {
		unknown := make([]string, 0, len(wanted))
		for k := range wanted {
			unknown = append(unknown, k)
		}
		return nil, fmt.Errorf("неизвестный класс задачи: %s", strings.Join(unknown, ", "))
	}
	return out, nil
}
