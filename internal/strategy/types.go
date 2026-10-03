package strategy

// Regime classifies the current market condition from price behavior.
type Regime string

const (
	RegimeTrendUp   Regime = "trend-up"
	RegimeTrendDown Regime = "trend-down"
	RegimeRange     Regime = "range"
	RegimeChoppy    Regime = "choppy"
)

// Vote is one strategy's opinion on the latest closed candle.
type Vote struct {
	Strategy   string  `json:"strategy"`
	Kind       string  `json:"kind"`       // "buy", "exit", or "" (none)
	Confidence float64 `json:"confidence"` // 0..1
	Reason     string  `json:"reason"`
}

// Strategy is one trading logic operating on a closed-candle stream.
// Implementations must be self-contained (own indicator state) and
// deterministic.
type Strategy interface {
	Name() string
	// SuitedFor scores how appropriate this strategy is for the given regime
	// (0..1). The ensemble multiplies votes by this weight.
	SuitedFor(r Regime) float64
	// OnCandle consumes one closed candle and returns this strategy's vote.
	OnCandle(c Candle) Vote
}

// ClassifyRegime derives the market regime from the candle window using
// Kaufman's Efficiency Ratio plus EMA drift direction.
//
//	ER = |close[t] - close[t-n]| / sum(|close[i]-close[i-1]|)
//
// High ER with positive drift = trend-up; high ER negative = trend-down;
// low ER = choppy (random walk); middle = range.
func ClassifyRegime(candles []Candle, fast, slow int) Regime {
	_, _ = fast, slow // reserved: drift could use EMA-based direction
	n := len(candles)
	look := 10
	if n < slow+look+1 {
		return RegimeChoppy // not enough history: assume worst
	}
	erWindow := candles[n-look:]
	net := mathAbs(erWindow[len(erWindow)-1].Close - erWindow[0].Close)
	path := 0.0
	for i := 1; i < len(erWindow); i++ {
		path += mathAbs(erWindow[i].Close - erWindow[i-1].Close)
	}
	if path <= 0 {
		return RegimeRange
	}
	er := net / path

	drift := candles[n-1].Close - candles[n-1-look].Close

	switch {
	case er >= 0.30 && drift > 0:
		return RegimeTrendUp
	case er >= 0.30 && drift < 0:
		return RegimeTrendDown
	case er < 0.15:
		return RegimeChoppy
	default:
		return RegimeRange
	}
}

func mathAbs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
