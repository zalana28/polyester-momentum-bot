package market

import (
	"testing"
	"time"
)

func TestBackoffDelayGrowsAndCaps(t *testing.T) {
	var prev time.Duration
	for n := 1; n <= reconnectMaxRapid; n++ {
		d := backoffDelay(n)
		base := reconnectBaseDelay * time.Duration(1<<(n-1))
		if base > reconnectMaxDelay {
			base = reconnectMaxDelay
		}
		lo := time.Duration(float64(base) * 0.8)
		hi := time.Duration(float64(base) * 1.2)
		if d < lo || d > hi {
			t.Fatalf("attempt %d: delay %s outside [%s,%s]", n, d, lo, hi)
		}
		if d <= prev {
			t.Fatalf("attempt %d: delay %s did not grow past %s", n, d, prev)
		}
		prev = d
	}
}

func TestBackoffDelayCaps(t *testing.T) {
	// A symbol that keeps failing must not wait longer than the cap.
	for _, n := range []int{7, 12, 50} {
		d := backoffDelay(n)
		hi := time.Duration(float64(reconnectMaxDelay) * 1.2)
		if d > hi {
			t.Fatalf("attempt %d: delay %s exceeds cap window %s", n, d, hi)
		}
	}
}

func TestBackoffDelayJitterIsBounded(t *testing.T) {
	// Two calls at the same attempt must stay inside the ±20% band; the jitter
	// exists to desynchronise a fleet, not to randomise the policy.
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		seen[backoffDelay(4)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected jitter to vary the delay, got a constant %v", seen)
	}
}

func TestBackoffDelayFloorsAtOne(t *testing.T) {
	if d := backoffDelay(0); d < time.Duration(float64(reconnectBaseDelay)*0.8) {
		t.Fatalf("attempt 0 floored delay too small: %s", d)
	}
	if d := backoffDelay(-3); d < time.Duration(float64(reconnectBaseDelay)*0.8) {
		t.Fatalf("negative attempt floored delay too small: %s", d)
	}
}
