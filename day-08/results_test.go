// Package day08 holds no program: day 8 grew internal/agent and day-06 rather than
// copying an interface into a new folder. What it holds is the measurement data and
// the write-up of it.
//
// This test is the write-up's check. Every figure in RESULTS.md is recomputed from
// the rows the probes wrote and looked up in the text, because a number typed by hand
// into a document goes stale the moment a run is repeated — and a stale number in a
// report is indistinguishable from a fabricated one.
package day08

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

type row struct {
	Probe               string  `json:"probe"`
	Turn                int     `json:"turn"`
	EstTotal            int     `json:"estTotal"`
	EstSystem           int     `json:"estSystem"`
	EstHistory          int     `json:"estHistory"`
	EstInput            int     `json:"estInput"`
	PromptTokens        int     `json:"promptTokens"`
	CacheHit            int     `json:"cacheHit"`
	CacheMiss           int     `json:"cacheMiss"`
	CompletionTokens    int     `json:"completionTokens"`
	CumPromptTokens     int     `json:"cumPromptTokens"`
	CumCompletionTokens int     `json:"cumCompletionTokens"`
	CumCost             float64 `json:"cumCost"`
	Cost                float64 `json:"cost"`
	Policy              string  `json:"policy"`
	Dropped             int     `json:"dropped"`
	Outcome             string  `json:"outcome"`
	Truncated           bool    `json:"truncated"`
	ValidJSON           *bool   `json:"validJson"`
	ElapsedMs           int64   `json:"elapsedMs"`
}

func load(t *testing.T, name string) []row {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out []row
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out = append(out, r)
	}
	return out
}

// spaced formats an integer the way the document writes them: 78700 → "78 700".
func spaced(n int) string {
	s := fmt.Sprintf("%d", n)
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	return strings.Join(append([]string{s}, parts...), " ")
}

