// Package bot wires feeds, strategy, risk, state, and execution into a single
// event loop supporting MULTIPLE symbols in one process. Every symbol gets
// its own engine, feeds, executor, and position; account-level risk (daily
// loss, kill switch, dead-man switch) is global. All state mutation happens
// on the loop goroutine; stream pumps and timers enqueue closures.
package bot

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/codecs"
	"github.com/Fabric-Labs/polyester-sdk-go/models"
	"github.com/Fabric-Labs/polyester-sdk-go/realtime"

	"polyester-momentum-bot/internal/brain"
	"polyester-momentum-bot/internal/config"
	"polyester-momentum-bot/internal/execution"
	"polyester-momentum-bot/internal/journal"
	"polyester-momentum-bot/internal/ledgerx"
	"polyester-momentum-bot/internal/market"
	"polyester-momentum-bot/internal/num"
	"polyester-momentum-bot/internal/risk"
	"polyester-momentum-bot/internal/state"
	"polyester-momentum-bot/internal/strategy"
)

// clientOrderPrefix marks every order/trigger this bot creates; each symbol
// instance extends it with a per-symbol key so a multi-symbol fleet never
// treats another symbol's orders as owned.
const clientOrderPrefix = "pmom-"

// symRT is one symbol's runtime inside the bot.
type symRT struct {
	symbol  string
	mi      execution.MarketInfo
	ens     *strategy.Ensemble
	exec    execution.Executor
	feeds   *market.Feeds
	ownPre  string
	paperEx *execution.PaperExecutor

	// loop-owned (no mutex needed): mutated only from the event loop.
	pos          *state.Position
	mark         float64
	window       []brain.Candle // closed-candle window the brain scores
	lastLoggedTs int64
	shadow       []shadowEntry // buy signals tracked for shadow outcomes
}

// Bot is the running bot instance.
type Bot struct {
	cfg *config.Config
	log *log.Logger
	// runCtx lives for the whole process (pumps, feed restarts); opCtx drives
	// operations and is swapped to a fresh short-lived context at shutdown so
	// cleanup calls are not cancelled together with the process signal.
	runCtx context.Context
	opCtx  context.Context
	client *polyester.Client

	syms           []*symRT
	symByPairID    map[uint32]*symRT
	st             *state.State
	dec            *risk.Decider
	brain          *brain.Brain
	jrnl           *journal.Journal
	orderSub       *realtime.Subscription[models.Order]
	trigSub        *realtime.Subscription[models.TriggerEvent]
	acts           chan func()
	deadManArmed   bool
	shutdownOnce   sync.Once
	sizingOverride float64 // brain conviction multiplier for the pending buy

	// loop-owned throttle for ledger read failures (see logEquityUnavailable).
	eqFailLast time.Time
	eqFailN    int

	// equityAt is the last successful balance read. Entries are blocked when it
	// goes stale: without equity the daily-loss guard cannot evaluate.
	equityAt time.Time

	// reconFailAt throttles per-symbol reconcile failures (loop-owned).
	reconFailAt map[string]time.Time
}

