// Package brain is the bot's decision layer on top of raw signals. A signal
// (EMA cross) is only a TRIGGER; the brain judges whether the market context
// actually supports taking the trade:
//
//   - multi-factor score (trend strength, ROC, RSI, volume, breakout, chop)
//   - regime detection: choppy markets suppress new entries
//   - conviction sizing: higher score -> larger budget multiplier
//   - adaptive cooldown: a symbol that just lost money stops buying for a while
//
// Everything is deterministic and testable — no network, no clock dependence
// beyond cooldowns.
package brain

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// Candle is one closed candle (true-price floats).
type Candle struct {
	TsSec  int64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

// Verdict is the brain's judgment of the current market for one symbol.
type Verdict struct {
	Score  float64 // 0..100, higher = stronger support for a buy
	Buy    bool    // score cleared the bar in a trending regime
	Regime string  // "trending" | "choppy"
	Reason string  // human-readable breakdown
	Sizing float64 // budget multiplier 0.5..1.5 derived from conviction
}

// perSymbol tracks adaptive state for one symbol.
type perSymbol struct {
	lastLossAt  time.Time
	lastResults []float64 // last few cycle PnLs
}

// Brain holds configuration and adaptive per-symbol state.
type Brain struct {
	MinScore       float64       // buy bar; 0 disables the brain (gate always open)
	Cooldown       time.Duration // pause for a symbol after a losing cycle
	ChopFlips      int           // EMA flips within lookback that mark a choppy market
	ChopLookback   int
	MaxHistory     int
	WinStreakBoost float64 // sizing bonus per consecutive win, capped
	overboughtRSI  float64
	underboughtRSI float64

	mu    sync.Mutex
	state map[string]*perSymbol
}

// New builds a brain with sensible defaults for the knobs left at zero.
func New(minScore float64, cooldown time.Duration) *Brain {
	b := &Brain{
		MinScore:       minScore,
		Cooldown:       cooldown,
		ChopFlips:      2,
		ChopLookback:   12,
		MaxHistory:     200,
		WinStreakBoost: 0.1,
		overboughtRSI:  76,
		underboughtRSI: 24,
	}
	if b.Cooldown <= 0 {
		b.Cooldown = 10 * time.Minute
	}
	b.state = map[string]*perSymbol{}
	return b
}

// Enabled reports whether the gate is active.
func (b *Brain) Enabled() bool { return b.MinScore > 0 }

// ReportResult feeds a realized cycle PnL back into the adaptive layer.
// A losing cycle starts the cooldown clock for that symbol.
func (b *Brain) ReportResult(symbol string, pnl float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.state[symbol]
	if st == nil {
		st = &perSymbol{}
		b.state[symbol] = st
	}
	st.lastResults = append(st.lastResults, pnl)
	if len(st.lastResults) > 5 {
		st.lastResults = st.lastResults[len(st.lastResults)-5:]
	}
	if pnl < 0 {
		st.lastLossAt = time.Now()
	}
}

// CoolingDown reports whether the symbol is still in its post-loss cooldown.
func (b *Brain) CoolingDown(symbol string, now time.Time) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.state[symbol]
	if st == nil || st.lastLossAt.IsZero() {
		return false, 0
	}
	elapsed := now.Sub(st.lastLossAt)
	if elapsed < b.Cooldown {
		return true, b.Cooldown - elapsed
	}
	return false, 0
}

// Evaluate scores the market from the closed-candle window. crossUp is the
// raw trigger (fast EMA crossed above slow on the latest closed candle);
// emaFast/emaSlow are the current EMA values.
func (b *Brain) Evaluate(symbol string, candles []Candle, crossUp bool, emaFast, emaSlow float64) Verdict {
	if !b.Enabled() {
		// Gate disabled: pure pass-through of the raw signal.
		return Verdict{Buy: crossUp, Sizing: 1.0, Regime: "passthrough", Reason: "brain disabled"}
	}
	_, _ = emaFast, emaSlow // reserved for future trend filters
	v := Verdict{Regime: "trending"}
	if len(candles) < 30 {
		v.Reason = "not enough history"
		return v
	}

	// --- factor: trend strength (price drift over 12 candles vs ATR) ---
	_, atrPct := atrPercent(candles, 14)
	pc12 := priceChange(candles, 12)
	drift := 0.0
	if atrPct > 0 {
		drift = pc12 / (3 * atrPct)
	}
	trendScore := clamp(drift, 0, 1) * 25 // 3 ATR of upward drift = full marks

	// --- factor: RSI position ---
	rsi := rsi(candles, 14)
	rsiScore := 0.0
	switch {
	case rsi >= 50 && rsi <= 70:
		rsiScore = 15
	case rsi > 40 && rsi < 80:
		rsiScore = 8
	}
	overbought := rsi >= b.overboughtRSI

	// --- factor: volume confirmation ---
	volRatio := volumeRatio(candles, 20)
	volScore := clamp((volRatio-0.8)/1.2, 0, 1) * 15 // 2.0x = full marks

	// --- factor: breakout position in recent range ---
	brk := breakoutScore(candles, 20) // 0..1, 1 = closing at 20-candle high
	brkScore := brk * 15

	// --- factor: momentum (ROC over 5 candles) ---
	roc := roc(candles, 5)
	rocScore := clamp(roc/0.01, 0, 1) * 20 // 1% ROC = full marks

	// --- regime: choppiness (EMA flips) + dead volatility ---
	flips := emaFlips(candles, 12)
	choppy := flips >= b.ChopFlips || atrPct < 0.0005
	chopPenalty := 0.0
	if flips >= b.ChopFlips {
		chopPenalty = 20
	}
	if atrPct < 0.0005 {
		chopPenalty += 10
	}

	v.Score = clamp(trendScore+rsiScore+volScore+brkScore+rocScore-chopPenalty, 0, 100)

	if choppy {
		v.Regime = "choppy"
	}
	if overbought {
		v.Regime = "overbought"
	}

	// Conviction sizing: 0.5x at the bar, up to 1.5x at a perfect score.
	v.Sizing = clamp(0.5+v.Score/100, 0.5, 1.5)

	v.Buy = crossUp && !choppy && !overbought && v.Score >= b.MinScore

	v.Reason = fmt.Sprintf("score=%.0f/100 (drift %.0f@%.2f%%, rsi %.0f@%.0f, vol %.0f@%.2fx, breakout %.0f@%.2f, roc %.0f@%.3f%%, chop -%.0f@%d flips, atr %.2f%%) regime=%s",
		v.Score, trendScore, pc12*100, rsiScore, rsi, volScore, volRatio, brkScore, brk, rocScore, roc*100, chopPenalty, flips, atrPct*100, v.Regime)
	return v
}

