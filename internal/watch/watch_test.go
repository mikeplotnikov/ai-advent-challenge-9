package watch

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
)

func instant(value string) time.Time { parsed, _ := time.Parse(time.RFC3339, value); return parsed }

func TestSlotRules(t *testing.T) {
	now := instant("2026-09-24T11:00:00Z")
	watch := Watch{Status: "active", EveryMinutes: 60, Polls: []Poll{{At: "2026-09-24T10:59:00Z"}}}
	if !Due(watch, now) {
		t.Fatal("10:59 poll must be due at 11:00")
	}
	if Due(watch, instant("2026-09-24T10:59:59Z")) {
		t.Fatal("same slot must not be due")
	}
	if !Due(Watch{Status: "active", EveryMinutes: 60}, now) {
		t.Fatal("never polled must be due")
	}
	watch.Status = "stopped"
	if Due(watch, now) {
		t.Fatal("stopped watch must not be due")
	}
	recorded := []time.Time{instant("2026-09-24T01:00:00Z")}
	if DigestNeeded(recorded, instant("2026-09-24T02:59:00Z"), 3*time.Hour) {
		t.Fatal("same digest slot")
	}
	if !DigestNeeded(recorded, instant("2026-09-24T03:00:00Z"), 3*time.Hour) {
		t.Fatal("next digest slot")
	}
}

func TestAggregateUsesPublicationsNotPollFrequency(t *testing.T) {
	w := Watch{ID: "w1", Codes: []string{"USD", "EUR"}, EveryMinutes: 60, Status: "active", Polls: []Poll{
		{At: "2026-09-24T01:00:00Z", OK: true, RatesDate: "2026-09-24", Rates: map[string]float64{"USD": 80, "EUR": 90}},
		{At: "2026-09-24T02:00:00Z", OK: true, RatesDate: "2026-09-24", Rates: map[string]float64{"USD": 80.2, "EUR": 90}, Missing: []string{"CNY"}},
		{At: "2026-09-24T03:00:00Z", OK: true, RatesDate: "2026-09-25", Rates: map[string]float64{"USD": 81}},
		{At: "2026-09-24T04:00:00Z", OK: true, RatesDate: "2026-09-25", Rates: map[string]float64{"USD": 81}},
		{At: "2026-09-24T05:00:00Z", OK: false, Error: "boom"},
		{At: "2026-09-24T06:00:00Z", OK: true, RatesDate: "2026-09-26", Rates: map[string]float64{"USD": 80.5}},
		{At: "2026-09-20T06:00:00Z", OK: true, RatesDate: "2026-09-20", Rates: map[string]float64{"USD": 70}},
	}}
	s := Aggregate(w, 24, instant("2026-09-24T07:00:00Z"))
	if s.Polls.Total != 6 || s.Polls.OK != 5 || s.Polls.Failed != 1 || len(s.Publications) != 3 {
		t.Fatalf("counts: %+v publications=%d", s.Polls, len(s.Publications))
	}
	usd := s.Currencies[0]
	if usd.First.UnitRate != 80.2 || usd.Last.UnitRate != 80.5 || usd.Min != 80.2 || usd.Max != 81 || usd.Avg != 80.566667 || usd.Change != 0.3 || usd.ChangePct != 0.3741 {
		t.Fatalf("USD aggregate: %+v", usd)
	}
	if len(s.MissingCodes) != 1 || s.MissingCodes[0] != "CNY" {
		t.Fatalf("missing: %v", s.MissingCodes)
	}

	failed := Watch{ID: "w2", Codes: []string{"USD"}, Status: "active", Polls: []Poll{{At: "2026-09-24T06:00:00Z", OK: false, Error: "x"}}}
	zero := Aggregate(failed, 24, instant("2026-09-24T07:00:00Z"))
	if len(zero.Currencies) != 0 || zero.Note != "в окне нет успешных опросов" {
		t.Fatalf("zero: %+v", zero)
	}
	one := Aggregate(Watch{ID: "w3", Codes: []string{"USD"}, Status: "active", Polls: []Poll{{At: "2026-09-24T06:00:00Z", OK: true, RatesDate: "2026-09-25", Rates: map[string]float64{"USD": 80}}}}, 24, instant("2026-09-24T07:00:00Z"))
	if one.Currencies[0].Change != 0 || one.Note != "за окно ЦБ опубликовал один курс" {
		t.Fatalf("one: %+v", one)
	}
}