// Run builds and runs a bot until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config, logr *log.Logger) error {
	if logr == nil {
		logr = log.Default()
	}
	b := &Bot{
		cfg:         cfg,
		log:         logr,
		runCtx:      ctx,
		opCtx:       ctx,
		st:          state.New(cfg.Mode == config.ModePaper),
		dec:         &risk.Decider{MaxDailyLossPct: cfg.DailyLossLimitPct},
		brain:       brain.New(cfg.BrainMinScore, time.Duration(cfg.BrainCooldownMin)*time.Minute),
		symByPairID: map[uint32]*symRT{},
		acts:        make(chan func(), 1024),
	}

	logr.Printf("[BOOT] mode=%s timeframe=%s budget/trade=%s sl=%.2f%% tp=%.2f%% maxSlip=%dbps lossLimit=%.1f%% maxOpenPos=%d",
		cfg.Mode, cfg.Timeframe, cfg.TradeBudgetQuote,
		float64(cfg.StopLossBps)/100, float64(cfg.TakeProfitBps)/100, cfg.MaxSlippageBps, cfg.DailyLossLimitPct, cfg.MaxOpenPositions)

	if err := b.connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer b.client.Close()

	if err := b.buildJournal(); err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	defer b.jrnl.Close()

	if err := b.buildSymbols(); err != nil {
		return fmt.Errorf("symbols: %w", err)
	}

	// Paper mode is single-symbol (one virtual wallet).
	if cfg.Mode == config.ModePaper && len(b.syms) > 1 {
		logr.Printf("[BOOT] paper mode trades only %s (multi-symbol needs live mode)", b.syms[0].symbol)
		b.syms = b.syms[:1]
	}

	for _, s := range b.syms {
		feeds, err := market.Start(ctx, b.client, s.symbol, cfg.Timeframe, b.candlePoll(), b.tradePoll(), logr)
		if err != nil {
			return fmt.Errorf("feeds %s: %w", s.symbol, err)
		}
		s.feeds = feeds
		sym := s
		feeds.SetOnRestore(func() {
			// Feeds recovered: re-check account state for this symbol. Posted to
			// the event loop because all state mutation is loop-owned.
			b.post(func() {
				if b.cfg.Mode == config.ModeLive && !b.st.Halted() {
					b.reconcile(b.opCtx, sym)
				}
			})
		})
		if err := b.seedEngine(ctx, s); err != nil {
			return fmt.Errorf("seed %s: %w", s.symbol, err)
		}
		b.startFeedDrivers(ctx, s)
	}

	// Live-only: reconcile, streams, dead-man switch.
	if cfg.Mode == config.ModeLive {
		if err := b.startupReconcile(b.opCtx); err != nil {
			return fmt.Errorf("startup reconcile: %w", err)
		}
		b.startLiveStreams(ctx)
		if err := b.syms[0].exec.ArmDeadMan(b.opCtx); err != nil {
			logr.Printf("[WARN] dead-man switch arm failed (%v); continuing without it", err)
		} else {
			b.deadManArmed = true
			logr.Printf("[RISK] dead-man switch armed: cancel-all after %ds, refresh every %ds",
				cfg.DeadManTimeoutSec, cfg.DeadManRefreshSec)
		}
	}

	if eq := b.globalEquity(b.opCtx); eq > 0 {
		key := risk.DayKeyUTC(time.Now())
		b.st.RollDay(key, eq)
		b.log.Printf("[RISK] day %s baseline equity %.2f", key, eq)
	}

	if cfg.ControlEnabled {
		sock := cfg.ControlSocket
		if sock == "" {
			sock = filepath.Join(cfg.StateDir, "control.sock")
		}
		go func() {
			if err := b.ServeControl(sock); err != nil {
				logr.Printf("[CTL] server stopped: %v", err)
			}
		}()
	}

	b.log.Printf("[BOOT] running — %d symbol(s), waiting for signals", len(b.syms))
	return b.loop()
}

// candlePoll / tradePoll scale REST-poll intervals with fleet size.
func (b *Bot) candlePoll() time.Duration {
	switch n := len(b.syms); {
	case n <= 5:
		return 5 * time.Second
	case n <= 10:
		return 10 * time.Second
	default:
		return 15 * time.Second
	}
}

func (b *Bot) tradePoll() time.Duration {
	switch n := len(b.syms); {
	case n <= 5:
		return 3 * time.Second
	case n <= 10:
		return 6 * time.Second
	default:
		return 9 * time.Second
	}
}

// connect builds the SDK client for the configured environment/mode.
func (b *Bot) connect() error {
	pc := polyester.Config{
		Environment:     &polyester.TestnetEnvironment,
		APIURL:          b.cfg.APIURL,
		WSURL:           b.cfg.WSURL,
		HydrateCatalogs: true,
	}
	if b.cfg.Mode == config.ModeLive {
		pc.APIKeyID = b.cfg.APIKeyID
		pc.APIPrivateKey = b.cfg.APIPrivateKey
		acct := b.cfg.AccountID
		pc.DefaultAccountID = &acct
	}
	client, err := polyester.New(pc)
	if err != nil {
		return err
	}
	b.client = client

	ctx, cancel := context.WithTimeout(b.opCtx, 60*time.Second)
	defer cancel()
	if err := client.WaitForCatalogs(ctx); err != nil {
		return fmt.Errorf("catalog hydration: %w", err)
	}
	return nil
}

