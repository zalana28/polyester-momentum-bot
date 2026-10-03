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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/codecs"
	"github.com/Fabric-Labs/polyester-sdk-go/models"

	"polyester-momentum-bot/internal/brain"
	"polyester-momentum-bot/internal/journal"
	"polyester-momentum-bot/internal/market"
	"polyester-momentum-bot/internal/num"
	"polyester-momentum-bot/internal/strategy"
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

	br = brain.New(envF("DIP_BRAIN_MIN", 40), time.Duration(envI("BRAIN_COOLDOWN_MIN", 10))*time.Minute)
	loadStats()
	netPnL = st.TotalNet
	logf("otak aktif: mom-score %.0f | stats dimuat: %d siklus (%d W/%d L), net $%.4f",
		envF("DIP_MOM_SCORE", 65), st.Cycles, st.Wins, st.Losses, st.TotalNet)

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
		sym := pickNext(cycleNo)
		if rerr := runCycle(ctx, sym, cycleNo); rerr != nil {
			logf("cycle %s: %v", sym, rerr)
		}
		if ctx.Err() != nil {
			break
		}
		time.Sleep(time.Duration(envI("DIP_REST_SEC", 2)) * time.Second)
	}
	// Shutdown pakai context segar: ctx sinyal sudah canceled, jadi call API
	// dengan ctx lama gagal (penyebab "saldo akhir 0.00" dan order yatim yang
	// nyangkol setelah SIGINT). Semua order sendiri (prefix "dip-") dibatalkan;
	// order pihak lain (mis. rescue-sell-) tidak disentuh.
	sctx, scancel := context.WithTimeout(context.Background(), 45*time.Second)
	cancelOwnOrders(sctx)
	endUS := usdtBalance(sctx)
	scancel()
	logf("dip berhenti. saldo USDT akhir: %.2f (delta %.2f) | cumulative net PnL: %.4f",
		endUS, endUS-startUS, netPnL)
}

// cancelOwnOrders membatalkan semua order terbuka dengan client_order_id
// berprefix "dip-" — janitor untuk order yang tertinggal saat mati.
func cancelOwnOrders(ctx context.Context) {
	open, err := client.Orders.ListOpen(ctx, nil, nil, nil, nil, false, false, nil)
	if err != nil {
		logf("shutdown: gagal list order terbuka (%v)", err)
		return
	}
	n := 0
	for _, o := range open.Orders {
		if !strings.HasPrefix(o.ClientOrderID, "dip-") {
			continue
		}
		if _, err := client.Orders.Cancel(ctx, nil, models.OrderKeyByID(o.OrderID), nil, nil, nil); err == nil {
			n++
		}
	}
	if n > 0 {
		logf("shutdown: %d order sisa dibatalkan", n)
	}
}

// runCycle executes one buy-fill then sell-fill cycle on one symbol.
// runCycle memilih mode per pair: momentum (trend-up + volume surge + skor
// tinggi) atau dip (default). Analisis dari candle 5m via otak + regime.
func runCycle(ctx context.Context, symbol string, cycleNo int) error {
	info := pairs[symbol]
	lf := func(format string, a ...any) {
		logf("%s "+format, append([]any{symbol}, a...)...)
	}
	mv, aerr := analyze(ctx, symbol, info)
	if aerr != nil {
		// API lambat / candle kurang: fallback perilaku dip standar.
		lf("analyze gagal (%v) - mode dip standar", aerr)
		return runDipCycle(ctx, symbol, cycleNo, envI("DIP_OFFSET_BPS", 30))
	}
	switch {
	case mv.regime == strategy.RegimeTrendDown && mv.score < 55:
		bumpSkip(symbol)
		lf("skip: trend-down (score %.0f) - jangan gotong pisau", mv.score)
		return nil
	case mv.regime == strategy.RegimeTrendUp && mv.volSurge && mv.score >= envF("DIP_MOM_SCORE", 65):
		lf("MOMENTUM: regime=%s score=%.0f volSurge=%v atr=%.2f%% - entry IOC",
			mv.regime, mv.score, mv.volSurge, mv.atrPct)
		return runMomentumCycle(ctx, symbol, cycleNo, info)
	default:
		return runDipCycle(ctx, symbol, cycleNo, envI("DIP_OFFSET_BPS", 30))
	}
}

