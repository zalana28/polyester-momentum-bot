// Command dip is a maker mean-reversion bot for Polyester testnet.
//
// Strategy: place a POST-ONLY limit buy a few bps BELOW the mid price (buying
// the dip as a maker, fee 0.10% instead of taker 0.15%), and once filled place
// a post-only limit sell ~60 bps above entry (selling the bounce). A
// server-side stop-loss trigger protects inventory. The bot rotates across
// all enabled USDT pairs, one cycle at a time.
//
// Profit math per winning cycle (defaults, $300 budget):
//
//	entry  = mid - 30 bps, maker fee 0.10%
//	target = entry + 60 bps, maker fee 0.10%
//	net    ~ +40 bps = +$1.20 per cycle
//	SL     = entry - 120 bps, net ~ -$4.50 per losing cycle
//
// All cycles are journaled and exported to state-dip/trades.csv.
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/codecs"
	"github.com/Fabric-Labs/polyester-sdk-go/models"

	"polyester-momentum-bot/internal/journal"
	"polyester-momentum-bot/internal/market"
	"polyester-momentum-bot/internal/num"
)

type pairInfo struct {
	Symbol       string
	BaseScale    int
	QuoteScale   int
	TickTicks    int64
	StepS6       int64
	MinQtyS6     int64
	MinNotional  int64
	BaseAssetID  uint32
	QuoteAssetID uint32
	WireShift    int64
}

type orderSnap struct {
	status string
	cum    int64
	avg    int64
}

var (
	client *polyester.Client
	jrnl   *journal.Journal
	pairs  = map[string]pairInfo{}
	rota   []string
	netPnL = 0.0
)

func envF(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return def
}

func envI(key string, def int) int { return int(envF(key, float64(def))) }

func ts() string { return time.Now().UTC().Format("15:04:05") }

func logf(format string, a ...any) {
	fmt.Printf("[%s] "+format+"\n", append([]any{ts()}, a...)...)
}

func strPtr(s string) *string { return &s }

func f6(v int64) string { return num.FormatScaled(v, 6) }

func jrnlEntry(kind, coid string, detail map[string]string) journal.Entry {
	return journal.Entry{Kind: kind, ClientOrderID: coid, Detail: detail}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	jrnl, err = journal.Open("state-dip")
	if err != nil {
		fmt.Println("journal:", err)
		os.Exit(1)
	}
	defer jrnl.Close()

	kid, priv := readCreds()
	client, err = polyester.New(polyester.Config{
		Environment:     &polyester.TestnetEnvironment,
		APIKeyID:        kid,
		APIPrivateKey:   priv,
		HydrateCatalogs: true,
	})
	if err != nil {
		fmt.Println("client:", err)
		os.Exit(1)
	}
	defer client.Close()
	if err := client.WaitForCatalogs(ctx); err != nil {
		fmt.Println("catalogs:", err)
		os.Exit(1)
	}

	spot, err := client.MarketData.GetSpotConfig(ctx)
	if err != nil {
		fmt.Println("spot config:", err)
		os.Exit(1)
	}
	symbols := resolveSymbols(spot.Raw, strings.ToUpper(strings.TrimSpace(os.Getenv("DIP_SYMBOLS"))))
	wireScale := int(envF("POLYESTER_PRICE_SCALE", 0))
	for _, sym := range symbols {
		info, perr := pairInfoFor(spot.Raw, sym, wireScale)
		if perr != nil {
			logf("skip %s: %v", sym, perr)
			continue
		}
		pairs[sym] = info
		rota = append(rota, sym)
	}
	if len(pairs) == 0 {
		fmt.Println("tidak ada pair valid")
		os.Exit(1)
	}
	if wireScale == 0 {
		ws, werr := market.DetectWirePriceScale(ctx, client, rota[0], "5m", nil)
		if werr != nil {
			fmt.Println("price scale:", werr)
			os.Exit(1)
		}
		wireScale = ws
	}
	for sym, info := range pairs {
		info.WireShift = int64(wireScale - 6)
		pairs[sym] = info
	}

	startUS := usdtBalance(ctx)
	logf("DIP maker mean-reversion: budget $%.0f, offset %dbps, target +%dbps, SL -%dbps, %d pair",
		envF("DIP_BUDGET", 300), envI("DIP_OFFSET_BPS", 30), envI("DIP_TARGET_BPS", 60),
		envI("DIP_SL_BPS", 120), len(rota))
	logf("pairs: %s", strings.Join(rota, " "))
	logf("saldo USDT awal: %.2f", startUS)

	maxLoss := envF("DIP_MAX_LOSS", 25)
	cycleNo := 0
	for {
		if ctx.Err() != nil {
			break
		}
		if netPnL <= -maxLoss {
			logf("cumulative net PnL %.2f <= -%.0f - berhenti (loss cap)", netPnL, maxLoss)
			break
		}
		cycleNo++
		sym := rota[(cycleNo-1)%len(rota)]
		if rerr := runCycle(ctx, sym, cycleNo); rerr != nil {
			logf("cycle %s: %v", sym, rerr)
		}
		if ctx.Err() != nil {
			break
		}
		time.Sleep(time.Duration(envI("DIP_REST_SEC", 2)) * time.Second)
	}
	endUS := usdtBalance(ctx)
	logf("dip berhenti. saldo USDT akhir: %.2f (delta %.2f) | cumulative net PnL: %.4f",
		endUS, endUS-startUS, netPnL)
}

