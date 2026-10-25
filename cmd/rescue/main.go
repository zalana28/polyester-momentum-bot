// Command rescue arms a server-side stop_loss sell trigger for a stranded
// dip-bot position (bot died right after its buy filled, SL never armed).
// One-shot, read-then-write: prints current book first, then arms the trigger.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Fabric-Labs/polyester-sdk-go/codecs"
	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/chain"
	"github.com/Fabric-Labs/polyester-sdk-go/models"
)

func creds() (string, string) {
	raw, _ := os.ReadFile("/Users/jazeerahusna/.zcode/workspace/default/polyester-momentum-bot/.env")
	var kid, priv string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "API_KEY_ID=") {
			kid = strings.TrimPrefix(line, "API_KEY_ID=")
		}
		if strings.HasPrefix(line, "API_PRIVATE_KEY=") {
			priv = strings.TrimPrefix(line, "API_PRIVATE_KEY=")
		}
	}
	return kid, priv
}

func strPtr(s string) *string { return &s }

func main() {
	const (
		symbol   = "HYPE-USDT"
		entryS6  = int64(91838000) // fill avgpx 91.838 (dari order history)
		slS6     = int64(90735000) // -120bps, align tick 0.001
		qtyS6    = int64(27221800) // 27.2218 HYPE (dari fill)
		baseSc   = 6
		wireSh   = 9 // empiris: avgpx ticks 91838000000 = true6 91838000 x1000
	)
	kid, priv := creds()
	toolEnv := chain.PolyesterTestnetEnvironment
	toolEnv.APIURL = "https://api.testnet.polyester.com"
	client, err := polyester.New(polyester.Config{Environment: &toolEnv, APIKeyID: kid, APIPrivateKey: priv, HydrateCatalogs: true})
	if err != nil { fmt.Println("client:", err); os.Exit(1) }
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = client.WaitForCatalogs(ctx)

	ob, err := client.Orderbook.Get(ctx, symbol, 5)
	if err != nil { fmt.Println("book:", err); os.Exit(1) }
	if len(ob.Bids) == 0 || len(ob.Asks) == 0 { fmt.Println("book kosong"); os.Exit(1) }
	bid := float64(ob.Bids[0].Price.Ticks()) / 1e9
	ask := float64(ob.Asks[0].Price.Ticks()) / 1e9
	fmt.Printf("HYPE sekarang: bid=%.3f ask=%.3f | entry fill=91.838 | SL akan dipasang di 90.735 (-120bps) | target TP 92.389 (+60bps)\n", bid, ask)

	pr := models.PriceFromTicksInt(slS6 * int64(1000))
	qty, _ := models.NewQtyScaled(qtyS6)
	qty = qty.WithScale(baseSc).WithSymbol(symbol)
	coid := "dip-rescue-sl-hype-1"
	res, err := client.Triggers.Create(ctx, nil, symbol, "stop_loss", &pr, "sell",
		models.QtyFromScaled(qty), "market", nil, "", "", nil, &coid, false,
		codecs.CreateTriggerOptions{SelfTradePreventionMode: strPtr("expire_maker")})
	if err != nil {
		fmt.Println("gagal pasang SL:", err)
		os.Exit(1)
	}
	fmt.Println("OK SL trigger terpasang, id:", res.TriggerID)
}
