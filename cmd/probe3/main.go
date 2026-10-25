// probe3: uji admission order di sub-akun — saldo, harga pasar, order uji
// dekat pasar (notional ~$5000), lalu dibatalkan langsung.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Fabric-Labs/polyester-sdk-go/chain"
	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/models"
)

func sp(s string) *string { return &s }
func baseScaleOf(sym string, c *polyester.Client) int {
	sc, ok := c.Catalogs.BaseQuantityScaleForSymbol(sym)
	if !ok { return 6 }
	return sc
}
func mustQtyScaled(v int64, sym string, scale int) models.QtyInput {
	q, _ := models.NewQtyScaled(v)
	out := q.WithScale(scale).WithSymbol(sym)
	return models.QtyFromScaled(out)
}

func main() {
	raw, _ := os.ReadFile("/Users/jazeerahusna/.zcode/workspace/default/polyester-momentum-bot/.env")
	var kid, priv string
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "API_KEY_ID=") {
			kid = strings.TrimPrefix(l, "API_KEY_ID=")
		}
		if strings.HasPrefix(l, "API_PRIVATE_KEY=") {
			priv = strings.TrimPrefix(l, "API_PRIVATE_KEY=")
		}
	}
	env := chain.PolyesterTestnetEnvironment
	env.APIURL = "https://api.testnet.polyester.com"
	client, err := polyester.New(polyester.Config{Environment: &env, APIURL: env.APIURL, APIKeyID: kid, APIPrivateKey: priv, HydrateCatalogs: true})
	if err != nil { fmt.Println("client:", err); os.Exit(1) }
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := client.WaitForCatalogs(ctx); err != nil { fmt.Println("catalogs:", err); os.Exit(1) }

	bal, berr := client.Balances.List(ctx, nil, nil)
	if berr != nil { fmt.Println("balances:", berr); os.Exit(1) }
	for _, b := range bal.Balances {
		if b.Trading != "" && b.Trading != "0" {
			fmt.Printf("saldo: assetID %d = %s (reserved %s)\n", b.AssetID, b.Trading, b.Reserved)
		}
	}

	ob, oerr := client.Orderbook.Get(ctx, "DOGE-USDT", 3)
	if oerr != nil { fmt.Println("orderbook:", oerr); os.Exit(1) }
	mid := (float64(ob.Bids[0].Price.Ticks()) + float64(ob.Asks[0].Price.Ticks())) / 2 / 1e9
	fmt.Printf("DOGE mid sekarang: %.6g\n", mid)

	sym := "DOGE-USDT"
	pc, _ := client.Catalogs.PairConstraintsForSymbol(sym)
	entry := mid * 0.99
	tick := pc.TickSizeTicks
	if tick <= 0 { tick = 1 }
	entry6 := int64(entry * 1e6)
	entry6 = (entry6 / tick) * tick // align tick (TickSizeTicks dlm satuan true6)
	price := models.PriceFromTicksInt(entry6 * 1000) // true6 -> wire ticks
	entryStr := fmt.Sprintf("%.6f", float64(entry6)/1e6)
	step := pc.StepSizeScaled
	if step <= 0 { step = 1 }
	qtyS6 := (int64(5000/entry*1e6) / step) * step
	qty := mustQtyScaled(qtyS6, sym, baseScaleOf(sym, client))

	coid := fmt.Sprintf("test-dup-%d", time.Now().UnixNano())
	fmt.Printf("uji: BUY %s @ %s (tick %d, notional ~$500) coid=%s\n", sym, entryStr, tick, coid)
	res, err := client.Orders.Create(ctx, models.CreateOrderRequest{
		Symbol: &sym, Side: "buy", OrderType: "limit", TIF: sp("gtc"),
		Price: &price, Qty: qty, ClientOrderID: &coid,
	}, nil)
	if err != nil {
		fmt.Println("CREATE ERROR:", err)
		os.Exit(1)
	}
	fmt.Println("CREATE OK — status:", res.Status, "order:", res.OrderID)
	if _, cerr := client.Orders.Cancel(ctx, nil, models.OrderKeyByID(res.OrderID), &sym, nil, nil); cerr != nil {
		fmt.Println("cancel:", cerr)
	} else {
		fmt.Println("cancel OK")
	}
}
