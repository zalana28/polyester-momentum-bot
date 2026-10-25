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
	if err != nil { fmt.Println("0"); os.Exit(1) }
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bal, err := client.Balances.List(ctx, nil, nil)
	if err != nil { fmt.Println("0"); os.Exit(1) }
	for _, b := range bal.Balances {
		if b.AssetID == 1 {
			s, _ := codecs.FormatLedgerU128(b.Available, codecs.LedgerScale)
			fmt.Println(s)
			return
		}
	}
	fmt.Println("0")
}