func TestEveryFigureInResultsComesFromTheRuns(t *testing.T) {
	raw, err := os.ReadFile("RESULTS.md")
	if err != nil {
		t.Fatalf("RESULTS.md: %v", err)
	}
	text := string(raw)
	want := func(what, s string) {
		t.Helper()
		if !strings.Contains(text, s) {
			t.Errorf("%s: в RESULTS.md нет %q — документ разошёлся с прогонами", what, s)
		}
	}

	growth := load(t, "growth.jsonl")
	ceiling := load(t, "ceiling.jsonl")
	window := load(t, "window.jsonl")
	output := load(t, "output.jsonl")
	all := append(append(append(append([]row{}, growth...), ceiling...), window...), output...)

	// 1. The counter against the fact, over every call that was billed.
	var ratios []float64
	billed := 0
	under := 0
	minIn, maxIn := 1<<30, 0
	for _, r := range all {
		if r.PromptTokens == 0 {
			continue
		}
		billed++
		ratios = append(ratios, float64(r.EstTotal)/float64(r.PromptTokens))
		if r.EstTotal < r.PromptTokens {
			under++
		}
		if r.PromptTokens < minIn {
			minIn = r.PromptTokens
		}
		if r.PromptTokens > maxIn {
			maxIn = r.PromptTokens
		}
	}
	sort.Float64s(ratios)
	want("число оплаченных вызовов", fmt.Sprintf("%d вызова", billed))
	want("диапазон входа", fmt.Sprintf("от %s до %s токенов", spaced(minIn), spaced(maxIn)))
	want("минимум отношения", fmt.Sprintf("| минимум | %.3f |", ratios[0]))
	want("медиана отношения", fmt.Sprintf("| медиана | %.3f |", ratios[len(ratios)/2]))
	want("максимум отношения", fmt.Sprintf("| максимум | %.3f |", ratios[len(ratios)-1]))
	want("занижения", fmt.Sprintf("**%d из %d**", under, billed))
	if under != 0 {
		t.Errorf("счётчик занизил %d раз — утверждение о безопасной стороне ошибки больше не верно", under)
	}

	// 2. The growth table, row by row.
	byTurn := map[int]row{}
	for _, r := range growth {
		byTurn[r.Turn] = r
	}
	for _, turn := range []int{1, 5, 10, 15, 20, 25} {
		r, ok := byTurn[turn]
		if !ok {
			t.Fatalf("в growth.jsonl нет хода %d", turn)
		}
		want(fmt.Sprintf("строка роста, ход %d", turn),
			fmt.Sprintf("| %d | %s | %s | %d | $%.6f | %s | $%.6f |",
				turn, spaced(r.EstTotal), spaced(r.PromptTokens), r.CompletionTokens,
				r.Cost, spaced(r.CumPromptTokens), r.CumCost))
	}
	first, last := byTurn[1], byTurn[25]
	want("во сколько вырос вход", fmt.Sprintf("вырос в %.0f раз", float64(last.PromptTokens)/float64(first.PromptTokens)))
	want("сумма входа", fmt.Sprintf("%s токенов, то есть **%.1f последних", spaced(last.CumPromptTokens),
		float64(last.CumPromptTokens)/float64(last.PromptTokens)))
	want("раскладка последнего запроса", fmt.Sprintf("%s\nтокена: системный промпт %d (%.1f%%), история %s (%.0f%%), сам вопрос %d",
		spaced(last.EstTotal), last.EstSystem, 100*float64(last.EstSystem)/float64(last.EstTotal),
		spaced(last.EstHistory), 100*float64(last.EstHistory)/float64(last.EstTotal), last.EstInput))
	want("цена первого и последнего хода", fmt.Sprintf("$%.6f → $%.6f", first.Cost, last.Cost))

	var gHit, gMiss int
	for _, r := range growth {
		gHit += r.CacheHit
		gMiss += r.CacheMiss
	}
	want("доля кэша в прогоне роста", fmt.Sprintf("%.0f%% входа пришло из кэша", 100*float64(gHit)/float64(gHit+gMiss)))
	want("попадания и промахи", fmt.Sprintf("(%s попадания против %s промахов)", spaced(gHit), spaced(gMiss)))

	// 3. The window ladder.
	if len(window) != 2 {
		t.Fatalf("в window.jsonl %d ступеней, ожидалось 2", len(window))
	}
	ok, refused := window[0], window[1]
	want("контрольная ступень", fmt.Sprintf("| %s | %s | ok | $%.6f |", spaced(ok.EstTotal), spaced(ok.PromptTokens), ok.Cost))
	want("отвергнутая ступень", fmt.Sprintf("| %s | 1 133 947 (из текста отказа) | **400 Bad Request** | $%.6f |",
		spaced(refused.EstTotal), refused.Cost))
	want("время отказа", fmt.Sprintf("занял %.1f секунды", float64(refused.ElapsedMs)/1000))
	if refused.Outcome != "error" {
		t.Errorf("верхняя ступень окна вернулась как %q — вывод об отказе построен не на этих данных", refused.Outcome)
	}
	if refused.Cost != 0 {
		t.Errorf("у отвергнутого запроса цена %f — утверждение «ни одного токена» неверно", refused.Cost)
	}

	// 4. The ceiling policies.
	type agg struct {
		answered, refused, in, hit, miss, lateIn, lateHit, lateMiss int
		cost, lateCost                                              float64
	}
	pol := map[string]*agg{}
	for _, r := range ceiling {
		a := pol[r.Policy]
		if a == nil {
			a = &agg{}
			pol[r.Policy] = a
		}
		a.cost += r.Cost
		switch r.Outcome {
		case "ok":
			a.answered++
			a.in += r.PromptTokens
			a.hit += r.CacheHit
			a.miss += r.CacheMiss
			if r.Turn >= 6 {
				a.lateIn += r.PromptTokens
				a.lateHit += r.CacheHit
				a.lateMiss += r.CacheMiss
				a.lateCost += r.Cost
			}
		case "refused":
			a.refused++
		}
	}
	for _, name := range []string{"refuse", "trim", "warn"} {
		a := pol[name]
		if a == nil {
			t.Fatalf("в ceiling.jsonl нет политики %s", name)
		}
		want("строка политики "+name, fmt.Sprintf("| `%s` | %d | %d | %s | %.0f%% | $%.6f |",
			name, a.answered, a.refused, spaced(a.in),
			100*float64(a.hit)/float64(a.hit+a.miss), a.cost))
	}
	trim, warn := pol["trim"], pol["warn"]
	want("ходы 6-10 у trim", fmt.Sprintf("| `trim` | %s | %.0f%% | **$%.6f** |",
		spaced(trim.lateIn), 100*float64(trim.lateHit)/float64(trim.lateHit+trim.lateMiss), trim.lateCost))
	want("ходы 6-10 у warn", fmt.Sprintf("| `warn` | %s | %.0f%% | $%.6f |",
		spaced(warn.lateIn), 100*float64(warn.lateHit)/float64(warn.lateHit+warn.lateMiss), warn.lateCost))
	want("во сколько меньше токенов", fmt.Sprintf("**в %.1f раза меньше**", float64(warn.lateIn)/float64(trim.lateIn)))
	want("насколько дороже", fmt.Sprintf("**на %.0f%% больше**", 100*(trim.lateCost/warn.lateCost-1)))

	// 5. The generation cap.
	if len(output) != 2 {
		t.Fatalf("в output.jsonl %d строк, ожидалось 2", len(output))
	}
	for _, r := range output {
		if r.ValidJSON == nil {
			t.Fatalf("строка output без признака разбора JSON")
		}
	}
	whole, cut := output[0], output[1]
	if !*whole.ValidJSON || whole.Truncated {
		t.Errorf("контрольный ответ оборван или не разбирается — сравнивать не с чем")
	}
	if *cut.ValidJSON || !cut.Truncated {
		t.Errorf("ответ под низким потолком не оборван или всё ещё разбирается: вывод главы C не про эти данные")
	}
	want("выход контрольного ответа", fmt.Sprintf("| 400 | %d | stop |", whole.CompletionTokens))
	want("выход оборванного ответа", fmt.Sprintf("| 16 | %d | length |", cut.CompletionTokens))

	// 6. What the day cost, in total.
	var total float64
	for _, f := range []string{"growth.jsonl", "growth-uncalibrated.jsonl", "ceiling.jsonl", "window.jsonl", "output.jsonl"} {
		for _, r := range load(t, f) {
			total += r.Cost
		}
	}
	want("итог по деньгам", fmt.Sprintf("**$%.6f**", total))
}