// runCycle executes one buy-fill then sell-fill cycle on one symbol.
func runCycle(ctx context.Context, symbol string, cycleNo int) error {
	info := pairs[symbol]
	lf := func(format string, a ...any) {
		logf("%s "+format, append([]any{symbol}, a...)...)
	}

	bid, ask, err := bookTop(ctx, info)
	if err != nil {
		return err
	}
	midS6 := (bid + ask) / 2
	offset := int64(envI("DIP_OFFSET_BPS", 30))
	target := int64(envI("DIP_TARGET_BPS", 60))

	var filledS6, avgS6 int64
	reprices := 0
	for {
		entryS6 := tickAlign(midS6*(1-float64(offset)/10000), info.TickTicks)
		qtyS6 := budgetQty(entryS6, info)
		if qtyS6 < info.MinQtyS6 || notional(qtyS6, entryS6, info) < info.MinNotional {
			return fmt.Errorf("budget di bawah minimum pair")
		}
		coidBuy := fmt.Sprintf("dip-b-%d-%d-%d", time.Now().UnixNano()%1e9, cycleNo, reprices)
		_ = jrnl.Append(jrnlEntry("buy_intent", coidBuy, map[string]string{
			"symbol": symbol, "price": f6(entryS6), "qty": f6(qtyS6)}))
		lf("BUY LIMIT %s @ %s (offset %dbps)", f6(qtyS6), f6(entryS6), offset)

		filledS6, avgS6, err = placeAndAwaitBuy(ctx, symbol, entryS6, qtyS6, coidBuy, info)
		if err == nil {
			break
		}
		if !errors.Is(err, errReprice) {
			return err
		}
		reprices++
		if reprices >= 4 {
			return fmt.Errorf("harga terus lari - skip symbol")
		}
		lf("harga lari - reprice")
		bid, ask, err = bookTop(ctx, info)
		if err != nil {
			return err
		}
		midS6 = (bid + ask) / 2
	}
	_ = jrnl.Append(jrnlEntry("buy_result", "", map[string]string{
		"symbol": symbol, "qty": f6(filledS6), "avg": f6(avgS6)}))
	lf("FILLED %s @ %s", f6(filledS6), f6(avgS6))

	// SL server-side tidak dipakai lagi: trigger SL mereserve base sehingga
	// TP limit sell selalu kena "Insufficient funds" (terbukti live 12:42 UTC).
	// SL dipindah ke sisi bot: cek mid tiap ~6 dtk di loop hold di bawah.
	slID := ""
	slPrice := tickAlign(float64(avgS6)*(1-float64(envI("DIP_SL_BPS", 120))/10000), info.TickTicks)

	targetS6 := tickAlign(float64(avgS6)*(1+float64(target)/10000), info.TickTicks)
	sellCoid := fmt.Sprintf("dip-s-%d-%d", time.Now().UnixNano()%1e9, cycleNo)
	_ = jrnl.Append(jrnlEntry("sell_intent", sellCoid, map[string]string{
		"symbol": symbol, "price": f6(targetS6), "qty": f6(filledS6)}))
	lf("SELL LIMIT %s @ %s (target +%dbps)", f6(filledS6), f6(targetS6), target)
	if err := placeLimitSell(ctx, symbol, targetS6, filledS6, sellCoid); err != nil {
		lf("limit sell gagal (%v) - taker exit", err)
		cancelTrigger(slID)
		return takerExit(ctx, symbol, filledS6, avgS6, targetS6, slID)
	}

	started := time.Now()
	maxHold := time.Duration(envI("DIP_MAX_HOLD_MIN", 45)) * time.Minute
	slChecked := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(2 * time.Second)
		st, err := orderSnapshot(ctx, symbol, sellCoid)
		if err == nil {
			if st.status == "filled" || st.cum >= filledS6 {
				cancelTrigger(slID)
				bookCycle(symbol, avgS6, filledS6, st.avg, "target", 0.001, 0.001)
				return nil
			}
			if st.status == "canceled" {
				rest := filledS6 - st.cum
				if rest >= info.MinQtyS6 {
					sellCoid = fmt.Sprintf("%s-r%d", sellCoid, time.Now().UnixNano()%1000)
					lf("partial %s - re-place sell sisanya (coid baru)", f6(rest))
					_ = placeLimitSell(ctx, symbol, targetS6, rest, sellCoid)
					continue
				}
				cancelTrigger(slID)
				bookCycle(symbol, avgS6, filledS6, st.avg, "partial-done", 0.001, 0.001)
				return nil
			}
		}
		slChecked++
		if slChecked%3 == 0 {
			if b2, a2, berr := bookTop(ctx, info); berr == nil {
				if (b2+a2)/2 <= float64(slPrice)/1e6 {
					cancelOrder(ctx, symbol, sellCoid)
					time.Sleep(400 * time.Millisecond)
					remaining := filledS6
					if st2, gerr := orderSnapshot(ctx, symbol, sellCoid); gerr == nil {
						remaining = filledS6 - st2.cum
					}
					if remaining >= info.MinQtyS6 {
						lf("SL bot-side kena (mid <= %s) - taker exit %s", f6(slPrice), f6(remaining))
						return takerExit(ctx, symbol, remaining, avgS6, targetS6, slID)
					}
					lf("SL bot-side kena, sisa %s di bawah min qty - selesai", f6(remaining))
					return nil
				}
			}
		}
		if time.Since(started) > maxHold {
			cancelOrder(ctx, symbol, sellCoid)
			time.Sleep(400 * time.Millisecond)
			remaining := filledS6
			if st2, gerr := orderSnapshot(ctx, symbol, sellCoid); gerr == nil {
				remaining = filledS6 - st2.cum
			}
			cancelTrigger(slID)
			lf("max hold tercapai - taker exit %s", f6(remaining))
			return takerExit(ctx, symbol, remaining, avgS6, targetS6, slID)
		}
	}
}

