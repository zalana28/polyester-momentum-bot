// rescuesell: one-shot GTC limit sell of the full available base balance.
// Usage: go run ./cmd/rescuesell <SYMBOL> <LIMIT_PRICE>
package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/models"
)

func strPtr(s string) *string { return &s }

func main() {
	if len(os.Args) != 3 { fmt.Println("usage: rescuesell <SYMBOL> <PRICE>"); os.Exit(2) }
	symbol, priceStr := strings.ToUpper(os.Args[1]), os.Args[2]
	raw, _ := os.ReadFile("/Users/jazeerahusna/.zcode/workspace/default/polyester-momentum-bot/.env")
	var kid, priv string
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "API_KEY_ID=") { kid = strings.TrimPrefix(l, "API_KEY_ID=") }
		if strings.HasPrefix(l, "API_PRIVATE_KEY=") { priv = strings.TrimPrefix(l, "API_PRIVATE_KEY=") }
	}
	client, err := polyester.New(polyester.Config{Environment: &polyester.TestnetEnvironment, APIKeyID: kid, APIPrivateKey: priv, HydrateCatalogs: true})
	if err != nil { fmt.Println(err); os.Exit(1) }
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = client.WaitForCatalogs(ctx)

	bal, err := client.Balances.List(ctx, nil, nil)
	if err != nil { fmt.Println("balances:", err); os.Exit(1) }
	// resolve base asset id & scale dari katalog (typed helpers)
	parts := strings.SplitN(symbol, "-", 2)
	idp := client.Catalogs.LedgerIDForAsset(parts[0])
	if idp == nil { fmt.Println("asset id", parts[0], "tidak ketemu"); os.Exit(1) }
	baseID := *idp
	baseScale, ok := client.Catalogs.BaseQuantityScaleForSymbol(symbol)
	if !ok { fmt.Println("katalog scale belum lengkap"); os.Exit(1) }
	var availS string
	for _, b := range bal.Balances {
		if b.AssetID == baseID { availS = b.Available }
	}
	if availS == "" { fmt.Println("tidak ada balance", symbol); os.Exit(1) }
	// qty scaled: available string adalah desimal pada baseScale
	// order service mau tick skala wire (10^9 utk HYPE): decimal -> true6 -> x1000
	var priceF float64
	fmt.Sscan(priceStr, &priceF)
	price := models.PriceFromTicksInt(int64(math.Round(priceF*1e6)) * 1000)
	var availF float64
	fmt.Sscan(availS, &availF)
	pc, _ := client.Catalogs.PairConstraintsForSymbol(symbol)
	fmt.Printf("constraints: step=%d minqty=%d tick=%d\n", pc.StepSizeScaled, pc.MinQtyScaled, pc.TickSizeTicks)
	availS6 := int64(math.Round(availF / math.Pow10(18-baseScale))) // raw 18dp -> base scale
	step := pc.StepSizeScaled
	if step <= 0 { step = 1 }
	qtyScaled := (availS6 / step) * step
	qty, _ := models.NewQtyScaled(qtyScaled)
	qty = qty.WithScale(baseScale).WithSymbol(symbol)
	postOnly := true
	for attempt := 0; attempt < 4; attempt++ {
		coid := fmt.Sprintf("rescue-sell-%d-%d", time.Now().Unix(), attempt)
		req := models.CreateOrderRequest{
			Symbol: &symbol, Side: "sell", OrderType: "limit", TIF: strPtr("gtc"),
			PostOnly: postOnly, Price: &price, Qty: models.QtyFromScaled(qty),
			FeeAsset: strPtr("quote"), ClientOrderID: &coid,
		}
		res, err := client.Orders.Create(ctx, req, nil)
		if err == nil {
			fmt.Printf("LIMIT SELL %s qty=%d @ %s postOnly=%v status=%s order=%s\n", symbol, qtyScaled, priceStr, postOnly, res.Status, res.OrderID)
			return
		}
		fmt.Printf("attempt %d postOnly=%v err: %v\n", attempt, postOnly, err)
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "post") || strings.Contains(msg, "cross") || strings.Contains(msg, "take") {
			postOnly = false
			continue
		}
		time.Sleep(2 * time.Second)
	}
	os.Exit(1)
}
