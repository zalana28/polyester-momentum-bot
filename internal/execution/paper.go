package execution

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"

	"polyester-momentum-bot/internal/num"
)

// paperTakerFeeBps models a conservative taker fee in quote on both sides so
// paper PnL does not overstate reality.
const paperTakerFeeBps = int64(10)

// PaperExecutor simulates trading against live market data without an API
// key. Fills are immediate at mark ± the configured slippage; protective
// triggers are virtual and evaluated against the trade stream by the bot.
type PaperExecutor struct {
	mi      MarketInfo
	jrnl    Journal
	log     *log.Logger
	slipBps int32

	mu          sync.Mutex
	markTrue6   int64 // mark price in TRUE price ticks (1e6 scale)
	quoteScaled int64 // at quote scale
	baseScaled  int64 // at base scale
	exits       []paperExit
}

type paperExit struct {
	triggerID       string
	clientTriggerID string
	kind            string // "stop_loss" | "take_profit"
	priceScaled6    int64
	qtyScaled       int64
}

// NewPaperExecutor starts with a virtual quote balance.
func NewPaperExecutor(mi MarketInfo, jrnl Journal, logr *log.Logger, slipBps int32, startQuoteScaled int64) *PaperExecutor {
	if logr == nil {
		logr = log.Default()
	}
	return &PaperExecutor{
		mi:          mi,
		jrnl:        jrnl,
		log:         logr,
		slipBps:     slipBps,
		quoteScaled: startQuoteScaled,
	}
}

func (p *PaperExecutor) Kind() string { return "paper" }

// Mark returns the mark in quote units per base (TRUE price).
func (p *PaperExecutor) Mark() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return num.ScaledToFloat(p.markTrue6, 6)
}

// SetMark updates the mark price (TRUE price, quote units per base).
func (p *PaperExecutor) SetMark(price float64) {
	if price <= 0 {
		return
	}
	p.mu.Lock()
	p.markTrue6 = num.FloatToScaledRounded(price, 6)
	p.mu.Unlock()
}

// Buy simulates a full-budget market buy at mark + slippage.
func (p *PaperExecutor) Buy(ctx context.Context, budgetQuoteScaled int64, clientOrderID string) (*Fill, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = p.jrnl.Append(Entry{Kind: "buy_intent", ClientOrderID: clientOrderID,
		Detail: map[string]string{"budget_quote_scaled": strconv.FormatInt(budgetQuoteScaled, 10)}})

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.markTrue6 <= 0 {
		return nil, fmt.Errorf("paper buy: no mark price yet")
	}
	fillPrice := num.MulBpsFloor(p.markTrue6, 10000+int64(p.slipBps))
	// All-in debit (mirrors the MaxQuoteDebitScaled contract): the taker fee
	// comes out of the quote budget, so base received = (budget-fee)/price.
	fee := budgetQuoteScaled * paperTakerFeeBps / 10000
	qty, err := num.ScaledDiv(budgetQuoteScaled-fee, p.mi.QuoteScale, fillPrice, 6, p.mi.BaseScale)
	if err != nil {
		return nil, err
	}
	notional, err := num.ScaledMulDiv(qty, p.mi.BaseScale, fillPrice, 6, 1, 0, p.mi.QuoteScale)
	if err != nil {
		return nil, err
	}
	if notional < p.mi.MinNotionalQuoteScaled {
		return nil, fmt.Errorf("paper buy: notional below pair minimum (%d < %d)", notional, p.mi.MinNotionalQuoteScaled)
	}
	if qty < p.mi.MinQtyScaled {
		return nil, fmt.Errorf("%w: qty %d < min %d", ErrDust, qty, p.mi.MinQtyScaled)
	}
	if budgetQuoteScaled > p.quoteScaled {
		return nil, fmt.Errorf("paper buy: budget %d exceeds virtual quote %d", budgetQuoteScaled, p.quoteScaled)
	}
	p.quoteScaled -= budgetQuoteScaled
	p.baseScaled += qty

	fill := &Fill{
		ClientOrderID:   clientOrderID,
		OrderID:         "paper-" + clientOrderID,
		BaseQtyScaled:   qty,
		AvgPriceScaled6: fillPrice,
		Status:          "filled",
	}
	_ = p.jrnl.Append(Entry{Kind: "buy_result", ClientOrderID: clientOrderID,
		Detail: map[string]string{
			"base_scaled": strconv.FormatInt(qty, 10), "avg_true6": strconv.FormatInt(fillPrice, 10),
			"fee_quote_scaled": strconv.FormatInt(fee, 10),
		}})
	p.log.Printf("[PAPER] buy fill: qty=%s price=%s budget=%s",
		num.FormatScaled(qty, p.mi.BaseScale), num.FormatScaled(fillPrice, 6), num.FormatScaled(budgetQuoteScaled, p.mi.QuoteScale))
	return fill, nil
}

