package execution

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"sync"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	"github.com/Fabric-Labs/polyester-sdk-go/codecs"
	sdkerrors "github.com/Fabric-Labs/polyester-sdk-go/errors"
	"github.com/Fabric-Labs/polyester-sdk-go/models"

	"polyester-momentum-bot/internal/ledgerx"
	"polyester-momentum-bot/internal/num"
)

// Entry is one journal record written by the executor.
type Entry struct {
	Kind            string
	ClientOrderID   string
	ClientTriggerID string
	Detail          map[string]string
}

// Journal is the minimal journal surface executors depend on.
type Journal interface {
	Append(e Entry) error
}

// LiveExecutor places real testnet orders through the Polyester Go SDK.
type LiveExecutor struct {
	client *polyester.Client
	mi     MarketInfo
	jrnl   Journal
	log    *log.Logger

	slippageBps    int32
	deadManSecs    int
	fillWait       time.Duration
	maxRateRetries int

	markMu      sync.RWMutex
	mark        float64   // true price (quote units per base)
	markAt      time.Time // when the mark was last updated
	orderMu     sync.RWMutex
	orderShiftK int  // k such that orderWireTicks = trueScaled6 × 10^k
	calibrated  bool // set after the first fill is compared against market data
}

// NewLiveExecutor builds the live executor. client must already have hydrated
// catalogs (WaitForCatalogs).
func NewLiveExecutor(client *polyester.Client, mi MarketInfo, jrnl Journal, logr *log.Logger, slippageBps int32, deadManSecs int) *LiveExecutor {
	if logr == nil {
		logr = log.Default()
	}
	return &LiveExecutor{
		client:         client,
		mi:             mi,
		jrnl:           jrnl,
		log:            logr,
		slippageBps:    slippageBps,
		deadManSecs:    deadManSecs,
		fillWait:       20 * time.Second,
		maxRateRetries: 4,
		orderShiftK:    mi.WireScale - 6,
	}
}

func (l *LiveExecutor) Kind() string { return "live" }

// Mark returns the last known price (quote units per base), fed by the bot
// from trade streams.
func (l *LiveExecutor) Mark() float64 {
	l.markMu.RLock()
	defer l.markMu.RUnlock()
	return l.mark
}

// SetMark updates the last known price.
func (l *LiveExecutor) SetMark(price float64) {
	if price <= 0 {
		return
	}
	l.markMu.Lock()
	l.mark = price
	l.markAt = time.Now()
	l.markMu.Unlock()
}

// Buy spends up to budgetQuoteScaled (at quote scale) with a market IOC buy.
func (l *LiveExecutor) Buy(ctx context.Context, budgetQuoteScaled int64, clientOrderID string) (*Fill, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req := l.marketReq("buy", models.QtyInput{}, budgetQuoteScaled, clientOrderID)

	// Journal the intent BEFORE any create attempt (idempotency backbone).
	_ = l.jrnl.Append(Entry{Kind: "buy_intent", ClientOrderID: clientOrderID,
		Detail: map[string]string{"budget_quote_scaled": strconv.FormatInt(budgetQuoteScaled, 10), "symbol": l.mi.Symbol}})

	if err := l.preview(ctx, req, "buy"); err != nil {
		return nil, err
	}
	if err := l.createWithReconcile(ctx, req, clientOrderID, "buy"); err != nil {
		return nil, err
	}
	fill, err := l.observeFill(ctx, clientOrderID)
	if err != nil {
		return nil, err
	}
	_ = l.jrnl.Append(Entry{Kind: "buy_result", ClientOrderID: clientOrderID,
		Detail: map[string]string{
			"order_id": fill.OrderID, "status": fill.Status,
			"base_scaled": strconv.FormatInt(fill.BaseQtyScaled, 10),
			"avg_true6":   strconv.FormatInt(fill.AvgPriceScaled6, 10),
		}})
	return fill, nil
}