var errReprice = errors.New("reprice")

func placeAndAwaitBuy(ctx context.Context, symbol string, entryS6, qtyS6 int64, coid string, info pairInfo) (int64, int64, error) {
	if err := placeLimitBuy(ctx, symbol, entryS6, qtyS6, coid); err != nil {
		return 0, 0, err
	}
	deadline := time.Now().Add(time.Duration(envI("DIP_REPRICE_SEC", 90)) * time.Second)
	for {
		if ctx.Err() != nil {
			return 0, 0, ctx.Err()
		}
		time.Sleep(2 * time.Second)
		st, err := orderSnapshot(ctx, symbol, coid)
		if err != nil {
			continue
		}
		if st.status == "filled" || st.cum >= qtyS6 {
			return qtyS6, avgOrEntry(entryS6, st.avg), nil
		}
		if st.status == "canceled" {
			if st.cum > 0 {
				return st.cum, avgOrEntry(entryS6, st.avg), nil
			}
			return 0, 0, errReprice
		}
		if time.Now().After(deadline) {
			cancelOrder(ctx, symbol, coid)
			time.Sleep(500 * time.Millisecond)
			if st2, gerr := orderSnapshot(ctx, symbol, coid); gerr == nil && st2.cum > 0 {
				return st2.cum, avgOrEntry(entryS6, st2.avg), nil
			}
			return 0, 0, errReprice
		}
	}
}

func avgOrEntry(entryS6, avg int64) int64 {
	if avg > 0 {
		return avg
	}
	return entryS6
}

