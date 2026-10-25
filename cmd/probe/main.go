package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/chain"
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

func main() {
	kid, priv := creds()
	probeEnv := chain.PolyesterTestnetEnvironment
	probeEnv.APIURL = "https://api.testnet.polyester.com"
	client, err := polyester.New(polyester.Config{Environment: &probeEnv, APIKeyID: kid, APIPrivateKey: priv, HydrateCatalogs: true})
	if err != nil { fmt.Println("client:", err); os.Exit(1) }
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := client.WaitForCatalogs(ctx); err != nil { fmt.Println("catalogs:", err) }

	// asset id -> symbol dari katalog pair
	name := map[uint32]string{}
	if spot, err := client.MarketData.GetSpotConfig(ctx); err == nil {
		if pairs, ok := spot.Raw["pairs"].([]any); ok {
			for _, p := range pairs {
				pm, _ := p.(map[string]any)
				sym, _ := pm["symbol"].(string)
				base, _ := pm["base_asset_id"].(string)
				quote, _ := pm["quote_asset_id"].(string)
				var bid, qid uint32
				fmt.Sscan(base, &bid)
				fmt.Sscan(quote, &qid)
				if i := strings.Index(sym, "-"); i > 0 { name[bid] = sym[:i]; name[qid] = sym[i+1:] }
			}
		}
	}

	bal, err := client.Balances.List(ctx, nil, nil)
	if err != nil { fmt.Println("balances:", err) } else {
		fmt.Println("=== saldo (trading) ===")
		for _, b := range bal.Balances {
			if b.Trading != "" && b.Trading != "0" {
				fmt.Printf("%-8s (id %d) trading=%s reserved=%s available=%s\n", name[b.AssetID], b.AssetID, b.Trading, b.Reserved, b.Available)
			}
		}
	}
	open, err := client.Orders.ListOpen(ctx, nil, nil, nil, nil, false, false, nil)
	if err != nil { fmt.Println("open:", err) } else {
		fmt.Printf("=== order terbuka: %d ===\n", len(open.Orders))
		for _, o := range open.Orders {
			fmt.Printf("sym %d %s status=%s orig=%+v cum=%+v price=%+v\n", o.SymbolID, o.Side, o.Status, o.OrigQty, o.CumQty, o.Price)
		}
	}
	hist, err := client.Orders.ListHistory(ctx, nil, nil, nil, nil, nil, 15, false, false, nil)
	if err != nil { fmt.Println("history:", err) } else {
		fmt.Println("=== 15 order terakhir ===")
		for _, o := range hist.Orders {
			fmt.Printf("sym %d %s status=%-8s orig=%+v cum=%+v avgpx=%+v\n", o.SymbolID, o.Side, o.Status, o.OrigQty, o.CumQty, o.AvgPx)
		}
	}
}
