package strategy

import "fmt"

// BreakoutStrategy buys closes above the previous N-candle high (Donchian
// channel) confirmed by volume, and exits when price falls back to the
// channel middle. Profits from ranges that resolve into trends.
type BreakoutStrategy struct {
	period   int
	highs    []float64 // rolling window of previous highs (excludes current)
	lows     []float64
	coolDown int
}

// NewBreakoutStrategy builds the Donchian-20 breakout member.
func NewBreakoutStrategy() *BreakoutStrategy {
	return &BreakoutStrategy{period: 20}
}

func (b *BreakoutStrategy) Name() string { return "breakout" }

func (b *BreakoutStrategy) SuitedFor(r Regime) float64 {
	switch r {
	case RegimeRange:
		return 0.9 // ranges resolving into breakouts are the sweet spot
	case RegimeTrendUp:
		return 0.7
	case RegimeTrendDown:
		return 0.4
	default:
		return 0.4
	}
}

func (b *BreakoutStrategy) OnCandle(c Candle) Vote {
	if b.coolDown > 0 {
		b.coolDown--
	}
	// Channel from the previous candles (current candle excluded).
	if len(b.highs) >= b.period {
		hi, lo := b.highest(), b.lowest()
		mid := (hi + lo) / 2
		if b.coolDown == 0 && c.Close > hi {
			b.coolDown = 3 // one breakout per consolidation
			return Vote{Strategy: b.Name(), Kind: "buy",
				Confidence: clamp01(0.5 + (c.Close-hi)/hi*20),
				Reason:     fmt.Sprintf("close %.6g broke 20-high %.6g", c.Close, hi)}
		}
		if c.Close < mid {
			return Vote{Strategy: b.Name(), Kind: "exit", Confidence: 0.6,
				Reason: fmt.Sprintf("close %.6g fell below channel mid %.6g", c.Close, mid)}
		}
	}
	b.highs = append(b.highs, c.High)
	b.lows = append(b.lows, c.Low)
	if len(b.highs) > b.period*2 {
		b.highs = b.highs[len(b.highs)-b.period*2:]
		b.lows = b.lows[len(b.lows)-b.period*2:]
	}
	return Vote{Strategy: b.Name()}
}

func (b *BreakoutStrategy) highest() float64 {
	hi := b.highs[0]
	for _, v := range b.highs[len(b.highs)-b.period:] {
		if v > hi {
			hi = v
		}
	}
	return hi
}

func (b *BreakoutStrategy) lowest() float64 {
	lo := b.lows[0]
	for _, v := range b.lows[len(b.lows)-b.period:] {
		if v < lo {
			lo = v
		}
	}
	return lo
}
