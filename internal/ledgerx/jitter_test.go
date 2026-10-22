package ledgerx

import (
	"testing"
	"time"
)

// Retry waits carry ±20% jitter so symbols that failed together do not retry
// together. The bounds are the contract; exact values must stay random.
func TestJitterStaysWithinBounds(t *testing.T) {
	const base = 800 * time.Millisecond
	lo := time.Duration(float64(base) * 0.8)
	hi := time.Duration(float64(base) * 1.2)
	for i := 0; i < 200; i++ {
		got := jitter(base)
		if got < lo || got > hi {
			t.Fatalf("jitter(%s) = %s, want within [%s, %s]", base, got, lo, hi)
		}
	}
}

func TestJitterLeavesFastRetriesAlone(t *testing.T) {
	for _, d := range []time.Duration{0, time.Millisecond, 10 * time.Millisecond} {
		if got := jitter(d); got != d {
			t.Fatalf("jitter(%s) = %s, want unchanged for test-speed waits", d, got)
		}
	}
}
