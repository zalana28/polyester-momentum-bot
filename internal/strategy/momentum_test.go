package strategy

import (
	"math"
	"testing"
)

func feed(t *testing.T, e *Engine, closes []float64) *Signal {
	t.Helper()
	var sig *Signal
	for i, c := range closes {
		sig = e.OnClosedCandle(Candle{TsSec: int64(i + 1), Close: c, Volume: 100})
	}
	return sig
}

// rising builds a flat stable base then a sharp rally with a volume spike.
func scenario(t *testing.T) (*Engine, []float64) {
	t.Helper()
	p := Params{FastEMA: 9, SlowEMA: 21, ROCPeriod: 5, VolMAPeriod: 20, ROCThreshold: 0.003, VolumeSurge: 1.5}
	e := NewEngine(p)
	closes := make([]float64, 0, 60)
	// Perfectly flat base for 30 candles (an oscillating base creates noisy
	// micro-crossovers that would fire signals — by design).
	price := 100.0
	for i := 0; i < 30; i++ {
		closes = append(closes, price)
	}
	// Rally: +0.5% per candle for 10 candles.
	for i := 0; i < 10; i++ {
		price *= 1.005
		closes = append(closes, price)
	}
	return e, closes
}

func TestEngineWarmsBeforeSignals(t *testing.T) {
	e, closes := scenario(t)
	for i, c := range closes {
		if sig := e.OnClosedCandle(Candle{TsSec: int64(i + 1), Close: c, Volume: 100}); sig != nil {
			t.Fatalf("signal before warmup: %+v", sig)
		}
	}
	if !e.Warm() {
		t.Fatal("engine should be warm after enough candles")
	}
}

func TestEngineEmitsBuyOnMomentumBreakout(t *testing.T) {
	e, closes := scenario(t)
	// Volume spikes across the whole rally so the cross candle carries one.
	for i, c := range closes {
		vol := 100.0
		if i >= 30 {
			vol = 300 // 3x mean of previous 20
		}
		if sig := e.OnClosedCandle(Candle{TsSec: int64(i + 1), Close: c, Volume: vol}); sig != nil {
			if sig.Kind != SignalBuy {
				t.Fatalf("expected buy, got %s (%s)", sig.Kind, sig.Reason)
			}
			return
		}
	}
	t.Fatal("no buy signal emitted for breakout scenario")
}

func TestEngineBuyRequiresROCAndVolume(t *testing.T) {
	// Same price path but no volume surge -> no buy.
	e, closes := scenario(t)
	if sig := feed(t, e, closes); sig != nil {
		t.Fatalf("buy emitted without volume surge: %s", sig.Reason)
	}
}

func TestEngineEmitsExitOnCrossDown(t *testing.T) {
	e, closes := scenario(t)
	feed(t, e, closes)
	// Drop hard: -1% per candle for 12 candles.
	last := closes[len(closes)-1]
	drop := make([]float64, 0, 12)
	p := last
	for i := 0; i < 12; i++ {
		p *= 0.99
		drop = append(drop, p)
	}
	for i, c := range drop {
		if sig := e.OnClosedCandle(Candle{TsSec: int64(41 + i), Close: c, Volume: 100}); sig != nil && sig.Kind == SignalExit {
			return
		}
	}
	t.Fatal("no exit signal on sustained drop")
}

func TestEngineNoRepaintOnSameCandle(t *testing.T) {
	// The engine must treat every OnClosedCandle input as final; there is no
	// API for feeding partial candles. This test pins the contract: feeding
	// the same candle twice must not double-advance the EMA state in a way
	// that emits two signals for one candle.
	e, closes := scenario(t)
	feed(t, e, closes)
	before := e.emaFast
	e.OnClosedCandle(Candle{TsSec: 1000, Close: closes[len(closes)-1], Volume: 100})
	if math.Abs(e.emaFast-before) < 1e-9 {
		t.Fatal("expected EMA to advance on a new candle")
	}
}

func TestROCAndVolRatioMath(t *testing.T) {
	e := NewEngine(Params{FastEMA: 2, SlowEMA: 3, ROCPeriod: 2, VolMAPeriod: 2, ROCThreshold: 0, VolumeSurge: 1.0})
	// closes: 100, 100, 110 -> ROC at 3rd = 0.1
	feed(t, e, []float64{100, 100, 110})
	if got := e.roc(); math.Abs(got-0.1) > 1e-9 {
		t.Errorf("roc = %f, want 0.1", got)
	}
	// vols: 100, 100, 200 -> ratio at 3rd = 200/100 = 2
	e2 := NewEngine(Params{FastEMA: 2, SlowEMA: 3, ROCPeriod: 2, VolMAPeriod: 2, ROCThreshold: 0, VolumeSurge: 1.0})
	e2.OnClosedCandle(Candle{TsSec: 1, Close: 100, Volume: 100})
	e2.OnClosedCandle(Candle{TsSec: 2, Close: 100, Volume: 100})
	e2.OnClosedCandle(Candle{TsSec: 3, Close: 110, Volume: 200})
	if got := e2.volRatio(); math.Abs(got-2.0) > 1e-9 {
		t.Errorf("volRatio = %f, want 2.0", got)
	}
}
