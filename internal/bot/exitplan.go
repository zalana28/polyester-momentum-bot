package bot

import (
	"time"

	"polyester-momentum-bot/internal/num"
	"polyester-momentum-bot/internal/state"
)

// exitPlanCfg is the pure, testable half of the exit plan: no bot, no orders.
type exitPlanCfg struct {
	ActivateBps    int
	DistanceBps    int
	StepBps        int
	PartialBps     int
	PartialFrac    float64
	TimeStopMin    int
	TimeStopMinBps int
}

// bpsBetween expresses price relative to entry in basis points.
func bpsBetween(entry, price int64) float64 {
	if entry <= 0 {
		return 0
	}
	return (float64(price) - float64(entry)) / float64(entry) * 10000
}

// scaleBps returns price adjusted by bps/10000, keeping integer ticks.
func scaleBps(price int64, bps int) int64 {
	return price * int64(10000+bps) / 10000
}

// trailLevel is the stop a trailing exit should sit at for a given peak.
//
// The ratchet only moves up, and only when the improvement clears StepBps of
// the entry price: without the step a 1-tick wiggle would rewrite the level on
// every trade and the log would drown. Returns current when nothing should move.
func trailLevel(entry, peak, current int64, cfg exitPlanCfg) int64 {
	if entry <= 0 || peak <= 0 || cfg.DistanceBps <= 0 {
		return current
	}
	desired := scaleBps(peak, -cfg.DistanceBps)
	if desired <= current {
		return current
	}
	if current > 0 && cfg.StepBps > 0 {
		minMove := entry * int64(cfg.StepBps) / 10000
		if desired-current < minMove {
			return current
		}
	}
	return desired
}

// trailArms reports whether profit has reached the activation threshold.
func trailArms(pnlBps float64, cfg exitPlanCfg) bool {
	return cfg.ActivateBps > 0 && pnlBps >= float64(cfg.ActivateBps)
}

// partialDue reports whether the partial take-profit should fire now.
func partialDue(pnlBps float64, taken bool, cfg exitPlanCfg) bool {
	if taken || cfg.PartialBps <= 0 || cfg.PartialFrac <= 0 || cfg.PartialFrac >= 1 {
		return false
	}
	return pnlBps >= float64(cfg.PartialBps)
}

// timeStopDue reports whether a position has sat too long without progress.
// A position that is doing better than TimeStopMinBps keeps running: the clock
// is there to recycle dead capital, not to cut winners.
func timeStopDue(openedAt, now time.Time, pnlBps float64, cfg exitPlanCfg) bool {
	if cfg.TimeStopMin <= 0 || openedAt.IsZero() {
		return false
	}
	if now.Sub(openedAt) < time.Duration(cfg.TimeStopMin)*time.Minute {
		return false
	}
	return pnlBps < float64(cfg.TimeStopMinBps)
}

func (b *Bot) exitCfg() exitPlanCfg {
	return exitPlanCfg{
		ActivateBps:    b.cfg.TrailActivateBps,
		DistanceBps:    b.cfg.TrailDistanceBps,
		StepBps:        b.cfg.TrailStepBps,
		PartialBps:     b.cfg.TrailPartialBps,
		PartialFrac:    b.cfg.TrailPartialFrac,
		TimeStopMin:    b.cfg.TimeStopMinutes,
		TimeStopMinBps: b.cfg.TimeStopMinBps,
	}
}

