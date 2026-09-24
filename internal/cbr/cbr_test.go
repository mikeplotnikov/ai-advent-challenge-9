package cbr

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTheFetcherDoesNotSendGosDefaultUserAgent(t *testing.T) {
	// cbr.ru refuses "Go-http-client/1.1" with 403 (live run, 2026-09-22). The fake
	// behaves the same way, so dropping the header makes this test fail, not pass.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != UserAgent {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	body, err := HTTPFetcher{URL: server.URL}.Fetch(context.Background(), "2026-09-01")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("тело = %q", body)
	}
}

func TestParseKeepsCBRValuteID(t *testing.T) {
	rates, err := parse([]byte(`<?xml version="1.0"?><ValCurs Date="24.09.2026"><Valute ID="R01235"><CharCode>USD</CharCode><Nominal>1</Nominal><Name>Dollar</Name><Value>84,3969</Value></Valute></ValCurs>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := rates.Currencies[0].ID; got != "R01235" {
		t.Fatalf("ID=%q", got)
	}
}

func TestGetRangeValidatesAndParsesWithoutFloatingPoint(t *testing.T) {
	daily := []byte(`<?xml version="1.0"?><ValCurs Date="24.09.2026"><Valute ID="R01235"><CharCode>USD</CharCode><Nominal>1</Nominal><Name>Dollar</Name><Value>84,3969</Value></Valute></ValCurs>`)
	dynamic := []byte(`<?xml version="1.0"?><ValCurs ID="R01235"><Record Date="11.09.2026" Id="R01235"><Nominal>100</Nominal><Value>12,3456785</Value><VunitRate>0,1234565</VunitRate></Record><Record Date="10.09.2026" Id="R01235"><Nominal>1</Nominal><Value>84,3969</Value><VunitRate>84,3969</VunitRate></Record></ValCurs>`)
	var gotID, gotFrom, gotTo string
	client := New(Options{
		Fetcher: FetchFunc(func(context.Context, string) ([]byte, error) { return daily, nil }),
		RangeFetcher: RangeFetchFunc(func(_ context.Context, id, from, to string) ([]byte, error) {
			gotID, gotFrom, gotTo = id, from, to
			return dynamic, nil
		}),
		Now: func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, Moscow) },
	})
	rates, err := client.GetRange(context.Background(), "usd", "2026-09-10", "2026-09-11")
	if err != nil {
		t.Fatal(err)
	}
	if gotID != "R01235" || gotFrom != "2026-09-10" || gotTo != "2026-09-11" {
		t.Fatalf("request=%s %s %s", gotID, gotFrom, gotTo)
	}
	if len(rates.Rows) != 2 || rates.Rows[0].Date != "2026-09-10" || rates.Rows[1].ValueMicros != 12_345_679 || rates.Rows[1].UnitRateMicros != 123_457 {
		t.Fatalf("rows=%+v", rates.Rows)
	}
}

func TestGetRangeDateRules(t *testing.T) {
	daily := []byte(`<?xml version="1.0"?><ValCurs Date="24.09.2026"><Valute ID="R01235"><CharCode>USD</CharCode><Nominal>1</Nominal><Name>Dollar</Name><Value>84,3969</Value></Valute></ValCurs>`)
	client := New(Options{Fetcher: FetchFunc(func(context.Context, string) ([]byte, error) { return daily, nil }), RangeFetcher: RangeFetchFunc(func(context.Context, string, string, string) ([]byte, error) {
		return []byte(`<ValCurs ID="R01235"></ValCurs>`), nil
	}), Now: func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, Moscow) }})
	for _, tc := range []struct{ from, to, want string }{
		{"2026-09-24", "2026-09-10", "date_from должна"},
		{"2026-01-01", "2026-04-04", "93 дней"},
		{"1997-12-31", "1998-01-01", "деноминации"},
		{"2026-09-25", "2026-09-26", "ещё не устанавливал"},
	} {
		_, err := client.GetRange(context.Background(), "USD", tc.from, tc.to)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s..%s err=%v", tc.from, tc.to, err)
		}
	}
}

func TestHTTPRangeFetcherUsesProjectUserAgentAndRangeQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != UserAgent || r.URL.Query().Get("VAL_NM_RQ") != "R01235" || r.URL.Query().Get("date_req1") != "10/09/2026" || r.URL.Query().Get("date_req2") != "24/09/2026" {
			http.Error(w, "wrong request", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	raw, err := (HTTPRangeFetcher{URL: server.URL}).FetchRange(context.Background(), "R01235", "2026-09-10", "2026-09-24")
	if err != nil || string(raw) != "ok" {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
}

func TestHTTPRangeFetcherRejectsStatusAndOversizedBody(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "broken", http.StatusInternalServerError) }))
		defer server.Close()
		_, err := (HTTPRangeFetcher{URL: server.URL}).FetchRange(context.Background(), "R01235", "2026-09-10", "2026-09-24")
		if err == nil || err.Error() != "ЦБ ответил HTTP 500" {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bytes.Repeat([]byte("x"), maxBody+1)) }))
		defer server.Close()
		_, err := (HTTPRangeFetcher{URL: server.URL}).FetchRange(context.Background(), "R01235", "2026-09-10", "2026-09-24")
		if err == nil || err.Error() != "ответ ЦБ больше 1 МиБ" {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestGetRangeAcceptsExactly93InclusiveDays(t *testing.T) {
	daily := []byte(`<?xml version="1.0"?><ValCurs Date="03.04.2026"><Valute ID="R01235"><CharCode>USD</CharCode><Nominal>1</Nominal><Name>Dollar</Name><Value>84,0000</Value></Valute></ValCurs>`)
	dynamic := []byte(`<?xml version="1.0"?><ValCurs ID="R01235"><Record Date="01.01.2026" Id="R01235"><Nominal>1</Nominal><Value>84,0000</Value><VunitRate>84,0000</VunitRate></Record></ValCurs>`)
	called := false
	client := New(Options{Fetcher: FetchFunc(func(context.Context, string) ([]byte, error) { return daily, nil }), RangeFetcher: RangeFetchFunc(func(context.Context, string, string, string) ([]byte, error) { called = true; return dynamic, nil }), Now: func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, Moscow) }})
	rates, err := client.GetRange(context.Background(), "USD", "2026-01-01", "2026-04-03")
	if err != nil || !called || len(rates.Rows) != 1 {
		t.Fatalf("called=%v rows=%d err=%v", called, len(rates.Rows), err)
	}
}
