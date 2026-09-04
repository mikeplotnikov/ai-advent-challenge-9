package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestCountingTruthIsTwentyFour(t *testing.T) {
	// Independently derived rather than copied from the code under test: "divisible
	// by 4 and digit sum divisible by 3" is "divisible by 12", so the candidates
	// are the 33 multiples of 12 up to 400, less the nine containing an 8
	// (48, 84, 108, 168, 180, 228, 288, 348, 384). Days 3 and 4 scored against the
	// same 24, so a drift here would silently break comparability with them.
	if got := countingTruth(); got != 24 {
		t.Fatalf("эталон счётной задачи = %d, ожидалось 24", got)
	}
}

func TestCountingTruthAgreesWithAnIndependentSieve(t *testing.T) {
	want := 0
	for n := 12; n <= rangeEnd; n += 12 {
		if !strings.ContainsRune(strconv.Itoa(n), forbiddenDigit) {
			want++
		}
	}
	if got := countingTruth(); got != want {
		t.Fatalf("перебор дал %d, решето — %d", got, want)
	}
}

func TestLetterFromEndCountsRunesNotBytes(t *testing.T) {
	// Cyrillic is two bytes per letter: a byte index would answer a different
	// question and score every model against the wrong truth.
	// Counted by hand from the end: о, в, т, с, д, о, в — the seventh is "в".
	got, ok := letterFromEnd("делопроизводство", 7)
	if !ok {
		t.Fatal("эталон не посчитан")
	}
	if want := "в"; got != want {
		t.Fatalf("буква = %q, ожидалась %q", got, want)
	}
	// Every position from the end, against a reversal built here rather than a
	// literal typed by hand: reversing and then indexing forward is a different
	// route to the same answer, so an off-by-one in either cannot hide behind one
	// lucky index.
	runes := []rune("делопроизводство")
	reversed := make([]rune, len(runes))
	for i, r := range runes {
		reversed[len(runes)-1-i] = r
	}
	for i, want := range reversed {
		if got, ok := letterFromEnd("делопроизводство", i+1); !ok || got != string(want) {
			t.Errorf("позиция %d с конца: %q, ожидалась %q", i+1, got, string(want))
		}
	}
}

func TestLetterFromEndRefusesPositionsOutsideTheWord(t *testing.T) {
	for _, position := range []int{0, -3, 17, 1000} {
		if _, ok := letterFromEnd("делопроизводство", position); ok {
			t.Errorf("позиция %d объявлена посчитанной", position)
		}
	}
	if _, ok := letterFromEnd("", 1); ok {
		t.Error("для пустого слова эталон объявлен посчитанным")
	}
}

func TestOrderTextAndTotalCannotDriftApart(t *testing.T) {
	// The prompt is built from the same slice as the answer. This checks the two
	// really do come from it: every quantity must appear in the question, and the
	// total must be their sum.
	text := orderText()
	sum := 0
	for _, o := range order {
		sum += o.qty
		if !strings.Contains(text, strconv.Itoa(o.qty)) {
			t.Errorf("в тексте заказа нет количества %d (%s)", o.qty, o.item)
		}
		if !strings.Contains(text, o.item) {
			t.Errorf("в тексте заказа нет позиции %q", o.item)
		}
	}
	if got := orderTotal(); got != sum {
		t.Fatalf("итог = %d, сумма позиций = %d", got, sum)
	}
}

func TestExactCheckSeparatesNoAnswerFromWrongAnswer(t *testing.T) {
	check := exactCheck(numericAnswer, "19")
	cases := []struct {
		name            string
		raw             string
		correct, parsed bool
	}{
		{"чистый ответ", "ANSWER: 19", true, true},
		{"без пробела после двоеточия", "ANSWER:19", true, true},
		{"жирная разметка вокруг метки", "**ANSWER:** 19", true, true},
		{"жирная разметка вокруг числа", "ANSWER: **19**", true, true},
		{"нижний регистр метки", "answer: 19", true, true},
		{"точка в конце", "ANSWER: 19.", true, true},
		{"с рассуждением до ответа", "Считаем 3+12+4\nANSWER: 19", true, true},
		{"отрицательное число", "ANSWER: -19", false, true},

		// The defect this parser was rewritten for. Working on the answer line is
		// not a wrong answer — it is a format failure, and the number inside it must
		// not be mistaken for the answer in either direction.
		{"выкладка на строке ответа", "ANSWER: 3 + 12 + 4 = 19", false, false},
		{"единицы измерения после числа", "ANSWER: 19 шт.", false, false},
		{"слово перед числом", "ANSWER: всего 19", false, false},
		// Whitespace after the colon may span the line break: what the rule forbids
		// is extra content on the answer line, not extra layout.
		{"ответ на следующей строке", "ANSWER:\n19", true, true},
		{"перевод строки, но с единицами", "ANSWER:\n19 шт.", false, false},

		{"неверное число, форма соблюдена", "ANSWER: 20", false, true},
		{"строки ANSWER нет вовсе", "Ответ: девятнадцать", false, false},
		{"число без метки", "19", false, false},
		{"пустой ответ", "", false, false},
	}
	for _, c := range cases {
		correct, parsed := check(c.raw)
		if correct != c.correct || parsed != c.parsed {
			t.Errorf("%s (%q): получили correct=%v parsed=%v, ожидалось %v/%v",
				c.name, c.raw, correct, parsed, c.correct, c.parsed)
		}
	}
}

