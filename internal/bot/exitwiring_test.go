package bot

import (
	"context"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"polyester-momentum-bot/internal/brain"
	"polyester-momentum-bot/internal/config"
	"polyester-momentum-bot/internal/execution"
	"polyester-momentum-bot/internal/journal"
	"polyester-momentum-bot/internal/state"
)

// fakeExec implements only what the exit plan touches; the embedded nil
// Executor panics loudly if the bot reaches for anything unexpected.
type fakeExec struct {
	execution.Executor
	price       int64
	sells       []int64 // quantities sold, in order
	buys        []int64 // quote budgets bought, in order
	qty         int64   // last stop-loss quantity
	sls         int
	closed      []string // client order ids of sells
	baseBalance float64  // BaseBalance() answer for spot sells
}

func (f *fakeExec) BaseBalance(context.Context) (float64, error) { return f.baseBalance, nil }

func (f *fakeExec) Buy(_ context.Context, budget int64, id string) (*execution.Fill, error) {
	f.buys = append(f.buys, budget)
	return &execution.Fill{
		BaseQtyScaled: 1000, AvgPriceScaled6: f.price,
		ClientOrderID: id, OrderID: "ord-" + id, Status: "filled",
	}, nil
}

func (f *fakeExec) Sell(_ context.Context, qty int64, id string) (*execution.Fill, error) {
	f.sells = append(f.sells, qty)
	f.closed = append(f.closed, id)
	return &execution.Fill{BaseQtyScaled: qty, AvgPriceScaled6: f.price}, nil
}

func (f *fakeExec) PlaceStopLoss(_ context.Context, qty, sl int64, _ string) (*execution.StopLoss, error) {
	f.qty = qty
	f.sls++
	return &execution.StopLoss{TriggerID: "sl-" + time.Now().Format("150405.000000"), PriceScaled6: sl}, nil
}

func (f *fakeExec) CancelTrigger(context.Context, string) error { return nil }

func (f *fakeExec) TriggerStatus(context.Context, string) (string, error) { return "cancelled", nil }

func newExitFixture(t *testing.T, pos *state.Position) (*Bot, *symRT, *fakeExec) {
	t.Helper()
	dir := t.TempDir()
	j, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })

	cfg := &config.Config{
		Mode: config.ModePaper, StateDir: dir, StopLossBps: 100,
		TrailActivateBps: 150, TrailDistanceBps: 100, TrailStepBps: 10,
		TrailPartialBps: 100, TrailPartialFrac: 0.5,
		TimeStopMinutes: 20, TimeStopMinBps: 50,
	}
	ex := &fakeExec{price: pos.AvgEntryScaled6}
	b := &Bot{
		cfg: cfg, log: log.New(io.Discard, "", 0), jrnl: j,
		st: state.New(true), brain: brain.New(45, 10*time.Minute),
		runCtx: context.Background(), opCtx: context.Background(),
	}
	s := &symRT{
		symbol: "TEST-USDT", exec: ex, pos: pos,
		mi: execution.MarketInfo{BaseScale: 6, QuoteScale: 6, MinQtyScaled: 1, TickTicks: 1},
	}
	return b, s, ex
}

func newPos(qty int64) *state.Position {
	return &state.Position{
		BaseQtyScaled: qty, BaseScale: 6, AvgEntryScaled6: 1_000_000,
		OpenedAt: time.Now(), SLTriggerID: "sl-old",
	}
}

// A partial take-profit sells exactly half, shrinks the position, and re-arms
// the stop on what is left — the remainder must never be left naked.
func TestManagePositionTakesPartialAndRearms(t *testing.T) {
	pos := newPos(1000)
	b, s, ex := newExitFixture(t, pos)

	b.managePosition(s, 1_012_000) // +120 bps

	if len(ex.sells) != 1 || ex.sells[0] != 500 {
		t.Fatalf("expected one sell of 500, got %v", ex.sells)
	}
	if !pos.PartialTaken || pos.BaseQtyScaled != 500 {
		t.Fatalf("position not shrunk: taken=%v qty=%d", pos.PartialTaken, pos.BaseQtyScaled)
	}
	if ex.sls != 1 || ex.qty != 500 {
		t.Fatalf("stop must be re-armed for the remainder: sls=%d qty=%d", ex.sls, ex.qty)
	}
	if s.pos == nil {
		t.Fatal("position should still be open")
	}
}

// Once armed, a drop through the trail level closes the rest with reason
// "trail"; the trail level itself never moves down.
func TestManagePositionTrailClosesRemainder(t *testing.T) {
	pos := newPos(1000)
	b, s, ex := newExitFixture(t, pos)

	b.managePosition(s, 1_030_000) // +300 bps: arms at peak, partial already fired
	if !pos.TrailArmed || pos.TrailStopScaled6 == 0 {
		t.Fatalf("trail should be armed with a level: %+v", pos)
	}
	level := pos.TrailStopScaled6
	b.managePosition(s, 1_020_000) // pull back but above the trail
	if pos.TrailStopScaled6 != level {
		t.Fatalf("trail moved without a new peak: %d -> %d", level, pos.TrailStopScaled6)
	}

	ex.price = level - 1
	b.managePosition(s, level-1)
	if s.pos != nil {
		t.Fatal("trail hit must close the position")
	}
	if len(ex.sells) < 2 {
		t.Fatalf("expected partial + trail sells, got %v", ex.sells)
	}
}

// A position that sits past the time window without progress is closed; the
// reason lands in trades.csv so the report can attribute it.
func TestManagePositionTimeStopBooksTrade(t *testing.T) {
	pos := newPos(1000)
	pos.OpenedAt = time.Now().Add(-21 * time.Minute)
	b, s, _ := newExitFixture(t, pos)

	b.managePosition(s, 1_001_000) // +10 bps, under the 50 bps bar
	if s.pos != nil {
		t.Fatal("time stop must close the position")
	}
	body, err := os.ReadFile(b.cfg.StateDir + "/trades.csv")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(body), "time stop") {
		t.Fatalf("trades.csv must record the reason, got:\n%s", body)
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (hay == needle || len(hay) > 0 && (indexOf(hay, needle) >= 0))
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