// Sell simulates a market sell at mark - slippage.
func (p *PaperExecutor) Sell(ctx context.Context, baseQtyScaled int64, clientOrderID string) (*Fill, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if baseQtyScaled < p.mi.MinQtyScaled {
		return nil, fmt.Errorf("%w: %d < min %d", ErrDust, baseQtyScaled, p.mi.MinQtyScaled)
	}
	_ = p.jrnl.Append(Entry{Kind: "sell_intent", ClientOrderID: clientOrderID,
		Detail: map[string]string{"base_scaled": strconv.FormatInt(baseQtyScaled, 10)}})

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.markTrue6 <= 0 {
		return nil, fmt.Errorf("paper sell: no mark price yet")
	}
	if baseQtyScaled > p.baseScaled {
		return nil, fmt.Errorf("paper sell: qty %d exceeds virtual base %d", baseQtyScaled, p.baseScaled)
	}
	fillPrice := num.MulBpsFloor(p.markTrue6, 10000-int64(p.slipBps))
	proceeds, err := num.ScaledMulDiv(baseQtyScaled, p.mi.BaseScale, fillPrice, 6, 1, 0, p.mi.QuoteScale)
	if err != nil {
		return nil, err
	}
	fee := proceeds * paperTakerFeeBps / 10000
	p.baseScaled -= baseQtyScaled
	p.quoteScaled += proceeds - fee

	fill := &Fill{
		ClientOrderID:   clientOrderID,
		OrderID:         "paper-" + clientOrderID,
		BaseQtyScaled:   baseQtyScaled,
		AvgPriceScaled6: fillPrice,
		Status:          "filled",
	}
	_ = p.jrnl.Append(Entry{Kind: "sell_result", ClientOrderID: clientOrderID,
		Detail: map[string]string{
			"base_scaled": strconv.FormatInt(baseQtyScaled, 10), "avg_true6": strconv.FormatInt(fillPrice, 10),
			"fee_quote_scaled": strconv.FormatInt(fee, 10), "proceeds_quote_scaled": strconv.FormatInt(proceeds, 10),
		}})
	p.log.Printf("[PAPER] sell fill: qty=%s price=%s proceeds=%s",
		num.FormatScaled(baseQtyScaled, p.mi.BaseScale), num.FormatScaled(fillPrice, 6), num.FormatScaled(proceeds, p.mi.QuoteScale))
	return fill, nil
}

// PlaceStopLoss arms the virtual stop_loss trigger. Take profit is enforced
// bot-side (same split as live mode).
func (p *PaperExecutor) PlaceStopLoss(ctx context.Context, baseQtyScaled, slScaled6 int64, slClientID string) (*StopLoss, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exits = append(p.exits,
		paperExit{triggerID: "paper-sl-" + slClientID, clientTriggerID: slClientID, kind: "stop_loss", priceScaled6: slScaled6, qtyScaled: baseQtyScaled},
	)
	_ = p.jrnl.Append(Entry{Kind: "triggers_placed", ClientTriggerID: slClientID,
		Detail: map[string]string{
			"sl_true6": strconv.FormatInt(slScaled6, 10), "base_scaled": strconv.FormatInt(baseQtyScaled, 10),
		}})
	return &StopLoss{
		TriggerID: "paper-sl-" + slClientID, ClientTriggerID: slClientID,
		PriceScaled6: slScaled6, BaseQtyScaled: baseQtyScaled,
	}, nil
}

// TriggerExecution reports a virtual trigger that fired against the stream.
type TriggerExecution struct {
	Kind            string
	TriggerID       string
	ClientTriggerID string
	Fill            *Fill
}

