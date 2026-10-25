// Command sell performs a ONE-SHOT market-IOC sell of the entire trading
// balance of one base asset (e.g. "LTC-USDT" to convert LTC -> USDT). It goes
// through the same guarded execution path as the bot (journal, preview,
// retries, fill observation).
//
// Usage:
//
//	go run ./cmd/sell LTC-USDT
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/chain"
	"github.com/Fabric-Labs/polyester-sdk-go/codecs"

	"polyester-momentum-bot/internal/execution"
	"polyester-momentum-bot/internal/num"
)

type journalAdapter struct{ j *sellJournal }

func (a journalAdapter) Append(e execution.Entry) error {
	a.j.entries = append(a.j.entries, e.Kind)
	return nil
}

type sellJournal struct{ entries []string }

func main() {
	if len(os.Args) != 2 {
		fmt.Println("usage: go run ./cmd/sell <SYMBOL>   (e.g. LTC-USDT)")
		os.Exit(2)
	}
	symbol := strings.ToUpper(os.Args[1])

	kid, priv := readCreds()
	toolEnv := chain.PolyesterTestnetEnvironment
	toolEnv.APIURL = "https://api.testnet.polyester.com"
	client, err := polyester.New(polyester.Config{
		Environment: &toolEnv, APIKeyID: kid, APIPrivateKey: priv, HydrateCatalogs: true,
	})
	if err != nil {
		fmt.Println("client:", err)
		os.Exit(1)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := client.WaitForCatalogs(ctx); err != nil {
		fmt.Println("catalogs:", err)
		os.Exit(1)
	}

	mi, err := marketInfo(ctx, client, symbol)
	if err != nil {
		fmt.Println("market info:", err)
		os.Exit(1)
	}

	bal, err := client.Balances.List(ctx, nil, nil)
	if err != nil {
		fmt.Println("balances:", err)
		os.Exit(1)
	}
	var baseScaled int64
	for _, b := range bal.Balances {
		if b.AssetID == mi.BaseAssetID {
			f, err := codecs.FormatLedgerU128(b.Trading, codecs.LedgerScale)
			if err != nil {
				fmt.Println("decode balance:", err)
				os.Exit(1)
			}
			baseScaled, err = num.ParseDecimalToScaled(f, mi.BaseScale)
			if err != nil {
				fmt.Println("parse balance:", err)
				os.Exit(1)
			}
		}
	}
	if baseScaled < mi.MinQtyScaled {
		fmt.Printf("tidak ada yang dijual: saldo %s di bawah minimum %s\n",
			num.FormatScaled(baseScaled, mi.BaseScale), num.FormatScaled(mi.MinQtyScaled, mi.BaseScale))
		return
	}

	sj := &sellJournal{}
	exec := execution.NewLiveExecutor(client, mi, journalAdapter{j: sj}, nil, 50, 15)
	fill, err := exec.Sell(ctx, baseScaled, fmt.Sprintf("pmom-manual-sell-%d", time.Now().Unix()))
	if err != nil {
		fmt.Println("SELL GAGAL:", err)
		os.Exit(1)
	}
	fmt.Printf("SOLD %s %s @ %s (status=%s partial=%v order=%s)\n",
		num.FormatScaled(fill.BaseQtyScaled, mi.BaseScale), symbol,
		num.FormatScaled(fill.AvgPriceScaled6, 6), fill.Status, fill.Partial, fill.OrderID)

	if bal2, err := client.Balances.List(ctx, nil, nil); err == nil {
		for _, b := range bal2.Balances {
			if b.AssetID == mi.QuoteAssetID {
				t, _ := codecs.FormatLedgerU128(b.Trading, codecs.LedgerScale)
				fmt.Printf("saldo trading %s sekarang: %s\n", strings.Split(symbol, "-")[1], t)
			}
		}
	}
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

// marketInfo mirrors the bot's resolution for one symbol.
func marketInfo(ctx context.Context, client *polyester.Client, symbol string) (execution.MarketInfo, error) {
	spot, err := client.MarketData.GetSpotConfig(ctx)
	if err != nil {
		return execution.MarketInfo{}, err
	}
	var pair map[string]any
	for _, key := range []string{"pairs", "symbols"} {
		rawList, _ := spot.Raw[key].([]any)
		for _, item := range rawList {
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
		return execution.MarketInfo{}, fmt.Errorf("no base scale for %s", symbol)
	}
	quoteScale, ok := client.Catalogs.QuoteQuantityScaleForSymbol(symbol)
	if !ok {
		return execution.MarketInfo{}, fmt.Errorf("no quote scale for %s", symbol)
	}
	pc, ok := client.Catalogs.PairConstraintsForSymbol(symbol)
	if !ok {
		return execution.MarketInfo{}, fmt.Errorf("no constraints for %s", symbol)
	}
	baseID := ledgerID(client, pair, symbol, true)
	quoteID := ledgerID(client, pair, symbol, false)
	if baseID == 0 || quoteID == 0 {
		return execution.MarketInfo{}, fmt.Errorf("asset id tidak ketemu untuk %s", symbol)
	}
	minNotional, _ := num.ParseDecimalToScaled(pc.MinNotionalQuote, quoteScale)
	return execution.MarketInfo{
		Symbol: symbol, BaseScale: baseScale, QuoteScale: quoteScale,
		TickTicks: pc.TickSizeTicks, MinQtyScaled: pc.MinQtyScaled,
		MinNotionalQuoteScaled: minNotional, BaseAssetID: baseID, QuoteAssetID: quoteID,
		WireScale: 9,
	}, nil
}

// ledgerID resolves the base/quote asset ledger id for the pair, using the
// pair's asset symbols with the same fallback convention as the bot.
func ledgerID(client *polyester.Client, pair map[string]any, symbol string, base bool) uint32 {
	var fields []string
	if base {
		fields = []string{"baseAsset", "base_asset"}
	} else {
		fields = []string{"quoteAsset", "quote_asset"}
	}
	sym := ""
	for _, k := range fields {
		if v, ok := pair[k].(string); ok && v != "" {
			sym = v
			break
		}
	}
	if sym == "" {
		parts := strings.SplitN(symbol, "-", 2)
		if base && len(parts) > 0 {
			sym = parts[0]
		}
		if !base && len(parts) == 2 {
			sym = parts[1]
		}
	}
	if id := client.Catalogs.LedgerIDForAsset(sym); id != nil {
		return *id
	}
	return 0
}