// Sell disposes baseQtyScaled base (at base scale) with a market IOC sell.
func (l *LiveExecutor) Sell(ctx context.Context, baseQtyScaled int64, clientOrderID string) (*Fill, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if baseQtyScaled < l.mi.MinQtyScaled {
		return nil, fmt.Errorf("%w: %d < min %d", ErrDust, baseQtyScaled, l.mi.MinQtyScaled)
	}
	req := l.marketReq("sell", l.baseQty(baseQtyScaled), 0, clientOrderID)

	_ = l.jrnl.Append(Entry{Kind: "sell_intent", ClientOrderID: clientOrderID,
		Detail: map[string]string{"base_scaled": strconv.FormatInt(baseQtyScaled, 10), "symbol": l.mi.Symbol}})

	if err := l.preview(ctx, req, "sell"); err != nil {
		return nil, err
	}
	if err := l.createWithReconcile(ctx, req, clientOrderID, "sell"); err != nil {
		return nil, err
	}
	fill, err := l.observeFill(ctx, clientOrderID)
	if err != nil {
		return nil, err
	}
	_ = l.jrnl.Append(Entry{Kind: "sell_result", ClientOrderID: clientOrderID,
		Detail: map[string]string{
			"order_id": fill.OrderID, "status": fill.Status,
			"base_scaled": strconv.FormatInt(fill.BaseQtyScaled, 10),
			"avg_true6":   strconv.FormatInt(fill.AvgPriceScaled6, 10),
		}})
	return fill, nil
}

// marketReq builds a market IOC create request. Exactly one sizing mode is
// set: quote budget for buys, explicit base qty for sells.
func (l *LiveExecutor) marketReq(side string, qty models.QtyInput, budgetQuoteScaled int64, clientOrderID string) models.CreateOrderRequest {
	sym := l.mi.Symbol
	feeAsset := "quote"
	slip := l.slippageBps
	req := models.CreateOrderRequest{
		Symbol:         &sym,
		Side:           side,
		OrderType:      "market",
		FeeAsset:       &feeAsset,
		MaxSlippageBps: &slip,
		ClientOrderID:  &clientOrderID,
	}
	if side == "buy" {
		req.MaxQuoteDebitScaled = models.QtyFromQuoteScaled(budgetQuoteScaled, l.mi.QuoteScale)
	} else {
		req.Qty = qty
	}
	return req
}

// baseQty wraps a base scaled integer with its catalog scale and symbol.
func (l *LiveExecutor) baseQty(scaled int64) models.QtyInput {
	return models.QtyFromScaled(models.MustQtyScaled(scaled).WithScale(l.mi.BaseScale).WithSymbol(l.mi.Symbol))
}

// preview runs Orders.Preview per the SDK contract: real rejections abort the
// create; an unavailable preview host or transport noise continues (create is
// authoritative and re-evaluates).
func (l *LiveExecutor) preview(ctx context.Context, req models.CreateOrderRequest, side string) error {
	pv, err := l.client.Orders.Preview(ctx, req, nil)
	switch {
	case err == nil:
		if pv.Admissible != nil && !*pv.Admissible {
			code, fields := "", ""
			if pv.Rejection != nil {
				code = pv.Rejection.Code
				for _, v := range pv.Rejection.Violations {
					fields += fmt.Sprintf(" %s=%s(%s)", v.FieldPath, v.Message, v.RuleID)
				}
			}
			return fmt.Errorf("preview rejected %s %s: code=%s%s", side, l.mi.Symbol, code, fields)
		}
		l.log.Printf("[EXEC] preview ok %s %s (admissible=%v)", side, l.mi.Symbol, pv.Admissible != nil && *pv.Admissible)
		return nil
	case previewUnavailable(err):
		l.log.Printf("[EXEC] preview unavailable on this host; continuing without it: %v", err)
		return nil
	case fatalValidationError(err):
		return fmt.Errorf("preview validation failure: %w", err)
	default:
		l.log.Printf("[EXEC] preview error; continuing without it: %v", err)
		return nil
	}
}

