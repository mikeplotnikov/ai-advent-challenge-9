// Package watch stores and aggregates scheduled observations of CBR rates.
package watch

import "time"

const (
	Version          = 1
	MaxActiveWatches = 10
	MaxPollsPerWatch = 5000
)

type File struct {
	Version int     `json:"version"`
	NextID  int     `json:"next_id"`
	Watches []Watch `json:"watches"`
}

type Watch struct {
	ID           string   `json:"id"`
	Codes        []string `json:"codes"`
	EveryMinutes int      `json:"every_minutes"`
	Status       string   `json:"status"`
	CreatedAt    string   `json:"created_at"`
	StoppedAt    string   `json:"stopped_at"`
	Polls        []Poll   `json:"polls"`
}

type Poll struct {
	At        string             `json:"at"`
	OK        bool               `json:"ok"`
	RatesDate string             `json:"rates_date"`
	Rates     map[string]float64 `json:"rates"`
	Missing   []string           `json:"missing"`
	Error     string             `json:"error"`
}

type WatchView struct {
	ID           string   `json:"id"`
	Codes        []string `json:"codes"`
	EveryMinutes int      `json:"every_minutes"`
	Status       string   `json:"status"`
	CreatedAt    string   `json:"created_at"`
	StoppedAt    string   `json:"stopped_at"`
	LastPollAt   string   `json:"last_poll_at"`
	NextSlotAt   string   `json:"next_slot_at"`
	PollsTotal   int      `json:"polls_total"`
	PollsFailed  int      `json:"polls_failed"`
	LastError    string   `json:"last_error"`
}

type SummaryResponse struct {
	Now     string    `json:"now"`
	Watches []Summary `json:"watches"`
	Note    string    `json:"note,omitempty"`
}

type Summary struct {
	WatchID      string            `json:"watch_id"`
	Codes        []string          `json:"codes"`
	EveryMinutes int               `json:"every_minutes"`
	Status       string            `json:"status"`
	Hours        int               `json:"hours"`
	From         string            `json:"from"`
	To           string            `json:"to"`
	Polls        PollStats         `json:"polls"`
	Publications []Publication     `json:"publications"`
	Currencies   []CurrencySummary `json:"currencies"`
	MissingCodes []string          `json:"missing_codes"`
	Note         string            `json:"note,omitempty"`
}

type PollStats struct {
	Total     int    `json:"total"`
	OK        int    `json:"ok"`
	Failed    int    `json:"failed"`
	LastAt    string `json:"last_at"`
	LastError string `json:"last_error"`
}

type Publication struct {
	RatesDate   string `json:"rates_date"`
	FirstSeenAt string `json:"first_seen_at"`
}

type RatePoint struct {
	RatesDate string  `json:"rates_date"`
	UnitRate  float64 `json:"unit_rate"`
}

type CurrencySummary struct {
	Code      string    `json:"code"`
	First     RatePoint `json:"first"`
	Last      RatePoint `json:"last"`
	Min       float64   `json:"min"`
	Max       float64   `json:"max"`
	Avg       float64   `json:"avg"`
	Change    float64   `json:"change"`
	ChangePct float64   `json:"change_pct"`
}

func EmptyFile() File { return File{Version: Version, NextID: 1, Watches: []Watch{}} }

func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}