func takerExit(ctx context.Context, symbol string, qtyS6, avgS6, targetS6 int64, slID string) error {
	cancelTrigger(slID)
	info := pairs[symbol]
	qty, _ := models.NewQtyScaled(qtyS6)
	qty = qty.WithScale(info.BaseScale).WithSymbol(symbol)
	coid := fmt.Sprintf("dip-tk-%d", time.Now().UnixNano()%1e9)
	if _, err := client.Orders.Create(ctx, models.CreateOrderRequest{
		Symbol:        &symbol,
		Side:          "sell",
		OrderType:     "market",
		Qty:           models.QtyFromScaled(qty),
		FeeAsset:      strPtr("quote"),
		ClientOrderID: &coid,
	}, nil); err != nil {
		return fmt.Errorf("taker sell: %w", err)
	}
	exitS6 := int64(0)
	if gr, gerr := client.Orders.Get(ctx, nil, models.OrderKeyByClientID(coid), nil, false, false); gerr == nil && gr.Order != nil {
		exitS6 = wireTicksToTrue6(gr.Order.AvgPx.Ticks(), info.WireShift)
	}
	bookCycle(symbol, avgS6, qtyS6, exitS6, "taker_exit", 0.001, 0.0015)
	logf("%s TAKER EXIT %s @ %s", symbol, f6(qtyS6), f6(exitS6))
	return nil
}

// placeLimitBuy places a post-only GTC limit buy. If the exchange rejects it
// because it would cross, the price is shifted down one tick and retried
// (max 3), after which it is placed without the post-only flag.
func placeLimitBuy(ctx context.Context, symbol string, priceS6, qtyS6 int64, coid string) error {
	info := pairs[symbol]
	price := priceS6
	postOnly := true
	for attempt := 0; attempt < 4; attempt++ {
		wire := price * int64(math.Pow10(int(info.WireShift)))
		pr := models.PriceFromTicksInt(wire)
		qty, _ := models.NewQtyScaled(qtyS6)
		qty = qty.WithScale(info.BaseScale).WithSymbol(symbol)
		req := models.CreateOrderRequest{
			Symbol:        &symbol,
			Side:          "buy",
			OrderType:     "limit",
			TIF:           strPtr("gtc"),
			PostOnly:      postOnly,
			Price:         &pr,
			Qty:           models.QtyFromScaled(qty),
			FeeAsset:      strPtr("quote"),
			ClientOrderID: &coid,
		}
		_, err := client.Orders.Create(ctx, req, nil)
		if err == nil {
			return nil
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "post") || strings.Contains(msg, "would cross") || strings.Contains(msg, "would take") {
			price -= info.TickTicks
			postOnly = false
			continue
		}
		if postOnly {
			postOnly = false
			continue
		}
		return err
	}
	return fmt.Errorf("limit buy gagal setelah retry")
}

// placeLimitSell places a post-only GTC limit sell. If it would cross (price
// rose past our level), it is re-placed WITHOUT post-only so it fills
// immediately at the improved price.
func placeLimitSell(ctx context.Context, symbol string, priceS6, qtyS6 int64, coid string) error {
	info := pairs[symbol]
	price := priceS6
	postOnly := true
	for attempt := 0; attempt < 4; attempt++ {
		wire := price * int64(math.Pow10(int(info.WireShift)))
		pr := models.PriceFromTicksInt(wire)
		qty, _ := models.NewQtyScaled(qtyS6)
		qty = qty.WithScale(info.BaseScale).WithSymbol(symbol)
		req := models.CreateOrderRequest{
			Symbol:        &symbol,
			Side:          "sell",
			OrderType:     "limit",
			TIF:           strPtr("gtc"),
			PostOnly:      postOnly,
			Price:         &pr,
			Qty:           models.QtyFromScaled(qty),
			FeeAsset:      strPtr("quote"),
			ClientOrderID: &coid,
		}
		_, err := client.Orders.Create(ctx, req, nil)
		if err == nil {
			return nil
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "post") || strings.Contains(msg, "would cross") || strings.Contains(msg, "would take") {
			postOnly = false
			continue
		}
		return err
	}
	return fmt.Errorf("limit sell gagal setelah retry")
}