// createWithReconcile performs the create loop: rate-limit retries keep the
// same client order ID; ambiguous outcomes look the ID up before retrying;
// definitive rejections and auth failures return immediately.
func (l *LiveExecutor) createWithReconcile(ctx context.Context, req models.CreateOrderRequest, clientOrderID, side string) error {
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := l.client.Orders.Create(ctx, req, nil)
		if err == nil {
			return nil
		}
		lastErr = err
		switch classifyCreateError(err) {
		case createClassRejected, createClassFatal:
			return fmt.Errorf("create %s rejected: %w", side, err)
		case createClassRetryRateLimit:
			delay := rateLimitDelayOf(err)
			l.log.Printf("[EXEC] rate limited on create; retry %d/%d in %s (%v)", attempt+1, maxAttempts, delay, err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		default: // ambiguous
			order, gerr := l.client.Orders.Get(ctx, nil, models.OrderKeyByClientID(clientOrderID), nil, false, false)
			switch {
			case gerr == nil && order.Order != nil:
				l.log.Printf("[EXEC] ambiguous create resolved: order %s admitted (status=%s)", order.Order.OrderID, order.Order.Status)
				return nil
			case gerr == nil, notFound(gerr):
				// Server confirms the client order ID does not exist: the
				// attempt was never admitted; retry with the same ID.
				l.log.Printf("[EXEC] ambiguous create resolved: not admitted; retrying with same client_order_id")
			case notFound(gerr):
				l.log.Printf("[EXEC] ambiguous create resolved: not admitted; retrying with same client_order_id")
			default:
				return fmt.Errorf("%w: create failed (%v) and lookup failed (%v)", ErrAmbiguous, err, gerr)
			}
		}
	}
	return fmt.Errorf("create %s failed after %d attempts: %w", side, maxAttempts, lastErr)
}

// rateLimitDelayOf extracts a retry delay from a RateLimitError.
func rateLimitDelayOf(err error) time.Duration {
	var rl *sdkerrors.RateLimitError
	if errors.As(err, &rl) {
		return rateLimitDelay(rl)
	}
	return time.Second
}

// observeFill waits for trade projection to catch up, then reports the fill
// from the order record. Handles partial IOC fills (status canceled with
// cumulative quantity) and refuses non-terminal ambiguity.
func (l *LiveExecutor) observeFill(ctx context.Context, clientOrderID string) (*Fill, error) {
	gr, err := l.client.Orders.WaitForOrderTradesComplete(ctx, nil, models.OrderKeyByClientID(clientOrderID), nil, l.fillWait)
	if err != nil {
		// Projection lag or timeout: take one authoritative read.
		gr2, gerr := l.client.Orders.Get(ctx, nil, models.OrderKeyByClientID(clientOrderID), nil, false, false)
		if gerr != nil {
			return nil, fmt.Errorf("fill observation failed: %v (last: %v)", gerr, err)
		}
		gr = gr2
	}
	if gr.Order == nil {
		return nil, fmt.Errorf("fill observation: order %s not found", clientOrderID)
	}
	o := gr.Order
	baseScaled, err := l.resolveScaled(o.CumQty, o.SymbolID)
	if err != nil {
		return nil, err
	}
	fill := &Fill{
		ClientOrderID:   clientOrderID,
		OrderID:         o.OrderID,
		BaseQtyScaled:   baseScaled,
		AvgPriceScaled6: l.orderTicksToTrue6(o.AvgPx.Ticks()),
		Status:          o.Status,
	}
	switch {
	case o.Status == "filled":
		// complete
	case o.Status == "canceled" && baseScaled > 0:
		fill.Partial = true
	default:
		return nil, fmt.Errorf("order %s reached status %q with fill=%d", clientOrderID, o.Status, baseScaled)
	}
	if baseScaled <= 0 {
		return nil, fmt.Errorf("order %s reached status %q with zero fill", clientOrderID, o.Status)
	}
	return fill, nil
}