// managePosition runs the bot-side exit plan on every trade tick.
//
// Order matters. The partial take-profit banks half before the trail can give
// the gain back; the trail then ratchets behind the peak; the time stop only
// applies to positions that are not working. Every exit goes through flatten so
// the protective trigger is cancelled and a failed sell re-arms it.
func (b *Bot) managePosition(s *symRT, priceScaled6 int64) {
	pos := s.pos
	if pos == nil || pos.AvgEntryScaled6 <= 0 || priceScaled6 <= 0 {
		return
	}
	cfg := b.exitCfg()
	if priceScaled6 > pos.PeakScaled6 {
		pos.PeakScaled6 = priceScaled6
	}
	pnl := bpsBetween(pos.AvgEntryScaled6, priceScaled6)

	if partialDue(pnl, pos.PartialTaken, cfg) {
		b.takePartial(s, pos)
		if s.pos == nil {
			return
		}
		pos = s.pos
	}

	if !pos.TrailArmed && trailArms(pnl, cfg) {
		pos.TrailArmed = true
		b.log.Printf("[EXIT] %s trailing armed at %+.0fbps (peak %s, trail %dbps)",
			s.symbol, pnl, num.FormatScaled(pos.PeakScaled6, 6), cfg.DistanceBps)
	}
	if pos.TrailArmed {
		if lvl := trailLevel(pos.AvgEntryScaled6, pos.PeakScaled6, pos.TrailStopScaled6, cfg); lvl > pos.TrailStopScaled6 {
			pos.TrailStopScaled6 = lvl
			b.log.Printf("[EXIT] %s trail stop raised to %s", s.symbol, num.FormatScaled(lvl, 6))
		}
		if pos.TrailStopScaled6 > 0 && priceScaled6 <= pos.TrailStopScaled6 {
			b.log.Printf("[EXIT] %s trail hit at %s (stop %s)",
				s.symbol, num.FormatScaled(priceScaled6, 6), num.FormatScaled(pos.TrailStopScaled6, 6))
			b.flatten(s, "trail")
			return
		}
	}

	if timeStopDue(pos.OpenedAt, time.Now(), pnl, cfg) {
		b.log.Printf("[EXIT] %s time stop: %.0f min, %+.0fbps", s.symbol, time.Since(pos.OpenedAt).Minutes(), pnl)
		b.flatten(s, "time stop")
	}
}

// takePartial sells PartialFrac of the position once, books the realized PnL,
// and re-arms the stop-loss for the remainder so it is never unprotected.
func (b *Bot) takePartial(s *symRT, pos *state.Position) {
	qty := int64(float64(pos.BaseQtyScaled) * b.cfg.TrailPartialFrac)
	if qty < s.mi.MinQtyScaled {
		pos.PartialTaken = true // too small to split; do not retry forever
		return
	}
	pos.PartialTaken = true
	sold, _, exitScaled6, err := b.sellWithOneRetry(s, qty)
	if err != nil {
		b.log.Printf("[EXIT] %s partial sell failed (%v); re-arming full size", s.symbol, err)
		if _, rerr := b.rearmSL(s, pos.BaseQtyScaled, pos.AvgEntryScaled6); rerr != nil {
			b.log.Printf("[EXIT] CRITICAL: %s re-arm after partial failure failed (%v)", s.symbol, rerr)
		}
		return
	}
	if sold <= 0 {
		return
	}
	b.bookPartialPnL(pos, sold, exitScaled6)
	remaining := pos.BaseQtyScaled - sold
	b.log.Printf("[EXIT] %s partial take-profit: sold=%s remaining=%s at %s",
		s.symbol, num.FormatScaled(sold, s.mi.BaseScale), num.FormatScaled(remaining, s.mi.BaseScale),
		num.FormatScaled(exitScaled6, 6))
	if remaining < s.mi.MinQtyScaled {
		s.pos = nil
		return
	}
	pos.BaseQtyScaled = remaining
	b.cancelExits(s, pos)
	if sl, rerr := b.rearmSL(s, remaining, pos.AvgEntryScaled6); rerr != nil {
		b.log.Printf("[EXIT] CRITICAL: %s re-arm on remainder failed (%v)", s.symbol, rerr)
	} else if sl.TriggerID != pos.SLTriggerID {
		pos.SLTriggerID, pos.SLClientTriggerID = sl.TriggerID, sl.ClientTriggerID
		pos.SLPriceScaled6 = sl.PriceScaled6
	}
	s.pos = pos
}
