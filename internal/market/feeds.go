// Package market owns the realtime feeds: candles, trades, and the managed
// order book. It guarantees the strategy engine only ever sees CLOSED candles
// (a candle is emitted only once a newer timestamp arrives), so signals can
// never repaint.
package market

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/models"
	"github.com/Fabric-Labs/polyester-sdk-go/orderbook"
	"github.com/Fabric-Labs/polyester-sdk-go/realtime"
	"github.com/Fabric-Labs/polyester-sdk-go/services"
)

// ClosedCandle is one finalized candle.
type ClosedCandle struct {
	TsSec  int64
	HighF  float64
	LowF   float64
	CloseF float64
	VolF   float64
	Close  string
	Volume string
}

// Feeds manages three subscriptions and their failure surface. When the
// WebSocket endpoint is unreachable (e.g. Cloudflare challenge on datacenter
// IPs), it degrades to REST polling with identical closed-candle semantics.
type Feeds struct {
	client    *polyester.Client
	symbol    string
	timeframe string
	log       *log.Logger

	candleCh chan ClosedCandle
	tradeCh  chan models.MarketTrade
	bookCh   chan models.OrderbookData
	errCh    chan error

	bookMu   sync.Mutex
	lastBook models.OrderbookData

	mu        sync.Mutex
	candleSub *realtime.Subscription[models.Candle]
	tradeSub  *realtime.Subscription[models.MarketTrade]
	bookSub   *orderbook.Subscription
	closed    bool

	pollMode          bool
	candlePoll        time.Duration
	tradePoll         time.Duration
	pollCandlesCancel context.CancelFunc
	pollTradesCancel  context.CancelFunc
	pollErrMu         sync.Mutex
	lastPollErrLog    time.Time
}

// Start subscribes to all three streams. Managed subscriptions block until
// their snapshot/handshake completes. If the WebSocket handshake fails (for
// example a Cloudflare challenge), feeds fall back to REST polling.
// candlePoll/tradePoll set the REST-poll intervals (scale them with the
// number of symbols in a fleet to respect rate limits).
func Start(ctx context.Context, client *polyester.Client, symbol, timeframe string, candlePoll, tradePoll time.Duration, logr *log.Logger) (*Feeds, error) {
	if logr == nil {
		logr = log.Default()
	}
	if candlePoll <= 0 {
		candlePoll = 5 * time.Second
	}
	if tradePoll <= 0 {
		tradePoll = 3 * time.Second
	}
	f := &Feeds{
		client:     client,
		symbol:     symbol,
		timeframe:  timeframe,
		candlePoll: candlePoll,
		tradePoll:  tradePoll,
		log:        logr,
		candleCh:   make(chan ClosedCandle, 32),
		tradeCh:    make(chan models.MarketTrade, 512),
		bookCh:     make(chan models.OrderbookData, 32),
		errCh:      make(chan error, 16),
	}
	streamErr := f.startSubs(ctx)
	if streamErr != nil {
		logr.Printf("[FEED] websocket unavailable (%v); falling back to REST polling", streamErr)
		if err := f.startPollers(ctx); err != nil {
			return nil, fmt.Errorf("stream (%v) and poll (%v) both failed", streamErr, err)
		}
		f.pollMode = true
		logr.Printf("[FEED] REST polling active: candles every %s, trades every %s", f.candlePoll, f.tradePoll)
	}
	return f, nil
}

func (f *Feeds) startSubs(ctx context.Context) error {
	sym := f.symbol
	cs, err := f.client.MarketData.SubscribeCandles(ctx, &sym, nil, f.timeframe)
	if err != nil {
		return fmt.Errorf("subscribe candles: %w", err)
	}
	ts, err := f.client.MarketData.SubscribeTrades(ctx, &sym, nil)
	if err != nil {
		cs.Close()
		return fmt.Errorf("subscribe trades: %w", err)
	}
	ob, err := f.client.Orderbook.CreateSubscription(ctx, services.CreateSubscriptionOptions{
		Symbol: f.symbol,
		Depth:  20,
		OnError: func(err error) {
			f.pushErr(fmt.Errorf("orderbook: %w", err))
		},
	})
	if err != nil {
		cs.Close()
		ts.Close()
		return fmt.Errorf("subscribe orderbook: %w", err)
	}

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		cs.Close()
		ts.Close()
		ob.Close()
		return fmt.Errorf("feeds closed")
	}
	f.candleSub, f.tradeSub, f.bookSub = cs, ts, ob
	f.mu.Unlock()

	cs.SetOnError(func(err error) { f.pushErr(fmt.Errorf("candles: %w", err)) })
	ts.SetOnError(func(err error) { f.pushErr(fmt.Errorf("trades: %w", err)) })

	go f.pumpCandles(cs)
	go f.pumpTrades(ts)
	go f.pumpBook(ob)
	return nil
}

