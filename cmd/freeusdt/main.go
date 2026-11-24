// freeusdt: cetak saldo USDT bebas (untuk keputusan supervisor).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/auth"
	"github.com/Fabric-Labs/polyester-sdk-go/chain"
	"github.com/Fabric-Labs/polyester-sdk-go/codecs"

	"polyester-momentum-bot/internal/ledgerx"
)

func parseFloat(s string) float64 {
	var f float64
	fmt.Sscan(s, &f)
	return f
}

func main() {
	raw, err := os.ReadFile(".env")
	if err != nil {
		raw, _ = os.ReadFile("/Users/jazeerahusna/.zcode/workspace/default/polyester-momentum-bot/.env")
	}
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
	client, err := polyester.New(polyester.Config{Environment: &env, APIURL: env.APIURL, APIKeyID: kid, APIPrivateKey: auth.NewSecret(priv)})
	if err != nil {
		fmt.Println("ERR")
		os.Exit(1)
	}
	defer client.Close()
	lastValidZero := false
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		bal, err := ledgerx.List(ctx, client)
		cancel()
		if err == nil {
			found := "0.00"
			for _, b := range bal.Balances {
				if b.AssetID == 1 {
					s, _ := codecs.FormatLedgerU128(b.Available, codecs.LedgerScale)
					if s != "" {
						found = s
					}
					break
				}
			}
			if f := parseFloat(found); f > 1 || lastValidZero {
				fmt.Println(found)
				return
			}
			// Bacaan <= $1 dikonfirmasi sekali lagi untuk membedakan cache 0 palsu vs saldo benar-benar habis
			lastValidZero = true
			time.Sleep(1500 * time.Millisecond)
			continue
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Println("ERR")
}
