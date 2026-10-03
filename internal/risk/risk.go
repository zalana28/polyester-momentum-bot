// Package risk holds the pure daily-loss decision core. It never touches the
// network: callers feed equity observations and act on the returned decisions.
package risk

import (
	"fmt"
	"time"
)

// Decider decides when the daily loss limit trips and when new exposure is
// allowed. Fixed rules: at most 1 open position, loss measured against the
// equity snapshot taken at the start of the current UTC day.
type Decider struct {
	MaxDailyLossPct float64
}

// Observation is one equity measurement.
type Observation struct {
	DayKey string  // UTC yyyy-mm-dd of the measurement
	Equity float64 // total equity in quote units
}

// Decision is the outcome of evaluating an observation.
type Decision struct {
	NewDay       bool    // a new risk day started; baseline was reset
	BaselineUsed float64 // equity baseline the loss check ran against
	Halt         bool
	Reason       string
}

// DayKeyUTC returns the yyyy-mm-dd key of a UTC time.
func DayKeyUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// Evaluate processes an observation against the current day state (curKey,
// curBaseline). It returns the updated (key, baseline) plus the decision.
func (d *Decider) Evaluate(curKey string, curBaseline float64, o Observation) (string, float64, Decision) {
	if o.DayKey != curKey {
		// New day: reset the baseline to current equity; never carry a halt
		// decision from yesterday's numbers (an explicit halt remains until
		// the process restarts — that is enforced by the caller).
		return o.DayKey, o.Equity, Decision{
			NewDay:       true,
			BaselineUsed: o.Equity,
		}
	}
	baseline := curBaseline
	if baseline <= 0 {
		// Defensive: no usable baseline yet — adopt current equity.
		baseline = o.Equity
	}
	floor := baseline * (1 - d.MaxDailyLossPct/100)
	if o.Equity < floor {
		loss := baseline - o.Equity
		return curKey, baseline, Decision{
			BaselineUsed: baseline,
			Halt:         true,
			Reason: fmt.Sprintf("daily loss limit %.2f%% tripped: equity %.2f < floor %.2f (loss %.2f from baseline %.2f)",
				d.MaxDailyLossPct, o.Equity, floor, loss, baseline),
		}
	}
	return curKey, baseline, Decision{BaselineUsed: baseline}
}

// CanOpenPosition returns an error when the bot must not open exposure.
func (d *Decider) CanOpenPosition(halted bool, openPositions int) error {
	if halted {
		return fmt.Errorf("risk: trading halted")
	}
	if openPositions > 0 {
		return fmt.Errorf("risk: position already open (max 1)")
	}
	return nil
}
