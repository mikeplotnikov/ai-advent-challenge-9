package cbr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
