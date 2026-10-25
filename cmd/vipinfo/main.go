// vipinfo: baca tier VIP (syarat volume per level) + status VIP akun sekarang.
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tiers, err := client.VIP.ListVIPTiers(ctx)
	if err != nil { fmt.Println("tiers:", err) } else {
		fmt.Println("=== VIP TIERS ===")
		for _, t := range tiers.Tiers {
			fmt.Printf("tier %+v\n", t)
		}
	}
	st, err := client.VIP.GetVIPStatus(ctx)
	if err != nil { fmt.Println("status:", err) } else {
		fmt.Println("=== STATUS AKUN ===")
		fmt.Printf("%+v\n", st)
	}
}