// runDipCycle adalah mode mean-reversion asli: post-only buy di bawah mid.
func runDipCycle(ctx context.Context, symbol string, cycleNo int, offBps int) error {
	info := pairs[symbol]
	lf := func(format string, a ...any) {
		logf("%s "+format, append([]any{symbol}, a...)...)
	}

	bid, ask, err := bookTop(ctx, info)
	if err != nil {
		return err
	}
	midS6 := (bid + ask) / 2
	target := int64(envI("DIP_TARGET_BPS", 60))

	var filledS6, avgS6 int64
	reprices := 0
	for {
		entryS6 := tickAlign(midS6*(1-float64(offBps)/10000), info.TickTicks)
		qtyS6 := budgetQty(entryS6, info)
		if qtyS6 < info.MinQtyS6 || notional(qtyS6, entryS6, info) < info.MinNotional {
			return fmt.Errorf("budget di bawah minimum pair")
		}
		coidBuy := fmt.Sprintf("dip-b-%d-%d-%d", time.Now().UnixNano()%1e9, cycleNo, reprices)
		_ = jrnl.Append(jrnlEntry("buy_intent", coidBuy, map[string]string{
			"symbol": symbol, "price": f6(entryS6), "qty": f6(qtyS6)}))
		lf("BUY LIMIT %s @ %s (offset %dbps)", f6(qtyS6), f6(entryS6), offBps)

		filledS6, avgS6, err = placeAndAwaitBuy(ctx, symbol, entryS6, qtyS6, coidBuy, info)
		if err == nil {
			break
		}
		if !errors.Is(err, errReprice) {
			return err
		}
		reprices++
		if reprices >= 4 {
			bumpSkip(symbol)
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

	// SL server-side tidak dipakai (mereserve base & memblokir TP sell).
	// SL bot-side + manajemen exit ada di holdAndExit.
	slPrice := tickAlign(float64(avgS6)*(1-float64(envI("DIP_SL_BPS", 120))/10000), info.TickTicks)
	targetS6 := tickAlign(float64(avgS6)*(1+float64(target)/10000), info.TickTicks)
	return holdAndExit(ctx, symbol, info, filledS6, avgS6, targetS6, slPrice, "dip-maker", envI("DIP_MAX_HOLD_MIN", 45))
}

// holdAndExit memasang TP limit sell lalu mengelola posisi sampai selesai:
// target maker, SL bot-side (cek mid tiap ~6 dtk), max-hold -> taker exit.
// Dipakai mode dip dan momentum.
func holdAndExit(ctx context.Context, symbol string, info pairInfo, filledS6, avgS6, targetS6, slPrice int64, strat string, maxHoldMin int) error {
	lf := func(format string, a ...any) {
		logf("%s "+format, append([]any{symbol}, a...)...)
	}
	sellCoid := fmt.Sprintf("dip-s-%d", time.Now().UnixNano()%1e9)
	_ = jrnl.Append(jrnlEntry("sell_intent", sellCoid, map[string]string{
		"symbol": symbol, "price": f6(targetS6), "qty": f6(filledS6), "strategy": strat}))
	lf("SELL LIMIT %s @ %s [%s]", f6(filledS6), f6(targetS6), strat)
	if err := placeLimitSell(ctx, symbol, targetS6, filledS6, sellCoid); err != nil {
		lf("limit sell gagal (%v) - taker exit", err)
		return takerExit(ctx, symbol, filledS6, avgS6, targetS6, strat)
	}

	started := time.Now()
	maxHold := time.Duration(maxHoldMin) * time.Minute
	slChecked := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(2 * time.Second)
		st, err := orderSnapshot(ctx, symbol, sellCoid)
		if err == nil {
			if st.status == "filled" || st.cum >= filledS6 {
				bookCycle(symbol, strat, avgS6, filledS6, st.avg, "target", 0.001, 0.001)
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
				bookCycle(symbol, strat, avgS6, filledS6, st.avg, "partial-done", 0.001, 0.001)
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
						return takerExit(ctx, symbol, remaining, avgS6, targetS6, strat)
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
			lf("max hold tercapai - taker exit %s", f6(remaining))
			return takerExit(ctx, symbol, remaining, avgS6, targetS6, strat)
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

func takerExit(ctx context.Context, symbol string, qtyS6, avgS6, targetS6 int64, strat string) error {
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
	bookCycle(symbol, strat, avgS6, qtyS6, exitS6, "taker_exit", 0.001, 0.0015)
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
	return budgetQtyWith(entryS6, envF("DIP_BUDGET", 300), info)
}

func budgetQtyWith(entryS6 int64, budgetF float64, info pairInfo) int64 {
	budget := int64(budgetF * math.Pow10(int(info.QuoteScale)))
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
func bookCycle(symbol, strat string, entryS6, qtyS6, exitS6 int64, reason string, feeIn, feeOut float64) {
	info := pairs[symbol]
	entry := float64(entryS6) / 1e6
	exit := float64(exitS6) / 1e6
	qty := float64(qtyS6) / math.Pow10(int(info.BaseScale))
	gross := (exit - entry) * qty
	fe := entry * qty * feeIn
	fx := exit * qty * feeOut
	net := gross - fe - fx
	netPnL += net

	ps := st.PerPair[symbol]
	ps.Fills++
	if net >= 0 {
		ps.Wins++
	} else {
		ps.Losses++
	}
	ps.Net += net
	st.PerPair[symbol] = ps
	st.TotalNet += net
	st.Cycles++
	if net >= 0 {
		st.Wins++
	} else {
		st.Losses++
	}
	saveStats()

	writeCSV(symbol, strat, entry, qty, exit, gross, fe, fx, net, reason)
	jrnl.Append(jrnlEntry("cycle_done", "", map[string]string{
		"symbol": symbol, "strategy": strat, "reason": reason, "net": fmt.Sprintf("%.4f", net)}))
	logf("%s cycle done [%s]: qty=%.6g entry=%.6g exit=%.6g net=%.4f (total %.4f)",
		symbol, strat, qty, entry, exit, net, netPnL)
}

var csvMu = make(chan struct{}, 1)

func writeCSV(symbol, strat string, entry, qty, exit, gross, fe, fx, net float64, reason string) {
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
	w.Write([]string{time.Now().UTC().Format(time.RFC3339), symbol, strat,
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

// ===================== upgrade: otak, regime, momentum, stats =====================

var (
	br          *brain.Brain
	regimeCache = map[string]regimeEntry{}
	st          dipStats
)

type regimeEntry struct {
	r  strategy.Regime
	at time.Time
}

type pairStat struct {
	Fills  int     `json:"fills"`
	Skips  int     `json:"skips"`
	Wins   int     `json:"wins"`
	Losses int     `json:"losses"`
	Net    float64 `json:"net"`
}

type dipStats struct {
	TotalNet float64             `json:"total_net"`
	Cycles   int                 `json:"cycles"`
	Wins     int                 `json:"wins"`
	Losses   int                 `json:"losses"`
	PerPair  map[string]pairStat `json:"per_pair"`
}

// loadStats memuat statistik lintas-restart; netPnL lanjut dari situ.
func loadStats() {
	st.PerPair = map[string]pairStat{}
	raw, err := os.ReadFile("state-dip/stats.json")
	if err != nil {
		return
	}
	_ = json.Unmarshal(raw, &st)
	if st.PerPair == nil {
		st.PerPair = map[string]pairStat{}
	}
}

func saveStats() {
	raw, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return
	}
	tmp := "state-dip/stats.json.tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, "state-dip/stats.json")
}

func bumpSkip(symbol string) {
	ps := st.PerPair[symbol]
	ps.Skips++
	st.PerPair[symbol] = ps
	saveStats()
}

// mktView adalah ringkasan kondisi pasar satu pair dari candle 5m.
type mktView struct {
	regime   strategy.Regime
	score    float64
	volSurge bool
	atrPct   float64
}

// analyze menarik 120 candle 5m dan menghitung regime (Efficiency Ratio),
// skor otak (drift/RSI/volume/breakout/ROC/chop), lonjakan volume candle
// terakhir, dan ATR14 dalam persen.
func analyze(ctx context.Context, symbol string, info pairInfo) (mktView, error) {
	res, err := client.MarketData.GetCandles(ctx, &symbol, nil, "5m", 120, nil, nil, false)
	if err != nil {
		return mktView{}, err
	}
	div := math.Pow10(int(info.WireShift))
	var sc []strategy.Candle
	var bc []brain.Candle
	var closes, vols []float64
	for _, c := range res.Candles {
		if c.Close == "" {
			continue
		}
		cl, e1 := strconv.ParseFloat(c.Close, 64)
		hi, e2 := strconv.ParseFloat(c.High, 64)
		lo, e3 := strconv.ParseFloat(c.Low, 64)
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		cl, hi, lo = cl/div, hi/div, lo/div
		vol := 0.0
		if c.Volume != "" {
			vol, _ = strconv.ParseFloat(c.Volume, 64)
		}
		sc = append(sc, strategy.Candle{TsSec: c.TsSec, High: hi, Low: lo, Close: cl, Volume: vol})
		bc = append(bc, brain.Candle{TsSec: c.TsSec, High: hi, Low: lo, Close: cl, Volume: vol})
		closes = append(closes, cl)
		vols = append(vols, vol)
	}
	n := len(sc)
	if n < 30 {
		return mktView{}, fmt.Errorf("candle kurang: %d", n)
	}
	mv := mktView{regime: strategy.ClassifyRegime(sc, 5, 15)}
	regimeCache[symbol] = regimeEntry{mv.regime, time.Now()}
	ef := emaSeries(closes, 5)
	es := emaSeries(closes, 15)
	v := br.Evaluate(symbol, bc, ef > es, ef, es)
	mv.score = v.Score
	if n >= 21 {
		mean := 0.0
		for _, x := range vols[n-21 : n-1] {
			mean += x
		}
		mean /= 20
		mv.volSurge = mean > 0 && vols[n-1] >= 1.5*mean
	}
	if n >= 15 {
		trSum := 0.0
		for i := n - 14; i < n; i++ {
			tr := sc[i].High - sc[i].Low
			if i > 0 {
				tr = math.Max(tr, math.Abs(sc[i].High-sc[i-1].Close))
				tr = math.Max(tr, math.Abs(sc[i].Low-sc[i-1].Close))
			}
			trSum += tr
		}
		mv.atrPct = trSum / 14 / closes[n-1] * 100
	}
	return mv, nil
}

func emaSeries(xs []float64, period int) float64 {
	if len(xs) == 0 {
		return 0
	}
	k := 2.0 / float64(period+1)
	e := xs[0]
	for _, x := range xs[1:] {
		e = x*k + e*(1-k)
	}
	return e
}

// pickNext memilih pair berikutnya: choppy/range didahulukan (sana dip sering
// muncul), trend-up menengah (kandidat momentum), trend-down paling akhir.
func pickNext(cycleNo int) string {
	rank := func(s string) int {
		re, ok := regimeCache[s]
		if !ok || time.Since(re.at) > 10*time.Minute {
			return 1 // belum dianalisis: posisi tengah
		}
		switch re.r {
		case strategy.RegimeChoppy, strategy.RegimeRange:
			return 0
		case strategy.RegimeTrendUp:
			return 1
		default:
			return 2
		}
	}
	ord := append([]string(nil), rota...)
	sort.SliceStable(ord, func(i, j int) bool { return rank(ord[i]) < rank(ord[j]) })
	return ord[(cycleNo-1)%len(ord)]
}

// runMomentumCycle: trend-up + volume surge + skor otak tinggi -> beli
// SEKARANG memakai limit-IOC di harga ask (market yang dicap harga, jadi
// slippage maksimal nol tick), lalu exit target maker + SL bot-side +
// max-hold pendek. Ukuran posisi lebih kecil dari mode dip.
func runMomentumCycle(ctx context.Context, symbol string, cycleNo int, info pairInfo) error {
	lf := func(format string, a ...any) {
		logf("%s "+format, append([]any{symbol}, a...)...)
	}
	bid, ask, err := bookTop(ctx, info)
	if err != nil {
		return err
	}
	entryS6 := tickAlign((bid+ask)/2, info.TickTicks)
	qtyS6 := budgetQtyWith(entryS6, envF("DIP_MOM_BUDGET", 500), info)
	if qtyS6 < info.MinQtyS6 || notional(qtyS6, entryS6, info) < info.MinNotional {
		return fmt.Errorf("budget momentum di bawah minimum pair")
	}
	coid := fmt.Sprintf("dip-m-%d-%d", time.Now().UnixNano()%1e9, cycleNo)
	_ = jrnl.Append(jrnlEntry("mom_intent", coid, map[string]string{
		"symbol": symbol, "price": f6(entryS6), "qty": f6(qtyS6)}))
	filledS6, avgS6, err := placeIOCBuy(ctx, symbol, entryS6, qtyS6, coid, info)
	if err != nil {
		bumpSkip(symbol)
		lf("momentum entry gagal (%v)", err)
		return nil
	}
	if filledS6 == 0 {
		bumpSkip(symbol)
		lf("momentum tidak terisi - kemungkinan momentum palsu, lanjut")
		return nil
	}
	lf("MOMENTUM FILLED %s @ %s", f6(filledS6), f6(avgS6))
	targetS6 := tickAlign(float64(avgS6)*(1+float64(envI("DIP_MOM_TARGET_BPS", 60))/10000), info.TickTicks)
	slPrice := tickAlign(float64(avgS6)*(1-float64(envI("DIP_SL_BPS", 120))/10000), info.TickTicks)
	return holdAndExit(ctx, symbol, info, filledS6, avgS6, targetS6, slPrice, "dip-momentum", envI("DIP_MOM_MAX_HOLD_MIN", 20))
}

// placeIOCBuy memasang limit-IOC buy di harga ask: langsung cross seperti
// market tapi tercap harga — tidak ada slippage di luar harga terpasang.
func placeIOCBuy(ctx context.Context, symbol string, priceS6, qtyS6 int64, coid string, info pairInfo) (int64, int64, error) {
	wire := priceS6 * int64(math.Pow10(int(info.WireShift)))
	pr := models.PriceFromTicksInt(wire)
	qty, _ := models.NewQtyScaled(qtyS6)
	qty = qty.WithScale(info.BaseScale).WithSymbol(symbol)
	tif := "ioc"
	if _, err := client.Orders.Create(ctx, models.CreateOrderRequest{
		Symbol:        &symbol,
		Side:          "buy",
		OrderType:     "limit",
		TIF:           &tif,
		Price:         &pr,
		Qty:           models.QtyFromScaled(qty),
		FeeAsset:      strPtr("quote"),
		ClientOrderID: &coid,
	}, nil); err != nil {
		return 0, 0, err
	}
	time.Sleep(300 * time.Millisecond)
	st, err := orderSnapshot(ctx, symbol, coid)
	if err != nil {
		return 0, 0, err
	}
	return st.cum, avgOrEntry(priceS6, st.avg), nil
}
