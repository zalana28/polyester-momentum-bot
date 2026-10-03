package strategy

import "fmt"

// PullbackStrategy buys dips to a rising EMA21 inside an established uptrend
// ("buy the dip, not the top"), and exits when the trend slope flips.
type PullbackStrategy struct {
	ema      []float64 // EMA21 series over closes
	slopeUp  bool
	coolDown int
}

// NewPullbackStrategy builds the pullback member.
func NewPullbackStrategy() *PullbackStrategy { return &PullbackStrategy{} }

func (p *PullbackStrategy) Name() string { return "pullback" }

func (p *PullbackStrategy) SuitedFor(r Regime) float64 {
	switch r {
	case RegimeTrendUp:
		return 1.0
	case RegimeRange:
		return 0.4
	default:
		return 0.2
	}
}

func (p *PullbackStrategy) OnCandle(c Candle) Vote {
	p.ema = append(p.ema, c.Close)
	if len(p.ema) > 200 {
		p.ema = p.ema[len(p.ema)-200:]
	}
	// EMA21 state.
	var ema21 float64
	if len(p.ema) >= 21 {
		// Recompute over the tail for stability after window trims.
		ema21 = emaAt(p.ema, 21)
	} else {
		return Vote{Strategy: p.Name()}
	}
	slope := ema21 - emaAt(p.ema[:len(p.ema)-1], 21)
	p.slopeUp = slope > 0

	if p.coolDown > 0 {
		p.coolDown--
		return Vote{Strategy: p.Name()}
	}

	// Uptrend + pullback: price touched near EMA21 but closed back above it
	// with a green candle.
	dist := (c.Close - ema21) / ema21
	if p.slopeUp && dist >= 0 && dist <= 0.02 && c.Close > c.Low {
		// price is hugging the EMA from above
		if c.High >= ema21 && c.Close >= ema21 {
			p.coolDown = 5
			return Vote{Strategy: p.Name(), Kind: "buy", Confidence: clamp01(0.5 + slope/ema21*80),
				Reason: fmt.Sprintf("pullback to rising EMA21 %.6g (dist %.3f%%)", ema21, dist*100)}
		}
	}
	if !p.slopeUp && len(p.ema) > 25 {
		// Trend flip after holding a position.
		if emaAt(p.ema[:len(p.ema)-1], 21) > emaAt(p.ema[:len(p.ema)-6], 21) {
			return Vote{Strategy: p.Name(), Kind: "exit", Confidence: 0.7,
				Reason: fmt.Sprintf("EMA21 slope flipped down (%.6g)", ema21)}
		}
	}
	return Vote{Strategy: p.Name()}
}

// emaAt computes the EMA(period) value at the END of the given series
// (SMA-seeded).
func emaAt(series []float64, period int) float64 {
	if len(series) < period || period <= 0 {
		return 0
	}
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += series[i]
	}
	ema := sum / float64(period)
	k := 2.0 / float64(period+1)
	for i := period; i < len(series); i++ {
		ema = k*series[i] + (1-k)*ema
	}
	return ema
}
