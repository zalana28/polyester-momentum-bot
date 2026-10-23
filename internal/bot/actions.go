package bot

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Fabric-Labs/polyester-sdk-go/models"

	"polyester-momentum-bot/internal/brain"
	"polyester-momentum-bot/internal/execution"
	"polyester-momentum-bot/internal/journal"
	"polyester-momentum-bot/internal/market"
	"polyester-momentum-bot/internal/num"
	"polyester-momentum-bot/internal/state"
	"polyester-momentum-bot/internal/strategy"
)

// onClosedCandle advances one symbol's engine and dispatches its signals.
// A buy signal is only a TRIGGER: the brain must approve it based on the
// multi-factor market score before any order is placed.
func (b *Bot) onClosedCandle(s *symRT, cc market.ClosedCandle) {
	if cc.CloseF > 0 {
		s.mark = b.truePrice(s, cc.CloseF)
		s.exec.SetMark(s.mark)
	}

	// Feed the brain's window (true prices).
	s.window = append(s.window, brain.Candle{
		TsSec: cc.TsSec, High: b.truePrice(s, cc.HighF),
		Low: b.truePrice(s, cc.LowF), Close: b.truePrice(s, cc.CloseF), Volume: cc.VolF,
	})
	if len(s.window) > 200 {
		s.window = s.window[len(s.window)-200:]
	}

	// Heartbeat candle log (dedup by ts against seed overlap).
	if cc.TsSec != s.lastLoggedTs {
		s.lastLoggedTs = cc.TsSec
		b.log.Printf("[CANDLE] %s ts=%d close=%.8g vol=%.6g", s.symbol, cc.TsSec, b.truePrice(s, cc.CloseF), cc.VolF)
	}

	sc := make([]strategy.Candle, len(s.window))
	for i, w := range s.window {
		sc[i] = strategy.Candle{TsSec: w.TsSec, High: w.High, Low: w.Low, Close: w.Close, Volume: w.Volume}
	}
	regime := strategy.ClassifyRegime(sc, b.cfg.EMAFast, b.cfg.EMASlow)
	b.shadowTick(s)
	buy, anyExit, votes := s.ens.OnCandle(strategy.Candle{
		TsSec: cc.TsSec, High: b.truePrice(s, cc.HighF), Low: b.truePrice(s, cc.LowF),
		Close: b.truePrice(s, cc.CloseF), Volume: cc.VolF,
	}, regime)

	if b.st.Halted() {
		reason, _ := b.st.HaltReason()
		b.log.Printf("[HALT] %s signals skipped (halted: %s)", s.symbol, reason)
		return
	}

	if buy != nil {
		var verdict brain.Verdict
		if b.brain.Enabled() {
			if cooling, wait := b.brain.CoolingDown(s.symbol, time.Now()); cooling {
				b.log.Printf("[BRAIN] %s buy vetoed: cooling down %.0fs after a loss", s.symbol, wait.Seconds())
				b.shadowSignal(s, cc.TsSec, *buy, 0, "cooldown", regime)
				return
			}
			verdict = b.brain.Evaluate(s.symbol, s.window, true, 0, 0)
			if !verdict.Buy {
				b.log.Printf("[BRAIN] %s buy VETOED: %s", s.symbol, verdict.Reason)
				b.shadowSignal(s, cc.TsSec, *buy, verdict.Score, "vetoed", regime)
				return
			}
			b.log.Printf("[BRAIN] %s buy approved (%s) sizing %.2fx", s.symbol, verdict.Reason, verdict.Sizing)
			b.sizingOverride = verdict.Sizing
			b.shadowSignal(s, cc.TsSec, *buy, verdict.Score, "approved", regime)
		} else {
			b.shadowSignal(s, cc.TsSec, *buy, -1, "brain-off", regime)
		}
		b.handleBuy(s, buy.Strategy, verdict.Score, string(verdict.Regime))
		return
	}

	if anyExit && s.pos != nil {
		for _, v := range votes {
			if v.Strategy == s.pos.Strategy && v.Kind == "exit" {
				b.log.Printf("[SIGNAL] %s exit via %s: %s", s.symbol, v.Strategy, v.Reason)
				b.handleExit(s)
				return
			}
		}
	}
}

// handleBuy applies the risk gate then executes buy + protective exits.
// stratName is the ensemble member whose signal triggered the entry; the
// position remembers it so exits and PnL attribution follow the same strategy.
func (b *Bot) handleBuy(s *symRT, stratName string, signalScore float64, signalRegime string) {
	if s.pos != nil {
		b.log.Printf("[EXEC] %s buy skipped: position already open", s.symbol)
		return
	}
	// Shadow mode: record what the approved trade would have done, send nothing.
	if b.cfg.ShadowOnly {
		b.log.Printf("[SHADOW] %s would open (score %.1f, %s): shadow-only, no order sent", s.symbol, signalScore, stratName)
		return
	}
	// Fail closed: without a fresh balance reading the daily-loss guard cannot
	// evaluate, so a new position would be opened blind.
	if b.cfg.Mode == configModeLive && !b.equityFresh(time.Now()) {
		b.log.Printf("[EXEC] %s buy skipped: equity unknown (ledger read failing) — daily-loss guard cannot evaluate", s.symbol)
		return
	}
	if err := b.dec.CanOpenPosition(b.st.Halted(), 0); err != nil {
		b.log.Printf("[EXEC] buy skipped: %v", err)
		return
	}
	if n := b.openPositionCount(); n >= b.cfg.MaxOpenPositions {
		b.log.Printf("[EXEC] %s buy skipped: max open positions reached (%d)", s.symbol, n)
		return
	}

	budget, err := num.ParseDecimalToScaled(b.cfg.TradeBudgetQuote, s.mi.QuoteScale)
	if err != nil {
		b.log.Printf("[EXEC] buy skipped: bad budget: %v", err)
		return
	}
	// Brain conviction sizing: scale the budget by the verdict multiplier.
	budget = num.FloatToScaledRounded(num.ScaledToFloat(budget, s.mi.QuoteScale)*b.sizingOverride, s.mi.QuoteScale)
	b.sizingOverride = 1.0
	// Clamp to available quote (2% buffer for fees/price movement).
	if avail, err := s.exec.QuoteAvailable(b.opCtx); err == nil && avail > 0 {
		availScaled := num.FloatToScaledRounded(avail*0.98, s.mi.QuoteScale)
		if availScaled < s.mi.MinNotionalQuoteScaled {
			b.log.Printf("[EXEC] %s buy skipped: available quote below pair minimum (%.2f)", s.symbol, avail)
			return
		}
		if availScaled < budget {
			b.log.Printf("[EXEC] %s budget clamped to available: %s -> %s",
				s.symbol, num.FormatScaled(budget, s.mi.QuoteScale), num.FormatScaled(availScaled, s.mi.QuoteScale))
			budget = availScaled
		}
	}

	b.executeEntry(s, budget, stratName, signalScore, signalRegime)
}