// buildSymbols resolves the symbol universe (explicit list or all enabled
// USDT pairs), then per-symbol market info + executors.
func (b *Bot) buildSymbols() error {
	spot, err := b.client.MarketData.GetSpotConfig(b.opCtx)
	if err != nil {
		return err
	}
	symbols := b.cfg.Symbols
	if len(symbols) == 1 && symbols[0] == "ALL" {
		symbols = enabledUSDTPairs(spot.Raw)
		b.log.Printf("[BOOT] SYMBOLS=all -> %d pairs: %s", len(symbols), strings.Join(symbols, " "))
	}
	if len(symbols) == 0 {
		symbols = []string{b.cfg.Symbol}
	}

	// Wire price scale is a deployment property: detect once via the first
	// symbol that yields enough data, reuse for every symbol.
	wireScale := b.cfg.PriceScale
	if wireScale == 0 {
		pctx, pcancel := context.WithTimeout(b.opCtx, 120*time.Second)
		defer pcancel()
		var lastErr error
		for _, sym := range symbols {
			w, err := market.DetectWirePriceScale(pctx, b.client, sym, b.cfg.Timeframe, b.log)
			if err == nil {
				wireScale = w
				break
			}
			lastErr = err
		}
		if wireScale == 0 {
			return fmt.Errorf("wire price scale detection failed on every symbol (set POLYESTER_PRICE_SCALE manually): %v", lastErr)
		}
	} else {
		b.log.Printf("[BOOT] wire price scale override W=%d", wireScale)
	}

	jrnlAdapter := journalAdapter{jrnl: b.jrnl}
	for _, sym := range symbols {
		s := &symRT{symbol: sym, ownPre: clientOrderPrefix + modeTag(b.cfg.Mode) + "-" + symbolKey(sym) + "-"}
		mi, err := b.resolveMarketInfo(spot, sym, wireScale)
		if err != nil {
			b.log.Printf("[BOOT] skipping %s: %v", sym, err)
			continue
		}
		s.mi = mi
		b.symByPairID[mi.SymbolID] = s
		if b.cfg.Mode == config.ModePaper {
			start, err := num.ParseDecimalToScaled(b.cfg.PaperStartQuote, mi.QuoteScale)
			if err != nil {
				return fmt.Errorf("PAPER_START_QUOTE: %w", err)
			}
			s.paperEx = execution.NewPaperExecutor(mi, jrnlAdapter, b.log, b.cfg.MaxSlippageBps, start)
			s.exec = s.paperEx
		} else {
			s.exec = execution.NewLiveExecutor(b.client, mi, jrnlAdapter, b.log, b.cfg.MaxSlippageBps, b.cfg.DeadManTimeoutSec)
		}
		s.ens = strategy.NewEnsemble(
			b.cfg.EnsembleThreshold,
			strategy.NewMomentumStrategy(strategy.Params{
				FastEMA:      b.cfg.EMAFast,
				SlowEMA:      b.cfg.EMASlow,
				ROCPeriod:    b.cfg.ROCTimeframes,
				VolMAPeriod:  b.cfg.VolMAPeriod,
				ROCThreshold: b.cfg.ROCThresholdPct / 100,
				VolumeSurge:  b.cfg.VolumeSurgeMult,
			}),
			strategy.NewMeanReversionStrategy(),
			strategy.NewBreakoutStrategy(),
			strategy.NewPullbackStrategy(),
		)
		b.syms = append(b.syms, s)
		b.log.Printf("[BOOT] market %s: baseScale=%d quoteScale=%d minQty=%s minNotional=%s",
			sym, mi.BaseScale, mi.QuoteScale,
			num.FormatScaled(mi.MinQtyScaled, mi.BaseScale), num.FormatScaled(mi.MinNotionalQuoteScaled, mi.QuoteScale))
	}
	if len(b.syms) == 0 {
		return fmt.Errorf("no tradable symbols resolved")
	}
	return nil
}