// --- pure math helpers ---------------------------------------------------

// emaSeries computes EMA values for the whole series (SMA-seeded).
func emaSeries(candles []Candle, period int) []float64 {
	if len(candles) < period || period <= 0 {
		return nil
	}
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += candles[i].Close
	}
	ema := sum / float64(period)
	out := make([]float64, 0, len(candles))
	for i := 0; i < period-1; i++ {
		out = append(out, math.NaN())
	}
	out = append(out, ema)
	k := 2.0 / float64(period+1)
	for i := period; i < len(candles); i++ {
		ema = k*candles[i].Close + (1-k)*ema
		out = append(out, ema)
	}
	return out
}

// emaFlips counts fast/slow EMA relation flips in the last lookback candles.
func emaFlips(candles []Candle, lookback int) int {
	fast := emaSeries(candles, 9)
	slow := emaSeries(candles, 21)
	if fast == nil || slow == nil {
		return 99 // insufficient history: treat as maximally choppy
	}
	n := len(candles)
	start := n - lookback
	if start < 22 {
		start = 22
	}
	flips := 0
	prevAbove := fast[start-1] > slow[start-1]
	for i := start; i < n; i++ {
		if math.IsNaN(fast[i]) || math.IsNaN(slow[i]) {
			continue
		}
		above := fast[i] > slow[i]
		if above != prevAbove {
			flips++
			prevAbove = above
		}
	}
	return flips
}

// rsi computes Wilder's RSI over the last `period` candles.
func rsi(candles []Candle, period int) float64 {
	if len(candles) < period+1 {
		return 50
	}
	gain, loss := 0.0, 0.0
	for i := len(candles) - period; i < len(candles); i++ {
		d := candles[i].Close - candles[i-1].Close
		if d >= 0 {
			gain += d
		} else {
			loss -= d
		}
	}
	if loss == 0 {
		return 100
	}
	rs := (gain / float64(period)) / (loss / float64(period))
	return 100 - 100/(1+rs)
}

// atrPercent computes ATR(period) as a fraction of the latest close.
func atrPercent(candles []Candle, period int) (atr, pct float64) {
	if len(candles) < period+1 {
		return 0, 0
	}
	sum := 0.0
	for i := len(candles) - period; i < len(candles); i++ {
		tr := math.Max(candles[i].High-candles[i].Low,
			math.Max(math.Abs(candles[i].High-candles[i-1].Close),
				math.Abs(candles[i].Low-candles[i-1].Close)))
		sum += tr
	}
	atr = sum / float64(period)
	close := candles[len(candles)-1].Close
	if close <= 0 {
		return atr, 0
	}
	return atr, atr / close
}

// volumeRatio is the latest volume vs the mean of the previous `period`.
func volumeRatio(candles []Candle, period int) float64 {
	n := len(candles)
	if n <= period {
		return 0
	}
	sum := 0.0
	for i := n - 1 - period; i < n-1; i++ {
		sum += candles[i].Volume
	}
	mean := sum / float64(period)
	if mean <= 0 {
		return 0
	}
	return candles[n-1].Volume / mean
}

// breakoutScore maps the latest close's position within the last `period`
// candles' high-low range to 0..1 (1 = at the top = breakout).
func breakoutScore(candles []Candle, period int) float64 {
	n := len(candles)
	if n < period+1 {
		return 0
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for i := n - period; i < n; i++ {
		lo = math.Min(lo, candles[i].Low)
		hi = math.Max(hi, candles[i].High)
	}
	if hi-lo <= 0 {
		return 0
	}
	return (candles[n-1].Close - lo) / (hi - lo)
}

// priceChange is close[t]/close[t-period]-1 (signed drift).
func priceChange(candles []Candle, period int) float64 {
	n := len(candles)
	if n <= period {
		return 0
	}
	base := candles[n-1-period].Close
	if base <= 0 {
		return 0
	}
	return candles[n-1].Close/base - 1
}

// roc is close[t]/close[t-period]-1.
func roc(candles []Candle, period int) float64 {
	n := len(candles)
	if n <= period {
		return 0
	}
	base := candles[n-1-period].Close
	if base <= 0 {
		return 0
	}
	return candles[n-1].Close/base - 1
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
