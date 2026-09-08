package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// The day-7 measurement, and the reason it lives in this package rather than in the
// interface: writing a row of it means encoding JSON, and day 6's encapsulation test
// forbids day-06 from importing encoding/json at all.
//
// What is being measured is item 4 of the week-1 plan in ~/dev/llm-practice
// (reviews/week-1.md, "Measure deliberately in week 2"): input tokens per turn as the
// history grows, and the cache-hit share once the system prompt is stable. Day 7 adds
// a question that could not be asked before it, because before it there was nothing
// to reload: does the provider's prefix cache survive a restart of the client?
//
// The instrument's negative control is free and built in: turn 1 of a fresh session
// has no prefix to hit, so every run demonstrably starts with the counter able to
// report a miss. A cache-hit figure that was never preceded by a miss would say
// nothing about the cache.

// ProbeSystemPrompt is the instrument's fixed setting, and it is long on purpose.
// Week 1 measured that the provider's cache works in 64-token blocks and gives no
// discount at all on short prompts: a 23-token prompt sent three times cached 0 of 23
// every time (internal/llm/client.go, measured 2026-09-04). A probe run under the
// CLI's ordinary short prompt would therefore measure the length of the prompt and
// report it as a fact about restarts.
const ProbeSystemPrompt = "Ты — счётный помощник в замере роста контекста. " +
	"Отвечай строго одним числом или одним коротким словом, без пояснений, без знаков " +
	"препинания в конце и без повторения вопроса. Если вопрос про число, выведи только " +
	"само число цифрами. Если вопрос про слово, выведи только это слово в именительном " +
	"падеже. Никогда не добавляй приветствий, извинений, оговорок и предложений помочь " +
	"дальше. Никогда не объясняй, как ты получил ответ. Никогда не задавай встречных " +
	"вопросов. Этот системный промпт неизменен от хода к ходу и от запуска к запуску: " +
	"он и есть тот стабильный префикс, попадания которого в кэш поставщика измеряет " +
	"этот прогон. Длина его выбрана намеренно — на коротком префиксе кэш поставщика " +
	"не даёт скидки вовсе, потому что работает блоками по 64 токена."

// ProbeQuestions are short by design: the answer must not grow the history faster
// than the history itself does, or the measurement is about output length. Each is
// distinct so that no two turns are accidentally the same request.
var ProbeQuestions = []string{
	"Сколько будет 2 плюс 3?",
	"Сколько будет 7 умножить на 6?",
	"Назови третье простое число.",
	"Сколько дней в високосном году?",
	"Сколько будет 100 минус 37?",
	"Назови столицу Франции.",
	"Сколько будет 12 в квадрате?",
	"Сколько букв в слове контекст?",
	"Назови шестое число Фибоначчи, считая с единицы.",
	"Сколько минут в сутках?",
	"Сколько будет 45 разделить на 9?",
	"Назови самую длинную реку в России.",
}

// ProbeRow is one turn as it was actually billed.
type ProbeRow struct {
	// Run is the identifier of the process that produced this row. The restart is
	// the point of the measurement, so it has to be visible in the data rather than
	// remembered by whoever ran it.
	Run              string `json:"run"`
	Turn             int    `json:"turn"`
	Restored         int    `json:"restoredTurns"`
	PromptTokens     int    `json:"promptTokens"`
	CacheHit         int    `json:"cacheHit"`
	CacheMiss        int    `json:"cacheMiss"`
	CompletionTokens int    `json:"completionTokens"`
	// Cost is in dollars at the moment of the call; Priced says whether the model
	// was in the price table, because a zero cost and an unknown one are different
	// facts.
	Cost      float64 `json:"cost"`
	Priced    bool    `json:"priced"`
	ElapsedMs int64   `json:"elapsedMs"`
	Model     string  `json:"model"`
	At        string  `json:"at"`
	Question  string  `json:"question"`
	Answer    string  `json:"answer"`
}

// CacheShare is the fraction of the input served from the provider's cache. It
// returns false when there was no input to speak of, so that "no data" is not
// printed as "0% cached".
func (r ProbeRow) CacheShare() (float64, bool) {
	total := r.CacheHit + r.CacheMiss
	if total == 0 {
		return 0, false
	}
	return float64(r.CacheHit) / float64(total), true
}

// RunContextProbe asks the agent n questions in a row, printing what each turn cost
// and appending one machine-readable row per turn.
//
// It deliberately does nothing about the restart itself: the process is restarted by
// running the binary a second time, and the rows carry the run id that makes the
// boundary visible. A "restart" simulated inside one process would prove that a
// struct can be rebuilt, which was never in doubt.
func RunContextProbe(a *Agent, run string, turns int, human io.Writer, rows io.Writer) error {
	if turns <= 0 {
		return fmt.Errorf("замер: число ходов %d, нужно хотя бы 1", turns)
	}
	restored := a.Restored().Turns

	fmt.Fprintf(human, "прогон %s · ходов в этом запуске: %d · загружено из истории: %d\n",
		run, turns, restored)
	fmt.Fprintln(human, "ход | вход | из кэша | мимо | доля кэша | выход | цена | всего")

	var spent float64
	var priced bool
	for i := 0; i < turns; i++ {
		q := ProbeQuestions[(restored+i)%len(ProbeQuestions)]
		reply, err := a.Ask(context.Background(), q)
		if err != nil {
			return fmt.Errorf("замер, ход %d: %w", restored+i+1, err)
		}
		row := ProbeRow{
			Run:              run,
			Turn:             reply.Turn,
			Restored:         restored,
			PromptTokens:     reply.Usage.PromptTokens,
			CacheHit:         reply.Usage.CachedTokens,
			CacheMiss:        reply.Usage.MissedTokens,
			CompletionTokens: reply.Usage.CompletionTokens,
			Cost:             reply.Usage.Cost,
			Priced:           reply.Usage.Priced,
			ElapsedMs:        reply.Elapsed.Milliseconds(),
			Model:            reply.Model,
			At:               time.Now().UTC().Format(time.RFC3339),
			Question:         q,
			Answer:           reply.Text,
		}
		spent += row.Cost
		priced = priced || row.Priced

		if rows != nil {
			line, err := json.Marshal(row)
			if err != nil {
				return fmt.Errorf("замер, ход %d: %w", row.Turn, err)
			}
			if _, err := rows.Write(append(line, '\n')); err != nil {
				return fmt.Errorf("замер, ход %d: запись строки: %w", row.Turn, err)
			}
		}

		share := "—"
		if v, ok := row.CacheShare(); ok {
			share = fmt.Sprintf("%.0f%%", v*100)
		}
		cost := "неизвестна"
		if row.Priced {
			cost = fmt.Sprintf("$%.6f", row.Cost)
		}
		total := "неизвестно"
		if priced {
			total = fmt.Sprintf("$%.6f", spent)
		}
		fmt.Fprintf(human, "%3d | %5d | %7d | %5d | %9s | %5d | %s | %s\n",
			row.Turn, row.PromptTokens, row.CacheHit, row.CacheMiss, share,
			row.CompletionTokens, cost, total)
	}
	return nil
}