// resolveScaled verifies the catalog knows the pair's base scale, then returns
// the raw scaled quantity. Private-stream quantities may arrive without an
// attached scale; the catalog is the only permitted source (never a fallback).
func (l *LiveExecutor) resolveScaled(q models.QtyScaled, symbolID uint32) (int64, error) {
	if s := q.Scale(); s != nil && *s != l.mi.BaseScale {
		return 0, fmt.Errorf("qty scale %d disagrees with catalog base scale %d", *s, l.mi.BaseScale)
	}
	if symbolID != 0 {
		if _, ok := l.client.Catalogs.BaseQuantityScaleForSymbolID(symbolID); !ok {
			return 0, fmt.Errorf("catalog has no base scale for symbol_id=%d", symbolID)
		}
	}
	return q.Scaled(), nil
}

// orderTicksToTrue6 converts order-service wire price ticks to TRUE 1e6-scale
// price ticks, calibrating the shift against market data on the first fill.
func (l *LiveExecutor) orderTicksToTrue6(wire int64) int64 {
	l.calibrateOrderShift(wire)
	l.orderMu.RLock()
	k := l.orderShiftK
	l.orderMu.RUnlock()
	if k <= 0 {
		return wire
	}
	return num.ShiftDown(wire, k)
}

// true6ToOrderTicks converts TRUE 1e6-scale price ticks to order-service wire
// ticks for encoding into trigger create requests.
func (l *LiveExecutor) true6ToOrderTicks(trueScaled6 int64) int64 {
	l.orderMu.RLock()
	k := l.orderShiftK
	l.orderMu.RUnlock()
	if k <= 0 {
		return trueScaled6
	}
	return num.ShiftUp(trueScaled6, k)
}

// calibrateOrderShift compares the first fill price against the market mark
// (true units) and adjusts the order-service shift if they disagree with the
// boot-time assumption. A single OMS should use one price scale; this guards
// against the market-data scale differing from the orders scale.
func (l *LiveExecutor) calibrateOrderShift(fillWire int64) {
	l.orderMu.Lock()
	defer l.orderMu.Unlock()
	if l.calibrated || fillWire <= 0 {
		return
	}
	l.calibrated = true
	l.markMu.Lock()
	mark, markAt := l.mark, l.markAt
	l.markMu.Unlock()
	// A stale mark (e.g. the boot seed) would mis-calibrate the shift; only
	// trust a reference updated within the last two minutes.
	if mark <= 0 || time.Since(markAt) > 2*time.Minute {
		l.log.Printf("[EXEC] order shift calibration skipped: no fresh market mark")
		return // keep boot assumption
	}
	// mark is the TRUE price (units per base); fillWire is wire ticks. A
	// consistent pair gives ratio = 10^orderShift where true6 = wire/10^k.
	ratio := float64(fillWire) / (mark * 1e6)
	est := math.Log10(ratio)
	boot := float64(l.orderShiftK)
	if math.Abs(est-boot) < 0.3 {
		return // consistent with the detected market scale
	}
	// Orders disagree with market data: trust the fill vs mark ratio.
	newK := int(math.Round(est))
	if newK < 0 || newK > 12 {
		l.log.Printf("[EXEC] order price shift calibration out of range (%.2f); keeping %d", est, l.orderShiftK)
		return
	}
	l.log.Printf("[EXEC] CALIBRATED order price shift: %d -> %d (fill/mark ratio %.4g)", l.orderShiftK, newK, ratio)
	l.orderShiftK = newK
}

// PlaceStopLoss arms the single protective sell trigger. The create is
// journaled before the attempt and reconciled by client trigger ID on
// ambiguity.
func (l *LiveExecutor) PlaceStopLoss(ctx context.Context, baseQtyScaled, slScaled6 int64, slClientID string) (*StopLoss, error) {
	id, err := l.createTrigger(ctx, "stop_loss", slScaled6, baseQtyScaled, slClientID)
	if err != nil {
		return nil, fmt.Errorf("stop_loss trigger: %w", err)
	}
	_ = l.jrnl.Append(Entry{Kind: "triggers_placed", ClientTriggerID: slClientID,
		Detail: map[string]string{
			"sl_trigger_id": id, "sl_true6": strconv.FormatInt(slScaled6, 10),
			"base_scaled": strconv.FormatInt(baseQtyScaled, 10),
		}})
	return &StopLoss{TriggerID: id, ClientTriggerID: slClientID, PriceScaled6: slScaled6, BaseQtyScaled: baseQtyScaled}, nil
}