// enabledUSDTPairs collects every enabled *-USDT pair except the stable
// USDC-USDT from the raw spot config.
func enabledUSDTPairs(spotRaw map[string]any) []string {
	var out []string
	for _, key := range []string{"pairs", "symbols"} {
		raw, _ := spotRaw[key].([]any)
		for _, item := range raw {
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

// resolveMarketInfo extracts pair constants for one symbol.
func (b *Bot) resolveMarketInfo(spot models.SpotConfig, symbol string, wireScale int) (execution.MarketInfo, error) {
	pair, err := pairForSymbol(spot.Raw, symbol)
	if err != nil {
		return execution.MarketInfo{}, err
	}
	baseScale, ok := b.client.Catalogs.BaseQuantityScaleForSymbol(symbol)
	if !ok {
		return execution.MarketInfo{}, fmt.Errorf("no base quantity scale")
	}
	quoteScale, ok := b.client.Catalogs.QuoteQuantityScaleForSymbol(symbol)
	if !ok {
		return execution.MarketInfo{}, fmt.Errorf("no quote quantity scale")
	}
	pc, ok := b.client.Catalogs.PairConstraintsForSymbol(symbol)
	if !ok {
		return execution.MarketInfo{}, fmt.Errorf("no pair constraints")
	}
	baseID := uintField(pair, "baseAssetId", "base_asset_id")
	quoteID := uintField(pair, "quoteAssetId", "quote_asset_id")
	if baseID == nil {
		baseID = b.client.Catalogs.LedgerIDForAsset(baseAssetSymbol(pair, symbol))
	}
	if quoteID == nil {
		quoteID = b.client.Catalogs.LedgerIDForAsset(quoteAssetSymbol(pair, symbol))
	}
	if baseID == nil || quoteID == nil {
		return execution.MarketInfo{}, fmt.Errorf("could not resolve base/quote asset IDs")
	}
	return execution.MarketInfo{
		Symbol:                 symbol,
		SymbolID:               pc.SymbolID,
		BaseScale:              baseScale,
		QuoteScale:             quoteScale,
		TickTicks:              pc.TickSizeTicks,
		MinQtyScaled:           pc.MinQtyScaled,
		MinNotionalQuoteScaled: minNotionalScaled(pc, quoteScale),
		BaseAssetID:            *baseID,
		QuoteAssetID:           *quoteID,
		WireScale:              wireScale,
	}, nil
}

func (b *Bot) buildJournal() error {
	jrnl, err := journal.Open(b.cfg.StateDir)
	if err != nil {
		return err
	}
	b.jrnl = jrnl
	b.log.Printf("[BOOT] journal at %s", jrnl.Path())
	return nil
}

func (b *Bot) seedEngine(ctx context.Context, s *symRT) error {
	seed, err := s.feeds.SeedCandles(ctx, b.cfg.WarmupCandles)
	if err != nil {
		return err
	}
	for _, c := range seed {
		s.ens.OnCandle(strategy.Candle{
			TsSec: c.TsSec, High: b.truePrice(s, c.HighF), Low: b.truePrice(s, c.LowF),
			Close: b.truePrice(s, c.CloseF), Volume: c.VolF,
		}, strategy.RegimeRange)
		if c.CloseF > 0 {
			s.mark = b.truePrice(s, c.CloseF)
			s.exec.SetMark(s.mark)
		}
		// The brain needs the same history (with High/Low for ATR), otherwise
		// it vetoes everything as "not enough history" until the stream
		// window fills.
		s.window = append(s.window, brain.Candle{
			TsSec: c.TsSec, High: b.truePrice(s, c.HighF), Low: b.truePrice(s, c.LowF),
			Close: b.truePrice(s, c.CloseF), Volume: c.VolF,
		})
	}
	if len(s.window) > 200 {
		s.window = s.window[len(s.window)-200:]
	}
	b.log.Printf("[BOOT] %s: seeded with %d closed candles (brain window %d)", s.symbol, len(seed), len(s.window))
	return nil
}

// toTrue6 converts a market-data price float (already /1e6 by the SDK) into
// TRUE price ticks (1e6 scale) using the detected wire scale.
func (b *Bot) toTrue6(s *symRT, rawF float64) int64 {
	return int64(rawF * pow10Float(12-s.mi.WireScale))
}

// truePrice converts a raw market-data price float into true quote units.
func (b *Bot) truePrice(s *symRT, rawF float64) float64 {
	return rawF / pow10Float(s.mi.WireScale-6)
}

// wireTicksToTrue6 converts wire price ticks (×10^W) into TRUE price ticks.
func (b *Bot) wireTicksToTrue6(s *symRT, wire int64) int64 {
	k := s.mi.WireScale - 6
	if k <= 0 {
		return wire
	}
	return num.ShiftDown(wire, k)
}

func pow10Int(k int) int64 {
	v := int64(1)
	for i := 0; i < k; i++ {
		v *= 10
	}
	return v
}

func pow10Float(k int) float64 {
	f := 1.0
	for i := 0; i < k; i++ {
		f *= 10
	}
	return f
}

// startLiveStreams subscribes once to the ACCOUNT-WIDE order and trigger
// streams and routes events to their symbol by pair id.
func (b *Bot) startLiveStreams(ctx context.Context) {
	osub, err := b.client.Orders.Subscribe(ctx, nil)
	if err != nil {
		b.log.Printf("[WARN] order stream unavailable (%v); relying on REST polling + reconcile", err)
		return
	}
	b.orderSub = osub
	tsub, err := b.client.Triggers.SubscribeEvents(ctx, nil)
	if err != nil {
		b.log.Printf("[WARN] trigger stream unavailable (%v); relying on reconcile", err)
		osub.Close()
		b.orderSub = nil
		return
	}
	b.trigSub = tsub

	go func() {
		for o := range osub.Messages() {
			o := o
			select {
			case b.acts <- func() { b.onOrderUpdate(o) }:
			case <-ctx.Done():
				return
			}
		}
		if err := osub.Err(); err != nil {
			b.pushErr(nil, fmt.Errorf("order stream ended: %w", err))
		}
	}()
	go func() {
		for te := range tsub.Messages() {
			te := te
			select {
			case b.acts <- func() { b.onTriggerEvent(te) }:
			case <-ctx.Done():
				return
			}
		}
		if err := tsub.Err(); err != nil {
			b.pushErr(nil, fmt.Errorf("trigger stream ended: %w", err))
		}
	}()
}

func (b *Bot) pushErr(s *symRT, err error) {
	select {
	case b.acts <- func() { b.onFeedError(s, err) }:
	case <-b.runCtx.Done():
	}
}

// startFeedDrivers pumps one symbol's feed channels onto the event loop.
func (b *Bot) startFeedDrivers(ctx context.Context, s *symRT) {
	go func() {
		for cc := range s.feeds.Candles() {
			cc := cc
			select {
			case b.acts <- func() { b.onClosedCandle(s, cc) }:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		for t := range s.feeds.Trades() {
			t := t
			select {
			case b.acts <- func() { b.onTrade(s, t) }:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		for range s.feeds.Book() { // drain; book is cached inside feeds
		}
	}()
	go func() {
		for err := range s.feeds.Errors() {
			err := err
			select {
			case b.acts <- func() { b.onFeedError(s, err) }:
			case <-ctx.Done():
				return
			}
		}
	}()
}

// loop is the single-threaded heart: it owns all state mutation.
func (b *Bot) loop() error {
	riskTick := time.NewTicker(10 * time.Second)
	defer riskTick.Stop()
	reconTick := time.NewTicker(15 * time.Second)
	defer reconTick.Stop()
	var deadManTick *time.Ticker
	var deadManC <-chan time.Time
	if b.cfg.Mode == config.ModeLive && b.deadManArmed {
		deadManTick = time.NewTicker(time.Duration(b.cfg.DeadManRefreshSec) * time.Second)
		defer deadManTick.Stop()
		deadManC = deadManTick.C
	}
	reconIdx := 0

	for {
		select {
		case <-b.runCtx.Done():
			// Give cleanup its own budget: the run context is cancelled, so
			// flatten/cancel calls would fail immediately with it.
			sctx, scancel := context.WithTimeout(context.Background(), 120*time.Second)
			b.opCtx = sctx
			b.shutdown()
			scancel()
			return nil

		case f := <-b.acts:
			f()

		case <-riskTick.C:
			if eq := b.globalEquity(b.opCtx); eq > 0 {
				b.riskEval(eq)
			}

		case <-reconTick.C:
			// Rotate: one symbol per tick keeps REST load bounded.
			if b.cfg.Mode == config.ModeLive && len(b.syms) > 0 && !b.st.Halted() {
				s := b.syms[reconIdx%len(b.syms)]
				reconIdx++
				rctx, rcancel := context.WithTimeout(b.opCtx, 60*time.Second)
				b.reconcile(rctx, s)
				rcancel()
			}

		case <-deadManC:
			if err := b.syms[0].exec.RefreshDeadMan(b.opCtx); err != nil {
				b.log.Printf("[WARN] dead-man refresh failed: %v", err)
			}
		}
	}
}

// shutdown flattens (optionally) all open positions and logs a summary.
func (b *Bot) shutdown() {
	b.shutdownOnce.Do(func() {
		b.log.Printf("[SHUTDOWN] received stop signal")
		for _, s := range b.syms {
			if s.pos == nil {
				continue
			}
			if b.cfg.FlattenOn {
				b.log.Printf("[SHUTDOWN] flattening %s (FLATTEN_ON_SHUTDOWN=true)", s.symbol)
				b.flatten(s, "shutdown")
			} else {
				b.log.Printf("[SHUTDOWN] %s position kept open with protective triggers armed (SL=%s TP=%s)",
					s.symbol, s.pos.SLTriggerID, s.pos.TPTriggerID)
			}
		}
		b.log.Printf("[SHUTDOWN] done. realized PnL (quote units): %.6f", b.st.RealizedPnL())
	})
}

// globalEquity estimates account equity in quote units: available quote plus
// every symbol's base position valued at its mark. One balances call.
func (b *Bot) globalEquity(ctx context.Context) float64 {
	balances, err := ledgerx.List(ctx, b.client)
	if err != nil {
		b.logEquityUnavailable(err)
		return 0
	}
	trading := map[uint32]float64{}
	for _, bal := range balances.Balances {
		t, err := codecs.FormatLedgerU128(bal.Trading, codecs.LedgerScale)
		if err != nil {
			continue
		}
		var f float64
		if _, e := fmt.Sscanf(t, "%g", &f); e == nil {
			trading[bal.AssetID] = f
		}
	}
	quote := 0.0
	if q, ok := trading[b.syms[0].mi.QuoteAssetID]; ok {
		quote = q
	}
	baseValue := 0.0
	for _, s := range b.syms {
		if baseQty, ok := trading[s.mi.BaseAssetID]; ok && s.mark > 0 {
			baseValue += baseQty * s.mark
		}
	}
	b.equityAt = time.Now()
	return quote + baseValue
}

// equityMaxAge bounds how old a balance read may be before new entries are
// refused. A stale reading makes the daily-loss guard blind, and the guard is
// the only thing that stops a bad day from compounding.
const equityMaxAge = 3 * time.Minute

// equityFresh reports whether the last successful balance read is recent
// enough to trust. Fail-closed: no fresh equity, no new positions. Exits and
// protective orders are never gated on this.
func (b *Bot) equityFresh(now time.Time) bool {
	return !b.equityAt.IsZero() && now.Sub(b.equityAt) <= equityMaxAge
}

// logReconFail throttles per-symbol reconcile warnings to one per minute so a
// ledger outage cannot bury every other line in the log.
func (b *Bot) logReconFail(symbol string, err error) {
	if b.reconFailAt == nil {
		b.reconFailAt = map[string]time.Time{}
	}
	if time.Since(b.reconFailAt[symbol]) < time.Minute {
		return
	}
	b.reconFailAt[symbol] = time.Now()
	b.log.Printf("[RECON] %s base balance failed: %v", symbol, err)
}

// logEquityUnavailable throttles ledger failure warnings. The ledger read
// service answers 503 in bursts; one line per retry budget would flood the log
// and hide real events. Also states the safety consequence explicitly: while
// equity is unknown the daily-loss guard cannot evaluate, so it is paused.
func (b *Bot) logEquityUnavailable(err error) {
	if time.Since(b.eqFailLast) < time.Minute {
		b.eqFailN++
		return
	}
	suppressed := ""
	if b.eqFailN > 0 {
		suppressed = fmt.Sprintf(" (%d similar failures suppressed)", b.eqFailN)
	}
	b.log.Printf("[WARN] equity unavailable (ledger read failed after retries): %v%s — daily loss guard paused", err, suppressed)
	b.eqFailLast = time.Now()
	b.eqFailN = 0
}

// riskEval rolls the risk day and trips the kill switch when needed.
func (b *Bot) riskEval(nowEquity float64) {
	if nowEquity <= 0 {
		return
	}
	key := risk.DayKeyUTC(time.Now())
	newKey, _, decision := b.dec.Evaluate(b.st.DayKeyOrEmpty(), b.st.DayStartEquity(), risk.Observation{DayKey: key, Equity: nowEquity})
	if decision.NewDay {
		b.st.RollDay(newKey, decision.BaselineUsed)
		b.log.Printf("[RISK] new risk day %s baseline %.2f", newKey, decision.BaselineUsed)
		return
	}
	if decision.Halt {
		b.killSwitch(decision.Reason)
	}
}

// killSwitch halts trading and disposes of every open position.
func (b *Bot) killSwitch(reason string) {
	if b.st.Halted() {
		return
	}
	b.st.Halt(reason)
	_ = b.jrnl.Append(journal.Entry{Kind: journal.KindKillSwitch, Detail: map[string]string{"reason": reason}})
	b.log.Printf("[RISK] KILL SWITCH: %s", reason)

	for _, s := range b.syms {
		pos := s.pos
		if pos == nil {
			continue
		}
		b.cancelExits(s, pos)
		sold, remaining, exitScaled6, err := b.sellWithOneRetry(s, pos.BaseQtyScaled)
		if err != nil && !isDust(err) {
			b.log.Printf("[RISK] kill-switch %s sell failed: %v; re-arming exits", s.symbol, err)
			if _, rerr := b.rearmSL(s, pos.BaseQtyScaled, pos.AvgEntryScaled6); rerr != nil {
				b.log.Printf("[RISK] CRITICAL: re-arm after failed kill sell also failed (%v)", rerr)
			}
			continue
		}
		if remaining > 0 {
			b.bookPartialPnL(pos, sold, exitScaled6)
			pos.BaseQtyScaled = remaining
			s.pos = pos
			if _, rerr := b.rearmSL(s, remaining, pos.AvgEntryScaled6); rerr != nil {
				b.log.Printf("[RISK] CRITICAL: %s kill-switch remainder left without exits (%v)", s.symbol, rerr)
			}
			continue
		}
		b.recordClosedPosition(s, pos, exitScaled6, "kill_switch", sold)
	}
	if err := b.syms[0].exec.CancelAllOrders(b.opCtx); err != nil {
		b.log.Printf("[RISK] kill-switch cancel-all failed: %v", err)
	}
	b.log.Printf("[RISK] trading halted. Restart the process to resume (a new day resets the baseline).")
}

func (b *Bot) newID(s *symRT, suffix string) string {
	return newClientOrderID(s.ownPre + suffix)
}

// modeTag / symbolKey build the per-instance prefix parts.
func modeTag(m config.Mode) string {
	if m == config.ModeLive {
		return "l"
	}
	return "p"
}

// symbolKey extracts a short tag from the display symbol ("BTC-USDT" -> "BTC").
func symbolKey(symbol string) string {
	if i := strings.Index(symbol, "-"); i > 0 {
		return symbol[:i]
	}
	if len(symbol) > 4 {
		return symbol[:4]
	}
	return symbol
}
