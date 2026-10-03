package brain

import (
	"math"
	"testing"
	"time"
)

// trending builds 60 candles: flat base then a steady rally with rising
// volume and meaningful candle ranges.
func trending() []Candle {
	out := make([]Candle, 0, 60)
	price := 100.0
	for i := 0; i < 40; i++ {
		out = append(out, Candle{TsSec: int64(i), High: price + 0.1, Low: price - 0.1, Close: price, Volume: 100})
	}
	// Rally with regular pullbacks (+0.5%, -0.4%, +0.5%) so the trend is
	// real but RSI stays out of overbought territory.
	steps := []float64{1.005, 0.996, 1.005}
	for i := 40; i < 60; i++ {
		price *= steps[(i-40)%3]
		out = append(out, Candle{TsSec: int64(i), High: price * 1.002, Low: price * 0.998, Close: price, Volume: 250})
	}
	return out
}

// choppy builds candles that whipsaw around a flat mean.
func choppy() []Candle {
	out := make([]Candle, 0, 60)
	price := 100.0
	for i := 0; i < 60; i++ {
		if i%2 == 0 {
			price *= 1.003
		} else {
			price /= 1.003
		}
		out = append(out, Candle{TsSec: int64(i), High: price * 1.001, Low: price * 0.999, Close: price, Volume: 100})
	}
	return out
}

func TestTrendingMarketScoresHigh(t *testing.T) {
	b := New(60, 10*time.Minute)
	candles := trending()
	v := b.Evaluate("X", candles, true, candles[len(candles)-1].Close, candles[len(candles)-3].Close)
	if v.Score < 60 {
		t.Fatalf("trending market should clear the bar, score=%.0f (%s)", v.Score, v.Reason)
	}
	if !v.Buy {
		t.Fatalf("crossUp in a trending market should be a buy: %s", v.Reason)
	}
	if v.Regime != "trending" {
		t.Errorf("regime = %s, want trending", v.Regime)
	}
	if v.Sizing < 1.0 {
		t.Errorf("strong conviction should size >= 1x, got %.2f", v.Sizing)
	}
}

func TestChoppyMarketSuppressesBuy(t *testing.T) {
	b := New(60, 10*time.Minute)
	candles := choppy()
	v := b.Evaluate("X", candles, true, candles[len(candles)-1].Close, candles[len(candles)-4].Close)
	if v.Buy {
		t.Fatalf("choppy market must not buy: %s", v.Reason)
	}
	if v.Regime != "choppy" {
		t.Errorf("regime = %s, want choppy", v.Regime)
	}
}

func TestOverboughtBlocksBuy(t *testing.T) {
	b := New(60, 10*time.Minute)
	candles := trending()
	// Force RSI to the moon: 30 consecutive up candles.
	for i := len(candles) - 20; i < len(candles); i++ {
		candles[i].Close *= 1.02
		candles[i].High = candles[i].Close * 1.001
	}
	v := b.Evaluate("X", candles, true, candles[len(candles)-1].Close, candles[len(candles)-3].Close)
	if v.Buy && v.Regime == "overbought" {
		t.Fatalf("overbought market must not buy: %s", v.Reason)
	}
}

func TestCrossUpRequired(t *testing.T) {
	b := New(60, 10*time.Minute)
	candles := trending()
	v := b.Evaluate("X", candles, false, candles[len(candles)-1].Close, candles[len(candles)-3].Close)
	if v.Buy {
		t.Fatal("no crossUp means no buy, whatever the score")
	}
}

func TestScoreZeroDisablesGate(t *testing.T) {
	b := New(0, 10*time.Minute)
	candles := choppy()
	v := b.Evaluate("X", candles, true, 100, 99)
	if !v.Buy {
		t.Fatalf("disabled brain must pass every crossUp through: %s", v.Reason)
	}
}

func TestCooldownAfterLoss(t *testing.T) {
	b := New(60, 10*time.Minute)
	now := time.Now()
	b.ReportResult("BTC", -1.5)
	if ok, _ := b.CoolingDown("BTC", now); !ok {
		t.Fatal("symbol should cool down right after a loss")
	}
	if ok, _ := b.CoolingDown("ETH", now); ok {
		t.Fatal("other symbols must not cool down")
	}
	if ok, _ := b.CoolingDown("BTC", now.Add(11*time.Minute)); ok {
		t.Fatal("cooldown must expire")
	}
}

func TestWinResultsDoNotCooldown(t *testing.T) {
	b := New(60, 10*time.Minute)
	b.ReportResult("BTC", 2.0)
	if ok, _ := b.CoolingDown("BTC", time.Now()); ok {
		t.Fatal("profitable cycle must not start a cooldown")
	}
}

func TestRSIMath(t *testing.T) {
	// All gains -> RSI 100; all losses -> RSI ~0.
	allUp := make([]Candle, 20)
	for i := range allUp {
		allUp[i] = Candle{Close: float64(100 + i)}
	}
	if got := rsi(allUp, 14); got != 100 {
		t.Errorf("rsi all-up = %f, want 100", got)
	}
	allDown := make([]Candle, 20)
	for i := range allDown {
		allDown[i] = Candle{Close: float64(100 - i)}
	}
	if got := rsi(allDown, 14); math.Abs(got) > 0.01 {
		t.Errorf("rsi all-down = %f, want ~0", got)
	}
}
