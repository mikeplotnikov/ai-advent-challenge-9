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
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultURL = "https://www.cbr.ru/scripts/XML_daily.asp"
	maxBody    = 1 << 20
)

var Moscow = time.FixedZone("MSK", 3*60*60)

// Fetcher makes the network edge replaceable without changing rate semantics.
type Fetcher interface {
	Fetch(context.Context, string) ([]byte, error)
}

type FetchFunc func(context.Context, string) ([]byte, error)

func (f FetchFunc) Fetch(ctx context.Context, date string) ([]byte, error) { return f(ctx, date) }

// Options supplies deterministic seams for callers which need reproducible rates.
type Options struct {
	Fetcher Fetcher
	Now     func() time.Time
}

// Currency is one CBR currency as published in XML_daily.
type Currency struct {
	Code     string
	Name     string
	Nominal  int
	Value    float64
	UnitRate float64
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
	fetcher Fetcher
	now     func() time.Time
	mu      sync.Mutex
	cache   map[string]cacheEntry
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
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Client{fetcher: fetcher, now: now, cache: make(map[string]cacheEntry)}
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
	minimum := time.Date(1998, time.January, 1, 0, 0, 0, 0, Moscow)
	if date.Before(minimum) {
		return time.Time{}, errors.New("курсы до 01.01.1998 даны в рублях до деноминации; поддерживаются даты с 1998-01-01")
	}
	now := c.now().In(Moscow)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, Moscow)
	tomorrow := today.AddDate(0, 0, 1)
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
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ЦБ ответил HTTP %d", response.StatusCode)
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
	Code    string `xml:"CharCode"`
	Nominal string `xml:"Nominal"`
	Name    string `xml:"Name"`
	Value   string `xml:"Value"`
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
		rates.Currencies = append(rates.Currencies, Currency{Code: strings.ToUpper(strings.TrimSpace(item.Code)), Name: strings.TrimSpace(item.Name), Nominal: nominal, Value: value, UnitRate: Round6(value / float64(nominal))})
	}
	return rates, nil
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
