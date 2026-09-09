package agent

// Day 8's measurements. They live in this package for the same reason day 7's do:
// writing a machine-readable row means encoding JSON, and day 6's encapsulation test
// forbids the CLI from importing encoding/json at all.
//
// Four runs, and each one carries its own negative control, because a number that
// could not have come out differently measures nothing:
//
//	growth   — the same conversation, turn after turn: what the request weighs, what
//	           the conversation has cost so far, and how far apart those two grow.
//	window   — a ladder of request sizes against the provider's real context window.
//	           The small rung must succeed, or the big rung's refusal says nothing.
//	ceiling  — the agent's own limit and what its policy does when a request exceeds
//	           it. Turns before the ceiling bites are the control.
//	output   — the generation cap: the same question under a cap that fits and one
//	           that does not.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// GrowthSystemPrompt makes the answers long enough for the history to grow at a rate
// worth plotting. Day 7's probe deliberately kept answers to one token — it was
// measuring the cache, and long answers would have measured the answers. Here the
// growth of the history IS the subject, so the answers have to be of a realistic
// size: the host puts one code-related request at 8-10K tokens [урок недели 2].
const GrowthSystemPrompt = "Ты — собеседник в замере роста контекста. Отвечай связным " +
	"текстом на русском языке, ровно 4-6 предложений, без списков, без заголовков и без " +
	"уточняющих вопросов. Не повторяй вопрос и не ссылайся на предыдущие ответы."

// GrowthQuestions are distinct on purpose: two identical turns would measure the
// provider's cache instead of the conversation's growth.
var GrowthQuestions = []string{
	"Чем отличается кэш поставщика от памяти агента?",
	"Почему история диалога отправляется в модель заново на каждом ходу?",
	"Что такое контекстное окно модели простыми словами?",
	"Чем токен отличается от символа?",
	"Почему длинный диалог дорожает быстрее, чем растёт его последний запрос?",
	"Какие есть способы уменьшить размер истории, если она перестала помещаться?",
	"Чем суммаризация истории хуже полного хранения?",
	"Почему у разных моделей разное число токенов на один и тот же текст?",
	"Что происходит с ответом, если упереться в потолок генерации?",
	"Зачем агенту знать вес запроса до отправки?",
	"Чем скользящее окно истории отличается от полного её хранения?",
	"Почему стоимость входа обычно ниже стоимости выхода?",
	"Что такое системный промпт и почему его держат первым?",
	"Чем отличается память агента от состояния модели?",
	"Почему на коротком промпте кэш не даёт экономии?",
	"Как объяснить рост расходов на длинном диалоге заказчику?",
	"Что теряет агент, когда обрезает старые ходы?",
	"Почему счётчик токенов на клиенте всегда приблизительный?",
	"Чем опасен агент, который не считает свои расходы?",
	"Зачем в ответе API нужны отдельные поля про кэш?",
	"Почему рассуждение модели тарифицируется как выход?",
	"Что важнее для стоимости — длина вопроса или длина истории?",
	"Как понять, что диалог пора начинать заново?",
	"Чем плох агент, который молча теряет часть контекста?",
	"Почему одна и та же задача может стоить по-разному в разное время суток?",
	"Что показывает доля попаданий в кэш и чего она не показывает?",
	"Зачем измерять ошибку собственного счётчика токенов?",
	"Почему потолок контекста — это политика, а не свойство модели?",
	"Как связаны число ходов и цена одного хода?",
	"Что стоит логировать в агенте, чтобы расход не был сюрпризом?",
}

