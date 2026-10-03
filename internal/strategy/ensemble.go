package strategy

import (
	"fmt"
	"sync"
)

// Ensemble runs several strategies on the same candle stream, weighs each
// vote by (a) how suited the strategy is to the current regime and (b) an
// ADAPTIVE weight updated from realized trade results — the "learn from
// mistakes" layer: a strategy that loses has its voice reduced.
type Ensemble struct {
	mu        sync.Mutex
	members   []Strategy
	weights   map[string]float64 // adaptive, start 1.0, clamped [0.2, 2.0]
	threshold float64            // weighted buy-score needed to emit a buy
	buyBar    float64
}

// NewEnsemble builds the ensemble with equal initial weights.
func NewEnsemble(threshold float64, members ...Strategy) *Ensemble {
	e := &Ensemble{members: members, weights: map[string]float64{}, threshold: threshold}
	for _, m := range members {
		e.weights[m.Name()] = 1.0
	}
	return e
}

// OnCandle feeds the candle to every member and combines their votes.
// Returns the combined buy vote (nil when not buying) and whether any member
// wants an exit.
func (e *Ensemble) OnCandle(c Candle, regime Regime) (buy *Vote, exit bool, votes []Vote) {
	e.mu.Lock()
	defer e.mu.Unlock()
	buyScore := 0.0
	var best *Vote
	for _, m := range e.members {
		v := m.OnCandle(c)
		votes = append(votes, v)
		w := e.weights[v.Strategy] * m.SuitedFor(regime)
		switch v.Kind {
		case "buy":
			score := w * v.Confidence
			buyScore += score
			if best == nil || score > buyScore {
				v2 := v
				best = &v2
			}
		case "exit":
			exit = true
		}
	}
	if buyScore >= e.threshold && best != nil {
		best.Confidence = clamp01(buyScore) // combined conviction
		buy = best
	}
	return buy, exit, votes
}

// Report updates the adaptive weight of one strategy from a realized trade
// result. pnlNorm is the PnL normalized to roughly [-1, +1] (pnl divided by
// the expected TP profit).
func (e *Ensemble) Report(strategy string, pnlNorm float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	w := e.weights[strategy]
	if w == 0 {
		w = 1.0
	}
	w += 0.25 * pnlNorm
	if w < 0.2 {
		w = 0.2
	}
	if w > 2.0 {
		w = 2.0
	}
	e.weights[strategy] = w
}

// Weights returns a copy of the current adaptive weights.
func (e *Ensemble) Weights() map[string]float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]float64{}
	for k, v := range e.weights {
		out[k] = v
	}
	return out
}

// Explain formats the combined decision for logs.
func Explain(votes []Vote, regime Regime) string {
	out := fmt.Sprintf("regime=%s", regime)
	for _, v := range votes {
		if v.Kind != "" {
			out += fmt.Sprintf(" | %s:%s conf=%.2f", v.Strategy, v.Kind, v.Confidence)
		}
	}
	return out
}
