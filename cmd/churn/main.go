// Command churn is a pure auto-trading loop with NO signals: it market-buys
// a fixed quote budget, immediately market-sells the fill, rests, and moves
// to the next pair — round-robin across every enabled USDT pair on the
// exchange. It stops after a fixed number of cycles.
//
// Usage (env or flags):
//
//	CHURN_BUDGET=55    quote budget per cycle (default 55)
//	CHURN_CYCLES=15    number of buy->sell cycles before stopping (default 15)
//	CHURN_REST_SEC=2   rest between cycles (default 2)
//	SYMBOLS=all        comma list, or "all" for every enabled USDT pair
//
// Example:
//
//	go run ./cmd/churn
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/codecs"

	"polyester-momentum-bot/internal/execution"
	"polyester-momentum-bot/internal/market"
	"polyester-momentum-bot/internal/num"
)

type stdoutJournal struct{}

func (stdoutJournal) Append(e execution.Entry) error {
	fmt.Printf("    [journal] %s id=%s%s\n", e.Kind, e.ClientOrderID, e.ClientTriggerID)
	return nil
}

func main() {
	budgetF := envF("CHURN_BUDGET", 55)
	cycles := int(envF("CHURN_CYCLES", 15))
	rest := time.Duration(envF("CHURN_REST_SEC", 2)) * time.Second
	symbolsRaw := strings.ToUpper(strings.TrimSpace(os.Getenv("SYMBOLS")))
	if symbolsRaw == "" {
		symbolsRaw = "all"
	}

	kid, priv := readCreds()
	client, err := polyester.New(polyester.Config{
		Environment: &polyester.TestnetEnvironment, APIKeyID: kid, APIPrivateKey: priv, HydrateCatalogs: true,
	})
	if err != nil {
		fmt.Println("client:", err)
		os.Exit(1)
	}
	defer client.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := client.WaitForCatalogs(ctx); err != nil {
		fmt.Println("catalogs:", err)
		os.Exit(1)
	}

	spot, err := client.MarketData.GetSpotConfig(ctx)
	if err != nil {
		fmt.Println("spot config:", err)
		os.Exit(1)
	}
	symbols := resolveSymbols(spot.Raw, symbolsRaw)
	if len(symbols) == 0 {
		fmt.Println("tidak ada symbol yang valid")
		os.Exit(1)
	}

	wireScale := int(envF("POLYESTER_PRICE_SCALE", 0))
	if wireScale == 0 {
		w, err := market.DetectWirePriceScale(ctx, client, symbols[0], "1m", log.Default())
		if err != nil {
			fmt.Println("price scale detect:", err)
			os.Exit(1)
		}
		wireScale = w
	}
	fmt.Printf("=== CHURN AUTO-TRADE: %d siklus x $%.0f, istirahat %s, %d pair ===\n",
		cycles, budgetF, rest, len(symbols))
	fmt.Println(strings.Join(symbols, " "))

	startUSDT := quoteBalance(ctx, client, 1)
	fmt.Printf("saldo USDT awal: %.2f\n", startUSDT)
	fmt.Println("(Ctrl+C untuk berhenti — siklus berjalan terus: 15 siklus -> jeda -> ulangi)")

	var totalPnL float64
	startWall := time.Now()
	seq := 0
	for batch := 1; ; batch++ {
		batchStartUSDT := quoteBalance(ctx, client, 1)
		batchStart := time.Now()
		fmt.Printf("\n===== BATCH %d — %d siklus =====\n", batch, cycles)
		done := 0
		for i := 0; i < cycles; i++ {
			if ctx.Err() != nil {
				break
			}
			symbol := symbols[seq%len(symbols)]
			seq++
			fmt.Printf("\n[%2d/%d] %s\n", i+1, cycles, symbol)

			mi, err := marketInfo(ctx, client, symbol, wireScale)
			if err != nil {
				fmt.Printf("  skip: %v\n", err)
				continue
			}
			budget, err := num.ParseDecimalToScaled(fmt.Sprintf("%.2f", budgetF), mi.QuoteScale)
			if err != nil {
				fmt.Printf("  skip: budget: %v\n", err)
				continue
			}
			exec := execution.NewLiveExecutor(client, mi, stdoutJournal{}, nil, 50, 15)
			coid := fmt.Sprintf("pmom-churn-%d-%d", time.Now().Unix(), i)

			buy, err := exec.Buy(ctx, budget, coid+"-b")
			if err != nil {
				fmt.Printf("  BUY gagal: %v\n", err)
				continue
			}
			fmt.Printf("  BUY  %s @ %s (status=%s)\n",
				num.FormatScaled(buy.BaseQtyScaled, mi.BaseScale),
				num.FormatScaled(buy.AvgPriceScaled6, 6), buy.Status)

			sell, err := exec.Sell(ctx, buy.BaseQtyScaled, coid+"-s")
			if err != nil {
				if strings.Contains(err.Error(), "dust") {
					fmt.Println("  sisa dust — lanjut siklus berikutnya")
					continue
				}
				fmt.Printf("  SELL gagal: %v (posisi %s tersisa — jalankan cmd/sell %s untuk membersihkan)\n",
					err, num.FormatScaled(buy.BaseQtyScaled, mi.BaseScale), symbol)
				done++
				break
			}
			pnl := (num.ScaledToFloat(sell.AvgPriceScaled6, 6) - num.ScaledToFloat(buy.AvgPriceScaled6, 6)) *
				num.ScaledToFloat(buy.BaseQtyScaled, mi.BaseScale)
			totalPnL += pnl
			fmt.Printf("  SELL %s @ %s (status=%s)  PnL siklus: %.4f\n",
				num.FormatScaled(sell.BaseQtyScaled, mi.BaseScale),
				num.FormatScaled(sell.AvgPriceScaled6, 6), sell.Status, pnl)
			done++

			if i < cycles-1 {
				time.Sleep(rest)
			}
		}
		batchEndUSDT := quoteBalance(ctx, client, 1)
		fmt.Printf("\n===== BATCH %d selesai: %d siklus, %s, delta USDT %.2f =====\n",
			batch, done, time.Since(batchStart).Round(time.Second), batchEndUSDT-batchStartUSDT)
		if ctx.Err() != nil {
			break
		}
		fmt.Printf("istirahat %s, lanjut batch %d...\n", rest, batch+1)
		time.Sleep(rest)
	}

	endUSDT := quoteBalance(ctx, client, 1)
	fmt.Printf("\n=== CHURN DIHENTIKAN setelah %s ===\n", time.Since(startWall).Round(time.Second))
	fmt.Printf("saldo USDT akhir: %.2f (delta %.2f) | akumulasi PnL siklus: %.4f\n",
		endUSDT, endUSDT-startUSDT, totalPnL)
}