// executeEntry places the market buy and its protection. Shared by the signal
// path (handleBuy) and manual orders from the control socket, so a chat-driven
// entry gets exactly the same stop-loss and exit plan as a signal-driven one.
func (b *Bot) executeEntry(s *symRT, budget int64, stratName string, signalScore float64, signalRegime string) error {
	coid := b.newID(s, "buy")
	fill, err := s.exec.Buy(b.opCtx, budget, coid)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrAmbiguous):
			if b.cfg.Mode == configModeLive {
				b.log.Printf("[EXEC] %s buy ambiguous: reconciling before anything else", s.symbol)
				b.reconcile(b.opCtx, s)
				return fmt.Errorf("buy ambiguous: %w", err)
			}
			b.log.Printf("[EXEC] %s buy ambiguous (paper): %v", s.symbol, err)
		case errors.Is(err, execution.ErrDust):
			b.log.Printf("[EXEC] buy skipped: %v", err)
		default:
			b.log.Printf("[EXEC] %s buy failed: %v", s.symbol, err)
		}
		return err
	}

	avgScaled6 := fill.AvgPriceScaled6
	if avgScaled6 <= 0 {
		avgScaled6 = num.FloatToScaledRounded(s.mark, 6)
	}
	slScaled6 := num.RoundToTickFloor(num.MulBpsFloor(avgScaled6, 10000-int64(b.cfg.StopLossBps)), s.mi.TickTicks)
	tpScaled6 := num.RoundToTickFloor(num.MulBpsFloor(avgScaled6, 10000+int64(b.cfg.TakeProfitBps)), s.mi.TickTicks)
	if slScaled6 <= 0 || tpScaled6 <= avgScaled6 {
		b.log.Printf("[EXEC] CRITICAL: %s derived trigger prices invalid (avg=%d sl=%d tp=%d); flattening",
			s.symbol, avgScaled6, slScaled6, tpScaled6)
		b.emergencyFlatten(s, fill.BaseQtyScaled, avgScaled6, "invalid trigger prices")
		return fmt.Errorf("derived trigger prices invalid")
	}
	if fill.Partial {
		b.log.Printf("[EXEC] note: partial IOC fill (status=%s)", fill.Status)
	}

	sl, err := s.exec.PlaceStopLoss(b.opCtx, fill.BaseQtyScaled, slScaled6, b.newID(s, "sl"))
	if err != nil {
		b.log.Printf("[EXEC] CRITICAL: %s stop-loss placement failed (%v); re-arming before anything else", s.symbol, err)
		sl2, rerr := b.rearmSL(s, fill.BaseQtyScaled, avgScaled6)
		if rerr != nil {
			b.log.Printf("[EXEC] CRITICAL: %s re-arm failed (%v); emergency flatten", s.symbol, rerr)
			b.emergencyFlatten(s, fill.BaseQtyScaled, avgScaled6, "exit placement failed")
			return fmt.Errorf("stop-loss placement failed: %w", err)
		}
		sl = sl2
	}

	bid, ask := bookTop(s)
	s.pos = &state.Position{
		BaseQtyScaled:      fill.BaseQtyScaled,
		BaseScale:          s.mi.BaseScale,
		AvgEntryScaled6:    avgScaled6,
		EntryClientOrderID: fill.ClientOrderID,
		EntryOrderID:       fill.OrderID,
		Strategy:           stratName,
		SignalScore:        signalScore,
		SignalRegime:       signalRegime,
		SignalTs:           time.Now().UTC(),
		BidOpen:            bid,
		AskOpen:            ask,
		SLTriggerID:        sl.TriggerID,
		SLClientTriggerID:  sl.ClientTriggerID,
		SLPriceScaled6:     sl.PriceScaled6,
		TPPriceScaled6:     tpScaled6,
		OpenedAt:           time.Now().UTC(),
	}
	b.log.Printf("[EXEC] %s position opened: base=%s entry=%s SL=%s TP=%s (sl_id=%s, TP bot-side)",
		s.symbol,
		num.FormatScaled(fill.BaseQtyScaled, s.mi.BaseScale),
		num.FormatScaled(avgScaled6, 6),
		num.FormatScaled(slScaled6, 6), num.FormatScaled(tpScaled6, 6),
		sl.TriggerID)
	return nil
}

