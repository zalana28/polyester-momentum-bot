package bot

import (
	"testing"
	"time"
)

func testCfg() exitPlanCfg {
	return exitPlanCfg{
		ActivateBps: 150, DistanceBps: 100, StepBps: 10,
		PartialBps: 100, PartialFrac: 0.5,
		TimeStopMin: 20, TimeStopMinBps: 50,
	}
}

// The trail must ratchet up behind the peak, never down, and must ignore moves
// smaller than one step so a wiggling tick does not rewrite the stop.
func TestTrailLevelRatchet(t *testing.T) {
	cfg := testCfg()
	const entry = 1_000_000 // scaled6

	if got := trailLevel(entry, 1_010_000, 0, cfg); got != 999_900 {
		t.Fatalf("first level from peak +100bps: got %d want 999900", got)
	}
	// Peak moved 1 tick: improvement (99) is under one step (1000) -> keep.
	if got := trailLevel(entry, 1_010_001, 999_900, cfg); got != 999_900 {
		t.Fatalf("sub-step move must not ratchet: got %d", got)
	}
	// Peak pushed to +200bps: desired 1_009_800, well past the step.
	if got := trailLevel(entry, 1_020_000, 999_900, cfg); got != 1_009_800 {
		t.Fatalf("ratchet to new peak: got %d want 1009800", got)
	}
	// Peak falls back: the stop never moves down.
	if got := trailLevel(entry, 1_005_000, 1_009_800, cfg); got != 1_009_800 {
		t.Fatalf("trail must not fall: got %d", got)
	}
}

func TestTrailArmsOnlyAboveActivation(t *testing.T) {
	cfg := testCfg()
	if trailArms(149.9, cfg) {
		t.Fatal("must not arm below 150bps")
	}
	if !trailArms(150, cfg) {
		t.Fatal("must arm at exactly 150bps")
	}
	if trailArms(999, exitPlanCfg{}) {
		t.Fatal("activation disabled (0) must never arm")
	}
}

// Half is taken once, at the partial threshold, and never again.
func TestPartialDueOnce(t *testing.T) {
	cfg := testCfg()
	if partialDue(99, false, cfg) {
		t.Fatal("must not fire below threshold")
	}
	if !partialDue(100, false, cfg) {
		t.Fatal("must fire at threshold")
	}
	if partialDue(300, true, cfg) {
		t.Fatal("must not fire twice")
	}
	if partialDue(300, false, exitPlanCfg{PartialBps: 100, PartialFrac: 1}) {
		t.Fatal("a 100% partial is a full exit; the plan must reject it")
	}
}

// The clock recycles dead capital: past 20 minutes, only positions under
// +50bps are closed. Winners keep running.
func TestTimeStop(t *testing.T) {
	cfg := testCfg()
	now := time.Now()
	opened := now.Add(-21 * time.Minute)

	if !timeStopDue(opened, now, 49, cfg) {
		t.Fatal("21 minutes at +49bps must time-stop")
	}
	if timeStopDue(opened, now, 50, cfg) {
		t.Fatal("+50bps must be allowed to run")
	}
	if timeStopDue(now.Add(-19*time.Minute), now, -100, cfg) {
		t.Fatal("must not fire before the window")
	}
	if timeStopDue(time.Time{}, now, -100, cfg) {
		t.Fatal("unknown open time must not fire")
	}
}

func TestBpsBetween(t *testing.T) {
	if got := bpsBetween(1_000_000, 1_010_000); got != 100 {
		t.Fatalf("got %v want 100", got)
	}
	if got := bpsBetween(0, 1_010_000); got != 0 {
		t.Fatalf("entry 0 must not divide: got %v", got)
	}
}
