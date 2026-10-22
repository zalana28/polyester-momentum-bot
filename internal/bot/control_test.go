package bot

import (
	"testing"

	"polyester-momentum-bot/internal/config"
	"polyester-momentum-bot/internal/execution"
	"polyester-momentum-bot/internal/state"
)

func ctlFixture(t *testing.T) (*Bot, *symRT, *fakeExec) {
	t.Helper()
	pos := newPos(1000)
	b, s, ex := newExitFixture(t, pos)
	b.cfg.TradeBudgetQuote = "250"
	b.cfg.ManualMaxNotionalQuote = 500
	b.cfg.MaxOpenPositions = 4
	b.cfg.TakeProfitBps = 200
	s.pos = nil // most cases start flat
	return b, s, ex
}

// "HYPE" and "hype-usdt" must both resolve; anything the bot does not trade is
// refused rather than guessed.
func TestResolveSymbol(t *testing.T) {
	b, s, _ := ctlFixture(t)
	b.syms = []*symRT{s} // s.symbol is TEST-USDT

	if got, err := b.resolveSymbol("test"); err != nil || got != s {
		t.Fatalf("short name should resolve: %v", err)
	}
	if got, err := b.resolveSymbol(" TEST-usdt "); err != nil || got != s {
		t.Fatalf("full name should resolve: %v", err)
	}
	if _, err := b.resolveSymbol("DOGE"); err == nil {
		t.Fatal("unknown symbol must be rejected")
	}
	if _, err := b.resolveSymbol(""); err == nil {
		t.Fatal("empty symbol must be rejected")
	}
}

func TestManualQtyValidation(t *testing.T) {
	b, s, _ := ctlFixture(t)
	if got, err := b.manualQty(s, "3.5"); err != nil || got != 3_500_000 {
		t.Fatalf("3.5 base units at scale 6: got %d err %v", got, err)
	}
	for _, bad := range []string{"", "0", "-1", "abc", "1e3", "3,5"} {
		if _, err := b.manualQty(s, bad); err == nil {
			t.Fatalf("qty %q must be rejected", bad)
		}
	}
}

func TestManualBudgetCap(t *testing.T) {
	b, s, _ := ctlFixture(t)
	if got, err := b.manualBudget(s, ""); err != nil || got != 250_000_000 {
		t.Fatalf("default budget: got %d err %v", got, err)
	}
	if _, err := b.manualBudget(s, "600"); err == nil {
		t.Fatal("budget above the manual cap must be rejected")
	}
	if _, err := b.manualBudget(s, "abc"); err == nil {
		t.Fatal("non-numeric budget must be rejected")
	}
	if _, err := b.manualBudget(s, "-5"); err == nil {
		t.Fatal("negative budget must be rejected")
	}
}

// A manual buy uses the ordinary execution path, so the position always comes
// with a stop-loss; and it is refused while a position is already open.
func TestManualBuyAttachesStopAndRefusesDouble(t *testing.T) {
	b, s, ex := ctlFixture(t)
	b.cfg.Mode = config.ModePaper
	ex.price = 1_000_000

	msg, err := b.doManualBuy(s, 250_000_000)
	if err != nil {
		t.Fatalf("manual buy: %v", err)
	}
	if msg == "" || s.pos == nil || ex.sls != 1 {
		t.Fatalf("manual buy must open a protected position: msg=%q pos=%v sls=%d", msg, s.pos, ex.sls)
	}
	if _, err := b.doManualBuy(s, 250_000_000); err == nil {
		t.Fatal("second buy must be refused while a position is open")
	}
}

// Selling less than the position reduces it and re-arms the stop on the rest.
func TestManualSellPartialRearms(t *testing.T) {
	b, s, ex := ctlFixture(t)
	s.pos = newPos(1000)
	ex.price = 1_010_000

	msg, err := b.doManualSell(s, 400)
	if err != nil {
		t.Fatalf("manual sell: %v", err)
	}
	if len(ex.sells) != 1 || ex.sells[0] != 400 {
		t.Fatalf("expected one sell of 400, got %v", ex.sells)
	}
	if s.pos == nil || s.pos.BaseQtyScaled != 600 {
		t.Fatalf("position should be 600 after partial sell, got %+v", s.pos)
	}
	if ex.sls != 1 || ex.qty != 600 {
		t.Fatalf("stop must be re-armed for 600: sls=%d qty=%d", ex.sls, ex.qty)
	}
	if msg == "" {
		t.Fatal("expected a human-readable message")
	}
}

func TestManualSellRefusesWithoutPosition(t *testing.T) {
	b, s, ex := ctlFixture(t)
	ex.baseBalance = 0
	if _, err := b.doManualSell(s, 10); err == nil {
		t.Fatal("sell with no position and no balance must be refused")
	}
	if _, err := b.doFlatten(s); err == nil {
		t.Fatal("flatten without position must be refused")
	}
}

// A manual holding (no tracked position) can be sold from the base balance; the
// bot must not invent position state for it, and must refuse more than it holds.
func TestSpotSellFromBalance(t *testing.T) {
	b, s, ex := ctlFixture(t)
	ex.baseBalance = 3.5
	ex.price = 78_500_000

	msg, err := b.doManualSell(s, 3_000_000) // 3.0 HYPE at scale 6
	if err != nil {
		t.Fatalf("spot sell: %v", err)
	}
	if len(ex.sells) != 1 || ex.sells[0] != 3_000_000 {
		t.Fatalf("expected one sell of 3.0, got %v", ex.sells)
	}
	if s.pos != nil {
		t.Fatal("spot sell must not create position state")
	}
	if msg == "" {
		t.Fatal("expected a message")
	}

	if _, err := b.doManualSell(s, 4_000_000); err == nil {
		t.Fatal("selling more than the balance must be refused")
	}
}

// Shadow mode and a halted bot must both refuse manual entries: the socket is
// not a way around the risk controls.
func TestManualBuyRespectsSafetyFlags(t *testing.T) {
	b, s, _ := ctlFixture(t)

	b.cfg.ShadowOnly = true
	if _, err := b.doManualBuy(s, 1_000_000); err == nil {
		t.Fatal("shadow mode must refuse manual orders")
	}
	b.cfg.ShadowOnly = false

	b.st.Halt("test")
	if _, err := b.doManualBuy(s, 1_000_000); err == nil {
		t.Fatal("halted bot must refuse manual orders")
	}
}

var _ = execution.MarketInfo{}
var _ = state.Position{}