// The calibration figures and the characters-per-token number are computed from the
// conversations the probes stored, which are not repository content (day 7 keeps
// .sessions out of git). On a clone without them the test skips: a check that cannot
// see its subject has found nothing, not found a problem. On this machine — the only
// one where the write-up can drift from the runs — it runs.
func TestCalibrationFiguresComeFromTheStoredConversations(t *testing.T) {
	raw, err := os.ReadFile("RESULTS.md")
	if err != nil {
		t.Fatalf("RESULTS.md: %v", err)
	}
	text := string(raw)
	want := func(what, s string) {
		t.Helper()
		if !strings.Contains(text, s) {
			t.Errorf("%s: в RESULTS.md нет %q", what, s)
		}
	}

	// The uncalibrated run: the ratio range the first version of the weights produced.
	old := load(t, "growth-uncalibrated.jsonl")
	var ratios []float64
	for _, r := range old {
		if r.PromptTokens > 0 {
			ratios = append(ratios, float64(r.EstTotal)/float64(r.PromptTokens))
		}
	}
	sort.Float64s(ratios)
	want("диапазон до калибровки", fmt.Sprintf("в %.2f–%.2f раза", ratios[0], ratios[len(ratios)-1]))

	// Prose against filler: the same counter, two kinds of text.
	var prose, filler []float64
	for _, f := range []string{"growth.jsonl", "ceiling.jsonl"} {
		for _, r := range load(t, f) {
			if r.PromptTokens > 0 {
				prose = append(prose, float64(r.EstTotal)/float64(r.PromptTokens))
			}
		}
	}
	for _, r := range load(t, "window.jsonl") {
		if r.PromptTokens > 0 {
			filler = append(filler, float64(r.EstTotal)/float64(r.PromptTokens))
		}
	}
	sort.Float64s(prose)
	want("диапазон на прозе", fmt.Sprintf("держится в %.3f–%.3f", prose[0], prose[len(prose)-1]))
	want("худшее значение", fmt.Sprintf("худшее значение %.3f", filler[len(filler)-1]))

	// Characters per token, from the conversation the calibrated run left behind.
	chars, tokens, ok := lastRequestSize(t, "../.sessions/day8-growth-cal.json", "growth.jsonl")
	if !ok {
		t.Skip("сохранённой беседы прогона роста нет рядом — сверять число символов на токен не с чем")
	}
	want("символов на токен", fmt.Sprintf("около %.2f\nсимвола на токен", float64(chars)/float64(tokens)))
	want("сырые числа", fmt.Sprintf("%s символа → %s токенов", spaced(chars), spaced(tokens)))
}

// lastRequestSize measures the request of the final turn: the system prompt plus every
// message except the last answer, which had not been written when the request went out.
func lastRequestSize(t *testing.T, session, rows string) (chars, tokens int, ok bool) {
	t.Helper()
	raw, err := os.ReadFile(session)
	if err != nil {
		return 0, 0, false
	}
	var snap struct {
		System   string `json:"system"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("%s: %v", session, err)
	}
	all := load(t, rows)
	last := all[len(all)-1]
	if len(snap.Messages) < 2*last.Turn {
		t.Fatalf("в беседе %d сообщений, а замер дошёл до хода %d", len(snap.Messages), last.Turn)
	}
	chars = len([]rune(snap.System))
	for _, m := range snap.Messages[:2*last.Turn-1] {
		chars += len([]rune(m.Content))
	}
	return chars, last.PromptTokens, true
}
