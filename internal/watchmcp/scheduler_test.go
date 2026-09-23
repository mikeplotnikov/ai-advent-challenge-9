package watchmcp

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/watch"
)

func TestRunSchedulerRepeatsAfterStartup(t *testing.T) {
	start := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	store := watch.NewStore(filepath.Join(t.TempDir(), "store.json"))
	first := watch.Poll{At: start.Format(time.RFC3339), OK: true, RatesDate: "2026-09-24", Rates: map[string]float64{"USD": 80}}
	if _, err := store.Create([]string{"USD"}, 60, start, first); err != nil {
		t.Fatal(err)
	}
	fixture, err := ratesmcp.Fixtures.ReadFile("fixtures/2026-09-01.xml")
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	var clockMu sync.Mutex
	current := start
	now := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return current }
	scheduler := &watch.Scheduler{Store: store, CBR: cbr.Options{Fetcher: cbr.FetchFunc(func(context.Context, string) ([]byte, error) { fetches.Add(1); return fixture, nil })}, Now: now, Output: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunScheduler(ctx, scheduler, 10*time.Millisecond, io.Discard); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(20 * time.Millisecond)
	if fetches.Load() != 0 {
		t.Fatalf("startup pass fetched in the same slot: %d", fetches.Load())
	}
	clockMu.Lock()
	current = start.Add(time.Hour)
	clockMu.Unlock()
	deadline := time.Now().Add(time.Second)
	var state watch.File
	for time.Now().Before(deadline) {
		state, err = store.Read()
		if err == nil && len(state.Watches[0].Polls) == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if fetches.Load() != 1 {
		t.Fatalf("repeating pass fetches=%d", fetches.Load())
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Watches[0].Polls) != 2 {
		t.Fatalf("polls=%d", len(state.Watches[0].Polls))
	}
}