func TestAggregateAcceptanceExample(t *testing.T) {
	w := Watch{ID: "w1", Codes: []string{"USD"}, Status: "active"}
	values := []float64{80, 81, 80.5}
	dates := []string{"2026-09-24", "2026-09-25", "2026-09-26"}
	for i, value := range values {
		w.Polls = append(w.Polls, Poll{At: instant("2026-09-24T01:00:00Z").Add(time.Duration(i) * time.Hour).Format(time.RFC3339), OK: true, RatesDate: dates[i], Rates: map[string]float64{"USD": value}})
	}
	s := Aggregate(w, 24, instant("2026-09-24T07:00:00Z"))
	u := s.Currencies[0]
	if u.First.UnitRate != 80 || u.Last.UnitRate != 80.5 || u.Min != 80 || u.Max != 81 || u.Avg != 80.5 || u.Change != .5 || u.ChangePct != .625 {
		t.Fatalf("%+v", u)
	}
}

func TestAggregateIncludesBothWindowBoundaries(t *testing.T) {
	now := instant("2026-09-24T07:00:00Z")
	from := now.Add(-24 * time.Hour)
	w := Watch{ID: "w1", Codes: []string{"USD"}, Status: "active", Polls: []Poll{
		{At: from.Format(time.RFC3339), OK: true, RatesDate: "2026-09-23", Rates: map[string]float64{"USD": 80}},
		{At: now.Format(time.RFC3339), OK: false, Error: "boundary failure"},
	}}
	summary := Aggregate(w, 24, now)
	if summary.Polls.Total != 2 || summary.Polls.OK != 1 || summary.Polls.Failed != 1 || len(summary.Publications) != 1 || summary.Publications[0].FirstSeenAt != from.Format(time.RFC3339) {
		t.Fatalf("boundary polls excluded: %+v", summary)
	}
}

func TestStoreMissingCorruptVersionCapsAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "store.json")
	store := NewStore(path)
	state, err := store.Read()
	if err != nil || state.Version != 1 || state.NextID != 1 || len(state.Watches) != 0 {
		t.Fatalf("empty: %+v %v", state, err)
	}
	for _, raw := range [][]byte{[]byte("not json"), []byte(`{"version":2,"next_id":1,"watches":[]}`)} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		before := sha256.Sum256(raw)
		if _, err := store.Read(); err == nil || !strings.Contains(err.Error(), "не прочитано") || !strings.Contains(err.Error(), "файл не изменён") {
			t.Fatalf("error=%v", err)
		}
		afterRaw, _ := os.ReadFile(path)
		after := sha256.Sum256(afterRaw)
		if before != after {
			t.Fatal("corrupt file changed")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	polls := make([]Poll, MaxPollsPerWatch+2)
	for i := range polls {
		polls[i] = Poll{At: time.Unix(int64(i), 0).UTC().Format(time.RFC3339)}
	}
	if err := store.Update(func(state *File) error {
		state.Watches = []Watch{{ID: "w1", Status: "active", Polls: polls}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err = store.Read()
	if err != nil || len(state.Watches[0].Polls) != MaxPollsPerWatch || state.Watches[0].Polls[0].At != polls[2].At {
		t.Fatalf("cap: %d %v", len(state.Watches[0].Polls), err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temp remains: %s", entry.Name())
		}
	}
}

func TestStoreLockTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	store := NewStore(path)
	store.LockTimeout = 30 * time.Millisecond
	if _, err := store.Read(); err == nil || !strings.Contains(err.Error(), "хранилище занято") {
		t.Fatalf("%v", err)
	}
}

func TestAppendPollRechecksSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	store := NewStore(path)
	at := instant("2026-09-24T10:00:00Z")
	poll := Poll{At: at.Format(time.RFC3339), OK: true, RatesDate: "2026-09-24", Rates: map[string]float64{"USD": 80}}
	created, err := store.Create([]string{"USD"}, 60, at, poll)
	if err != nil {
		t.Fatal(err)
	}
	appended, _, err := store.AppendPollIfDue(created.ID, at.Add(30*time.Minute), Poll{At: at.Add(30 * time.Minute).Format(time.RFC3339)})
	if err != nil || appended {
		t.Fatalf("same slot appended=%v err=%v", appended, err)
	}
	appended, pub, err := store.AppendPollIfDue(created.ID, at.Add(time.Hour), Poll{At: at.Add(time.Hour).Format(time.RFC3339), OK: true, RatesDate: "2026-09-25", Rates: map[string]float64{"USD": 81}})
	if err != nil || !appended || !pub {
		t.Fatalf("next slot appended=%v pub=%v err=%v", appended, pub, err)
	}
}

func TestGuestExpiryPurgeAndLegacyStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	store := NewStore(path)
	createdAt := instant("2026-09-24T10:00:00Z")
	poll := Poll{At: createdAt.Format(time.RFC3339), OK: true, RatesDate: "2026-09-24", Rates: map[string]float64{"USD": 80}}
	guest, existing, err := store.CreateGuest([]string{"USD"}, 60, createdAt, poll)
	if err != nil || existing || !guest.Guest || guest.ExpiresAt != createdAt.Add(24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("guest=%+v existing=%v err=%v", guest, existing, err)
	}
	repeated, existing, err := store.CreateGuest([]string{"USD"}, 60, createdAt.Add(time.Minute), poll)
	if err != nil || !existing || repeated.ID != guest.ID {
		t.Fatalf("repeated=%+v existing=%v err=%v", repeated, existing, err)
	}
	nonGuest, err := store.Create([]string{"EUR"}, 60, createdAt, poll)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stop(nonGuest.ID, createdAt); err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	afterExpiry := createdAt.Add(24*time.Hour + time.Second)
	scheduler := &Scheduler{
		Store: store,
		Now:   func() time.Time { return afterExpiry },
		CBR: cbr.Options{Fetcher: cbr.FetchFunc(func(context.Context, string) ([]byte, error) {
			fetches.Add(1)
			return nil, nil
		})},
	}
	if count, err := scheduler.RunOnce(context.Background()); err != nil || count != 0 || fetches.Load() != 0 {
		t.Fatalf("count=%d fetches=%d err=%v", count, fetches.Load(), err)
	}
	state, err := store.Read()
	if err != nil || len(state.Watches) != 2 || state.Watches[0].Status != "stopped" || state.Watches[0].StoppedAt != guest.ExpiresAt || len(state.Watches[0].Polls) != 1 {
		t.Fatalf("expired state=%+v err=%v", state, err)
	}
	if err := store.ExpireAndPurgeGuests(afterExpiry.Add(24*time.Hour + time.Second)); err != nil {
		t.Fatal(err)
	}
	state, err = store.Read()
	if err != nil || len(state.Watches) != 1 || state.Watches[0].ID != nonGuest.ID {
		t.Fatalf("purged state=%+v err=%v", state, err)
	}
	lateStore := NewStore(filepath.Join(dir, "late.json"))
	if _, _, err := lateStore.CreateGuest([]string{"USD"}, 60, createdAt, poll); err != nil {
		t.Fatal(err)
	}
	lateNow := createdAt.Add(49 * time.Hour)
	lateScheduler := &Scheduler{Store: lateStore, Now: func() time.Time { return lateNow }}
	if count, err := lateScheduler.RunOnce(context.Background()); err != nil || count != 0 {
		t.Fatalf("late scheduler count=%d err=%v", count, err)
	}
	lateState, err := lateStore.Read()
	if err != nil || len(lateState.Watches) != 0 {
		t.Fatalf("long-expired guest survived one pass: %+v err=%v", lateState, err)
	}

	legacyPath := filepath.Join(dir, "legacy.json")
	legacy := []byte(`{"version":1,"next_id":2,"watches":[{"id":"w1","codes":["USD"],"every_minutes":60,"status":"active","created_at":"2026-09-24T10:00:00Z","stopped_at":"","polls":[]}]}`)
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	legacyStore := NewStore(legacyPath)
	legacyState, err := legacyStore.Read()
	if err != nil || len(legacyState.Watches) != 1 || legacyState.Watches[0].Guest || legacyState.Watches[0].ExpiresAt != "" {
		t.Fatalf("legacy=%+v err=%v", legacyState, err)
	}
	if _, err := legacyStore.Stop("w1", createdAt); err != nil {
		t.Fatalf("legacy store no longer works: %v", err)
	}
}
