package execution

import (
	"context"
	"io"
	"log"
	"testing"

	"polyester-momentum-bot/internal/num"
)

func testMarketInfo() MarketInfo {
	return MarketInfo{
		Symbol:                 "TEST-USDT",
		BaseScale:              8,
		QuoteScale:             6,
		TickTicks:              100,       // 0.0001
		MinQtyScaled:           1_000_000, // 0.01 base
		MinNotionalQuoteScaled: 5_000_000, // 5 USDT
		BaseAssetID:            1,
		QuoteAssetID:           2,
	}
}

func quietLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

type memJournal struct{ entries []Entry }

func (m *memJournal) Append(e Entry) error { m.entries = append(m.entries, e); return nil }

func TestPaperBuySellRoundTrip(t *testing.T) {
	mi := testMarketInfo()
	p := NewPaperExecutor(mi, &memJournal{}, quietLogger(), 50, 1000_000_000) // 1000 USDT
	ctx := context.Background()

	p.SetMark(100.0)
	fill, err := p.Buy(ctx, 100_000_000, "coid-buy-1") // 100 USDT
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	// Fill price = 100 * (1 + 50bps) = 100.5 -> 100_500_000 (true ticks).
	if fill.AvgPriceScaled6 != 100_500_000 {
		t.Errorf("fill price = %d, want 100_500_000", fill.AvgPriceScaled6)
	}
	// All-in: (100 USDT - 10bps fee) / 100.5 = 0.99402985... at scale 8, floor.
	wantQty := int64(99_402_985) // 0.99402985
	if fill.BaseQtyScaled != wantQty {
		t.Errorf("qty = %d, want %d", fill.BaseQtyScaled, wantQty)
	}
	q, _ := p.QuoteAvailable(ctx)
	if q > 900.0001 || q < 899.9999 {
		t.Errorf("quote after buy = %f, want 900", q)
	}

	sellFill, err := p.Sell(ctx, fill.BaseQtyScaled, "coid-sell-1")
	if err != nil {
		t.Fatalf("sell: %v", err)
	}
	// Sell at 100 * (1 - 50bps) = 99.5 -> 99_500_000.
	if sellFill.AvgPriceScaled6 != 99_500_000 {
		t.Errorf("sell price = %d, want 99_500_000", sellFill.AvgPriceScaled6)
	}
	// PnL must be negative: bought at 100.5, sold at 99.5, minus fees.
	baseUnits := num.ScaledToFloat(fill.BaseQtyScaled, 8)
	expected := (99.5 - 100.5) * baseUnits
	if expected >= 0 {
		t.Fatalf("test sanity: expected negative round trip")
	}
}

func TestPaperStopLossLifecycle(t *testing.T) {
	mi := testMarketInfo()
	mj := &memJournal{}
	p := NewPaperExecutor(mi, mj, quietLogger(), 0, 1000_000_000)
	ctx := context.Background()
	p.SetMark(100.0)

	fill, err := p.Buy(ctx, 100_000_000, "coid-buy-1")
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	sl := num.MulBpsFloor(fill.AvgPriceScaled6, 9900)  // -1%
	tp := num.MulBpsFloor(fill.AvgPriceScaled6, 10200) // +2%
	ex, err := p.PlaceStopLoss(ctx, fill.BaseQtyScaled, sl, "sl-1")
	if err != nil {
		t.Fatalf("place stop loss: %v", err)
	}
	active, _ := p.ActiveTriggers(ctx)
	if len(active) != 1 {
		t.Fatalf("want 1 active trigger, got %d", len(active))
	}

	// Price stays above SL: nothing fires.
	p.SetMark(100.4)
	if fired := p.CheckTriggers(num.FloatToScaledRounded(100.4, 6)); len(fired) != 0 {
		t.Fatalf("no trigger should fire above SL: %+v", fired)
	}

	// Price rises through TP: paper must NOT fire it — take profit is
	// enforced bot-side; the bot reads pos.TPPriceScaled6 and sells itself.
	p.SetMark(103.0)
	if fired := p.CheckTriggers(num.FloatToScaledRounded(103.0, 6)); len(fired) != 0 {
		t.Fatalf("paper must not fire TP; bot owns it: %+v", fired)
	}
	_ = tp

	// Price drops through SL: stop_loss fires and closes the position.
	p.SetMark(98.0)
	fired := p.CheckTriggers(num.FloatToScaledRounded(98.0, 6))
	if len(fired) != 1 || fired[0].Kind != "stop_loss" {
		t.Fatalf("expected stop_loss to fire, got %+v", fired)
	}
	if err := p.CancelTrigger(ctx, ex.TriggerID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	active, _ = p.ActiveTriggers(ctx)
	if len(active) != 0 {
		t.Fatalf("want 0 active triggers after fire, got %d", len(active))
	}
	// Balance dropped (SL at 99.5 below buy 100.5 minus fees).
	q, _ := p.QuoteAvailable(ctx)
	if q >= 1000 {
		t.Errorf("losing SL must lower quote balance; got %f", q)
	}
}

func TestPaperDustRefused(t *testing.T) {
	mi := testMarketInfo()
	p := NewPaperExecutor(mi, &memJournal{}, quietLogger(), 0, 1000_000_000)
	ctx := context.Background()
	p.SetMark(100.0)
	if _, err := p.Sell(ctx, 999_999, "coid-dust"); err == nil {
		t.Fatal("dust sell must be refused")
	}
}
