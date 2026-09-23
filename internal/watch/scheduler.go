package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
)

type Scheduler struct {
	Store  *Store
	CBR    cbr.Options
	Now    func() time.Time
	Output io.Writer
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func FetchPoll(ctx context.Context, options cbr.Options, codes []string, at time.Time) (Poll, cbr.Rates, error) {
	client := cbr.New(options)
	rates, err := client.Get(ctx, "")
	if err != nil {
		return Poll{At: at.UTC().Format(time.RFC3339), OK: false, Rates: map[string]float64{}, Missing: []string{}, Error: "ЦБ недоступен: " + err.Error()}, cbr.Rates{}, err
	}
	available := make(map[string]float64, len(rates.Currencies))
	for _, currency := range rates.Currencies {
		available[currency.Code] = currency.UnitRate
	}
	poll := Poll{At: at.UTC().Format(time.RFC3339), OK: true, RatesDate: rates.RatesDate, Rates: map[string]float64{}, Missing: []string{}}
	for _, code := range codes {
		if value, ok := available[code]; ok {
			poll.Rates[code] = value
		} else {
			poll.Missing = append(poll.Missing, code)
		}
	}
	return poll, rates, nil
}

func AvailableCodes(rates cbr.Rates) []string {
	codes := make([]string, 0, len(rates.Currencies))
	for _, currency := range rates.Currencies {
		codes = append(codes, currency.Code)
	}
	sort.Strings(codes)
	return codes
}

func (s *Scheduler) RunOnce(ctx context.Context) (int, error) {
	now := s.now()
	due, err := s.Store.DueWatches(now)
	if err != nil {
		return 0, err
	}
	completed := 0
	for _, current := range due {
		pollAt := s.now()
		poll, _, _ := FetchPoll(ctx, s.CBR, current.Codes, pollAt)
		appended, publication, err := s.Store.AppendPollIfDue(current.ID, pollAt, poll)
		if err != nil {
			return completed, err
		}
		if !appended {
			continue
		}
		completed++
		if s.Output != nil {
			fmt.Fprintln(s.Output, mcpclient.SafeForTerminal(FormatPollLine(current, poll, publication)))
		}
	}
	return completed, nil
}

func FormatPollLine(current Watch, poll Poll, publication bool) string {
	at, _ := time.Parse(time.RFC3339, poll.At)
	prefix := fmt.Sprintf("[планировщик %s МСК] %s %s: ", at.In(cbr.Moscow).Format("02.01 15:04"), current.ID, strings.Join(current.Codes, ","))
	if !poll.OK {
		return prefix + "ошибка — " + poll.Error
	}
	date, _ := time.Parse("2006-01-02", poll.RatesDate)
	text := prefix + "курс на " + date.Format("02.01.2006")
	if publication {
		text += " (новая публикация)"
	}
	for _, code := range current.Codes {
		if value, ok := poll.Rates[code]; ok {
			text += fmt.Sprintf(" · %s %.4f", code, value)
		}
	}
	return text
}

func SourceUnavailable(err error) bool {
	var source *cbr.SourceError
	return errors.As(err, &source)
}
