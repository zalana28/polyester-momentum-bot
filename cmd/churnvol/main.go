// Command churn drives buy/sell volume directly against the exchange.
//
// It deliberately trusts balances instead of order acknowledgements: on this
// venue an order can be reported "not found" while it actually filled, so every
// decision here is made from what the account holds before and after each leg.
// A buy that returns an error but leaves base behind is still sold.
//
//	go run ./cmd/churn -env ~/polyester-bot-testnet2/.env -factor 0.95 -gap 3
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/codecs"
	"github.com/Fabric-Labs/polyester-sdk-go/models"
)

const usdtAssetID uint32 = 1

func loadEnv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "env:", err)
		os.Exit(1)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if ok {
			out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return out
}

// balances returns trading amounts per asset id.
func balances(ctx context.Context, c *polyester.Client) (map[uint32]string, error) {
	list, err := c.Balances.List(ctx, nil, nil)
	if err != nil {
		return nil, err
	}
	out := map[uint32]string{}
	for _, b := range list.Balances {
		s, err := codecs.FormatLedgerU128(b.Trading, codecs.LedgerScale)
		if err != nil {
			continue
		}
		out[b.AssetID] = s
	}
	return out, nil
}

func amount(m map[uint32]string, id uint32) float64 {
	s, ok := m[id]
	if !ok {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func scaled(v float64, scale int) int64 {
	p := 1.0
	for i := 0; i < scale; i++ {
		p *= 10
	}
	return int64(v * p)
}

type pairInfo struct {
	symbol string
	scale  int
}

// loadPairs reads base quantity scales from the spot catalog.
func loadPairs(ctx context.Context, c *polyester.Client, only []string) ([]pairInfo, error) {
	spot, err := c.MarketData.GetSpotConfig(ctx)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, s := range only {
		want[strings.ToUpper(s)] = true
	}
	var out []pairInfo
	for _, key := range []string{"pairs", "symbols"} {
		raw, _ := spot.Raw[key].([]any)
		for _, item := range raw {
			p, ok := item.(map[string]any)
			if !ok {
				continue
			}
			sym, _ := p["symbol"].(string)
			if sym == "" || !strings.HasSuffix(sym, "-USDT") {
				continue
			}
			base := strings.TrimSuffix(sym, "-USDT")
			if len(want) > 0 && !want[base] {
				continue
			}
			scaleF, ok := p["base_quantity_scale"].(float64)
			if !ok {
				continue
			}
			out = append(out, pairInfo{symbol: sym, scale: int(scaleF)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].symbol < out[j].symbol })
	return out, nil
}

func main() {
	envPath := flag.String("env", ".env", "path to .env with API credentials")
	factor := flag.Float64("factor", 0.95, "share of available USDT per buy")
	gap := flag.Int("gap", 3, "seconds between cycles")
	stopFile := flag.String("stop", "/tmp/polyester-churn.stop", "stop flag file")
	pairsFlag := flag.String("pairs", "", "comma-separated bases (default: all -USDT pairs)")
	flag.Parse()

	env := loadEnv(*envPath)
	acct := env["ACCOUNT_ID"]
	c, err := polyester.New(polyester.Config{
		APIURL: "https://api.testnet.polyester.com", WSURL: "wss://api.testnet.polyester.com",
		APIKeyID: env["API_KEY_ID"], APIPrivateKey: env["API_PRIVATE_KEY"],
		DefaultAccountID: &acct, HydrateCatalogs: true, Timeout: 20 * time.Second,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "client:", err)
		os.Exit(1)
	}
	defer c.Close()

	ctx := context.Background()
	var only []string
	if *pairsFlag != "" {
		only = strings.Split(*pairsFlag, ",")
	}
	pairs, err := loadPairs(ctx, c, only)
	if err != nil || len(pairs) == 0 {
		fmt.Fprintln(os.Stderr, "pairs:", err)
		os.Exit(1)
	}
	fmt.Printf("churn start: %d pairs, factor %.2f, gap %ds\n", len(pairs), *factor, *gap)

	cycles, failures := 0, 0
	for i := 0; ; i++ {
		if _, err := os.Stat(*stopFile); err == nil {
			fmt.Printf("stop flag found; cycles=%d failures=%d\n", cycles, failures)
			return
		}
		p := pairs[i%len(pairs)]
		before, err := balances(ctx, c)
		if err != nil {
			fmt.Println("balances:", err)
			time.Sleep(3 * time.Second)
			continue
		}
		avail := amount(before, usdtAssetID)
		budget := avail * *factor
		if budget < 5 {
			fmt.Printf("available %.2f USDT too small; stop\n", avail)
			return
		}
		fee := "quote"
		var slip int32 = 100
		coid := fmt.Sprintf("churn-b-%d", time.Now().UnixNano())
		sym := p.symbol
		buyReq := models.CreateOrderRequest{
			Symbol: &sym, Side: "buy", OrderType: "market",
			FeeAsset: &fee, MaxSlippageBps: &slip, ClientOrderID: &coid,
			MaxQuoteDebitScaled: models.QtyFromQuoteScaled(scaled(budget, 6), 6),
		}
		_, buyErr := c.Orders.Create(ctx, buyReq, nil)

		// balances decide whether the buy landed, not the acknowledgement
		var baseID uint32
		var baseQty float64
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(600 * time.Millisecond)
			cur, err := balances(ctx, c)
			if err != nil {
				continue
			}
			for id, s := range cur {
				if id == usdtAssetID {
					continue
				}
				delta := amount(cur, id) - amount(before, id)
				if delta > 0 {
					baseID, baseQty = id, delta
					_ = s
				}
			}
			if baseQty > 0 {
				break
			}
		}

		if baseQty <= 0 {
			failures++
			fmt.Printf("%s buy: err=%v, no base observed (usdt %.2f)\n", sym, buyErr, avail)
			time.Sleep(time.Duration(*gap) * time.Second)
			continue
		}

		sellReq := models.CreateOrderRequest{
			Symbol: &sym, Side: "sell", OrderType: "market",
			FeeAsset: &fee, MaxSlippageBps: &slip,
			ClientOrderID: stringPtr(fmt.Sprintf("churn-s-%d", time.Now().UnixNano())),
			Qty:           models.QtyFromScaled(models.MustQtyScaled(scaled(baseQty, p.scale)).WithScale(p.scale).WithSymbol(sym)),
		}
		_, sellErr := c.Orders.Create(ctx, sellReq, nil)

		// wait until the base is gone; report leftovers instead of hiding them
		var leftover float64
		deadline = time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(700 * time.Millisecond)
			cur, err := balances(ctx, c)
			if err != nil {
				continue
			}
			leftover = amount(cur, baseID) - amount(before, baseID)
			if leftover <= 0.00000001 {
				break
			}
		}
		after, _ := balances(ctx, c)
		usdtAfter := amount(after, usdtAssetID)
		cycles++
		if leftover > 0.00000001 {
			failures++
		}
		fmt.Printf("%s cycle %d: budget %.2f baseID %d qty %.8f sellErr=%v usdt %.2f -> %.2f (delta %+.2f) leftover %.8f\n",
			sym, cycles, budget, baseID, baseQty, sellErr, avail, usdtAfter, usdtAfter-avail, leftover)
		time.Sleep(time.Duration(*gap) * time.Second)
	}
}

func stringPtr(s string) *string { return &s }
