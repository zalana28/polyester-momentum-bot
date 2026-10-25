// probe2: decisive test — signed SDK request against the NEW domain
// (testnet.polyester.com) which is behind Cloudflare. If this passes, the bot
// can run on the new domain with a custom environment.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Fabric-Labs/polyester-sdk-go/chain"
	polyester "github.com/Fabric-Labs/polyester-sdk-go"
)

func strp(s string) *string { return &s }

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
	env.APIURL = "https://testnet.polyester.com"
	env.WebsocketURL = "wss://testnet.polyester.com/ws"
	client, err := polyester.New(polyester.Config{
		Environment: &env, APIURL: env.APIURL,
		APIKeyID: kid, APIPrivateKey: priv, HydrateCatalogs: true,
	})
	if err != nil {
		fmt.Println("client:", err)
		os.Exit(1)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	fmt.Println("--- probe RPC satu per satu:")
	bal, balErr := client.Balances.List(ctx, nil, nil)
	if balErr != nil { fmt.Println("balances.List:", balErr) } else { fmt.Println("balances.List OK —", len(bal.Balances), "baris") }
	var id18 = uint32(18)
	ob, obErr := client.Orderbook.Get(ctx, "HYPE-USDT", 3)
	if obErr != nil { fmt.Println("orderbook:", obErr) } else { fmt.Printf("orderbook OK — %d bid | best bid %v | best ask %v\n", len(ob.Bids), ob.Bids[0].Price, ob.Asks[0].Price) }
	cnd, cndErr := client.MarketData.GetCandles(ctx, nil, &id18, "1m", 2, nil, nil, false)
	if cndErr != nil { fmt.Println("candles:", cndErr) } else { fmt.Println("candles OK —", len(cnd.Candles), "candle, close:", cnd.Candles[len(cnd.Candles)-1].Close) }
	ords, ordErr := client.Orders.ListOpen(ctx, nil, nil, nil, nil, false, false, nil)
	if ordErr != nil { fmt.Println("orders.ListOpen:", ordErr) } else { fmt.Println("orders.ListOpen OK —", len(ords.Orders), "order") }
	_ = bal; _ = ob; _ = cnd; _ = ords
}