func orderSnapshot(ctx context.Context, symbol, coid string) (orderSnap, error) {
	gr, err := client.Orders.Get(ctx, nil, models.OrderKeyByClientID(coid), nil, false, false)
	if err != nil {
		return orderSnap{}, err
	}
	if gr.Order == nil {
		return orderSnap{}, fmt.Errorf("order tidak ditemukan")
	}
	info := pairs[symbol]
	st := orderSnap{status: gr.Order.Status}
	if gr.Order.CumQty.Scale() != nil {
		st.cum = gr.Order.CumQty.Scaled()
	} else if _, ok := client.Catalogs.BaseQuantityScaleForSymbolID(gr.Order.SymbolID); ok {
		st.cum = gr.Order.CumQty.Scaled()
	}
	st.avg = wireTicksToTrue6(gr.Order.AvgPx.Ticks(), info.WireShift)
	return st, nil
}

func cancelOrder(ctx context.Context, symbol, coid string) {
	_, _ = client.Orders.Cancel(ctx, nil, models.OrderKeyByClientID(coid), &symbol, nil, nil)
}

func armStopLoss(ctx context.Context, symbol string, slPriceS6, qtyS6 int64, coid string, info pairInfo) (string, error) {
	wire := slPriceS6 * int64(math.Pow10(int(info.WireShift)))
	pr := models.PriceFromTicksInt(wire)
	qty, _ := models.NewQtyScaled(qtyS6)
	qty = qty.WithScale(info.BaseScale).WithSymbol(symbol)
	res, err := client.Triggers.Create(ctx, nil, symbol, "stop_loss", &pr, "sell",
		models.QtyFromScaled(qty), "market", nil, "", "", nil, &coid, false,
		codecs.CreateTriggerOptions{SelfTradePreventionMode: strPtr("expire_maker")})
	if err != nil {
		return "", err
	}
	return res.TriggerID, nil
}

func cancelTrigger(id string) {
	if id == "" {
		return
	}
	_, _ = client.Triggers.Cancel(context.Background(), nil, id, nil)
}

// bookTop returns mid price (true6) and best bid from the order book.
func bookTop(ctx context.Context, info pairInfo) (float64, float64, error) {
	ob, err := client.Orderbook.Get(ctx, info.Symbol, 5)
	if err != nil {
		return 0, 0, err
	}
	if len(ob.Bids) == 0 || len(ob.Asks) == 0 {
		return 0, 0, fmt.Errorf("book kosong")
	}
	bid := float64(ob.Bids[0].Price.Ticks()) / math.Pow10(int(info.WireShift))
	ask := float64(ob.Asks[0].Price.Ticks()) / math.Pow10(int(info.WireShift))
	return (bid + ask) / 2, bid, nil
}

// budgetQty converts the quote budget into base qty (true6) at entry price.
func budgetQty(entryS6 int64, info pairInfo) int64 {
	budget := int64(envF("DIP_BUDGET", 300) * math.Pow10(int(info.QuoteScale)))
	if entryS6 <= 0 {
		return 0
	}
	qty := budget * int64(math.Pow10(int(info.BaseScale))) / entryS6
	if info.StepS6 > 0 {
		qty -= qty % info.StepS6
	}
	return qty
}

func notional(qtyS6, entryS6 int64, info pairInfo) int64 {
	return qtyS6 * entryS6 / int64(math.Pow10(int(info.BaseScale)))
}

func tickAlign(priceTrue6 float64, tickTicks int64) int64 {
	p := int64(priceTrue6)
	if tickTicks <= 0 {
		return p
	}
	return p - p%tickTicks
}

func wireTicksToTrue6(wire int64, wireShift int64) int64 {
	if wireShift <= 0 {
		return wire
	}
	return wire / int64(math.Pow10(int(wireShift)))
}