// createTrigger creates one trigger with the standard retry/reconcile loop.
func (l *LiveExecutor) createTrigger(ctx context.Context, kind string, trueScaled6, baseQtyScaled int64, clientTriggerID string) (string, error) {
	sym := l.mi.Symbol
	wireTicks := l.true6ToOrderTicks(trueScaled6)
	price := models.PriceFromTicksInt(wireTicks)
	slip := l.slippageBps
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		_ = l.jrnl.Append(Entry{Kind: "trigger_intent", ClientTriggerID: clientTriggerID,
			Detail: map[string]string{"type": kind, "wire_ticks": strconv.FormatInt(wireTicks, 10), "attempt": strconv.Itoa(attempt)}})
		res, err := l.client.Triggers.Create(ctx, nil, sym, kind, &price, "sell",
			l.baseQty(baseQtyScaled), "market", nil, "", "", nil, &clientTriggerID, false,
			codecs.CreateTriggerOptions{
				MaxSlippageBps:          &slip,
				SelfTradePreventionMode: strPtr("expire_maker"),
			})
		if err == nil {
			return res.TriggerID, nil
		}
		lastErr = err
		switch classifyCreateError(err) {
		case createClassRejected, createClassFatal:
			return "", err
		case createClassRetryRateLimit:
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(rateLimitDelayOf(err)):
			}
		default: // ambiguous
			id, found, lerr := l.findTriggerByClientID(ctx, clientTriggerID)
			switch {
			case lerr == nil && found:
				return id, nil
			case lerr == nil:
				// Not admitted; retry with the same client trigger ID.
			default:
				return "", fmt.Errorf("%w: trigger create failed (%v), lookup failed (%v)", ErrAmbiguous, err, lerr)
			}
		}
	}
	return "", fmt.Errorf("trigger create failed after retries: %w", lastErr)
}

// findTriggerByClientID scans our symbol's triggers for a client trigger ID.
func (l *LiveExecutor) findTriggerByClientID(ctx context.Context, clientTriggerID string) (string, bool, error) {
	refs, err := l.listTriggersAllStatuses(ctx)
	if err != nil {
		return "", false, err
	}
	for _, t := range refs {
		if t.ClientTriggerID == clientTriggerID {
			return t.TriggerID, true, nil
		}
	}
	return "", false, nil
}

// listTriggersAllStatuses pages triggers for our symbol across all lifecycle
// statuses (List filters by exactly one status per call).
func (l *LiveExecutor) listTriggersAllStatuses(ctx context.Context) ([]TriggerRef, error) {
	var out []TriggerRef
	sym := l.mi.Symbol
	statuses := []string{StatusCreated, StatusArmed, StatusRunning, StatusCompleted, StatusCancelled, StatusFailed, StatusPaused}
	for _, st := range statuses {
		list, err := l.client.Triggers.List(ctx, nil, nil, &sym, []string{st}, 100, nil)
		if err != nil {
			return nil, err
		}
		for _, t := range list.Triggers {
			out = append(out, TriggerRef{
				TriggerID:       t.TriggerID,
				ClientTriggerID: t.ClientTriggerID,
				TriggerType:     t.TriggerType,
				Status:          t.Status,
			})
		}
	}
	return out, nil
}

// CancelTrigger cancels by ID; not-found and already-terminal count as done.
func (l *LiveExecutor) CancelTrigger(ctx context.Context, triggerID string) error {
	_, err := l.client.Triggers.Cancel(ctx, nil, triggerID, nil)
	if err == nil {
		return nil
	}
	if notFound(err) {
		return nil
	}
	// A race may have completed it server-side between create and cancel.
	st, serr := l.TriggerStatus(ctx, triggerID)
	if serr == nil && IsTerminalTriggerStatus(st) {
		return nil
	}
	return err
}

