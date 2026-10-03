package strategy

import "fmt"

// MeanReversionStrategy buys oversold stretches below the lower Bollinger
// band and exits when price reverts to the band middle. Best in ranging
// markets; dangerous in strong trends (guarded by ensemble regime weight).
type MeanReversionStrategy struct {
	period   int
	stdMult  float64
	closes   []Candle
	coolDown int // candles to wait after an exit before re-entering
}

// NewMeanReversionStrategy builds the mean-reversion member (Bollinger 20,2).
func NewMeanReversionStrategy() *MeanReversionStrategy {
	return &MeanReversionStrategy{period: 20, stdMult: 2.0}
}

func (m *MeanReversionStrategy) Name() string { return "meanrev" }

// SuitedFor: mean reversion is the ruler of ranges.
func (m *MeanReversionStrategy) SuitedFor(r Regime) float64 {
	switch r {
	case RegimeRange:
		return 1.0
	case RegimeChoppy:
		return 0.7
	case RegimeTrendDown:
		return 0.3 // catching falling knives is guarded
	default:
		return 0.4
	}
}

// bands computes SMA + stddev over the last `period` closes.
func (m *MeanReversionStrategy) bands() (mid, lower, upper float64, ok bool) {
	n := len(m.closes)
	if n < m.period {
		return 0, 0, 0, false
	}
	sum := 0.0
	for i := n - m.period; i < n; i++ {
		sum += m.closes[i].Close
	}
	mid = sum / float64(m.period)
	variance := 0.0
	for i := n - m.period; i < n; i++ {
		d := m.closes[i].Close - mid
		variance += d * d
	}
	sd := sqrt(variance / float64(m.period))
	return mid, mid - m.stdMult*sd, mid + m.stdMult*sd, true
}

func (m *MeanReversionStrategy) rsi() float64 {
	period := 14
	n := len(m.closes)
	if n < period+1 {
		return 50
	}
	gain, loss := 0.0, 0.0
	for i := n - period; i < n; i++ {
		d := m.closes[i].Close - m.closes[i-1].Close
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

func (m *MeanReversionStrategy) OnCandle(c Candle) Vote {
	m.closes = append(m.closes, c)
	if len(m.closes) > 200 {
		m.closes = m.closes[len(m.closes)-200:]
	}
	if m.coolDown > 0 {
		m.coolDown--
		return Vote{Strategy: m.Name()}
	}
	mid, lower, _, ok := m.bands()
	if !ok {
		return Vote{Strategy: m.Name()}
	}
	rsi := m.rsi()

	// Buy: price closed below the lower band AND RSI oversold-ish.
	if c.Close < lower && rsi < 35 {
		depth := (lower - c.Close) / lower // how far below the band
		return Vote{Strategy: m.Name(), Kind: "buy",
			Confidence: clamp01(0.5 + depth*10 + (35-rsi)/50),
			Reason:     fmt.Sprintf("close %.6g < lowerBB %.6g, RSI %.0f", c.Close, lower, rsi)}
	}
	// Exit: reverted to the middle band (RSI recovered past neutral).
	if rsi > 45 && c.Close >= mid {
		return Vote{Strategy: m.Name(), Kind: "exit", Confidence: 0.7,
			Reason: fmt.Sprintf("reverted to mid BB %.6g, RSI %.0f", mid, rsi)}
	}
	return Vote{Strategy: m.Name()}
}

// NoteExitDelay makes the strategy require a few candles after an exit before
// the next entry, avoiding instant re-entry loops.
func (m *MeanReversionStrategy) NoteExit() { m.coolDown = 3 }

func sqrt(f float64) float64 {
	if f <= 0 {
		return 0
	}
	x := f
	for i := 0; i < 40; i++ { // Newton-Raphson, dependency-free
		x = (x + f/x) / 2
	}
	return x
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
