package bot

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"polyester-momentum-bot/internal/config"
)

// The daily-loss guard can only run on a fresh balance read. These cases pin
// the fail-closed boundary: unknown or stale equity must never count as fresh.
func TestEquityFreshBoundaries(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		at    time.Time
		fresh bool
	}{
		{"never read", time.Time{}, false},
		{"just read", now, true},
		{"two minutes old", now.Add(-2 * time.Minute), true},
		{"exactly at limit", now.Add(-equityMaxAge), true},
		{"past limit", now.Add(-equityMaxAge - time.Nanosecond), false},
		{"ten minutes old", now.Add(-10 * time.Minute), false},
	}
	for _, tc := range cases {
		b := &Bot{cfg: &config.Config{Mode: config.ModeLive}, equityAt: tc.at}
		if got := b.equityFresh(now); got != tc.fresh {
			t.Fatalf("%s: equityFresh=%v want %v", tc.name, got, tc.fresh)
		}
	}
}

func TestLogReconFailThrottlesPerSymbol(t *testing.T) {
	var buf bytes.Buffer
	b := &Bot{log: log.New(&buf, "", 0)}

	b.logReconFail("BTC-USDT", errString("boom"))
	b.logReconFail("BTC-USDT", errString("boom"))
	b.logReconFail("SOL-USDT", errString("boom"))

	if got := strings.Count(buf.String(), "BTC-USDT"); got != 1 {
		t.Fatalf("expected 1 BTC-USDT line, got %d: %q", got, buf.String())
	}
	if got := strings.Count(buf.String(), "SOL-USDT"); got != 1 {
		t.Fatalf("expected 1 SOL-USDT line, got %d: %q", got, buf.String())
	}
}

type errString string

func (e errString) Error() string { return string(e) }