// handleExit flattens the symbol's open position (cancel exits first, then sell).
func (b *Bot) handleExit(s *symRT) {
	if s.pos == nil {
		return
	}
	b.flatten(s, "exit signal")
}

// flatten cancels both protective triggers, verifies, then sells the net base
// quantity. On sell failure it re-arms the exits so the position is never
// left unprotected.
func (b *Bot) flatten(s *symRT, reason string) {
	pos := s.pos
	if pos == nil {
		return
	}
	b.log.Printf("[EXEC] flattening %s (reason=%s): base=%s entry=%s",
		s.symbol, reason, num.FormatScaled(pos.BaseQtyScaled, s.mi.BaseScale), num.FormatScaled(pos.AvgEntryScaled6, 6))

	b.cancelExits(s, pos)

	sold, remaining, exitScaled6, err := b.sellWithOneRetry(s, pos.BaseQtyScaled)
	if err != nil {
		if errors.Is(err, execution.ErrDust) {
			b.log.Printf("[EXEC] %s position is dust (%v); closing tracking", s.symbol, err)
			s.pos = nil
			_ = b.jrnl.Append(journal.Entry{Kind: journal.KindFlat, Detail: map[string]string{"reason": reason + " (dust)"}})
			return
		}
		// Re-arm protection so the position is not naked, then give up this round.
		b.log.Printf("[EXEC] %s sell failed (%v); re-arming protective exits", s.symbol, err)
		if _, rerr := b.rearmSL(s, pos.BaseQtyScaled, pos.AvgEntryScaled6); rerr != nil {
			b.log.Printf("[EXEC] CRITICAL: %s re-arm after failed sell also failed (%v)", s.symbol, rerr)
		}
		b.blockExit(s.symbol, exitRetryCooldown)
		return
	}
	if remaining > 0 {
		// Partial fills left a remainder: book the sold portion, shrink the
		// position to the remainder, and re-arm exits on it so it stays
		// protected.
		b.log.Printf("[EXEC] %s partial flatten: sold=%s remaining=%s",
			s.symbol, num.FormatScaled(sold, s.mi.BaseScale), num.FormatScaled(remaining, s.mi.BaseScale))
		b.bookPartialPnL(pos, sold, exitScaled6)
		pos.BaseQtyScaled = remaining
		s.pos = pos
		if _, rerr := b.rearmSL(s, remaining, pos.AvgEntryScaled6); rerr != nil {
			b.log.Printf("[EXEC] CRITICAL: %s re-arm on remainder failed (%v)", s.symbol, rerr)
		}
		return
	}
	b.recordClosedPosition(s, pos, exitScaled6, reason, sold)
}

// sellWithOneRetry sells baseQty with a market IOC; partial IOC fills leave a
// remainder, so one follow-up sell of the remainder is attempted. Returns
// (totalSold, remainingDust, avgExitScaled6). remainingDust is 0 unless the
// leftover is below the pair minimum; avgExit is quantity-weighted.
func (b *Bot) sellWithOneRetry(s *symRT, baseQty int64) (sold, remaining, exitScaled6 int64, err error) {
	fill, err := s.exec.Sell(b.opCtx, baseQty, b.newID(s, "sell"))
	if err != nil {
		return 0, baseQty, 0, err
	}
	sold, remaining = fill.BaseQtyScaled, baseQty-fill.BaseQtyScaled
	num_, den := fill.BaseQtyScaled*fill.AvgPriceScaled6, fill.BaseQtyScaled
	if remaining >= s.mi.MinQtyScaled {
		fill2, err2 := s.exec.Sell(b.opCtx, remaining, b.newID(s, "sell"))
		if err2 != nil {
			b.log.Printf("[EXEC] %s remainder sell failed (%v): %s base unsold",
				s.symbol, err2, num.FormatScaled(remaining, s.mi.BaseScale))
		} else {
			num_ += fill2.BaseQtyScaled * fill2.AvgPriceScaled6
			den += fill2.BaseQtyScaled
			sold += fill2.BaseQtyScaled
			remaining -= fill2.BaseQtyScaled
		}
	}
	if den > 0 {
		exitScaled6 = num_ / den
	}
	if remaining < s.mi.MinQtyScaled {
		remaining = 0 // dust: untradeable, drop from tracking
	}
	return sold, remaining, exitScaled6, nil
}

// emergencyFlatten sells without touching triggers (used when exits cannot be
// placed at all).
func (b *Bot) emergencyFlatten(s *symRT, baseScaled, avgScaled6 int64, reason string) {
	sold, remaining, exitScaled6, err := b.sellWithOneRetry(s, baseScaled)
	if err != nil {
		b.log.Printf("[EXEC] CRITICAL: %s emergency sell failed (%v); manual attention required", s.symbol, err)
		return
	}
	pos := &state.Position{BaseQtyScaled: baseScaled, BaseScale: s.mi.BaseScale, AvgEntryScaled6: avgScaled6}
	if sold > 0 {
		b.bookPartialPnL(pos, sold, exitScaled6)
	}
	if remaining > 0 {
		b.log.Printf("[EXEC] CRITICAL: %s emergency flatten left %s base unsold; attempting exits for it",
			s.symbol, num.FormatScaled(remaining, s.mi.BaseScale))
		if _, rerr := b.rearmSL(s, remaining, avgScaled6); rerr != nil {
			b.log.Printf("[EXEC] CRITICAL: %s exits on emergency remainder failed (%v); base is unprotected",
				s.symbol, rerr)
		}
		return
	}
	b.log.Printf("[EXEC] %s emergency flatten complete (%s sold)", s.symbol, num.FormatScaled(sold, s.mi.BaseScale))
}

