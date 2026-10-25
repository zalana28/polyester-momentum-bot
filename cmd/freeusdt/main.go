// freeusdt: cetak saldo USDT bebas (untuk keputusan supervisor).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Fabric-Labs/polyester-sdk-go/chain"
	"github.com/Fabric-Labs/polyester-sdk-go/codecs"
	polyester "github.com/Fabric-Labs/polyester-sdk-go"
)

func parseFloat(s string) float64 {
	var f float64
	fmt.Sscan(s, &f)
	return f
}

func main() {
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
	if err != nil { fmt.Println("ERR"); os.Exit(1) }
	defer client.Close()
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		bal, err := client.Balances.List(ctx, nil, nil)
		if err == nil {
			for _, b := range bal.Balances {
				if b.AssetID == 1 {
					s, _ := codecs.FormatLedgerU128(b.Available, codecs.LedgerScale)
					if f := parseFloat(s); f > 1 { // sanity: baca nyata
						fmt.Println(s)
						cancel(); return
					}
				}
			}
		}
		cancel()
		time.Sleep(4 * time.Second)
	}
	fmt.Println("ERR")
}