func TestExactCheckTakesTheLastAnswerLine(t *testing.T) {
	// A model that works out loud writes intermediate ANSWER lines; the one it ends
	// on is what it commits to. Taking the first would score a corrected answer as
	// wrong.
	check := exactCheck(numericAnswer, "24")
	if correct, parsed := check("ANSWER: 25\nнет, пересчитаю\nANSWER: 24"); !correct || !parsed {
		t.Errorf("последняя строка ANSWER не выиграла: correct=%v parsed=%v", correct, parsed)
	}
	// And the last line still has to obey the form: a corrected answer buried in
	// working is a format failure, not a right answer.
	if correct, parsed := check("ANSWER: 25\nANSWER: итого 24"); correct || parsed {
		t.Errorf("выкладка в последней строке зачтена: correct=%v parsed=%v", correct, parsed)
	}
}

func TestLetterAnswerRequiresExactlyOneCyrillicLetter(t *testing.T) {
	check := exactCheck(letterAnswer, "в")
	cases := []struct {
		name            string
		raw             string
		correct, parsed bool
	}{
		{"одна буква", "ANSWER: в", true, true},
		{"верхний регистр", "ANSWER: В", true, true},
		{"в кавычках", `ANSWER: "в"`, true, true},
		{"в ёлочках", "ANSWER: «в»", true, true},
		{"с точкой", "ANSWER: В.", true, true},
		{"жирная", "ANSWER: **в**", true, true},

		{"другая буква — форма соблюдена", "ANSWER: с", false, true},

		// Two letters used to parse as the first one, so "ве" scored as a correct
		// "в". That inflated a rung's accuracy on the class most likely to produce it.
		{"две буквы", "ANSWER: ве", false, false},
		{"слово перед буквой", "ANSWER: буква в", false, false},
		{"фраза", "ANSWER: это в", false, false},
		{"латинская o вместо кириллической", "ANSWER: o", false, false},
		{"цифра вместо буквы", "ANSWER: 7", false, false},
		{"пусто после метки", "ANSWER:", false, false},
	}
	for _, c := range cases {
		correct, parsed := check(c.raw)
		if correct != c.correct || parsed != c.parsed {
			t.Errorf("%s (%q): получили correct=%v parsed=%v, ожидалось %v/%v",
				c.name, c.raw, correct, parsed, c.correct, c.parsed)
		}
	}
}

func TestCheckSchemaScoresShapeAndNothingElse(t *testing.T) {
	cases := []struct {
		name            string
		raw             string
		correct, parsed bool
	}{
		{"ровно три поля", `{"город":"Саратов","население":901361,"страна":"Россия"}`, true, true},
		// An absurd population still passes: there is no ground truth for the value,
		// and checking it would smuggle an unverifiable factual claim into a hit rate.
		{"в ограждении ```json", "```json\n{\"город\":\"Саратов\",\"население\":1,\"страна\":\"Россия\"}\n```", true, true},
		{"в простом ограждении", "```\n{\"город\":\"С\",\"население\":1,\"страна\":\"Р\"}\n```", true, true},
		{"с пробелами и переводами строк", "\n  {\"город\":\"С\",\"население\":1,\"страна\":\"Р\"}  \n", true, true},
		{"население строкой", `{"город":"С","население":"901361","страна":"Р"}`, false, true},
		{"нет одного поля", `{"город":"С","население":1}`, false, true},
		{"лишнее поле", `{"город":"С","население":1,"страна":"Р","регион":"Саратовская"}`, false, true},
		{"поля переименованы", `{"city":"Saratov","population":1,"country":"RU"}`, false, true},
		{"не JSON вовсе", "Саратов, население 901361, Россия", false, false},
		{"JSON-массив, не объект", `[{"город":"С","население":1,"страна":"Р"}]`, false, false},
		{"текст вокруг JSON", `Вот ответ: {"город":"С","население":1,"страна":"Р"}`, false, false},
		{"пусто", "", false, false},
	}
	for _, c := range cases {
		correct, parsed := checkSchema(c.raw)
		if correct != c.correct || parsed != c.parsed {
			t.Errorf("%s: получили correct=%v parsed=%v, ожидалось %v/%v",
				c.name, correct, parsed, c.correct, c.parsed)
		}
	}
}

