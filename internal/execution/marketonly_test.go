package execution

import (
	"testing"

	"github.com/Fabric-Labs/polyester-sdk-go/models"
)

// The bot trades market-only by policy: no limit orders, no resting orders, so
// every fill is immediate and nothing can sit in the book unnoticed. This test
// fails the build if someone reintroduces a limit path in the order builder.
func TestMarketReqIsMarketOnly(t *testing.T) {
	l := &LiveExecutor{
		mi:          MarketInfo{Symbol: "HYPE-USDT", BaseScale: 6, QuoteScale: 6},
		slippageBps: 50,
	}

	buy := l.marketReq("buy", models.QtyInput{}, 250_000_000, "coid-buy")
	if buy.OrderType != "market" {
		t.Fatalf("buy OrderType = %q, want market", buy.OrderType)
	}
	if buy.Side != "buy" {
		t.Fatalf("buy Side = %q", buy.Side)
	}
	if buy.ClientOrderID == nil || *buy.ClientOrderID != "coid-buy" {
		t.Fatal("buy must carry its client order id")
	}
	if buy.MaxSlippageBps == nil {
		t.Fatal("market orders must carry a slippage bound")
	}

	sellQty := models.QtyFromScaled(models.MustQtyScaled(3_000_000).WithScale(6).WithSymbol("HYPE-USDT"))
	sell := l.marketReq("sell", sellQty, 0, "coid-sell")
	if sell.OrderType != "market" {
		t.Fatalf("sell OrderType = %q, want market", sell.OrderType)
	}
	if sell.Side != "sell" {
		t.Fatalf("sell Side = %q", sell.Side)
	}
	if sell.ClientOrderID == nil || *sell.ClientOrderID != "coid-sell" {
		t.Fatal("sell must carry its client order id")
	}
}
