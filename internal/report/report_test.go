package report

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The report has to read the legacy schema and the v2 schema side by side:
// v2 carries ret_net_bps, v1 only has mark-to-mark returns.
func TestLoadSignalsHandlesBothSchemas(t *testing.T) {
	dir := t.TempDir()
	v1 := write(t, dir, "signals.csv", `ts,symbol,strategy,approved,score,regime,signal_price,horizon,ret_bps,mark_now
2026-10-08T00:43:10Z,POL-USDT,pullback,false,16.7,trend-up,0.10293,1,2.9,0.10296
2026-10-08T00:45:10Z,POL-USDT,pullback,false,16.7,trend-up,0.10293,3,-22.3,0.1027
`)
	v2 := write(t, dir, "signals-v2.csv", `ts,signal_ts,symbol,strategy,approved,score,regime,signal_price,bid_signal,ask_signal,horizon,ret_bps,ret_net_bps,mark_now,bid_now,ask_now
2026-10-22T09:50:00Z,2026-10-22T09:49:00Z,LTC-USDT,pullback,true,47.9,trend-up,59.6,59.58,59.62,3,-5.0,-8.0,59.57,59.57,59.6
`)

	sigs, err := LoadSignals(v1, v2)
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(sigs))
	}
	if sigs[0].Net != sigs[0].Ret || sigs[0].HasNet {
		t.Fatalf("v1 row must fall back to mark-to-mark: %+v", sigs[0])
	}
	if sigs[0].SignalTs != "2026-10-08T00:43:10Z" {
		t.Fatalf("v1 signal ts should fall back to write time, got %q", sigs[0].SignalTs)
	}
	last := sigs[2]
	if !last.HasNet || last.Net != -8.0 || last.SignalTs != "2026-10-22T09:49:00Z" {
		t.Fatalf("v2 row not parsed correctly: %+v", last)
	}
}

func TestLoadSignalsMissingFileIsEmpty(t *testing.T) {
	sigs, err := LoadSignals(filepath.Join(t.TempDir(), "nope.csv"))
	if err != nil {
		t.Fatalf("missing log must not be an error: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("expected no rows, got %d", len(sigs))
	}
}

func TestSummariseStats(t *testing.T) {
	sigs := []Signal{
		{Horizon: 3, Score: 50, Net: 10, Ret: 12},
		{Horizon: 3, Score: 51, Net: -30, Ret: -28},
		{Horizon: 3, Score: 52, Net: 20, Ret: 22},
		{Horizon: 3, Score: 53, Net: 0, Ret: 0},
		{Horizon: 1, Score: 50, Net: 999, Ret: 999}, // other horizon must not leak in
	}
	got := Summarise(sigs, func(s Signal) bool { return s.Horizon == 3 })
	if got.N != 4 {
		t.Fatalf("N=%d want 4", got.N)
	}
	if got.Mean != 0 {
		t.Fatalf("mean=%v want 0", got.Mean)
	}
	if got.Median != 5 {
		t.Fatalf("median=%v want 5", got.Median)
	}
	if got.WinRate != 50 {
		t.Fatalf("win=%.1f want 50", got.WinRate)
	}
}

// A report that says "no edge" must never be produced by a data set that has
// one, and vice versa: the verdict drives whether the threshold gets touched.
func TestVerdict(t *testing.T) {
	// Negative but not monotonically worse: there is no edge, yet the evidence
	// does not say the scorer ranks bad setups highest.
	noEdge := []Bucket{{Stats: Stats{N: 50, Mean: -20}}, {Stats: Stats{N: 50, Mean: -10}}}
	if got := Verdict(noEdge); got == "" || got[:3] != "TID" {
		t.Fatalf("all-negative buckets must read as no edge, got %q", got)
	}

	anti := []Bucket{
		{Stats: Stats{N: 50, Mean: -5}},
		{Stats: Stats{N: 50, Mean: -15}},
		{Stats: Stats{N: 50, Mean: -25}},
	}
	if got := Verdict(anti); got == "" || got[:4] != "ANTI" {
		t.Fatalf("monotonic decline must read as anti-predictive, got %q", got)
	}

	small := []Bucket{{Stats: Stats{N: 3, Mean: -1}}}
	if got := Verdict(small); got == "" || got[:6] != "sampel" {
		t.Fatalf("tiny sample must refuse to conclude, got %q", got)
	}
}

func TestLoadTradesAndSummary(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "trades.csv", `ts_close,symbol,strategy,regime,signal_score,signal_time,hold_secs,entry,qty,exit,gross_usdt,fee_entry_usdt,fee_exit_usdt,net_usdt,bid_at_open,ask_at_open,bid_at_close,ask_at_close,exit_reason
2026-10-22T09:50:00Z,LTC-USDT,pullback,trending,47.9,2026-10-22T09:49:15Z,45,59.649526,40.8,59.58,-2.8,0.4,0.4,-3.6,59.6,59.64,59.58,59.6,shutdown
2026-10-22T09:49:58Z,AVAX-USDT,pullback,trending,47.0,2026-10-22T09:49:18Z,40,9.245,90.3,9.234,-0.9,0.2,0.2,-1.3,9.24,9.245,9.234,9.24,shutdown
`)
	trades, err := LoadTrades(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := SummariseTrades(trades)
	if sum.N != 2 || sum.Wins != 0 || sum.ByReason["shutdown"] != 2 {
		t.Fatalf("unexpected summary: %+v", sum)
	}
	if sum.Net > -4.8 || sum.Net < -5.0 {
		t.Fatalf("net=%.2f want about -4.9", sum.Net)
	}
}