// CheckTriggers evaluates armed virtual exits against a trade price (TRUE
// price ticks, 1e6 scale) and executes any crossed trigger as a simulated
// sell. Called by the bot on every trade tick in paper mode.
func (p *PaperExecutor) CheckTriggers(priceScaled6 int64) []TriggerExecution {
	if priceScaled6 <= 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var fired []TriggerExecution
	var remaining []paperExit
	for _, ex := range p.exits {
		crossed := ex.kind == "stop_loss" && priceScaled6 <= ex.priceScaled6
		if !crossed {
			remaining = append(remaining, ex)
			continue
		}
		proceeds, err := num.ScaledMulDiv(ex.qtyScaled, p.mi.BaseScale, ex.priceScaled6, 6, 1, 0, p.mi.QuoteScale)
		if err != nil {
			p.log.Printf("[PAPER] trigger exec math error: %v", err)
			remaining = append(remaining, ex)
			continue
		}
		fee := proceeds * paperTakerFeeBps / 10000
		if ex.qtyScaled <= p.baseScaled {
			p.baseScaled -= ex.qtyScaled
			p.quoteScaled += proceeds - fee
		}
		fill := &Fill{
			ClientOrderID:   "paper-child-" + ex.clientTriggerID,
			OrderID:         "paper-child-" + ex.clientTriggerID,
			BaseQtyScaled:   ex.qtyScaled,
			AvgPriceScaled6: ex.priceScaled6,
			Status:          "filled",
		}
		fired = append(fired, TriggerExecution{
			Kind: ex.kind, TriggerID: ex.triggerID, ClientTriggerID: ex.clientTriggerID, Fill: fill,
		})
		_ = p.jrnl.Append(Entry{Kind: "trigger_executed", ClientTriggerID: ex.clientTriggerID,
			Detail: map[string]string{"kind": ex.kind, "true6": strconv.FormatInt(ex.priceScaled6, 10)}})
	}
	p.exits = remaining
	return fired
}

// CancelTrigger removes a virtual trigger (idempotent).
func (p *PaperExecutor) CancelTrigger(ctx context.Context, triggerID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var kept []paperExit
	for _, ex := range p.exits {
		if ex.triggerID != triggerID {
			kept = append(kept, ex)
		}
	}
	p.exits = kept
	return nil
}

// TriggerStatus reports "armed" for live virtual triggers.
func (p *PaperExecutor) TriggerStatus(ctx context.Context, triggerID string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ex := range p.exits {
		if ex.triggerID == triggerID {
			return StatusArmed, nil
		}
	}
	return "", fmt.Errorf("trigger %s not found", triggerID)
}

// ActiveTriggers lists armed virtual triggers.
func (p *PaperExecutor) ActiveTriggers(ctx context.Context) ([]TriggerRef, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]TriggerRef, 0, len(p.exits))
	for _, ex := range p.exits {
		out = append(out, TriggerRef{TriggerID: ex.triggerID, ClientTriggerID: ex.clientTriggerID, TriggerType: ex.kind, Status: StatusArmed})
	}
	return out, nil
}

// OpenOrders is always empty in paper mode (IOC fills are immediate).
func (p *PaperExecutor) OpenOrders(ctx context.Context) ([]OrderRef, error) { return nil, nil }

// CancelAllOrders is a no-op in paper mode.
func (p *PaperExecutor) CancelAllOrders(ctx context.Context) error { return nil }

// ArmDeadMan / RefreshDeadMan are no-ops in paper mode.
func (p *PaperExecutor) ArmDeadMan(ctx context.Context) error     { return nil }
func (p *PaperExecutor) RefreshDeadMan(ctx context.Context) error { return nil }

// QuoteAvailable returns the virtual quote balance.
func (p *PaperExecutor) QuoteAvailable(ctx context.Context) (float64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return num.ScaledToFloat(p.quoteScaled, p.mi.QuoteScale), nil
}

// BaseBalance returns the virtual base balance.
func (p *PaperExecutor) BaseBalance(ctx context.Context) (float64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return num.ScaledToFloat(p.baseScaled, p.mi.BaseScale), nil
}