func baseBalance(ctx context.Context, info pairInfo) float64 {
	bal, err := client.Balances.List(ctx, nil, nil)
	if err != nil {
		return -1
	}
	for _, b := range bal.Balances {
		if b.AssetID != info.BaseAssetID {
			continue
		}
		s, err := codecs.FormatLedgerU128(b.Trading, codecs.LedgerScale)
		if err != nil {
			return -1
		}
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	return 0
}

func usdtBalance(ctx context.Context) float64 {
	bal, err := client.Balances.List(ctx, nil, nil)
	if err != nil {
		return 0
	}
	for _, b := range bal.Balances {
		if b.AssetID != 1 { // USDT ledger id
			continue
		}
		s, err := codecs.FormatLedgerU128(b.Trading, codecs.LedgerScale)
		if err != nil {
			return 0
		}
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	return 0
}

// bookCycle records one completed cycle into state-dip/trades.csv.
func bookCycle(symbol string, entryS6, qtyS6, exitS6 int64, reason string, feeIn, feeOut float64) {
	info := pairs[symbol]
	entry := float64(entryS6) / 1e6
	exit := float64(exitS6) / 1e6
	qty := float64(qtyS6) / math.Pow10(int(info.BaseScale))
	gross := (exit - entry) * qty
	fe := entry * qty * feeIn
	fx := exit * qty * feeOut
	net := gross - fe - fx
	netPnL += net

	writeCSV(symbol, entry, qty, exit, gross, fe, fx, net, reason)
	jrnl.Append(jrnlEntry("cycle_done", "", map[string]string{
		"symbol": symbol, "reason": reason, "net": fmt.Sprintf("%.4f", net)}))
	logf("%s cycle done: qty=%.6g entry=%.6g exit=%.6g net=%.4f (total %.4f)",
		symbol, qty, entry, exit, net, netPnL)
}

var csvMu = make(chan struct{}, 1)

func writeCSV(symbol string, entry, qty, exit, gross, fe, fx, net float64, reason string) {
	csvMu <- struct{}{}
	defer func() { <-csvMu }()
	path := "state-dip/trades.csv"
	writeHeader := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeHeader = true
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if writeHeader {
		w.Write([]string{"ts_close", "symbol", "strategy", "entry", "qty", "exit",
			"gross_usdt", "fee_entry_usdt", "fee_exit_usdt", "net_usdt", "exit_reason"})
	}
	w.Write([]string{time.Now().UTC().Format(time.RFC3339), symbol, "dip-maker",
		fmt.Sprintf("%.8g", entry), fmt.Sprintf("%.8g", qty), fmt.Sprintf("%.8g", exit),
		fmt.Sprintf("%.4f", gross), fmt.Sprintf("%.4f", fe), fmt.Sprintf("%.4f", fx),
		fmt.Sprintf("%.4f", net), reason})
	w.Flush()
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

func resolveSymbols(spotRaw map[string]any, raw string) []string {
	if raw != "ALL" && raw != "" {
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

func pairInfoFor(spotRaw map[string]any, symbol string, wireScale int) (pairInfo, error) {
	var pair map[string]any
	for _, key := range []string{"pairs", "symbols"} {
		list, _ := spotRaw[key].([]any)
		for _, item := range list {
			if p, ok := item.(map[string]any); ok && p["symbol"] == symbol {
				pair = p
			}
		}
	}
	if pair == nil {
		return pairInfo{}, fmt.Errorf("pair %s tidak ada", symbol)
	}
	baseScale, ok1 := client.Catalogs.BaseQuantityScaleForSymbol(symbol)
	quoteScale, ok2 := client.Catalogs.QuoteQuantityScaleForSymbol(symbol)
	pc, ok3 := client.Catalogs.PairConstraintsForSymbol(symbol)
	if !ok1 || !ok2 || !ok3 {
		return pairInfo{}, fmt.Errorf("katalog belum lengkap")
	}
	baseID := ledgerID(pair, symbol, true)
	quoteID := ledgerID(pair, symbol, false)
	if baseID == 0 || quoteID == 0 {
		return pairInfo{}, fmt.Errorf("asset id tidak ketemu")
	}
	minNotional, _ := num.ParseDecimalToScaled(pc.MinNotionalQuote, quoteScale)
	return pairInfo{
		Symbol: symbol, BaseScale: baseScale, QuoteScale: quoteScale,
		TickTicks: pc.TickSizeTicks, StepS6: pc.StepSizeScaled, MinQtyS6: pc.MinQtyScaled,
		MinNotional: minNotional, BaseAssetID: baseID, QuoteAssetID: quoteID,
		WireShift: int64(wireScale - 6),
	}, nil
}

func ledgerID(pair map[string]any, symbol string, base bool) uint32 {
	keys := []string{"quoteAsset", "quote_asset"}
	if base {
		keys = []string{"baseAsset", "base_asset"}
	}
	sym := ""
	for _, k := range keys {
		if v, ok := pair[k].(string); ok && v != "" {
			sym = v
			break
		}
	}
	if sym == "" {
		parts := strings.SplitN(symbol, "-", 2)
		if base && len(parts) > 0 {
			sym = parts[0]
		} else if len(parts) == 2 {
			sym = parts[1]
		}
	}
	if id := client.Catalogs.LedgerIDForAsset(sym); id != nil {
		return *id
	}
	return 0
}
