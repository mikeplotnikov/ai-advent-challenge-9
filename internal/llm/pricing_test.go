package llm

import (
	"testing"
	"time"
)

// Every timestamp here is fixed. The first version of this suite priced through
// Cost(), which reads time.Now(), and the same assertion passed in the evening
// and failed at 07:00 UTC on a weekday — the provider bills double there. A test
// whose expected value depends on when it runs is not testing the price.
var (
	offPeak = time.Date(2026, 9, 4, 17, 0, 0, 0, time.UTC) // Friday evening
	onPeak  = time.Date(2026, 9, 4, 7, 0, 0, 0, time.UTC)  // Friday 07:00 UTC
)

func TestPeakWindowsFollowTheQuotedRule(t *testing.T) {
	// "Peak hours are 01:00 - 04:00 and 06:00 - 10:00 UTC, Monday through Friday
	// (all other hours are off-peak)." Endpoints are read half-open; the boundary
	// cases are pinned here so a later reading change is a failing test, not a
	// silent reprice.
	cases := []struct {
		when time.Time
		peak bool
		why  string
	}{
		{time.Date(2026, 9, 4, 0, 59, 0, 0, time.UTC), false, "перед первым окном"},
		{time.Date(2026, 9, 4, 1, 0, 0, 0, time.UTC), true, "начало первого окна включено"},
		{time.Date(2026, 9, 4, 3, 59, 0, 0, time.UTC), true, "конец первого окна"},
		{time.Date(2026, 9, 4, 4, 0, 0, 0, time.UTC), false, "04:00 читается как off-peak"},
		{time.Date(2026, 9, 4, 5, 30, 0, 0, time.UTC), false, "пауза между окнами"},
		{time.Date(2026, 9, 4, 6, 0, 0, 0, time.UTC), true, "начало второго окна"},
		{time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC), true, "предпоследний час второго окна"},
		{time.Date(2026, 9, 4, 9, 59, 0, 0, time.UTC), true, "последняя минута второго окна"},
		{time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC), false, "10:00 читается как off-peak"},
		{time.Date(2026, 9, 5, 7, 0, 0, 0, time.UTC), false, "суббота — выходной, окон нет"},
		{time.Date(2026, 9, 6, 2, 0, 0, 0, time.UTC), false, "воскресенье — тоже"},
		{time.Date(2026, 9, 7, 2, 0, 0, 0, time.UTC), true, "понедельник снова будний"},
	}
	for _, c := range cases {
		if got := IsPeak(c.when); got != c.peak {
			t.Errorf("%s: IsPeak(%s) = %v, ожидалось %v", c.why, c.when.Format(time.RFC3339), got, c.peak)
		}
	}
}

func TestLocalTimeZoneDoesNotMoveThePeakWindow(t *testing.T) {
	// 07:00 UTC is peak. The same instant written in Moscow time is 10:00 +03:00,
	// and reading its Hour() without converting would call it off-peak.
	msk := time.FixedZone("MSK", 3*60*60)
	if !IsPeak(onPeak.In(msk)) {
		t.Error("тот же момент в московском времени перестал быть peak")
	}
}

func TestCacheHitsAreBilledAtTheCacheRate(t *testing.T) {
	// 1M cached input + 1M uncached input + 1M output on flash, off-peak:
	// 0.007 + 0.22 + 0.66.
	u := Usage{
		PromptTokens:          2_000_000,
		PromptCacheHitTokens:  1_000_000,
		PromptCacheMissTokens: 1_000_000,
		CompletionTokens:      1_000_000,
	}
	got, known := CostAt("deepseek-v4-flash", u, offPeak)
	want := 0.007 + 0.22 + 0.66
	if !known || got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("цена = %.6f (known=%v), ожидалось %.6f", got, known, want)
	}
}

func TestUnreportedCacheSplitIsBilledAtTheExpensiveRate(t *testing.T) {
	// A provider that does not return the split, or a response the fields never
	// reached, must not price input as cache hits: that would read as a discount
	// nobody was given. Same 2M input, no split reported.
	u := Usage{PromptTokens: 2_000_000, CompletionTokens: 0}
	got, _ := CostAt("deepseek-v4-flash", u, offPeak)
	want := 2 * 0.22
	if got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("без разбивки цена = %.6f, ожидалось по miss-ставке %.6f", got, want)
	}
	// A split that does not add up to the reported total is equally untrustworthy.
	bad := Usage{PromptTokens: 2_000_000, PromptCacheHitTokens: 1_000_000, CompletionTokens: 0}
	if got, _ := CostAt("deepseek-v4-flash", bad, offPeak); got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("при несходящейся разбивке цена = %.6f, ожидалось %.6f", got, want)
	}
}

