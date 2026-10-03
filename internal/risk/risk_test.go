package risk

import (
	"testing"
	"time"
)

func base() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }

func TestEvaluateResetsBaselineOnNewDay(t *testing.T) {
	d := &Decider{MaxDailyLossPct: 5}
	key, baseline, dec := d.Evaluate("", 0, Observation{DayKey: DayKeyUTC(base()), Equity: 1000})
	if !dec.NewDay || key != "2026-09-29" || baseline != 1000 {
		t.Fatalf("new day handling wrong: %+v key=%s baseline=%f", dec, key, baseline)
	}
	if dec.Halt {
		t.Fatal("must not halt on a fresh baseline")
	}
}

func TestEvaluateHaltsOnDailyLoss(t *testing.T) {
	d := &Decider{MaxDailyLossPct: 5}
	day := DayKeyUTC(base())
	_, _, _ = d.Evaluate("", 0, Observation{DayKey: day, Equity: 1000})
	// 6% drop below baseline trips the limit.
	_, _, dec := d.Evaluate(day, 1000, Observation{DayKey: day, Equity: 939})
	if !dec.Halt {
		t.Fatalf("expected halt, got %+v", dec)
	}
}

func TestEvaluateToleratesSmallDrawdown(t *testing.T) {
	d := &Decider{MaxDailyLossPct: 5}
	day := DayKeyUTC(base())
	_, _, _ = d.Evaluate("", 0, Observation{DayKey: day, Equity: 1000})
	_, _, dec := d.Evaluate(day, 1000, Observation{DayKey: day, Equity: 960})
	if dec.Halt {
		t.Fatalf("4%% drawdown must not halt: %+v", dec)
	}
}

func TestDayBoundaryCarriesNothing(t *testing.T) {
	d := &Decider{MaxDailyLossPct: 5}
	day1 := DayKeyUTC(base())
	day2 := DayKeyUTC(base().Add(24 * time.Hour))
	_, _, _ = d.Evaluate("", 0, Observation{DayKey: day1, Equity: 1000})
	// Yesterday equity crashed to near zero; today a fresh measurement starts
	// a fresh baseline and must NOT halt on yesterday's data.
	_, _, dec := d.Evaluate(day1, 1000, Observation{DayKey: day2, Equity: 200})
	if dec.Halt || !dec.NewDay {
		t.Fatalf("day boundary must reset baseline without halting: %+v", dec)
	}
}

func TestCanOpenPosition(t *testing.T) {
	d := &Decider{MaxDailyLossPct: 5}
	if err := d.CanOpenPosition(false, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := d.CanOpenPosition(false, 1); err == nil {
		t.Fatal("second position must be refused")
	}
	if err := d.CanOpenPosition(true, 0); err == nil {
		t.Fatal("halted bot must not open positions")
	}
}