// cancelExits cancels both triggers and verifies terminal status.
func (b *Bot) cancelExits(s *symRT, pos *state.Position) {
	for _, id := range []string{pos.SLTriggerID, pos.TPTriggerID} {
		if id == "" {
			continue
		}
		if err := s.exec.CancelTrigger(b.opCtx, id); err != nil {
			b.log.Printf("[EXEC] %s trigger %s cancel error: %v", s.symbol, id, err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		allDone := true
		for _, id := range []string{pos.SLTriggerID, pos.TPTriggerID} {
			if id == "" {
				continue
			}
			st, err := s.exec.TriggerStatus(b.opCtx, id)
			if err == nil && !execution.IsTerminalTriggerStatus(st) {
				allDone = false
				_ = s.exec.CancelTrigger(b.opCtx, id)
			}
		}
		if allDone {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	b.log.Printf("[EXEC] warning: %s exit triggers may still be live after cancel attempts", s.symbol)
}

// rearmSL places a fresh stop-loss trigger for the position with a new
// client ID. Take profit stays bot-side.
func (b *Bot) rearmSL(s *symRT, baseScaled, avgEntryScaled6 int64) (*execution.StopLoss, error) {
	slScaled6 := num.RoundToTickFloor(num.MulBpsFloor(avgEntryScaled6, 10000-int64(b.cfg.StopLossBps)), s.mi.TickTicks)
	return s.exec.PlaceStopLoss(b.opCtx, baseScaled, slScaled6, b.newID(s, "sl"))
}

// recordClosedPosition clears state and books realized PnL.
func (b *Bot) recordClosedPosition(s *symRT, pos *state.Position, exitScaled6 int64, reason string, soldScaled int64) {
	// Idempotensi: cegah double-booking kalau dua jalur (reconcile + exit
	// signal) menutup posisi yang sama di tick yang sama.
	if s.pos == nil || s.pos.EntryOrderID != pos.EntryOrderID {
		return
	}
	if exitScaled6 <= 0 {
		// Defensive: an unreported fill price would fake a total loss.
		b.log.Printf("[EXEC] %s exit price missing for %s; booking at entry (PnL 0)", s.symbol, reason)
		exitScaled6 = pos.AvgEntryScaled6
	}
	baseUnits := num.ScaledToFloat(pos.BaseQtyScaled, s.mi.BaseScale)
	entry := num.ScaledToFloat(pos.AvgEntryScaled6, 6)
	exit := num.ScaledToFloat(exitScaled6, 6)
	pnl := (exit - entry) * baseUnits
	notional := entry * baseUnits
	feeIn, feeOut := notional*0.0015, exit*baseUnits*0.0015
	bidC, askC := bookTop(s)
	b.tradeCSV(map[string]string{
		"ts_close":       time.Now().UTC().Format(time.RFC3339),
		"symbol":         s.symbol,
		"strategy":       pos.Strategy,
		"regime":         pos.SignalRegime,
		"signal_score":   fmt.Sprintf("%.1f", pos.SignalScore),
		"signal_time":    pos.SignalTs.Format(time.RFC3339),
		"hold_secs":      strconv.FormatInt(int64(time.Since(pos.OpenedAt).Seconds()), 10),
		"entry":          num.FormatScaled(pos.AvgEntryScaled6, 6),
		"qty":            num.FormatScaled(pos.BaseQtyScaled, s.mi.BaseScale),
		"exit":           num.FormatScaled(exitScaled6, 6),
		"gross_usdt":     strconv.FormatFloat(pnl, 'f', 4, 64),
		"fee_entry_usdt": strconv.FormatFloat(feeIn, 'f', 4, 64),
		"fee_exit_usdt":  strconv.FormatFloat(feeOut, 'f', 4, 64),
		"net_usdt":       strconv.FormatFloat(pnl-feeIn-feeOut, 'f', 4, 64),
		"bid_at_open":    fmt.Sprintf("%.8g", pos.BidOpen),
		"ask_at_open":    fmt.Sprintf("%.8g", pos.AskOpen),
		"bid_at_close":   fmt.Sprintf("%.8g", bidC),
		"ask_at_close":   fmt.Sprintf("%.8g", askC),
		"exit_reason":    reason,
	})
	b.st.AddRealizedPnL(pnl)
	b.brain.ReportResult(s.symbol, pnl)
	// Normalized PnL (1.0 = a full TP win, -1.0 = a full SL loss) feeds the
	// ensemble's adaptive weighting: losing strategies lose their voice.
	if pos.Strategy != "" && pos.TPPriceScaled6 > 0 && pos.AvgEntryScaled6 > 0 {
		b.ensReport(s, pos.Strategy, exitScaled6, pos.AvgEntryScaled6, pos.TPPriceScaled6-pos.AvgEntryScaled6)
	}
	s.pos = nil
	_ = b.jrnl.Append(journal.Entry{Kind: journal.KindFlat, Detail: map[string]string{
		"symbol":     s.symbol,
		"reason":     reason,
		"pnl":        strconv.FormatFloat(pnl, 'f', 8, 64),
		"exit_true6": strconv.FormatInt(exitScaled6, 10),
	}})
	b.log.Printf("[EXEC] %s position closed (reason=%s): sold=%s exit=%s realizedPnL=%.6f total=%.6f",
		s.symbol, reason, num.FormatScaled(soldScaled, s.mi.BaseScale), num.FormatScaled(exitScaled6, 6), pnl, b.st.RealizedPnL())
}

// bookPartialPnL books realized PnL for a partially closed position without
// clearing it. exitScaled6 is the observed exit price (true ticks).
func (b *Bot) bookPartialPnL(pos *state.Position, soldScaled, exitScaled6 int64) {
	if exitScaled6 <= 0 {
		exitScaled6 = pos.AvgEntryScaled6
	}
	baseUnits := num.ScaledToFloat(soldScaled, pos.BaseScale)
	pnl := (num.ScaledToFloat(exitScaled6, 6) - num.ScaledToFloat(pos.AvgEntryScaled6, 6)) * baseUnits
	b.st.AddRealizedPnL(pnl)
	_ = b.jrnl.Append(journal.Entry{Kind: journal.KindFlat, Detail: map[string]string{
		"reason": "partial", "pnl": strconv.FormatFloat(pnl, 'f', 8, 64),
	}})
}

// onTrade updates the mark; in paper mode it also drives virtual triggers.
func (b *Bot) onTrade(s *symRT, t models.MarketTrade) {
	ticks := t.Price.Ticks()
	if ticks <= 0 {
		return
	}
	s.mark = num.ScaledToFloat(ticks, s.mi.WireScale) // TRUE price
	s.exec.SetMark(s.mark)

	priceScaled6 := b.wireTicksToTrue6(s, ticks)

	// Bot-side take profit (single server trigger design): TP touched ->
	// cancel SL, sell at market.
	if pos := s.pos; pos != nil && pos.TPPriceScaled6 > 0 && priceScaled6 >= pos.TPPriceScaled6 {
		b.log.Printf("[EXEC] %s take profit touched at %s (target %s)", s.symbol,
			num.FormatScaled(priceScaled6, 6), num.FormatScaled(pos.TPPriceScaled6, 6))
		b.flatten(s, "take profit")
		return
	}

	// Exit plan: partial take-profit, trailing stop, time stop. Runs before the
	// paper trigger loop because it may close the position outright.
	if s.pos != nil {
		b.managePosition(s, priceScaled6)
		if s.pos == nil {
			return
		}
	}

	if s.paperEx == nil {
		return
	}
	for _, fired := range s.paperEx.CheckTriggers(priceScaled6) {
		b.onPaperTriggerFired(s, fired)
	}
}

// onPaperTriggerFired handles a simulated SL/TP execution: cancel the sibling
// and close the position.
func (b *Bot) onPaperTriggerFired(s *symRT, fired execution.TriggerExecution) {
	pos := s.pos
	if pos == nil {
		return
	}
	b.log.Printf("[PAPER] %s trigger fired: %s at %s", s.symbol, fired.Kind, num.FormatScaled(fired.Fill.AvgPriceScaled6, 6))
	b.recordClosedPosition(s, pos, fired.Fill.AvgPriceScaled6, fired.Kind, fired.Fill.BaseQtyScaled)
}

// onOrderUpdate consumes the account-wide private order stream (live) and
// routes the order to its symbol by pair id.
func (b *Bot) onOrderUpdate(o models.Order) {
	s := b.symByPairID[o.SymbolID]
	if s == nil {
		return
	}
	pos := s.pos
	if pos == nil {
		return
	}
	if o.ClientOrderID != "" && o.ClientOrderID == pos.EntryClientOrderID {
		b.log.Printf("[STATE] %s entry order update: status=%s", s.symbol, o.Status)
		return
	}
	// A terminal sell while a position is open is likely a trigger child
	// order; verify flatness from balances rather than guessing quantities.
	if o.Side == "sell" && (o.Status == "filled" || o.Status == "canceled") {
		b.checkPositionFlat(s, "order stream")
	}
}

// onTriggerEvent consumes trigger lifecycle events (live): cancels the
// sibling when one leg terminates, and verifies flatness after a fire.
func (b *Bot) onTriggerEvent(te models.TriggerEvent) {
	s := b.symByPairID[te.SymbolID]
	if s == nil {
		return
	}
	pos := s.pos
	if pos == nil {
		return
	}
	if te.TriggerID != pos.SLTriggerID {
		return
	}
	b.log.Printf("[STATE] %s trigger event: id=%s type=%s event=%s", s.symbol, te.TriggerID, te.TriggerType, te.EventType)

	st, err := s.exec.TriggerStatus(b.opCtx, te.TriggerID)
	if err != nil {
		b.log.Printf("[STATE] trigger status check failed: %v", err)
		return
	}
	if !execution.IsTerminalTriggerStatus(st) {
		return // still armed/running
	}
	switch st {
	case execution.StatusCompleted:
		// The stop loss fired and its child sell ran: confirm flatness.
		b.checkPositionFlat(s, "stop loss completed")
	default:
		// Stop loss cancelled/failed while position open: re-arm it.
		if _, err := b.rearmSL(s, pos.BaseQtyScaled, pos.AvgEntryScaled6); err != nil {
			b.log.Printf("[STATE] CRITICAL: %s re-arm after trigger %s failed: %v", s.symbol, st, err)
		}
	}
}

// checkPositionFlat closes position tracking when the base balance is dust.
func (b *Bot) checkPositionFlat(s *symRT, via string) {
	pos := s.pos
	if pos == nil {
		return
	}
	base, err := s.exec.BaseBalance(b.opCtx)
	if err != nil {
		b.log.Printf("[STATE] flat check failed: %v", err)
		return
	}
	dustUnits := num.ScaledToFloat(s.mi.MinQtyScaled, s.mi.BaseScale)
	if base < dustUnits {
		b.log.Printf("[STATE] %s position flat via %s (base=%.8g < dust)", s.symbol, via, base)
		b.recordClosedPosition(s, pos, num.FloatToScaledRounded(s.mark, 6), "trigger exit ("+via+")", pos.BaseQtyScaled)
	}
}

// onFeedError records a symbol feed failure. Reconnect timing lives in
// market.Feeds (backoff, then REST polling); recovery re-triggers reconcile
// through Feeds.SetOnRestore.
func (b *Bot) onFeedError(s *symRT, err error) {
	// Feeds own their reconnect timing (backoff, then REST polling) so a
	// flapping stream cannot turn into a subscribe/reset storm. The bot only
	// records the failure; recovery is reported via Feeds.SetOnRestore.
	if s == nil {
		b.log.Printf("[FEED] stream failure: %v", err)
		return
	}
	b.log.Printf("[FEED] %s feed failure: %v", s.symbol, err)
}

// startupReconcile replays the journal then reconciles every symbol.
func (b *Bot) startupReconcile(ctx context.Context) error {
	if err := b.replayJournal(ctx); err != nil {
		b.log.Printf("[RECON] journal replay warning: %v", err)
	}
	for _, s := range b.syms {
		b.reconcile(ctx, s)
	}
	return nil
}

// replayJournal resolves unresolved buy intents: any buy_intent without a
// matching buy_result is looked up by client order ID; an admitted order is
// adopted (position + exits re-armed by reconcile).
func (b *Bot) replayJournal(ctx context.Context) error {
	entries, err := journal.Load(b.cfg.StateDir)
	if err != nil {
		return err
	}
	resolved := map[string]bool{}
	for _, e := range entries {
		if e.Kind == journal.KindBuyResult && e.ClientOrderID != "" {
			resolved[e.ClientOrderID] = true
		}
	}
	for _, e := range entries {
		if e.Kind != journal.KindBuyIntent || e.ClientOrderID == "" || resolved[e.ClientOrderID] {
			continue
		}
		gr, err := b.client.Orders.Get(ctx, nil, models.OrderKeyByClientID(e.ClientOrderID), nil, false, false)
		if err != nil {
			if execution.IsNotFound(err) {
				b.log.Printf("[RECON] buy intent %s was never admitted — safe", e.ClientOrderID)
				_ = b.jrnl.Append(journal.Entry{Kind: journal.KindReconcile,
					ClientOrderID: e.ClientOrderID, Detail: map[string]string{"verdict": "not_admitted"}})
				continue
			}
			return fmt.Errorf("lookup %s: %w", e.ClientOrderID, err)
		}
		if gr.Order != nil {
			b.log.Printf("[RECON] adopting admitted order %s (status=%s) from journal", gr.Order.OrderID, gr.Order.Status)
			_ = b.jrnl.Append(journal.Entry{Kind: journal.KindBuyResult, ClientOrderID: e.ClientOrderID,
				Detail: map[string]string{"verdict": "adopted", "status": gr.Order.Status}})
		}
	}
	return nil
}

// reconcile enforces the core invariants for one symbol against server state:
//   - no untracked bot orders (cancel them)
//   - position exists => both SL and TP exist (re-arm missing legs)
//   - no position     => no bot triggers active (cancel them)
//   - balances agree with tracked position (close tracking when flat)
func (b *Bot) reconcile(ctx context.Context, s *symRT) {
	if b.st.Halted() {
		return
	}
	pos := s.pos

	// 1. Untracked open orders with our prefix: cancel.
	if refs, err := s.exec.OpenOrders(ctx); err == nil {
		for _, r := range refs {
			if !isOurs(s, r.ClientOrderID) {
				continue
			}
			if pos != nil && r.ClientOrderID == pos.EntryClientOrderID {
				continue
			}
			b.log.Printf("[RECON] cancelling untracked open order %s (%s)", r.OrderID, r.ClientOrderID)
			_, _ = b.client.Orders.Cancel(ctx, nil, models.OrderKeyByID(r.OrderID), &s.mi.Symbol, nil, nil)
		}
	} else {
		b.log.Printf("[RECON] %s open order listing failed: %v", s.symbol, err)
	}

	// 2. Triggers: enforce the OCO invariant.
	active, err := s.exec.ActiveTriggers(ctx)
	if err != nil {
		b.log.Printf("[RECON] %s trigger listing failed: %v", s.symbol, err)
		return
	}
	var slRef *execution.TriggerRef
	for i := range active {
		if !isOurs(s, active[i].ClientTriggerID) {
			continue
		}
		if active[i].TriggerType == "stop_loss" {
			slRef = &active[i]
		}
	}

	if pos == nil {
		b.reconcileNoPosition(ctx, s, slRef)
		return
	}
	b.reconcileWithPosition(ctx, s, pos, slRef)
}

// reconcileNoPosition adopts an orphaned position (base balance + triggers)
// or cleans up stale triggers.
func (b *Bot) reconcileNoPosition(ctx context.Context, s *symRT, slRef *execution.TriggerRef) {
	baseUnits, berr := s.exec.BaseBalance(ctx)
	if berr != nil {
		b.logReconFail(s.symbol, berr)
		return
	}
	dustUnits := num.ScaledToFloat(s.mi.MinQtyScaled, s.mi.BaseScale)
	if baseUnits >= dustUnits && slRef != nil {
		b.log.Printf("[RECON] %s adopting orphaned position: base=%.8g sl_armed=%v",
			s.symbol, baseUnits, slRef != nil)
		adopted := &state.Position{
			BaseQtyScaled:   num.FloatToScaledRounded(baseUnits, s.mi.BaseScale),
			BaseScale:       s.mi.BaseScale,
			AvgEntryScaled6: num.FloatToScaledRounded(s.mark, 6),
			OpenedAt:        time.Now().UTC(),
		}
		sl, err := b.ensureStopLoss(ctx, s, adopted, slRef)
		if err != nil {
			b.log.Printf("[RECON] %s adopt failed: %v", s.symbol, err)
			return
		}
		adopted.SLTriggerID, adopted.SLClientTriggerID = sl.TriggerID, sl.ClientTriggerID
		adopted.SLPriceScaled6 = sl.PriceScaled6
		s.pos = adopted
		_ = b.jrnl.Append(journal.Entry{Kind: journal.KindReconcile, Detail: map[string]string{"verdict": "adopted_position", "symbol": s.symbol}})
		return
	}
	if slRef != nil {
		b.log.Printf("[RECON] %s cancelling stale trigger %s (%s)", s.symbol, slRef.TriggerID, slRef.TriggerType)
		if err := s.exec.CancelTrigger(ctx, slRef.TriggerID); err != nil {
			b.log.Printf("[RECON] stale trigger cancel failed: %v", err)
		}
	}
}

// reconcileWithPosition guarantees both exits exist for the tracked position
// and closes tracking when balances show flat. The flatness check runs FIRST
// so a position that was just closed by a trigger is never re-armed.
func (b *Bot) reconcileWithPosition(ctx context.Context, s *symRT, pos *state.Position, slRef *execution.TriggerRef) {
	// 1. Balance agreement: tracked position but base is dust => flat.
	if baseUnits, err := s.exec.BaseBalance(ctx); err == nil {
		dustUnits := num.ScaledToFloat(s.mi.MinQtyScaled, s.mi.BaseScale)
		if baseUnits < dustUnits {
			b.log.Printf("[RECON] %s tracked position but base balance is dust (%.8g) — closing tracking",
				s.symbol, baseUnits)
			b.recordClosedPosition(s, pos, num.FloatToScaledRounded(s.mark, 6), "reconcile (flat)", pos.BaseQtyScaled)
			// Any surviving triggers are now stale; the next cycle cleans up.
			return
		}
	}

	// 2. Protection invariant: the stop-loss trigger exists.
	sl, err := b.ensureStopLoss(ctx, s, pos, slRef)
	if err != nil {
		b.log.Printf("[RECON] %s protection invariant not satisfiable yet: %v", s.symbol, err)
		return
	}
	if sl.TriggerID != pos.SLTriggerID {
		pos.SLTriggerID, pos.SLClientTriggerID = sl.TriggerID, sl.ClientTriggerID
		pos.SLPriceScaled6 = sl.PriceScaled6
		s.pos = pos
		_ = b.jrnl.Append(journal.Entry{Kind: journal.KindReconcile, Detail: map[string]string{
			"symbol": s.symbol, "verdict": "rearmed", "sl": sl.TriggerID}})
		b.log.Printf("[RECON] %s stop loss re-armed: sl=%s", s.symbol, sl.TriggerID)
	}
}

// ensureStopLoss guarantees exactly one armed stop-loss for the position:
// adopts the matching active trigger, or places a fresh one.
func (b *Bot) ensureStopLoss(ctx context.Context, s *symRT, pos *state.Position, slRef *execution.TriggerRef) (*execution.StopLoss, error) {
	slScaled6 := num.RoundToTickFloor(num.MulBpsFloor(pos.AvgEntryScaled6, 10000-int64(b.cfg.StopLossBps)), s.mi.TickTicks)

	if slRef != nil {
		return &execution.StopLoss{
			TriggerID: slRef.TriggerID, ClientTriggerID: slRef.ClientTriggerID,
			PriceScaled6: slScaled6, BaseQtyScaled: pos.BaseQtyScaled,
		}, nil
	}
	return s.exec.PlaceStopLoss(ctx, pos.BaseQtyScaled, slScaled6, b.newID(s, "sl"))
}

// ensReport feeds a normalized result into the ensemble so the responsible
// strategy's adaptive weight moves toward its realized performance.
// norm = (exit - entry) / (TP - entry): +1 at a full TP win, -1 at a full SL
// loss — pure tick ratios, scale-safe.
func (b *Bot) ensReport(s *symRT, strategyName string, exitScaled6, entryScaled6, tpMove int64) {
	if tpMove <= 0 {
		return
	}
	norm := float64(exitScaled6-entryScaled6) / float64(tpMove)
	if norm > 2 {
		norm = 2
	}
	if norm < -2 {
		norm = -2
	}
	s.ens.Report(strategyName, norm)
}

// bookTop returns best bid/ask (true price) from the symbol's last book
// snapshot; zeros when the book is not available.
func bookTop(s *symRT) (bid, ask float64) {
	if s == nil || s.feeds == nil {
		return 0, 0 // no feeds attached (startup, tests): no book to quote
	}
	b := s.feeds.LastBook()
	if len(b.Bids) == 0 || len(b.Asks) == 0 {
		return 0, 0
	}
	return num.ScaledToFloat(b.Bids[0].Price.Ticks(), s.mi.WireScale),
		num.ScaledToFloat(b.Asks[0].Price.Ticks(), s.mi.WireScale)
}

// shadowEntry tracks a buy signal so its outcome can be measured at
// +1/+3/+10 closed candles WITHOUT taking the trade (shadow log).
type shadowEntry struct {
	symbol   string
	strategy string
	approved bool
	score    float64
	regime   string
	price    float64
	tsSec    int64   // signal candle timestamp (alignment key for analysis)
	bid      float64 // book at signal time: a long enters at the ask
	ask      float64
	age      int
	written  map[int]bool
}

// shadowSignal records a buy signal (approved or vetoed) for shadow tracking.
func (b *Bot) shadowSignal(s *symRT, tsSec int64, v strategy.Vote, score float64, status string, regime strategy.Regime) {
	bid, ask := bookTop(s)
	s.shadow = append(s.shadow, shadowEntry{
		symbol: s.symbol, strategy: v.Strategy, approved: status == "approved",
		score: score, regime: string(regime), price: s.mark, tsSec: tsSec,
		bid: bid, ask: ask, age: 0, written: map[int]bool{},
	})
}

// shadowTick advances shadow entries on each closed candle and writes
// outcomes (+1/+3/+10 candles) to signals-v2.csv.
//
// Two returns are recorded per outcome: ret_bps is mark-to-mark (comparable
// with the legacy signals.csv), ret_net_bps is the round trip a real position
// would earn — enter at the ask seen when the signal fired, exit at the bid
// seen now. The gap between them is the spread the bot actually pays.
func (b *Bot) shadowTick(s *symRT) {
	bidNow, askNow := bookTop(s)
	var keep []shadowEntry
	for _, e := range s.shadow {
		e.age++
		for _, horizon := range []int{1, 3, 10} {
			if e.age == horizon && !e.written[horizon] {
				e.written[horizon] = true
				retBps := 0.0
				if e.price > 0 && s.mark > 0 {
					retBps = (s.mark/e.price - 1) * 10000
				}
				netBps := 0.0
				if e.ask > 0 && bidNow > 0 {
					netBps = (bidNow/e.ask - 1) * 10000
				}
				b.signalCSV(map[string]string{
					"ts":           time.Now().UTC().Format(time.RFC3339),
					"signal_ts":    time.Unix(e.tsSec, 0).UTC().Format(time.RFC3339),
					"symbol":       s.symbol,
					"strategy":     e.strategy,
					"approved":     fmt.Sprintf("%t", e.approved),
					"score":        fmt.Sprintf("%.1f", e.score),
					"regime":       e.regime,
					"signal_price": fmt.Sprintf("%.8g", e.price),
					"bid_signal":   fmt.Sprintf("%.8g", e.bid),
					"ask_signal":   fmt.Sprintf("%.8g", e.ask),
					"horizon":      strconv.Itoa(horizon),
					"ret_bps":      fmt.Sprintf("%.1f", retBps),
					"ret_net_bps":  fmt.Sprintf("%.1f", netBps),
					"mark_now":     fmt.Sprintf("%.8g", s.mark),
					"bid_now":      fmt.Sprintf("%.8g", bidNow),
					"ask_now":      fmt.Sprintf("%.8g", askNow),
				})
			}
		}
		if e.age < 10 {
			keep = append(keep, e)
		}
	}
	s.shadow = keep
}

// tradeCSV appends one enriched row per closed trade.
func (b *Bot) tradeCSV(row map[string]string) {
	path := b.cfg.StateDir + "/trades.csv"
	writeHeader := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeHeader = true
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		b.log.Printf("[WARN] trade csv: %v", err)
		return
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if writeHeader {
		w.Write([]string{"ts_close", "symbol", "strategy", "regime", "signal_score", "signal_time", "hold_secs",
			"entry", "qty", "exit", "gross_usdt", "fee_entry_usdt", "fee_exit_usdt", "net_usdt",
			"bid_at_open", "ask_at_open", "bid_at_close", "ask_at_close", "exit_reason"})
	}
	w.Write([]string{
		row["ts_close"], row["symbol"], row["strategy"], row["regime"], row["signal_score"], row["signal_time"], row["hold_secs"],
		row["entry"], row["qty"], row["exit"], row["gross_usdt"], row["fee_entry_usdt"], row["fee_exit_usdt"], row["net_usdt"],
		row["bid_at_open"], row["ask_at_open"], row["bid_at_close"], row["ask_at_close"], row["exit_reason"],
	})
	w.Flush()
}

// signalCSV appends one shadow-signal outcome row to signals-v2.csv.
//
// v2 adds the signal candle timestamp and the book at signal/exit time, so an
// offline report can tell edge from spread instead of trusting a mark-to-mark
// number that a delayed mark can bias. The legacy file is left untouched.
func (b *Bot) signalCSV(row map[string]string) {
	path := b.cfg.StateDir + "/signals-v2.csv"
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
		w.Write([]string{"ts", "signal_ts", "symbol", "strategy", "approved", "score", "regime",
			"signal_price", "bid_signal", "ask_signal", "horizon", "ret_bps", "ret_net_bps",
			"mark_now", "bid_now", "ask_now"})
	}
	w.Write([]string{row["ts"], row["signal_ts"], row["symbol"], row["strategy"], row["approved"],
		row["score"], row["regime"], row["signal_price"], row["bid_signal"], row["ask_signal"],
		row["horizon"], row["ret_bps"], row["ret_net_bps"], row["mark_now"], row["bid_now"], row["ask_now"]})
	w.Flush()
}

// openPositionCount counts open positions across all symbols.
func (b *Bot) openPositionCount() int {
	n := 0
	for _, s := range b.syms {
		if s.pos != nil {
			n++
		}
	}
	return n
}

// --- small helpers -------------------------------------------------------

func isOurs(s *symRT, id string) bool {
	return strings.HasPrefix(id, s.ownPre)
}

// newClientOrderID builds an ID within the API charset (1-36 chars of
// [A-Za-z0-9._:/-]) with a time component for traceability.
func newClientOrderID(prefix string) string {
	ts := time.Now().UTC().Format("0102-150405")
	id := fmt.Sprintf("%s%s-%06x", prefix, ts, rand.Intn(0xFFFFFF))
	if len(id) > 36 {
		id = id[:36]
	}
	return id
}