func TestPeakDoublesEveryRate(t *testing.T) {
	u := Usage{
		PromptTokens:          2_000_000,
		PromptCacheHitTokens:  1_000_000,
		PromptCacheMissTokens: 1_000_000,
		CompletionTokens:      1_000_000,
	}
	off, _ := CostAt("deepseek-v4-pro", u, offPeak)
	on, _ := CostAt("deepseek-v4-pro", u, onPeak)
	if on < off*2-1e-9 || on > off*2+1e-9 {
		t.Fatalf("peak = %.6f, off-peak = %.6f: отношение %.4f, ожидалось 2", on, off, on/off)
	}
}

func TestProCostsThreeTimesFlashNotTwelve(t *testing.T) {
	// The table this replaced made pro 12.4x flash, and that ratio was quoted in
	// the project's own notes. On the published rates it is 3x. The assertion is
	// here so the claim and the table cannot drift apart again.
	u := Usage{PromptTokens: 1_000_000, PromptCacheMissTokens: 1_000_000, CompletionTokens: 1_000_000}
	flash, _ := CostAt("deepseek-v4-flash", u, offPeak)
	pro, _ := CostAt("deepseek-v4-pro", u, offPeak)
	if ratio := pro / flash; ratio < 2.99 || ratio > 3.01 {
		t.Fatalf("pro/flash = %.3f, ожидалось 3.00 (flash %.4f, pro %.4f)", ratio, flash, pro)
	}
}

func TestFreeAndUnknownDoNotLookAlike(t *testing.T) {
	u := Usage{PromptTokens: 1000, CompletionTokens: 1000}

	if _, known := CostAt("deepseek-v9-unreleased", u, offPeak); known {
		t.Error("для неизвестной модели цена объявлена известной")
	}
	if IsFree("deepseek-v9-unreleased") {
		t.Error("неизвестная модель объявлена бесплатной")
	}

	FreeModel("qwen3:test-only")
	t.Cleanup(func() { delete(pricing, "qwen3:test-only") })

	cost, known := CostAt("qwen3:test-only", u, onPeak)
	if !known {
		t.Fatal("для локальной модели цена должна быть известна: она равна нулю")
	}
	if cost != 0 {
		t.Fatalf("локальная модель стоит %.6f, ожидался ноль", cost)
	}
	if !IsFree("qwen3:test-only") {
		t.Error("локальная модель не помечена бесплатной")
	}
}

func TestZeroUsageCostsNothingButIsStillKnown(t *testing.T) {
	// A call that failed before generating anything is $0 — but for a priced
	// model that is a fact, not a missing price.
	got, known := CostAt("deepseek-v4-pro", Usage{}, offPeak)
	if !known || got != 0 {
		t.Fatalf("пустой вызов: цена %.6f, known=%v", got, known)
	}
}

func TestPricedModelsAreNotReportedAsFree(t *testing.T) {
	// IsFree returning ok — true for anything in the table — passes every other test
	// in this file. Both call sites print "$0, модель локальная" on it, so a priced
	// pro call would be published as free and the real spend would vanish from the
	// record.
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"} {
		if IsFree(model) {
			t.Errorf("%s объявлена бесплатной", model)
		}
	}
}

