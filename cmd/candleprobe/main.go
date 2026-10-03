// Command candleprobe prints raw candle strings and trade prices from the
// testnet REST API to diagnose price scaling.
package main

import (
	"context"
	"fmt"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
)

func main() {
	client, err := polyester.New(polyester.Config{
		Environment:     &polyester.TestnetEnvironment,
		HydrateCatalogs: true,
	})
	if err != nil {
		panic(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.WaitForCatalogs(ctx); err != nil {
		panic(err)
	}

	for _, sym := range []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"} {
		res, err := client.MarketData.GetCandles(ctx, &sym, nil, "1m", 1, nil, nil, true)
		if err != nil {
			fmt.Printf("%s: candle error: %v\n", sym, err)
			continue
		}
		if len(res.Candles) > 0 {
			c := res.Candles[0]
			fmt.Printf("%s: ts=%d open=%q close=%q vol=%q qvol=%q\n", sym, c.TsSec, c.Open, c.Close, c.Volume, c.QuoteVolume)
		}
		tr, err := client.MarketData.GetTrades(ctx, &sym, nil, 1, nil)
		if err != nil {
			fmt.Printf("%s: trade error: %v\n", sym, err)
			continue
		}
		if len(tr.Trades) > 0 {
			t := tr.Trades[0]
			fmt.Printf("%s: trade price_ticks=%d -> ticks/1e6=%f qty_scaled=%d\n",
				sym, t.Price.Ticks(), float64(t.Price.Ticks())/1e6, t.Qty.Scaled())
		}
	}
}
