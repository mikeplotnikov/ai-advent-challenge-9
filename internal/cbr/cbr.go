// Package cbr loads and normalizes the Central Bank's daily currency rates.
package cbr

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultURL        = "https://www.cbr.ru/scripts/XML_daily.asp"
	DefaultDynamicURL = "https://www.cbr.ru/scripts/XML_dynamic.asp"
	maxBody           = 1 << 20
	// UserAgent names this client. cbr.ru answers 403 to Go's default
	// "Go-http-client/1.1" while serving curl, an empty UA and this one with 200 —
	// measured 2026-09-22 by the first live run, which got 403 where curl got 200.
	UserAgent = "ai-advent-day-17/1.0 (+https://github.com/mikeplotnikov/ai-advent-challenge-9)"
)

var Moscow = time.FixedZone("MSK", 3*60*60)

// The date window the tools accept. MinDate is the 1998 denomination: earlier rates are
// in old rubles (30.12.1997 AUD 3912,74 against 01.01.1998 AUD 3,9127, live 2026-09-22).
// MaxDaysAfterToday is 1 because the CBR sets a rate the day before it applies. Both are
// exported so the -dump for the showcase reads them instead of repeating them.
const (
	MinDate           = "1998-01-01"
	MaxDaysAfterToday = 1
)

// StatusError is the failure for a non-200 answer. One constructor, so that the -dump's
// golden case for HTTP 500 is the same text the fetcher really produces.
func StatusError(code int) error { return fmt.Errorf("ЦБ ответил HTTP %d", code) }

// Fetcher makes the network edge replaceable without changing rate semantics.
type Fetcher interface {
	Fetch(context.Context, string) ([]byte, error)
}

type FetchFunc func(context.Context, string) ([]byte, error)

func (f FetchFunc) Fetch(ctx context.Context, date string) ([]byte, error) { return f(ctx, date) }

// RangeFetcher makes the XML_dynamic edge replaceable in deterministic tests.
type RangeFetcher interface {
	FetchRange(context.Context, string, string, string) ([]byte, error)
}

type RangeFetchFunc func(context.Context, string, string, string) ([]byte, error)

func (f RangeFetchFunc) FetchRange(ctx context.Context, id, from, to string) ([]byte, error) {
	return f(ctx, id, from, to)
}

// Options supplies deterministic seams for callers which need reproducible rates.
type Options struct {
	Fetcher      Fetcher
	RangeFetcher RangeFetcher
	Now          func() time.Time
}

// Currency is one CBR currency as published in XML_daily.
type Currency struct {
	ID       string
	Code     string
	Name     string
	Nominal  int
	Value    float64
	UnitRate float64
}

// RangeRate is one XML_dynamic publication. Monetary values are integer
// millionths so every later calculation can use deterministic integer arithmetic.
type RangeRate struct {
	Date           string
	Nominal        int64
	ValueMicros    int64
	UnitRateMicros int64
}

// RangeRates is one currency's official publications for an inclusive period.
type RangeRates struct {
	Currency Currency
	DateFrom string
	DateTo   string
	Source   string
	Rows     []RangeRate
}

// Rates is a normalized CBR response.
type Rates struct {
	RequestedDate string
	RatesDate     string
	Note          string
	Currencies    []Currency
}

// SourceError marks transport and malformed-upstream failures for tool handlers.
type SourceError struct{ Err error }

func (e *SourceError) Error() string { return e.Err.Error() }
func (e *SourceError) Unwrap() error { return e.Err }

type Client struct {
	fetcher      Fetcher
	rangeFetcher RangeFetcher
	now          func() time.Time
	mu           sync.Mutex
	cache        map[string]cacheEntry
}

type cacheEntry struct {
	rates   Rates
	expires time.Time
}