// TokenRow is one observation of any of the four runs. One row type rather than four
// because the showcase reads them all the same way, and because the fields that are
// empty in a given run say something too: a refused request has no prompt tokens.
type TokenRow struct {
	Probe string `json:"probe"`
	Run   string `json:"run"`
	Turn  int    `json:"turn"`

	// What the agent thought before sending.
	// No omitempty on the parts: a zero history is a fact about turn 1, and a
	// reader that cannot tell "no history" from "field absent" would have to guess.
	EstTotal   int `json:"estTotal"`
	EstSystem  int `json:"estSystem"`
	EstHistory int `json:"estHistory"`
	EstInput   int `json:"estInput"`

	// What the provider billed.
	PromptTokens     int `json:"promptTokens"`
	CacheHit         int `json:"cacheHit"`
	CacheMiss        int `json:"cacheMiss"`
	CompletionTokens int `json:"completionTokens"`
	ReasoningTokens  int `json:"reasoningTokens,omitempty"`

	// Running totals for the conversation this row belongs to.
	CumPromptTokens     int     `json:"cumPromptTokens"`
	CumCompletionTokens int     `json:"cumCompletionTokens"`
	CumCost             float64 `json:"cumCost"`

	Cost   float64 `json:"cost"`
	Priced bool    `json:"priced"`

	// The ceiling and what it did on this turn.
	Limit   int    `json:"limit,omitempty"`
	Policy  string `json:"policy,omitempty"`
	Dropped int    `json:"dropped,omitempty"`
	Warning string `json:"warning,omitempty"`

	// Outcome is "ok", "refused" (the agent's own ceiling stopped it, nothing spent)
	// or "error" (the provider refused). Error is the provider's answer verbatim —
	// the whole point of the window run is what it actually says.
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`

	Truncated bool `json:"truncated,omitempty"`
	// ValidJSON is set only by the output run, where the question asked for JSON:
	// a truncated answer is not a broken API call, it is a broken parse.
	ValidJSON *bool `json:"validJson,omitempty"`

	ElapsedMs int64  `json:"elapsedMs"`
	Model     string `json:"model,omitempty"`
	At        string `json:"at"`
	Question  string `json:"question,omitempty"`
	Answer    string `json:"answer,omitempty"`
}

// EstimateError is how far the local counter was from what the provider billed, in
// tokens. Positive means the counter over-counted, which is the safe direction: a
// ceiling built on an under-counting estimate lets through requests it was meant to
// stop. It reports false when the provider billed nothing, because there is no error
// to speak of against a call that never happened.
func (r TokenRow) EstimateError() (int, bool) {
	if r.PromptTokens == 0 {
		return 0, false
	}
	return r.EstTotal - r.PromptTokens, true
}

// RunGrowthProbe walks one conversation forward and records, per turn, the weight of
// the request against the weight of everything the conversation has cost so far.
// Those are the two numbers day 8 is about, and they grow at different rates: the
// request grows by one exchange, the bill grows by the whole request.
func RunGrowthProbe(a *Agent, run string, turns int, human, rows io.Writer) error {
	if turns <= 0 {
		return fmt.Errorf("замер роста: число ходов %d, нужно хотя бы 1", turns)
	}
	if turns > len(GrowthQuestions) {
		return fmt.Errorf("замер роста: вопросов заготовлено %d, запрошено ходов %d — повтор вопроса измерял бы кэш, а не рост",
			len(GrowthQuestions), turns)
	}
	started := a.Turns()

	fmt.Fprintf(human, "прогон %s · ходов: %d · уже в истории: %d\n", run, turns, started)
	fmt.Fprintln(human, "ход | оценка | вход | ошибка | из кэша | выход | цена хода | всего вход | всего $")

	for i := 0; i < turns; i++ {
		q := GrowthQuestions[(started+i)%len(GrowthQuestions)]
		reply, err := a.Ask(context.Background(), q)
		if errors.Is(err, ErrNotSaved) {
			fmt.Fprintf(human, "ВНИМАНИЕ: ход %d не сохранён (%v) — замер продолжается\n", reply.Turn, err)
		} else if err != nil {
			return fmt.Errorf("замер роста, ход %d: %w", started+i+1, err)
		}
		row := rowOf("growth", run, reply, q)
		row.CumPromptTokens = a.Totals().PromptTokens
		row.CumCompletionTokens = a.Totals().CompletionTokens
		row.CumCost = a.Totals().Cost
		if err := writeRow(rows, row); err != nil {
			return err
		}

		errText := "—"
		if delta, ok := row.EstimateError(); ok {
			errText = fmt.Sprintf("%+d", delta)
		}
		fmt.Fprintf(human, "%3d | %6d | %5d | %6s | %7d | %5d | %9s | %10d | %s\n",
			row.Turn, row.EstTotal, row.PromptTokens, errText, row.CacheHit,
			row.CompletionTokens, money(row.Cost, row.Priced),
			row.CumPromptTokens, money(row.CumCost, row.Priced))
	}
	return nil
}

// RunWindowProbe sends one oversized message at each size in the ladder and records
// what came back. The small rungs are the negative control: if they do not succeed,
// the big rung's refusal is not evidence about the window.
//
// Each rung is a fresh conversation by construction — the message is sent as the
// whole request — so a rung that fails does not poison the next one.
func RunWindowProbe(a *Agent, run string, sizes []int, human, rows io.Writer) error {
	if len(sizes) < 2 {
		return errors.New("замер окна: нужна хотя бы одна ступень контроля и одна за пределом")
	}
	fmt.Fprintf(human, "прогон %s · ступеней: %d\n", run, len(sizes))
	fmt.Fprintln(human, "ступень | оценка | вход | выход | итог | цена")

	for i, size := range sizes {
		if err := a.Reset(); err != nil {
			return fmt.Errorf("замер окна, ступень %d: %w", i+1, err)
		}
		blob := Filler(size)
		reply, err := a.Ask(context.Background(), blob)
		if errors.Is(err, ErrNotSaved) {
			err = nil
		}
		row := rowOf("window", run, reply, fmt.Sprintf("заполнитель на ~%d токенов", size))
		row.Turn = i + 1
		if err != nil {
			row.Outcome = "error"
			row.Error = err.Error()
			row.Answer = ""
		}
		row.CumCost = a.Totals().Cost
		if err := writeRow(rows, row); err != nil {
			return err
		}

		outcome := row.Outcome
		if row.Outcome == "error" {
			outcome = "ОТКАЗ"
		}
		fmt.Fprintf(human, "%7d | %6d | %5d | %5d | %s | %s\n",
			size, row.EstTotal, row.PromptTokens, row.CompletionTokens, outcome, money(row.Cost, row.Priced))
		if row.Error != "" {
			fmt.Fprintf(human, "        ответ поставщика дословно: %s\n", trim(row.Error, 600))
		}
	}
	return nil
}

// RunCeilingProbe walks a conversation into the agent's own ceiling. The agent is
// configured before it gets here — the policy under test is the one it holds — and
// the early turns, which fit, are the control: they show the same code path passing.
func RunCeilingProbe(a *Agent, run string, turns int, human, rows io.Writer) error {
	if a.cfg.MaxContextTokens <= 0 {
		return errors.New("замер потолка: потолок не задан — мерить нечего")
	}
	if turns <= 0 || turns > len(GrowthQuestions) {
		return fmt.Errorf("замер потолка: ходов %d, доступно вопросов %d", turns, len(GrowthQuestions))
	}
	policy := string(a.overflowPolicy())
	fmt.Fprintf(human, "прогон %s · потолок %d токенов · политика %s · ходов: %d\n",
		run, a.cfg.MaxContextTokens, policy, turns)
	fmt.Fprintln(human, "ход | оценка | потолок | вход | из кэша | мимо | итог | отброшено")

	started := a.Turns()
	for i := 0; i < turns; i++ {
		q := GrowthQuestions[(started+i)%len(GrowthQuestions)]
		reply, err := a.Ask(context.Background(), q)
		if errors.Is(err, ErrNotSaved) {
			err = nil
		}
		row := rowOf("ceiling", run, reply, q)
		row.Turn = started + i + 1
		row.Limit = a.cfg.MaxContextTokens
		row.Policy = policy
		switch {
		case errors.Is(err, ErrContextOverflow), errors.Is(err, ErrInputAlone):
			// The agent refused before the provider was asked: this costs nothing,
			// and that is the finding, not an incident.
			row.Outcome = "refused"
			row.Error = err.Error()
		case err != nil:
			row.Outcome = "error"
			row.Error = err.Error()
		}
		row.CumPromptTokens = a.Totals().PromptTokens
		row.CumCompletionTokens = a.Totals().CompletionTokens
		row.CumCost = a.Totals().Cost
		if err := writeRow(rows, row); err != nil {
			return err
		}

		fmt.Fprintf(human, "%3d | %6d | %7d | %5d | %7d | %5d | %s | %d\n",
			row.Turn, row.EstTotal, row.Limit, row.PromptTokens, row.CacheHit,
			row.CacheMiss, row.Outcome, row.Dropped)
		if row.Warning != "" {
			fmt.Fprintf(human, "      %s\n", row.Warning)
		}
		if row.Outcome == "refused" {
			fmt.Fprintf(human, "      отказ агента, вызова не было: %s\n", trim(row.Error, 300))
		}
	}
	return nil
}

// RunOutputProbe asks the same question of two agents that differ in one setting:
// the generation cap. The generous one is the control — it proves the question is
// answerable and that the parse succeeds when the answer is whole.
func RunOutputProbe(control, capped *Agent, run, question string, human, rows io.Writer) error {
	fmt.Fprintf(human, "прогон %s · один вопрос, два потолка генерации\n", run)
	for i, c := range []struct {
		label string
		a     *Agent
	}{{"потолок вмещает ответ", control}, {"потолок обрывает ответ", capped}} {
		reply, err := c.a.Ask(context.Background(), question)
		if errors.Is(err, ErrNotSaved) {
			err = nil
		}
		row := rowOf("output", run, reply, question)
		row.Turn = i + 1
		if err != nil {
			row.Outcome = "error"
			row.Error = err.Error()
		}
		valid := json.Valid([]byte(reply.Text))
		row.ValidJSON = &valid
		if err := writeRow(rows, row); err != nil {
			return err
		}

		fmt.Fprintf(human, "--- %s\n", c.label)
		fmt.Fprintf(human, "выход: %d токенов · оборван: %v · JSON разбирается: %v\n",
			row.CompletionTokens, row.Truncated, valid)
		fmt.Fprintf(human, "ответ: %s\n", trim(reply.Text, 400))
		if row.Error != "" {
			fmt.Fprintf(human, "ошибка: %s\n", trim(row.Error, 300))
		}
	}
	return nil
}

// Filler builds a message of roughly the requested weight in tokens, as the local
// counter measures it. The lines are numbered so that no two are identical: a blob
// of one repeated sentence would be a question about the provider's cache rather
// than about the size of a request.
//
// It over-shoots rather than under-shoots for the same reason the counter does —
// a rung of the window ladder that lands just under the limit measures nothing.
func Filler(tokens int) string {
	const line = "Строка %d: этот текст существует только затем, чтобы занять место в контекстном окне модели и ничего больше не значит.\n"
	var b strings.Builder
	// The running sum is the same counter the ceiling uses, so the blob is at least
	// the requested weight by construction. Estimating a "typical" line and
	// multiplying would land under the target whenever the estimate was generous,
	// and a window rung that lands under the limit measures nothing.
	total := 0
	for i := 1; total < tokens; i++ {
		s := fmt.Sprintf(line, i)
		b.WriteString(s)
		total += EstimateTokens(s)
	}
	return b.String()
}

// rowOf builds a row from the reply. The estimate comes from the reply rather than
// from a Preflight call made before Ask, and that distinction is not cosmetic: when
// the ceiling trims the history, the request that travels is not the one Preflight
// weighed. Rows written from the pre-trim estimate compared a request that was never
// sent against tokens that were — the first version of this file did exactly that,
// and it made the counter look 92% off on precisely the turns the trim ran.
func rowOf(probe, run string, reply Reply, question string) TokenRow {
	est := reply.Estimated
	row := TokenRow{
		Probe:            probe,
		Run:              run,
		Turn:             reply.Turn,
		EstTotal:         est.Total,
		EstSystem:        est.System,
		EstHistory:       est.History,
		EstInput:         est.Input,
		PromptTokens:     reply.Usage.PromptTokens,
		CacheHit:         reply.Usage.CachedTokens,
		CacheMiss:        reply.Usage.MissedTokens,
		CompletionTokens: reply.Usage.CompletionTokens,
		ReasoningTokens:  reply.Usage.ReasoningTokens,
		Cost:             reply.Usage.Cost,
		Priced:           reply.Usage.Priced,
		Dropped:          reply.Dropped,
		Warning:          reply.Warning,
		Truncated:        reply.Truncated,
		Outcome:          "ok",
		ElapsedMs:        reply.Elapsed.Milliseconds(),
		Model:            reply.Model,
		At:               time.Now().UTC().Format(time.RFC3339),
		Question:         trim(question, 200),
		Answer:           trim(reply.Text, 200),
	}
	return row
}

func writeRow(w io.Writer, row TokenRow) error {
	if w == nil {
		return nil
	}
	line, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("строка замера: %w", err)
	}
	if _, err := w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("запись строки замера: %w", err)
	}
	return nil
}

func money(v float64, priced bool) string {
	if !priced {
		return "неизвестна"
	}
	return fmt.Sprintf("$%.6f", v)
}

func trim(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