// TriggerStatus reads one trigger's lifecycle status.
func (l *LiveExecutor) TriggerStatus(ctx context.Context, triggerID string) (string, error) {
	t, err := l.client.Triggers.Get(ctx, nil, triggerID, nil)
	if err != nil {
		return "", err
	}
	if t == nil {
		return "", fmt.Errorf("trigger %s not found", triggerID)
	}
	return t.Status, nil
}

// ActiveTriggers lists our triggers in active statuses.
func (l *LiveExecutor) ActiveTriggers(ctx context.Context) ([]TriggerRef, error) {
	all, err := l.listTriggersAllStatuses(ctx)
	if err != nil {
		return nil, err
	}
	active := map[string]bool{}
	for _, s := range ActiveTriggerStatuses() {
		active[s] = true
	}
	var out []TriggerRef
	for _, t := range all {
		if active[t.Status] {
			out = append(out, t)
		}
	}
	return out, nil
}

// OpenOrders lists working orders owned by this bot (client ID prefix filter
// happens at the caller, which owns the prefix).
func (l *LiveExecutor) OpenOrders(ctx context.Context) ([]OrderRef, error) {
	list, err := l.client.Orders.ListOpen(ctx, nil, nil, nil, nil, false, false, nil)
	if err != nil {
		return nil, err
	}
	var out []OrderRef
	for _, o := range list.Orders {
		out = append(out, OrderRef{OrderID: o.OrderID, ClientOrderID: o.ClientOrderID, Status: o.Status})
	}
	return out, nil
}

// CancelAllOrders cancels all open orders for the account (idempotent when
// none are open).
func (l *LiveExecutor) CancelAllOrders(ctx context.Context) error {
	rid := fmt.Sprintf("cancel-all-%d", time.Now().UnixNano())
	_, err := l.client.Orders.CancelAll(ctx, nil, nil, nil, nil, false, &rid)
	if err != nil && notFound(err) {
		return nil
	}
	return err
}

// ArmDeadMan arms the server-side cancel-all-after window.
func (l *LiveExecutor) ArmDeadMan(ctx context.Context) error {
	return l.RefreshDeadMan(ctx)
}

// RefreshDeadMan re-arms cancel-all-after. With no orders open this is a
// cheap, harmless server-side timer reset.
func (l *LiveExecutor) RefreshDeadMan(ctx context.Context) error {
	rid := fmt.Sprintf("dma-%d", time.Now().UnixNano())
	_, err := l.client.Orders.CancelAllAfter(ctx, nil, l.deadManSecs, nil, nil, nil, &rid)
	if err != nil && notFound(err) {
		return nil
	}
	return err
}

// QuoteAvailable returns available quote balance (quote units, float) for
// risk math. Trading sizing itself stays in scaled integers.
func (l *LiveExecutor) QuoteAvailable(ctx context.Context) (float64, error) {
	return l.balanceFloat(ctx, l.mi.QuoteAssetID, "available")
}

// BaseBalance returns total base trading balance (base units, float).
func (l *LiveExecutor) BaseBalance(ctx context.Context) (float64, error) {
	return l.balanceFloat(ctx, l.mi.BaseAssetID, "trading")
}

func (l *LiveExecutor) balanceFloat(ctx context.Context, assetID uint32, field string) (float64, error) {
	balances, err := ledgerx.List(ctx, l.client)
	if err != nil {
		return 0, err
	}
	for _, b := range balances.Balances {
		if b.AssetID != assetID {
			continue
		}
		raw := b.Available
		if field == "trading" {
			raw = b.Trading
		}
		s, err := codecs.FormatLedgerU128(raw, codecs.LedgerScale)
		if err != nil {
			return 0, err
		}
		return strconv.ParseFloat(s, 64)
	}
	return 0, nil
}

func strPtr(s string) *string { return &s }