// marketInfo mirrors the bot's pair resolution for one symbol.
func marketInfo(ctx context.Context, client *polyester.Client, symbol string, wireScale int) (execution.MarketInfo, error) {
	spot, err := client.MarketData.GetSpotConfig(ctx)
	if err != nil {
		return execution.MarketInfo{}, err
	}
	var pair map[string]any
	for _, key := range []string{"pairs", "symbols"} {
		list, _ := spot.Raw[key].([]any)
		for _, item := range list {
			if p, ok := item.(map[string]any); ok && p["symbol"] == symbol {
				pair = p
			}
		}
	}
	if pair == nil {
		return execution.MarketInfo{}, fmt.Errorf("pair %s tidak ada di spot config", symbol)
	}
	baseScale, ok := client.Catalogs.BaseQuantityScaleForSymbol(symbol)
	if !ok {
		return execution.MarketInfo{}, fmt.Errorf("no base scale")
	}
	quoteScale, ok := client.Catalogs.QuoteQuantityScaleForSymbol(symbol)
	if !ok {
		return execution.MarketInfo{}, fmt.Errorf("no quote scale")
	}
	pc, ok := client.Catalogs.PairConstraintsForSymbol(symbol)
	if !ok {
		return execution.MarketInfo{}, fmt.Errorf("no constraints")
	}
	baseID, quoteID := uint32(0), uint32(0)
	if id := client.Catalogs.LedgerIDForAsset(assetSymbol(pair, symbol, true)); id != nil {
		baseID = *id
	}
	if id := client.Catalogs.LedgerIDForAsset(assetSymbol(pair, symbol, false)); id != nil {
		quoteID = *id
	}
	if baseID == 0 || quoteID == 0 {
		return execution.MarketInfo{}, fmt.Errorf("asset id tidak ketemu")
	}
	minNotional, _ := num.ParseDecimalToScaled(pc.MinNotionalQuote, quoteScale)
	return execution.MarketInfo{
		Symbol: symbol, SymbolID: pc.SymbolID, BaseScale: baseScale, QuoteScale: quoteScale,
		TickTicks: pc.TickSizeTicks, MinQtyScaled: pc.MinQtyScaled,
		MinNotionalQuoteScaled: minNotional, BaseAssetID: baseID, QuoteAssetID: quoteID,
		WireScale: wireScale,
	}, nil
}

func assetSymbol(pair map[string]any, symbol string, base bool) string {
	keys := []string{"quoteAsset", "quote_asset"}
	if base {
		keys = []string{"baseAsset", "base_asset"}
	}
	for _, k := range keys {
		if v, ok := pair[k].(string); ok && v != "" {
			return v
		}
	}
	parts := strings.SplitN(symbol, "-", 2)
	if base {
		return parts[0]
	}
	if len(parts) == 2 {
		return parts[1]
	}
	return "USDT"
}

func envF(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return def
}

func quoteBalance(ctx context.Context, client *polyester.Client, quoteAssetID uint32) float64 {
	bal, err := client.Balances.List(ctx, nil, nil)
	if err != nil {
		return 0
	}
	for _, b := range bal.Balances {
		if b.AssetID == quoteAssetID {
			s, _ := codecs.FormatLedgerU128(b.Trading, codecs.LedgerScale)
			f, _ := strconv.ParseFloat(s, 64)
			return f
		}
	}
	return 0
}

func resolveSymbols(spotRaw map[string]any, raw string) []string {
	if raw != "ALL" {
		var out []string
		for _, part := range strings.Split(raw, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	var out []string
	for _, key := range []string{"pairs", "symbols"} {
		list, _ := spotRaw[key].([]any)
		for _, item := range list {
			p, ok := item.(map[string]any)
			if !ok {
				continue
			}
			sym, _ := p["symbol"].(string)
			st, _ := p["status"].(string)
			if sym == "" || st == "PAIR_STATUS_DISABLED" {
				continue
			}
			if strings.HasSuffix(sym, "-USDT") && sym != "USDC-USDT" {
				out = append(out, sym)
			}
		}
	}
	return out
}

func readCreds() (string, string) {
	var kid, priv string
	for _, p := range []string{".env", "/Users/jazeerahusna/.zcode/workspace/default/polyester-momentum-bot/.env"} {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "API_KEY_ID=") {
				kid = strings.TrimPrefix(line, "API_KEY_ID=")
			}
			if strings.HasPrefix(line, "API_PRIVATE_KEY=") {
				priv = strings.TrimPrefix(line, "API_PRIVATE_KEY=")
			}
		}
		if kid != "" && priv != "" {
			return kid, priv
		}
	}
	fmt.Println("credentials tidak ditemukan di .env")
	os.Exit(1)
	return "", ""
}