func New(options Options) *Client {
	fetcher := options.Fetcher
	if fetcher == nil {
		fetcher = HTTPFetcher{URL: os.Getenv("CBR_URL"), Timeout: 15 * time.Second}
	}
	rangeFetcher := options.RangeFetcher
	if rangeFetcher == nil {
		rangeFetcher = HTTPRangeFetcher{URL: os.Getenv("CBR_DYNAMIC_URL"), Timeout: 15 * time.Second}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Client{fetcher: fetcher, rangeFetcher: rangeFetcher, now: now, cache: make(map[string]cacheEntry)}
}

// GetRange resolves the CBR internal currency id from XML_daily, then loads the
// inclusive XML_dynamic period. Day 19 deliberately caps the period at 93 days.
func (c *Client) GetRange(ctx context.Context, code, from, to string) (RangeRates, error) {
	fromDate, err := c.validateDate(from)
	if err != nil {
		return RangeRates{}, err
	}
	toDate, err := c.validateDate(to)
	if err != nil {
		return RangeRates{}, err
	}
	if from == "" || to == "" {
		return RangeRates{}, errors.New("date_from и date_to обязательны")
	}
	if fromDate.After(toDate) {
		return RangeRates{}, errors.New("date_from должна быть не позже date_to")
	}
	if days := int(toDate.Sub(fromDate).Hours()/24) + 1; days > 93 {
		return RangeRates{}, fmt.Errorf("период не должен быть длиннее 93 дней (получено %d)", days)
	}

	daily, err := c.Get(ctx, "")
	if err != nil {
		return RangeRates{}, err
	}
	normalized := strings.ToUpper(strings.TrimSpace(code))
	var currency Currency
	for _, candidate := range daily.Currencies {
		if candidate.Code == normalized {
			currency = candidate
			break
		}
	}
	if currency.ID == "" {
		codes := make([]string, 0, len(daily.Currencies))
		for _, candidate := range daily.Currencies {
			codes = append(codes, candidate.Code)
		}
		sort.Strings(codes)
		return RangeRates{}, fmt.Errorf("неизвестный код валюты %q; доступны: %s", normalized, strings.Join(codes, ", "))
	}
	raw, err := c.rangeFetcher.FetchRange(ctx, currency.ID, from, to)
	if err != nil {
		return RangeRates{}, &SourceError{Err: err}
	}
	rows, err := parseRange(raw, currency.ID)
	if err != nil {
		return RangeRates{}, err
	}
	if len(rows) == 0 {
		return RangeRates{}, fmt.Errorf("ЦБ не публиковал курс %s в периоде %s—%s", normalized, from, to)
	}
	return RangeRates{Currency: currency, DateFrom: from, DateTo: to, Source: "cbr.ru XML_dynamic", Rows: rows}, nil
}

// Get validates the requested calendar date and obtains the applicable published rates.
func (c *Client) Get(ctx context.Context, requested string) (Rates, error) {
	date, err := c.validateDate(requested)
	if err != nil {
		return Rates{}, err
	}
	key := requested
	if cached, ok := c.cached(key); ok {
		return cached, nil
	}

	raw, err := c.fetcher.Fetch(ctx, requested)
	if err != nil {
		return Rates{}, &SourceError{Err: err}
	}
	rates, err := parse(raw)
	if err != nil {
		return Rates{}, err
	}
	rates.RequestedDate = requested
	if requested != "" && rates.RatesDate != date.Format("2006-01-02") {
		rates.Note = fmt.Sprintf("ЦБ не устанавливал курс на %s (выходной или праздник, либо курс ещё не опубликован); действует курс от %s", date.Format("02.01.2006"), parseISODate(rates.RatesDate).Format("02.01.2006"))
	}
	c.store(key, date, rates)
	return rates, nil
}

func (c *Client) validateDate(requested string) (time.Time, error) {
	if requested == "" {
		return time.Time{}, nil
	}
	if len(requested) != len("2006-01-02") {
		return time.Time{}, fmt.Errorf("некорректная дата %q: нужен формат ГГГГ-ММ-ДД", requested)
	}
	date, err := time.ParseInLocation("2006-01-02", requested, Moscow)
	if err != nil || date.Format("2006-01-02") != requested {
		return time.Time{}, fmt.Errorf("некорректная дата %q: нужен формат ГГГГ-ММ-ДД", requested)
	}
	minimum, _ := time.ParseInLocation("2006-01-02", MinDate, Moscow)
	if date.Before(minimum) {
		return time.Time{}, errors.New("курсы до 01.01.1998 даны в рублях до деноминации; поддерживаются даты с 1998-01-01")
	}
	now := c.now().In(Moscow)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, Moscow)
	tomorrow := today.AddDate(0, 0, MaxDaysAfterToday)
	if date.After(tomorrow) {
		return time.Time{}, fmt.Errorf("курс на %s ЦБ ещё не устанавливал: ЦБ устанавливает курс накануне, последний возможный — на завтра, %s", date.Format("02.01.2006"), tomorrow.Format("02.01.2006"))
	}
	return date, nil
}

func (c *Client) cached(key string) (Rates, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[key]
	if !ok || (!entry.expires.IsZero() && !c.now().Before(entry.expires)) {
		return Rates{}, false
	}
	return entry.rates, true
}

func (c *Client) store(key string, requested time.Time, rates Rates) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := cacheEntry{rates: rates}
	if key == "" {
		entry.expires = c.now().Add(10 * time.Minute)
	} else {
		now := c.now().In(Moscow)
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, Moscow)
		if !requested.Before(today) {
			return
		}
	}
	c.cache[key] = entry
}

// HTTPFetcher is the production CBR XML_daily client.
type HTTPFetcher struct {
	URL     string
	Timeout time.Duration
}