func TestEveryTaskIsWellFormed(t *testing.T) {
	all := tasks()
	seen := map[string]bool{}
	for _, task := range all {
		if seen[task.key] {
			t.Errorf("ключ класса %s повторяется", task.key)
		}
		seen[task.key] = true
		if task.check == nil {
			t.Errorf("%s: нет проверки ответа", task.key)
		}
		if strings.TrimSpace(task.prompt) == "" {
			t.Errorf("%s: пустой запрос", task.key)
		}
		if task.maxTokens <= 0 {
			t.Errorf("%s: лимит токенов %d", task.key, task.maxTokens)
		}
		// A class with a ground truth must carry it, and a class without one must
		// not pretend to: an empty truth compared against a parsed answer would
		// score everything wrong.
		if task.kind == exact && strings.TrimSpace(task.truth) == "" {
			t.Errorf("%s: класс с эталоном, но эталон пустой", task.key)
		}
		if task.kind != exact && task.truth != "" {
			t.Errorf("%s: у класса без эталона задан truth = %q", task.key, task.truth)
		}
	}
	if len(all) == 0 {
		t.Fatal("классов нет вовсе")
	}
}

func TestExactTasksScoreTheirOwnTruthAsCorrect(t *testing.T) {
	// The oracle and the checker have to agree. If they ever disagree, every model
	// is scored against something no answer can satisfy — and the report would
	// read as "all models failed" instead of "the harness is broken".
	for _, task := range tasks() {
		if task.kind != exact {
			continue
		}
		correct, parsed := task.check("ANSWER: " + task.truth)
		if !correct || !parsed {
			t.Errorf("%s: собственный эталон %q не зачтён (correct=%v parsed=%v)",
				task.key, task.truth, correct, parsed)
		}
	}
}

func TestPickTasksRejectsUnknownKeys(t *testing.T) {
	all := tasks()
	if _, err := pickTasks(all, "T1,T404"); err == nil {
		t.Error("неизвестный класс принят молча")
	}
	got, err := pickTasks(all, " t1 , T0 ")
	if err != nil {
		t.Fatalf("разбор списка классов: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("отобрано %d классов, ожидалось 2", len(got))
	}
	if _, err := pickTasks(all, ""); err != nil {
		t.Errorf("пустой список должен означать «все»: %v", err)
	}
}

func TestNoSystemPromptCarriesACopyableePlaceholder(t *testing.T) {
	// The pilot's first run found qwen3:1.7b answering "…= 19 шт. ANSWER: <число>."
	// It had done the arithmetic and then copied the placeholder instead of
	// substituting into it, so a format failure scored as an arithmetic one. The
	// prompt came from day 4, which only ever ran it on flash.
	//
	// A weak model copies whatever looks like a slot. This test keeps slots out of
	// the prompts, so the accuracy column measures ability and the format axis
	// stays where it belongs — in T3.
	for _, task := range tasks() {
		for _, slot := range []string{"<", ">", "{}", "[]", "..."} {
			if strings.Contains(task.system, slot) {
				t.Errorf("%s: в системном промпте есть копируемая заглушка %q:\n  %s",
					task.key, slot, task.system)
			}
			if strings.Contains(task.prompt, slot) {
				t.Errorf("%s: в запросе есть копируемая заглушка %q:\n  %s",
					task.key, slot, task.prompt)
			}
		}
	}
}

func TestAnswerTailStripsDecorationInAnyNesting(t *testing.T) {
	// Decoration nests in whatever order the model felt like, and a single pass of
	// trims leaves the inner layer behind: "**«19».**" came out as "19»", which the
	// numeric check then rejected as no answer at all. That turns a correct answer
	// into a format defect, which is the opposite of what the split is for.
	cases := map[string]string{
		"ANSWER: 19":         "19",
		"ANSWER: **19**":     "19",
		"ANSWER: «19»":       "19",
		"ANSWER: **«19».**":  "19",
		"ANSWER: `«19»`.":    "19",
		"ANSWER: **«в».**":   "в",
		"ANSWER: _19_":       "19",
		"ANSWER: 3 + 4 = 19": "3 + 4 = 19",
	}
	for raw, want := range cases {
		got, ok := answerTail(raw)
		if !ok || got != want {
			t.Errorf("%q -> %q (ok=%v), ожидалось %q", raw, got, ok, want)
		}
	}
	if _, ok := answerTail("никакой метки"); ok {
		t.Error("строка без ANSWER объявлена разобранной")
	}
}

func TestSchemaCheckerRejectsALiteralNull(t *testing.T) {
	// "null" unmarshals into a nil map without an error, so without an explicit
	// guard it would be reported as "produced an object, wrong keys" here while the
	// JS mirror reports "produced no object at all". The parity test cannot see a
	// case neither side is asked about, so the case lives here too.
	for _, raw := range []string{"null", " null ", "```json\nnull\n```"} {
		correct, parsed := checkSchema(raw)
		if correct || parsed {
			t.Errorf("%q: correct=%v parsed=%v, ожидалось false/false", raw, correct, parsed)
		}
	}
}