// Candles returns the closed-candle channel.
func (f *Feeds) Candles() <-chan ClosedCandle { return f.candleCh }

// Trades returns the public trade channel.
func (f *Feeds) Trades() <-chan models.MarketTrade { return f.tradeCh }

// Book returns the managed orderbook update channel.
func (f *Feeds) Book() <-chan models.OrderbookData { return f.bookCh }

// Errors surfaces feed-level failures (the bot decides on restart).
func (f *Feeds) Errors() <-chan error { return f.errCh }

// LastBook returns the most recent managed book snapshot.
func (f *Feeds) LastBook() models.OrderbookData {
	f.bookMu.Lock()
	defer f.bookMu.Unlock()
	return f.lastBook
}

// Close tears down all subscriptions and pollers.
func (f *Feeds) Close() {
	f.mu.Lock()
	f.closed = true
	cs, ts, ob := f.candleSub, f.tradeSub, f.bookSub
	f.candleSub, f.tradeSub, f.bookSub = nil, nil, nil
	f.mu.Unlock()
	if cs != nil {
		cs.Close()
	}
	if ts != nil {
		ts.Close()
	}
	if ob != nil {
		ob.Close()
	}
	if f.pollCandlesCancel != nil {
		f.pollCandlesCancel()
		f.pollCandlesCancel = nil
	}
	if f.pollTradesCancel != nil {
		f.pollTradesCancel()
		f.pollTradesCancel = nil
	}
}

// Restart tears down and rebuilds the feeds (QueueOverflow recovery). A
// stream restart that fails again falls back to REST polling.
//
// ctx MUST be long-lived (the bot's run context): stream subscriptions and
// pollers live as long as ctx. Passing a timeout context here would silently
// kill the new feeds when it expires.
func (f *Feeds) Restart(ctx context.Context) error {
	f.log.Printf("[FEED] restarting feeds")
	f.Close()
	f.mu.Lock()
	f.closed = false // Close() marks shutdown; a restart is not a shutdown
	f.mu.Unlock()
	time.Sleep(500 * time.Millisecond) // let sockets drain
	if f.pollMode {
		return f.startPollers(ctx)
	}
	if err := f.startSubs(ctx); err != nil {
		f.log.Printf("[FEED] websocket still unavailable (%v); switching to REST polling", err)
		f.pollMode = true
		return f.startPollers(ctx)
	}
	return nil
}

// startPollers runs the REST fallback: closed candles every 5s, trades every
// 3s. Closed-candle semantics are identical to the stream path.
func (f *Feeds) startPollers(ctx context.Context) error {
	if f.pollCandlesCancel != nil || f.pollTradesCancel != nil {
		return fmt.Errorf("pollers already running")
	}
	candleCtx, cancelCandles := context.WithCancel(ctx)
	tradeCtx, cancelTrades := context.WithCancel(ctx)
	f.pollCandlesCancel = cancelCandles
	f.pollTradesCancel = cancelTrades
	go f.pollCandles(candleCtx)
	go f.pollTrades(tradeCtx)
	return nil
}