// HTTPRangeFetcher is the production CBR XML_dynamic client. It intentionally
// repeats HTTPFetcher's 1 MiB cap and User-Agent because cbr.ru rejects Go's
// default User-Agent on this endpoint too.
type HTTPRangeFetcher struct {
	URL     string
	Timeout time.Duration
}

func (f HTTPRangeFetcher) FetchRange(ctx context.Context, id, from, to string) ([]byte, error) {
	base := f.URL
	if base == "" {
		base = DefaultDynamicURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("некорректный адрес ЦБ: %w", err)
	}
	fromDate, _ := time.Parse("2006-01-02", from)
	toDate, _ := time.Parse("2006-01-02", to)
	q := u.Query()
	q.Set("date_req1", fromDate.Format("02/01/2006"))
	q.Set("date_req2", toDate.Format("02/01/2006"))
	q.Set("VAL_NM_RQ", id)
	u.RawQuery = q.Encode()
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", UserAgent)
	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, StatusError(response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, errors.New("ответ ЦБ больше 1 МиБ")
	}
	return body, nil
}

func (f HTTPFetcher) Fetch(ctx context.Context, requested string) ([]byte, error) {
	base := f.URL
	if base == "" {
		base = DefaultURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("некорректный адрес ЦБ: %w", err)
	}
	if requested != "" {
		date, _ := time.Parse("2006-01-02", requested)
		q := u.Query()
		q.Set("date_req", date.Format("02/01/2006"))
		u.RawQuery = q.Encode()
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", UserAgent)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, StatusError(response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, errors.New("ответ ЦБ больше 1 МиБ")
	}
	return body, nil
}

type xmlDocument struct {
	Date       string        `xml:"Date,attr"`
	Currencies []xmlCurrency `xml:"Valute"`
}

type xmlCurrency struct {
	ID      string `xml:"ID,attr"`
	Code    string `xml:"CharCode"`
	Nominal string `xml:"Nominal"`
	Name    string `xml:"Name"`
	Value   string `xml:"Value"`
}

type xmlRangeDocument struct {
	ID      string           `xml:"ID,attr"`
	Records []xmlRangeRecord `xml:"Record"`
}

type xmlRangeRecord struct {
	Date     string `xml:"Date,attr"`
	ID       string `xml:"Id,attr"`
	Nominal  string `xml:"Nominal"`
	Value    string `xml:"Value"`
	UnitRate string `xml:"VunitRate"`
}

func parse(raw []byte) (Rates, error) {
	decoded := decodeWindows1251(raw)
	if strings.Contains(decoded, "Error in parameters") {
		return Rates{}, errors.New("ЦБ отклонил запрос: Error in parameters")
	}
	if strings.HasPrefix(strings.TrimSpace(decoded), "<?xml") {
		if end := strings.Index(decoded, "?>"); end >= 0 {
			decoded = decoded[end+2:]
		}
	}
	var document xmlDocument
	if err := xml.NewDecoder(bytes.NewReader([]byte(decoded))).Decode(&document); err != nil {
		return Rates{}, &SourceError{Err: fmt.Errorf("не удалось разобрать ответ ЦБ: %w", err)}
	}
	date, err := time.Parse("02.01.2006", document.Date)
	if err != nil {
		return Rates{}, &SourceError{Err: fmt.Errorf("некорректная дата в ответе ЦБ: %q", document.Date)}
	}
	if len(document.Currencies) == 0 {
		return Rates{}, errors.New("ЦБ не вернул курсов на эту дату")
	}
	rates := Rates{RatesDate: date.Format("2006-01-02"), Currencies: make([]Currency, 0, len(document.Currencies))}
	for _, item := range document.Currencies {
		nominal, err := strconv.Atoi(strings.TrimSpace(item.Nominal))
		if err != nil || nominal <= 0 {
			return Rates{}, &SourceError{Err: fmt.Errorf("некорректный номинал %q в ответе ЦБ", item.Nominal)}
		}
		value, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(item.Value), ",", "."), 64)
		if err != nil {
			return Rates{}, &SourceError{Err: fmt.Errorf("некорректный курс %q в ответе ЦБ", item.Value)}
		}
		rates.Currencies = append(rates.Currencies, Currency{ID: strings.TrimSpace(item.ID), Code: strings.ToUpper(strings.TrimSpace(item.Code)), Name: strings.TrimSpace(item.Name), Nominal: nominal, Value: value, UnitRate: Round6(value / float64(nominal))})
	}
	return rates, nil
}

