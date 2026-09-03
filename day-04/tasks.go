package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Three axes are asked for — accuracy, creativity, diversity — and no single
// request carries all three: a counting problem has no creativity to judge, a
// coffee-shop name has no correct answer to score. So the request is held byte
// for byte inside one comparison, and there are three of them. A participant in
// the challenge chat on 2026-09-03 put the same thing from the other side: "не
// на все категории запросов температура имеет влияние" [чат #1667].

// kind decides which numbers may be printed about a task at all. An open task
// has no truth, so no accuracy column exists for it — not an empty one, none.
type kind int

const (
	exact kind = iota // machine-checkable answer
	open              // no ground truth: only diversity and a judge
)

type task struct {
	key   string
	title string
	kind  kind
	// note says what this class is for, so the report explains its own columns.
	note      string
	system    string
	prompt    string
	maxTokens int
	// truth is the expected answer, computed rather than typed in — see the
	// constructors below.
	truth string
	// parse pulls the comparable answer out of the raw text. For an open task it
	// normalises the whole answer instead, because the whole answer is the datum.
	parse func(string) (string, bool)
}

// outputRule is identical in both exact tasks: where the answer is written down
// says nothing about temperature, so it must not vary with it.
const outputRule = "В последней строке ответа напиши ANSWER: "

const russian = "Отвечай на русском языке. "

// --- T1: the counting task, carried over from day 3 -------------------------
//
// Same constants, same brute force, and the wording is built from them so the
// two cannot drift apart. Calibrated on deepseek-v4-flash on 2026-09-03: the
// plain request got it right in 5 of 8 runs, which is the band where a change
// is visible — a task the model always solves or never solves would measure
// nothing about temperature.

const (
	rangeEnd       = 400
	divisibleBy    = 4
	digitSumBy     = 3
	forbiddenDigit = '8'
)

func countingTruthSet() []int {
	var found []int
	for n := 1; n <= rangeEnd; n++ {
		if n%divisibleBy != 0 || digitSum(n)%digitSumBy != 0 ||
			strings.ContainsRune(strconv.Itoa(n), forbiddenDigit) {
			continue
		}
		found = append(found, n)
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

// --- T3: the letter task ----------------------------------------------------
//
// Straight out of the challenge chat, where it is offered as a "базовая проверка
// для нейронок" [чат #1669]. Its answer is one letter and the code counts it, so
// the truth is computed here too. Calibration on flash, 8 runs each, found the
// result this day exists to catch: 3 of 8 correct at temperature 0 against 7 of 8
// at 1.2 — the opposite of what "lower temperature is more accurate" predicts.

const (
	letterWord     = "делопроизводство"
	letterPosition = 7 // counted from the end
)

// letterFromEnd is the oracle for T3. Written as a loop over runes rather than a
// byte index because Cyrillic is two bytes per letter and a byte index would
// silently answer a different question.
func letterFromEnd(word string, position int) (string, bool) {
	runes := []rune(word)
	i := len(runes) - position
	if position < 1 || i < 0 {
		return "", false
	}
	return string(runes[i]), true
}

// --- answer extraction ------------------------------------------------------

// Models decorate the line they were told to write: "**ANSWER:** 24" and
// "ANSWER: **24**" are both common, and a strict pattern would score a correct
// answer as no answer at all — deflating accuracy for a reason that has nothing
// to do with temperature.
var (
	numberLine = regexp.MustCompile(`(?i)\*{0,2}ANSWER\*{0,2}\s*:\s*\*{0,2}\s*(-?\d+)`)
	letterLine = regexp.MustCompile(`(?i)\*{0,2}ANSWER\*{0,2}\s*:\s*\*{0,2}\s*«?"?([\p{Cyrillic}])`)
)

// lastMatch takes the last match, not the first: a model that reasons out loud
// writes intermediate ANSWER lines, and the one it ends on is what it commits to.
func lastMatch(re *regexp.Regexp, text string) (string, bool) {
	found := re.FindAllStringSubmatch(text, -1)
	if len(found) == 0 {
		return "", false
	}
	return found[len(found)-1][1], true
}

func parseNumber(text string) (string, bool) { return lastMatch(numberLine, text) }

func parseLetter(text string) (string, bool) {
	got, ok := lastMatch(letterLine, text)
	if !ok {
		return "", false
	}
	return strings.ToLower(got), true
}

// parseWholeAnswer is the open task's reading: there is nothing to extract, the
// answer itself is the observation. It is normalised only for comparison —
// case, surrounding quotes and punctuation are not creative choices, and
// counting "Депо Кофе" and "депо кофе." as two different answers would inflate
// diversity with formatting noise. The raw text is what gets printed.
func parseWholeAnswer(text string) (string, bool) {
	normalised := strings.ToLower(strings.TrimSpace(text))
	normalised = strings.Trim(normalised, ` .!"'«»„“”`)
	normalised = strings.Join(strings.Fields(normalised), " ")
	if normalised == "" {
		return "", false
	}
	return normalised, true
}

// tokenCap is generous and identical everywhere. The counting task reasons on
// its own initiative even with thinking disabled — 700 to 4500 tokens on flash
// during calibration — and a cap that cut some runs short would show up as lost
// accuracy attributed to temperature.
const tokenCap = 8000

func tasks() []task {
	truth := countingTruthSet()
	letter, ok := letterFromEnd(letterWord, letterPosition)
	if !ok {
		// Unreachable with the constants above, and a panic is the right answer
		// if someone edits them into nonsense: a task with no computable truth
		// must not silently score every answer as wrong.
		panic("буквенная задача: позиция вне слова")
	}
	return []task{
		{
			key: "T1", title: "Счётная задача",
			kind:   exact,
			note:   "есть эталон, посчитан перебором в day-04/tasks.go — точность измерима",
			system: russian + outputRule + "<число>.",
			prompt: fmt.Sprintf(
				"Сколько существует целых чисел от 1 до %d включительно, "+
					"которые делятся на %d, сумма цифр которых делится на %d "+
					"и в записи которых нет цифры %c?",
				rangeEnd, divisibleBy, digitSumBy, forbiddenDigit),
			maxTokens: tokenCap,
			truth:     strconv.Itoa(len(truth)),
			parse:     parseNumber,
		},
		{
			key: "T2", title: "Творческая задача",
			kind:      open,
			note:      "эталона нет — точность не измеряется, только разнообразие и оценка судьи",
			system:    russian + "Отвечай одной короткой строкой, без пояснений и без кавычек.",
			prompt:    "Придумай одно название для кофейни в старом трамвайном депо.",
			maxTokens: 60,
			parse:     parseWholeAnswer,
		},
		{
			key: "T3", title: "Буквенная задача",
			kind:   exact,
			note:   "один верный ответ, любое разнообразие здесь — дефект",
			system: russian + outputRule + "<буква>.",
			prompt: fmt.Sprintf("Какая буква стоит на %s месте с конца в слове «%s»?",
				ordinalFromEnd(letterPosition), letterWord),
			maxTokens: tokenCap,
			truth:     letter,
			parse:     parseLetter,
		},
	}
}

// ordinalFromEnd spells the position out in words, the way the question was
// actually asked in the chat. Writing "на 7 месте" would be a different prompt
// from the one that was calibrated.
func ordinalFromEnd(n int) string {
	words := map[int]string{5: "пятом", 6: "шестом", 7: "седьмом", 8: "восьмом"}
	if w, ok := words[n]; ok {
		return w
	}
	return strconv.Itoa(n) + "-м"
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
