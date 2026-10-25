// cancelown cancels every OPEN order whose client_order_id has the given
// prefix (default "dip-"). One-shot janitor for orders orphaned by a dead
// bot instance. Orders with other prefixes (e.g. rescue-sell-) are untouched.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/chain"
	"github.com/Fabric-Labs/polyester-sdk-go/models"
)

func main() {
	prefix := "dip-"
	if len(os.Args) > 1 {
		prefix = os.Args[1]
	}
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
	toolEnv := chain.PolyesterTestnetEnvironment
	toolEnv.APIURL = "https://api.testnet.polyester.com"
	client, err := polyester.New(polyester.Config{Environment: &toolEnv, APIKeyID: kid, APIPrivateKey: priv, HydrateCatalogs: true})
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = client.WaitForCatalogs(ctx)

	open, err := client.Orders.ListOpen(ctx, nil, nil, nil, nil, false, false, nil)
	if err != nil {
		fmt.Println("list:", err)
		os.Exit(1)
	}
	n := 0
	for _, o := range open.Orders {
		if !strings.HasPrefix(o.ClientOrderID, prefix) {
			continue
		}
		if _, err := client.Orders.Cancel(ctx, nil, models.OrderKeyByID(o.OrderID), nil, nil, nil); err != nil {
			fmt.Printf("cancel %s (%s): %v\n", o.OrderID, o.ClientOrderID, err)
			continue
		}
		fmt.Printf("canceled %s (%s)\n", o.OrderID, o.ClientOrderID)
		n++
	}
	fmt.Printf("selesai: %d order dibatalkan (prefix %q)\n", n, prefix)

	// bersihkan trigger sisa (SL/TP yatim yang mereserve dana)
	if tg, terr := client.Triggers.List(ctx, nil, nil, nil, []string{"created", "armed"}, 50, nil); terr == nil {
		m := 0
		for _, t := range tg.Triggers {
			if _, cerr := client.Triggers.Cancel(ctx, nil, t.TriggerID, nil); cerr == nil {
				fmt.Printf("trigger dibatalkan: %s (%s)\n", t.TriggerID, t.ClientTriggerID)
				m++
			}
		}
		fmt.Printf("trigger sisa dibatalkan: %d\n", m)
	} else {
		fmt.Println("triggers list:", terr)
	}
}
