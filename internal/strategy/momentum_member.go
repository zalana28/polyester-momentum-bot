package strategy

// MomentumStrategy wraps the classic EMA cross engine as an ensemble member.
// Buys on fast-EMA crossing above slow-EMA with ROC + volume confirmation;
// exits on the opposite cross.
type MomentumStrategy struct {
	eng *Engine
}

// NewMomentumStrategy builds the momentum member from engine params.
func NewMomentumStrategy(p Params) *MomentumStrategy {
	return &MomentumStrategy{eng: NewEngine(p)}
}

func (m *MomentumStrategy) Name() string { return "momentum" }

// SuitedFor: momentum thrives in trends, dies in chop.
func (m *MomentumStrategy) SuitedFor(r Regime) float64 {
	switch r {
	case RegimeTrendUp:
		return 1.0
	case RegimeTrendDown:
		return 0.5 // exit-only value
	case RegimeRange:
		return 0.3
	default:
		return 0.1
	}
}

func (m *MomentumStrategy) OnCandle(c Candle) Vote {
	sig := m.eng.OnClosedCandle(Candle{TsSec: c.TsSec, Close: c.Close, Volume: c.Volume})
	if sig == nil {
		return Vote{Strategy: m.Name()}
	}
	switch sig.Kind {
	case SignalBuy:
		conf := clamp01(0.4 + sig.VolRatio/4 + sig.ROC*40)
		return Vote{Strategy: m.Name(), Kind: "buy", Confidence: conf, Reason: sig.Reason}
	case SignalExit:
		return Vote{Strategy: m.Name(), Kind: "exit", Confidence: 0.8, Reason: sig.Reason}
	}
	return Vote{Strategy: m.Name()}
}
