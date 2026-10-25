// ordertest: uji notional minimum (limit jauh di bawah pasar + cancel) — tanpa fee.
// Usage: go run ./cmd/ordertest <SYMBOL> <NOTIONAL_USD>
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

func main() {
	if len(os.Args) < 3 { fmt.Println("usage: ordertest <SYMBOL> <NOTIONAL>"); os.Exit(2) }
	sym := strings.ToUpper(os.Args[1])
	var notional float64
	fmt.Sscan(os.Args[2], &notional)
	raw, _ := os.ReadFile("/Users/jazeerahusna/.zcode/workspace/default/polyester-momentum-bot/.env")
	var kid, priv string
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "API_KEY_ID=") { kid = strings.TrimPrefix(l, "API_KEY_ID=") }
		if strings.HasPrefix(l, "API_PRIVATE_KEY=") { priv = strings.TrimPrefix(l, "API_PRIVATE_KEY=") }
	}
	env := chain.PolyesterTestnetEnvironment
	env.APIURL = "https://api.testnet.polyester.com"
	client, err := polyester.New(polyester.Config{Environment: &env, APIURL: env.APIURL, APIKeyID: kid, APIPrivateKey: priv, HydrateCatalogs: true})
	if err != nil { fmt.Println("client:", err); os.Exit(1) }
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.WaitForCatalogs(ctx); err != nil { fmt.Println("catalogs:", err); os.Exit(1) }

	ob, err := client.Orderbook.Get(ctx, sym, 2)
	if err != nil { fmt.Println("orderbook:", err); os.Exit(1) }
	mid := (float64(ob.Bids[0].Price.Ticks()) + float64(ob.Asks[0].Price.Ticks())) / 2 / 1e9
	pc, _ := client.Catalogs.PairConstraintsForSymbol(sym)
	bs, _ := client.Catalogs.BaseQuantityScaleForSymbol(sym)
	tick := pc.TickSizeTicks
	entry6 := int64(mid * 0.90 * 1e6)
	entry6 = entry6 / tick * tick
	price := models.PriceFromTicksInt(entry6 * 1000)
	qtyS6 := int64(notional / (float64(entry6) / 1e6) * 1e6)
	step := pc.StepSizeScaled
	qtyS6 = qtyS6 / step * step
	q, _ := models.NewQtyScaled(qtyS6)
	q = q.WithScale(bs).WithSymbol(sym)
	coid := fmt.Sprintf("ot-%d", time.Now().UnixNano())
	res, err := client.Orders.Create(ctx, models.CreateOrderRequest{
		Symbol: &sym, Side: "buy", OrderType: "limit", TIF: sp("gtc"),
		Price: &price, Qty: models.QtyFromScaled(q), ClientOrderID: &coid,
	}, nil)
	if err != nil { fmt.Printf("REJECT %s $%.0f: %v\n", sym, notional, err); os.Exit(1) }
	fmt.Printf("ACCEPT %s $%.0f (order %s) — cancel...\n", sym, notional, res.OrderID)
	_, cerr := client.Orders.Cancel(ctx, nil, models.OrderKeyByID(res.OrderID), &sym, nil, nil)
	fmt.Println("cancel:", cerr == nil)
}