func parseRange(raw []byte, expectedID string) ([]RangeRate, error) {
	decoded := decodeWindows1251(raw)
	if strings.Contains(decoded, "Error in parameters") {
		return nil, errors.New("ЦБ отклонил запрос: Error in parameters")
	}
	if strings.HasPrefix(strings.TrimSpace(decoded), "<?xml") {
		if end := strings.Index(decoded, "?>"); end >= 0 {
			decoded = decoded[end+2:]
		}
	}
	var document xmlRangeDocument
	if err := xml.NewDecoder(bytes.NewReader([]byte(decoded))).Decode(&document); err != nil {
		return nil, &SourceError{Err: fmt.Errorf("не удалось разобрать ответ ЦБ: %w", err)}
	}
	if id := strings.TrimSpace(document.ID); id != expectedID {
		return nil, &SourceError{Err: fmt.Errorf("ЦБ вернул данные другой валюты: %s вместо %s", id, expectedID)}
	}
	rows := make([]RangeRate, 0, len(document.Records))
	for _, item := range document.Records {
		if id := strings.TrimSpace(item.ID); id != expectedID {
			return nil, &SourceError{Err: fmt.Errorf("ЦБ вернул запись другой валюты: %s вместо %s", id, expectedID)}
		}
		date, err := time.Parse("02.01.2006", strings.TrimSpace(item.Date))
		if err != nil {
			return nil, &SourceError{Err: fmt.Errorf("некорректная дата в ответе ЦБ: %q", item.Date)}
		}
		nominal, err := strconv.ParseInt(strings.TrimSpace(item.Nominal), 10, 64)
		if err != nil || nominal <= 0 {
			return nil, &SourceError{Err: fmt.Errorf("некорректный номинал %q в ответе ЦБ", item.Nominal)}
		}
		value, err := decimalMicros(item.Value)
		if err != nil {
			return nil, &SourceError{Err: fmt.Errorf("некорректный курс %q в ответе ЦБ", item.Value)}
		}
		unit, err := decimalMicros(item.UnitRate)
		if err != nil {
			return nil, &SourceError{Err: fmt.Errorf("некорректный курс за единицу %q в ответе ЦБ", item.UnitRate)}
		}
		rows = append(rows, RangeRate{Date: date.Format("2006-01-02"), Nominal: nominal, ValueMicros: value, UnitRateMicros: unit})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Date < rows[j].Date })
	return rows, nil
}

// decimalMicros parses a CBR decimal without binary floating point and rounds
// a seventh decimal digit half away from zero. XML_dynamic currently publishes
// positive rates, but the sign handling keeps the rule explicit and testable.
func decimalMicros(raw string) (int64, error) {
	value := strings.TrimSpace(strings.ReplaceAll(raw, ",", "."))
	sign := int64(1)
	if strings.HasPrefix(value, "-") {
		sign, value = -1, strings.TrimPrefix(value, "-")
	} else {
		value = strings.TrimPrefix(value, "+")
	}
	exponent := int64(0)
	if index := strings.IndexAny(value, "eE"); index >= 0 {
		parsed, err := strconv.ParseInt(value[index+1:], 10, 32)
		if err != nil {
			return 0, errors.New("not a decimal")
		}
		exponent = parsed
		value = value[:index]
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts) == 0 || parts[0] == "" {
		return 0, errors.New("not a decimal")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	digits := parts[0] + fraction
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, errors.New("not a decimal")
		}
	}
	integer, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, errors.New("not a decimal")
	}
	shift := int64(6-len(fraction)) + exponent
	if shift >= 0 {
		for ; shift > 0; shift-- {
			if integer > (1<<63-1)/10 {
				return 0, errors.New("decimal overflow")
			}
			integer *= 10
		}
		return sign * integer, nil
	}
	denominator := int64(1)
	for ; shift < 0; shift++ {
		if denominator > (1<<63-1)/10 {
			return 0, errors.New("decimal overflow")
		}
		denominator *= 10
	}
	if integer > (1<<63-1)-denominator/2 {
		return 0, errors.New("decimal overflow")
	}
	rounded := (integer + denominator/2) / denominator
	return sign * rounded, nil
}

func parseISODate(value string) time.Time {
	date, _ := time.Parse("2006-01-02", value)
	return date
}

func decodeWindows1251(raw []byte) string {
	var out strings.Builder
	out.Grow(len(raw))
	for _, b := range raw {
		switch {
		case b < 0x80:
			out.WriteByte(b)
		case b >= 0xC0 && b <= 0xDF:
			out.WriteRune(rune(0x0410 + int(b) - 0xC0))
		case b >= 0xE0:
			out.WriteRune(rune(0x0430 + int(b) - 0xE0))
		case b == 0xA8:
			out.WriteRune('Ё')
		case b == 0xB8:
			out.WriteRune('ё')
		default:
			out.WriteRune('\uFFFD')
		}
	}
	return out.String()
}

func Round6(value float64) float64 { return math.Round(value*1e6) / 1e6 }
func Round4(value float64) float64 { return math.Round(value*1e4) / 1e4 }