func TestEveryQuotedRateIsChargedSomewhere(t *testing.T) {
	// Each rate is exercised on its own, against the figure quoted from the
	// provider's page. Ratio tests alone let a rate be wrong by a consistent factor
	// in both bands and stay green — and pro's cache-hit rate was reachable only
	// through ratios, so 0.022 could have been 0.22 with the suite passing.
	million := func(hit, miss, out int) Usage {
		return Usage{
			PromptTokens:          hit + miss,
			PromptCacheHitTokens:  hit,
			PromptCacheMissTokens: miss,
			CompletionTokens:      out,
		}
	}
	const m = 1_000_000
	cases := []struct {
		model string
		usage Usage
		when  time.Time
		want  float64
		what  string
	}{
		{"deepseek-v4-flash", million(m, 0, 0), offPeak, 0.007, "flash, вход из кэша, off-peak"},
		{"deepseek-v4-flash", million(m, 0, 0), onPeak, 0.014, "flash, вход из кэша, peak"},
		{"deepseek-v4-flash", million(0, m, 0), offPeak, 0.22, "flash, вход мимо кэша, off-peak"},
		{"deepseek-v4-flash", million(0, m, 0), onPeak, 0.44, "flash, вход мимо кэша, peak"},
		{"deepseek-v4-flash", million(0, 0, m), offPeak, 0.66, "flash, выход, off-peak"},
		{"deepseek-v4-flash", million(0, 0, m), onPeak, 1.32, "flash, выход, peak"},
		{"deepseek-v4-pro", million(m, 0, 0), offPeak, 0.022, "pro, вход из кэша, off-peak"},
		{"deepseek-v4-pro", million(m, 0, 0), onPeak, 0.044, "pro, вход из кэша, peak"},
		{"deepseek-v4-pro", million(0, m, 0), offPeak, 0.66, "pro, вход мимо кэша, off-peak"},
		{"deepseek-v4-pro", million(0, m, 0), onPeak, 1.32, "pro, вход мимо кэша, peak"},
		{"deepseek-v4-pro", million(0, 0, m), offPeak, 1.98, "pro, выход, off-peak"},
		{"deepseek-v4-pro", million(0, 0, m), onPeak, 3.96, "pro, выход, peak"},
	}
	for _, c := range cases {
		got, known := CostAt(c.model, c.usage, c.when)
		if !known || got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("%s: %.6f (known=%v), ожидалось %.6f", c.what, got, known, c.want)
		}
	}
}

func TestUnreportedSplitIsExpensiveInBothBands(t *testing.T) {
	// The fourth combination: no split reported, during peak. Nothing else covered
	// it, so a refactor that picked the fallback rate before the band — the natural
	// shape once this branch grows — would hand out a half-price discount exactly
	// when the rates are highest.
	u := Usage{PromptTokens: 1_000_000}
	off, _ := CostAt("deepseek-v4-flash", u, offPeak)
	on, _ := CostAt("deepseek-v4-flash", u, onPeak)
	if off < 0.22-1e-9 || off > 0.22+1e-9 {
		t.Errorf("без разбивки off-peak: %.6f, ожидалась ставка miss 0.22", off)
	}
	if on < 0.44-1e-9 || on > 0.44+1e-9 {
		t.Errorf("без разбивки peak: %.6f, ожидалась ставка miss 0.44", on)
	}
}

func TestMalformedUsageNeverBecomesADiscount(t *testing.T) {
	// Shapes no provider has returned to us, and the direction of the mistake is the
	// point: an input token whose classification is broken must cost the expensive
	// rate, never nothing.
	cases := []struct {
		name  string
		usage Usage
		want  float64
	}{
		{
			// The split arrived, the total did not. Resetting to PromptTokens would
			// price 285 input tokens at zero.
			name:  "разбивка без общего числа",
			usage: Usage{PromptCacheHitTokens: 256, PromptCacheMissTokens: 29},
			want:  285.0 / 1e6 * 0.22,
		},
		{
			name:  "разбивка не сходится с общим числом",
			usage: Usage{PromptTokens: 100, PromptCacheHitTokens: 40},
			want:  100.0 / 1e6 * 0.22,
		},
		{
			name:  "отрицательный вход",
			usage: Usage{PromptTokens: -50, CompletionTokens: 10},
			want:  10.0 / 1e6 * 0.66,
		},
		{
			name:  "отрицательный выход",
			usage: Usage{PromptTokens: 10, PromptCacheMissTokens: 10, CompletionTokens: -5},
			want:  10.0 / 1e6 * 0.22,
		},
	}
	for _, c := range cases {
		got, known := CostAt("deepseek-v4-flash", c.usage, offPeak)
		if !known || got < c.want-1e-12 || got > c.want+1e-12 {
			t.Errorf("%s: %.9f (known=%v), ожидалось %.9f", c.name, got, known, c.want)
		}
	}
}
