package watch

import (
	"fmt"
	"sort"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
)

func View(current Watch) WatchView {
	view := WatchView{ID: current.ID, Codes: append([]string(nil), current.Codes...), EveryMinutes: current.EveryMinutes, Status: current.Status, CreatedAt: current.CreatedAt, StoppedAt: current.StoppedAt, PollsTotal: len(current.Polls)}
	for _, poll := range current.Polls {
		if !poll.OK {
			view.PollsFailed++
			view.LastError = poll.Error
		}
	}
	if len(current.Polls) > 0 {
		view.LastPollAt = current.Polls[len(current.Polls)-1].At
	}
	if current.Status == "active" {
		base := time.Now().UTC()
		if last, ok := parseTime(view.LastPollAt); ok {
			base = last
		}
		view.NextSlotAt = SlotStart(base, time.Duration(current.EveryMinutes)*time.Minute).Add(time.Duration(current.EveryMinutes) * time.Minute).Format(time.RFC3339)
	}
	return view
}

func Summaries(state File, watchID string, hours int, now time.Time) (SummaryResponse, error) {
	response := SummaryResponse{Now: now.UTC().Format(time.RFC3339), Watches: []Summary{}}
	selected := make([]Watch, 0)
	known := make([]string, 0, len(state.Watches))
	for _, current := range state.Watches {
		known = append(known, current.ID)
		if watchID == current.ID || (watchID == "" && current.Status == "active") {
			selected = append(selected, current)
		}
	}
	if watchID != "" && len(selected) == 0 {
		return response, fmt.Errorf("наблюдение %s не найдено; известные: %s", watchID, join(known))
	}
	if watchID == "" && len(selected) == 0 {
		response.Note = "активных наблюдений нет"
		return response, nil
	}
	for _, current := range selected {
		response.Watches = append(response.Watches, Aggregate(current, hours, now))
	}
	return response, nil
}

func Aggregate(current Watch, hours int, now time.Time) Summary {
	from := now.Add(-time.Duration(hours) * time.Hour)
	result := Summary{WatchID: current.ID, Codes: append([]string(nil), current.Codes...), EveryMinutes: current.EveryMinutes, Status: current.Status, Hours: hours, From: from.UTC().Format(time.RFC3339), To: now.UTC().Format(time.RFC3339), Publications: []Publication{}, Currencies: []CurrencySummary{}, MissingCodes: []string{}}
	type publicationData struct {
		first time.Time
		last  time.Time
		rates map[string]float64
	}
	publications := map[string]publicationData{}
	missing := map[string]bool{}
	for _, poll := range current.Polls {
		at, ok := parseTime(poll.At)
		if !ok || at.Before(from) || at.After(now) {
			continue
		}
		result.Polls.Total++
		if result.Polls.LastAt == "" || at.After(mustTime(result.Polls.LastAt)) {
			result.Polls.LastAt = poll.At
		}
		if !poll.OK {
			result.Polls.Failed++
			result.Polls.LastError = poll.Error
			continue
		}
		result.Polls.OK++
		for _, code := range poll.Missing {
			missing[code] = true
		}
		entry, exists := publications[poll.RatesDate]
		if !exists {
			entry = publicationData{first: at, last: at, rates: map[string]float64{}}
		}
		if at.Before(entry.first) {
			entry.first = at
		}
		if !at.Before(entry.last) {
			entry.last = at
			entry.rates = cloneRates(poll.Rates)
		}
		publications[poll.RatesDate] = entry
	}
	dates := make([]string, 0, len(publications))
	for date := range publications {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	for _, date := range dates {
		result.Publications = append(result.Publications, Publication{RatesDate: date, FirstSeenAt: publications[date].first.UTC().Format(time.RFC3339)})
	}
	for _, code := range current.Codes {
		var values []RatePoint
		for _, date := range dates {
			if value, ok := publications[date].rates[code]; ok {
				values = append(values, RatePoint{RatesDate: date, UnitRate: value})
			}
		}
		if len(values) == 0 {
			continue
		}
		minimum, maximum, total := values[0].UnitRate, values[0].UnitRate, 0.0
		for _, value := range values {
			if value.UnitRate < minimum {
				minimum = value.UnitRate
			}
			if value.UnitRate > maximum {
				maximum = value.UnitRate
			}
			total += value.UnitRate
		}
		change := cbr.Round6(values[len(values)-1].UnitRate - values[0].UnitRate)
		pct := 0.0
		if values[0].UnitRate != 0 {
			pct = cbr.Round4(change / values[0].UnitRate * 100)
		}
		result.Currencies = append(result.Currencies, CurrencySummary{Code: code, First: values[0], Last: values[len(values)-1], Min: minimum, Max: maximum, Avg: cbr.Round6(total / float64(len(values))), Change: change, ChangePct: pct})
	}
	for code := range missing {
		result.MissingCodes = append(result.MissingCodes, code)
	}
	sort.Strings(result.MissingCodes)
	if result.Polls.OK == 0 {
		result.Note = "в окне нет успешных опросов"
	} else if len(dates) == 1 {
		result.Note = "за окно ЦБ опубликовал один курс"
	}
	return result
}

func mustTime(value string) time.Time { parsed, _ := time.Parse(time.RFC3339, value); return parsed }
func cloneRates(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
