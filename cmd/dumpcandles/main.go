// dumpcandles mengekspor candle historis (default 5m) semua pair USDT ke
// tools/lab/candles/<SYM>.json untuk lab backtest Python. Harga dikonversi
// ke true price (wire scale dideteksi empiris), volume dalam base unit.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"polyester-momentum-bot/internal/market"
)

type outCandle struct {
	Ts int64   `json:"ts"`
	O  float64 `json:"o"`
	H  float64 `json:"h"`
	L  float64 `json:"l"`
	C  float64 `json:"c"`
	V  float64 `json:"v"`
}

func main() {
	days := 10
	if v := os.Getenv("DUMP_DAYS"); v != "" {
		fmt.Sscan(v, &days)
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
	client, err := polyester.New(polyester.Config{Environment: &polyester.TestnetEnvironment, APIKeyID: kid, APIPrivateKey: priv, HydrateCatalogs: true})
	if err != nil {
		fmt.Println("client:", err)
		os.Exit(1)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := client.WaitForCatalogs(ctx); err != nil {
		fmt.Println("catalogs:", err)
		os.Exit(1)
	}
	spot, err := client.MarketData.GetSpotConfig(ctx)
	if err != nil {
		fmt.Println("spot:", err)
		os.Exit(1)
	}
	var symbols []string
	if env := strings.ToUpper(strings.TrimSpace(os.Getenv("DIP_SYMBOLS"))); env != "" && env != "ALL" {
		symbols = strings.Split(env, ",")
	} else {
		if pairs, ok := spot.Raw["pairs"].([]any); ok {
			for _, p := range pairs {
				pm, _ := p.(map[string]any)
				if s, _ := pm["symbol"].(string); strings.HasSuffix(s, "-USDT") {
					symbols = append(symbols, s)
				}
			}
		}
	}
	sort.Strings(symbols)
	ws, err := market.DetectWirePriceScale(ctx, client, symbols[0], "5m", nil)
	if err != nil {
		fmt.Println("wire scale:", err)
		os.Exit(1)
	}
	div := math.Pow10(ws - 6)
	fmt.Printf("wire scale W=%d (bagi harga dengan 10^%d)\n", ws, ws-6)

	for _, sym := range symbols {
		candles := map[int64]outCandle{}
		end := time.Now()
		for len(candles) < days*288 {
			res, err := client.MarketData.GetCandles(ctx, &sym, nil, "5m", 500, nil, &end, false)
			if err != nil {
				fmt.Printf("%s: %v\n", sym, err)
				break
			}
			if len(res.Candles) == 0 {
				break
			}
			for _, c := range res.Candles {
				if c.Close == "" || c.Open == "" {
					continue
				}
				var o, h, l, cl, v float64
				fmt.Sscan(c.Open, &o)
				fmt.Sscan(c.High, &h)
				fmt.Sscan(c.Low, &l)
				fmt.Sscan(c.Close, &cl)
				if c.Volume != "" {
					fmt.Sscan(c.Volume, &v)
				}
				candles[c.TsSec] = outCandle{Ts: c.TsSec, O: o / div, H: h / div, L: l / div, C: cl / div, V: v}
			}
			oldest := res.Candles[0].TsSec
			for _, c := range res.Candles {
				if c.TsSec < oldest {
					oldest = c.TsSec
				}
			}
			if len(res.Candles) < 2 {
				break
			}
			end = time.Unix(oldest, 0)
		}
		list := make([]outCandle, 0, len(candles))
		for _, c := range candles {
			list = append(list, c)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Ts < list[j].Ts })
		path := fmt.Sprintf("/Users/jazeerahusna/.zcode/workspace/default/polyester-momentum-bot/tools/lab/candles/%s.json", sym)
		b, _ := json.MarshalIndent(map[string]any{"symbol": sym, "timeframe": "5m", "wire_scale": ws, "candles": list}, "", " ")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			fmt.Println("write:", err)
			continue
		}
		fmt.Printf("%s: %d candle -> %s\n", sym, len(list), path)
	}
}