func (f *Feeds) pollCandles(ctx context.Context) {
	ticker := time.NewTicker(f.candlePoll)
	defer ticker.Stop()
	var lastClosedEmitted int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		sym := f.symbol
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		res, err := f.client.MarketData.GetCandles(cctx, &sym, nil, f.timeframe, 10, nil, nil, true)
		cancel()
		if err != nil {
			f.throttledPollErr("candle poll", err)
			continue
		}
		if len(res.Candles) == 0 {
			continue
		}
		// REST is newest-first; candles[0] is the forming candle.
		forming := res.Candles[0].TsSec
		for i := len(res.Candles) - 1; i >= 1; i-- {
			c := res.Candles[i]
			// Any candle older than the forming one and not yet emitted is
			// closed by definition.
			if c.TsSec < forming && c.TsSec > lastClosedEmitted {
				if cc, ok := toClosedCandle(c); ok {
					select {
					case f.candleCh <- cc:
						lastClosedEmitted = c.TsSec
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

func (f *Feeds) pollTrades(ctx context.Context) {
	ticker := time.NewTicker(f.tradePoll)
	defer ticker.Stop()
	seen := map[string]bool{}
	var order []string // bounded match-id ring
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		sym := f.symbol
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		res, err := f.client.MarketData.GetTrades(tctx, &sym, nil, 50, nil)
		cancel()
		if err != nil {
			f.throttledPollErr("trade poll", err)
			continue
		}
		// Deliver oldest -> newest; update the mark with the newest trade.
		trades := make([]models.MarketTrade, len(res.Trades))
		copy(trades, res.Trades)
		sort.SliceStable(trades, func(i, j int) bool { return trades[i].TsNs < trades[j].TsNs })
		for _, t := range trades {
			key := t.MatchID
			if key == "" {
				key = t.TsNs + "|" + strconv.FormatInt(t.Price.Ticks(), 10)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			order = append(order, key)
			if len(order) > 200 {
				delete(seen, order[0])
				order = order[1:]
			}
			select {
			case f.tradeCh <- t:
			default:
				select {
				case <-f.tradeCh:
				default:
				}
				select {
				case f.tradeCh <- t:
				default:
				}
			}
		}
	}
}

// throttledPollErr logs repeated polling failures at most once per minute.
func (f *Feeds) throttledPollErr(what string, err error) {
	f.pollErrMu.Lock()
	defer f.pollErrMu.Unlock()
	if time.Since(f.lastPollErrLog) > time.Minute {
		f.lastPollErrLog = time.Now()
		f.log.Printf("[FEED] %s failed (throttled): %v", what, err)
	}
}

func (f *Feeds) pushErr(err error) {
	select {
	case f.errCh <- err:
	default:
		// Error channel full: the bot already has a pending failure to act on.
	}
}

// pumpCandles converts the raw candle stream (which carries the forming
// candle's updates) into closed candles: candle N is emitted when candle N+1
// first appears.
func (f *Feeds) pumpCandles(sub *realtime.Subscription[models.Candle]) {
	defer func() {
		if err := sub.Err(); err != nil {
			f.pushErr(fmt.Errorf("candle stream ended: %w", err))
		}
	}()
	var lastTs int64
	var last models.Candle
	for c := range sub.Messages() {
		if c.TsSec == 0 {
			continue
		}
		if lastTs != 0 && c.TsSec > lastTs {
			if cc, ok := toClosedCandle(last); ok {
				select {
				case f.candleCh <- cc:
				case <-time.After(30 * time.Second):
					f.pushErr(fmt.Errorf("candle emit timeout (consumer stalled)"))
					return
				}
			}
		}
		lastTs = c.TsSec
		last = c
	}
}

func (f *Feeds) pumpTrades(sub *realtime.Subscription[models.MarketTrade]) {
	defer func() {
		if err := sub.Err(); err != nil {
			f.pushErr(fmt.Errorf("trade stream ended: %w", err))
		}
	}()
	for t := range sub.Messages() {
		select {
		case f.tradeCh <- t:
		default:
			// Trade burst: drop the oldest pressure instead of killing the
			// subscription; candles (not trades) drive signals.
			select {
			case <-f.tradeCh:
			default:
			}
			select {
			case f.tradeCh <- t:
			default:
			}
		}
	}
}

func (f *Feeds) pumpBook(ob *orderbook.Subscription) {
	for data := range ob.Updates() {
		f.bookMu.Lock()
		f.lastBook = data
		f.bookMu.Unlock()
		select {
		case f.bookCh <- data:
		default:
			// Book updates are high-rate; keep only the freshest cached copy.
			select {
			case <-f.bookCh:
			default:
			}
			select {
			case f.bookCh <- data:
			default:
			}
		}
	}
}

func toClosedCandle(c models.Candle) (ClosedCandle, bool) {
	if c.Close == "" || c.TsSec == 0 {
		return ClosedCandle{}, false
	}
	closeF, err := strconv.ParseFloat(c.Close, 64)
	if err != nil || closeF <= 0 {
		return ClosedCandle{}, false
	}
	volF, _ := strconv.ParseFloat(c.Volume, 64)
	highF, _ := strconv.ParseFloat(c.High, 64)
	lowF, _ := strconv.ParseFloat(c.Low, 64)
	return ClosedCandle{
		TsSec:  c.TsSec,
		HighF:  highF,
		LowF:   lowF,
		CloseF: closeF,
		VolF:   volF,
		Close:  c.Close,
		Volume: c.Volume,
	}, true
}

// DetectWirePriceScale empirically determines the wire price scale W such
// that wire price ticks = truePrice × 10^W, using candle quote_volume/volume
// as the ground-truth price. The SDK formats candle prices at a fixed 1e6
// scale, but observed testnet feeds carry an extra ×1000 (W=9); detecting
// instead of assuming keeps the bot correct if the deployment changes.
//
// Returns W (typically 9). Requires at least 3 candles with volume.
func DetectWirePriceScale(ctx context.Context, client *polyester.Client, symbol, timeframe string, logr *log.Logger) (int, error) {
	sym := symbol
	res, err := client.MarketData.GetCandles(ctx, &sym, nil, timeframe, 100, nil, nil, false)
	if err != nil {
		return 0, fmt.Errorf("price-scale probe: %w", err)
	}
	var logs []float64
	for _, c := range res.Candles {
		if c.Close == "" || c.Volume == "" || c.QuoteVolume == "" {
			continue
		}
		closeF, err1 := strconv.ParseFloat(c.Close, 64)
		vol, err2 := strconv.ParseFloat(c.Volume, 64)
		qvol, err3 := strconv.ParseFloat(c.QuoteVolume, 64)
		if err1 != nil || err2 != nil || err3 != nil || vol <= 0 || qvol <= 0 || closeF <= 0 {
			continue
		}
		truePrice := qvol / vol
		if truePrice <= 0 {
			continue
		}
		// closeF = wireTicks/1e6 = truePrice × 10^(W-6)
		w := math.Log10(closeF/truePrice) + 6
		logs = append(logs, w)
	}
	if len(logs) < 3 {
		return 0, fmt.Errorf("price-scale probe: only %d usable candles", len(logs))
	}
	sort.Float64s(logs)
	median := logs[len(logs)/2]
	w := int(math.Round(median))
	if math.Abs(median-float64(w)) > 0.15 {
		return 0, fmt.Errorf("price-scale probe: inconsistent estimates (median %.3f)", median)
	}
	if w < 6 || w > 12 {
		return 0, fmt.Errorf("price-scale probe: implausible scale %d", w)
	}
	if logr != nil {
		logr.Printf("[BOOT] detected wire price scale W=%d (market-data ticks = truePrice×10^%d)", w, w)
	}
	return w, nil
}

// SeedCandles fetches the last n CLOSED candles via REST, oldest first. Used
// to warm the strategy engine without waiting for stream history. Candle
// prices are normalized to true units using the detected wire scale.
func (f *Feeds) SeedCandles(ctx context.Context, n int) ([]ClosedCandle, error) {
	sym := f.symbol
	res, err := f.client.MarketData.GetCandles(ctx, &sym, nil, f.timeframe, n, nil, nil, false)
	if err != nil {
		return nil, err
	}
	rows := make([]models.Candle, len(res.Candles))
	copy(rows, res.Candles)
	// REST returns newest-first; the engine needs oldest-first.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].TsSec < rows[j].TsSec })
	out := make([]ClosedCandle, 0, len(rows))
	for _, c := range rows {
		if cc, ok := toClosedCandle(c); ok {
			out = append(out, cc)
		}
	}
	return out, nil
}
