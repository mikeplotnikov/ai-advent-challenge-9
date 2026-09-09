package main

import "testing"

// The window ladder's rungs are sizes in tokens, and the blob for a rung is built in
// memory before anything is sent. An extra zero in the flag is therefore not a bad
// measurement but an out-of-memory, so the sizes are bounded on the way in.
func TestLadderSizesAreBounded(t *testing.T) {
	if _, err := parseSizes("50000,1500000"); err != nil {
		t.Fatalf("нормальная лестница отвергнута: %v", err)
	}
	for _, bad := range []string{
		"50000,15000000",             // лишний ноль
		"50000,3000001",              // на единицу выше потолка
		"50000,-1",                   // отрицательная ступень
		"50000",                      // без ступени за пределом
		"50000,много",                // не число
		"50000,99999999999999999999", // не влезает в int64
	} {
		if _, err := parseSizes(bad); err == nil {
			t.Errorf("лестница %q принята", bad)
		}
	}
	if _, err := parseSizes("50000,3000000"); err != nil {
		t.Errorf("ступень ровно в потолок отвергнута: %v", err)
	}
}
