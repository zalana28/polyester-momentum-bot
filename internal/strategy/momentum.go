// Package strategy implements the momentum signal engine. It is pure: no
// network, no clock, no globals — feed it closed candles and read signals.
// Signals are evaluated ONLY on closed candles so they can never repaint.
package strategy

import (
	"fmt"
	"math"
)

// Candle is one closed candle with decimal-string fields as delivered by the
// API plus parsed float forms for indicator math.
type Candle struct {
	TsSec  int64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

// Params tunes the momentum engine.
type Params struct {
	FastEMA     int
	SlowEMA     int
	ROCPeriod   int
	VolMAPeriod int
	// ROCThreshold is the minimum |ROC| (as a fraction, e.g. 0.003 = 0.3%)
	// required to confirm a crossover.
	ROCThreshold float64
	// VolumeSurge is the required ratio of the current candle volume to the
	// mean volume of the previous VolMAPeriod candles.
	VolumeSurge float64
}

// SignalKind classifies engine output.
type SignalKind string

const (
	SignalBuy  SignalKind = "buy"
	SignalExit SignalKind = "exit"
)

// Signal is one actionable engine output.
type Signal struct {
	Kind     SignalKind
	TsSec    int64
	Close    float64
	Reason   string
	EMAFast  float64
	EMASlow  float64
	ROC      float64
	VolRatio float64
}

// Engine accumulates closed candles and emits momentum signals.
type Engine struct {
	p Params

	closes []float64
	vols   []float64

	emaFast float64
	emaSlow float64
	warmed  bool

	// above is the EMASlow-vs-EMAFast relation of the previous closed candle
	// (true = fast above slow). Cross detection compares against it.
	above bool

	// lastTs deduplicates candles across the REST-seed / stream handoff.
	lastTs int64
}

// NewEngine builds an engine. Warmup needs max(SlowEMA, ROCPeriod+1,
// VolMAPeriod+1) closed candles.
func NewEngine(p Params) *Engine {
	return &Engine{p: p}
}

// WarmupNeed reports how many closed candles the engine needs before signals.
func (e *Engine) WarmupNeed() int {
	n := e.p.SlowEMA
	if e.p.ROCPeriod+1 > n {
		n = e.p.ROCPeriod + 1
	}
	if e.p.VolMAPeriod+1 > n {
		n = e.p.VolMAPeriod + 1
	}
	return n
}

// Warm reports whether the engine has enough history to emit signals.
func (e *Engine) Warm() bool { return e.warmed }

// LastTs returns the timestamp of the last consumed candle (dedupe boundary).
func (e *Engine) LastTs() int64 { return e.lastTs }

// OnClosedCandle feeds one CLOSED candle and returns an optional signal.
// Feeding an in-progress candle is invalid input; the feeds layer guarantees
// closed candles.
func (e *Engine) OnClosedCandle(c Candle) *Signal {
	if c.Close <= 0 || !sane(c.Close) || !sane(c.Volume) {
		return nil
	}
	if c.TsSec != 0 && c.TsSec <= e.lastTs {
		// Duplicate or out-of-order candle (REST seed vs stream handoff).
		return nil
	}
	if c.TsSec != 0 {
		e.lastTs = c.TsSec
	}
	e.closes = append(e.closes, c.Close)
	e.vols = append(e.vols, c.Volume)
	e.trim()

	if !e.warmed {
		if len(e.closes) < e.WarmupNeed() {
			return nil
		}
		e.seedAndWarm()
		// No signal on the warmup candle: we only know the relation now.
		return nil
	}

	kf := 2.0 / float64(e.p.FastEMA+1)
	ks := 2.0 / float64(e.p.SlowEMA+1)
	e.emaFast = kf*c.Close + (1-kf)*e.emaFast
	e.emaSlow = ks*c.Close + (1-ks)*e.emaSlow

	prevAbove := e.above
	e.above = e.emaFast > e.emaSlow

	crossUp := e.above && !prevAbove
	crossDown := !e.above && prevAbove

	roc := e.roc()
	volRatio := e.volRatio()

	switch {
	case crossUp && roc >= e.p.ROCThreshold && volRatio >= e.p.VolumeSurge:
		return &Signal{
			Kind:  SignalBuy,
			TsSec: c.TsSec,
			Close: c.Close,
			Reason: fmt.Sprintf("EMA%d crossed above EMA%d, ROC(%d)=%.3f%% >= %.3f%%, volume %.2fx mean",
				e.p.FastEMA, e.p.SlowEMA, e.p.ROCPeriod, roc*100, e.p.ROCThreshold*100, volRatio),
			EMAFast:  e.emaFast,
			EMASlow:  e.emaSlow,
			ROC:      roc,
			VolRatio: volRatio,
		}
	case crossDown:
		return &Signal{
			Kind:     SignalExit,
			TsSec:    c.TsSec,
			Close:    c.Close,
			Reason:   fmt.Sprintf("EMA%d crossed below EMA%d", e.p.FastEMA, e.p.SlowEMA),
			EMAFast:  e.emaFast,
			EMASlow:  e.emaSlow,
			ROC:      roc,
			VolRatio: volRatio,
		}
	default:
		return nil
	}
}

// roc computes close[t]/close[t-ROCPeriod]-1 over closed candles.
func (e *Engine) roc() float64 {
	n := len(e.closes)
	if n <= e.p.ROCPeriod {
		return 0
	}
	base := e.closes[n-1-e.p.ROCPeriod]
	if base <= 0 {
		return 0
	}
	return e.closes[n-1]/base - 1
}

// volRatio compares the current candle volume to the mean of the previous
// VolMAPeriod candles (window excludes the current candle).
func (e *Engine) volRatio() float64 {
	n := len(e.vols)
	if n <= e.p.VolMAPeriod {
		return 0
	}
	window := e.vols[n-1-e.p.VolMAPeriod : n-1]
	sum := 0.0
	for _, v := range window {
		sum += v
	}
	mean := sum / float64(len(window))
	if mean <= 0 {
		return 0
	}
	return e.vols[n-1] / mean
}

// seedAndWarm initializes EMAs with an SMA seed over the accumulated history,
// which makes values independent of when the bot was started.
func (e *Engine) seedAndWarm() {
	e.emaFast = smaSeed(e.closes, e.p.FastEMA)
	e.emaSlow = smaSeed(e.closes, e.p.SlowEMA)
	e.above = e.emaFast > e.emaSlow
	e.warmed = true
}

// smaSeed computes the EMA at the end of history using an SMA seed at index
// period-1 followed by recursive smoothing.
func smaSeed(closes []float64, period int) float64 {
	if len(closes) < period || period <= 0 {
		return 0
	}
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += closes[i]
	}
	ema := sum / float64(period)
	k := 2.0 / float64(period+1)
	for i := period; i < len(closes); i++ {
		ema = k*closes[i] + (1-k)*ema
	}
	return ema
}

// trim bounds memory: keep enough history for warmup + lookbacks with slack.
func (e *Engine) trim() {
	need := e.WarmupNeed() + 8
	if len(e.closes) > need {
		e.closes = e.closes[len(e.closes)-need:]
		e.vols = e.vols[len(e.vols)-need:]
	}
}

// sane guards against NaN/Inf leaking into signals.
func sane(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
