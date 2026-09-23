package watch

import "time"

func Slot(t time.Time, period time.Duration) int64 {
	if period <= 0 {
		return 0
	}
	return t.Unix() / int64(period/time.Second)
}

func SlotStart(t time.Time, period time.Duration) time.Time {
	seconds := int64(period / time.Second)
	return time.Unix((t.Unix()/seconds)*seconds, 0).UTC()
}

func Due(w Watch, now time.Time) bool {
	if w.Status != "active" {
		return false
	}
	if len(w.Polls) == 0 {
		return true
	}
	last, ok := parseTime(w.Polls[len(w.Polls)-1].At)
	if !ok {
		return true
	}
	period := time.Duration(w.EveryMinutes) * time.Minute
	return Slot(now, period) > Slot(last, period)
}

func DigestNeeded(recorded []time.Time, now time.Time, period time.Duration) bool {
	current := Slot(now, period)
	for _, at := range recorded {
		if Slot(at, period) == current {
			return false
		}
	}
	return true
}
