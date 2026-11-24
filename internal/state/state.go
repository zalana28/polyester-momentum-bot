// Package state holds the bot's runtime view of the world: the open position,
// its protective triggers, halts, and the daily equity baseline. All access is
// mutex-guarded; the bot loop is single-threaded but stream pumps touch mark
// price concurrently.
package state

import (
	"sync"
	"time"
)

// Position is one open spot position with its protective exits. All prices
// are TRUE price ticks (1e6 scale) regardless of backend wire units.
type Position struct {
	BaseQtyScaled   int64
	BaseScale       int
	AvgEntryScaled6 int64

	EntryClientOrderID string
	EntryOrderID       string
	Strategy           string  // ensemble member that opened this position
	SignalScore        float64 // brain score at entry (negative = brain disabled)
	SignalRegime       string  // regime classification at entry
	SignalTs           time.Time
	BidOpen            float64 // best bid at entry (true price)
	AskOpen            float64 // best ask at entry

	SLTriggerID       string
	SLClientTriggerID string
	TPTriggerID       string
	TPClientTriggerID string
	SLPriceScaled6    int64
	TPPriceScaled6    int64

	// Exit plan state (bot-side): highest price seen, trail ratchet level,
	// and whether the partial take-profit already fired.
	PeakScaled6      int64
	TrailArmed       bool
	TrailStopScaled6 int64
	PartialTaken     bool

	OpenedAt time.Time
}

// Clone returns a deep copy.
func (p *Position) Clone() *Position {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// State is the shared runtime state.
type State struct {
	mu sync.Mutex

	pos *Position

	halted     bool
	haltReason string

	dayKey         string  // UTC yyyy-mm-dd of the current risk day
	dayStartEquity float64 // equity snapshot at day start (quote units)
	realizedPnL    float64 // cumulative realized PnL in quote units
	markPrice      float64 // last known mark (quote units per base)
	lastClosedTs   int64   // last closed candle ts seen
	paper          bool
}

// New builds state. paper only affects log strings.
func New(paper bool) *State {
	return &State{paper: paper}
}

// Position returns a copy of the open position or nil.
func (s *State) Position() *Position {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pos.Clone()
}

// SetPosition stores the open position.
func (s *State) SetPosition(p *Position) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pos = p.Clone()
}

// ClearPosition drops the position and returns the previous one.
func (s *State) ClearPosition() *Position {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pos
	s.pos = nil
	return p
}

// Halted reports whether trading is halted.
func (s *State) Halted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.halted
}

// Halt stops all trading with a reason. Idempotent.
func (s *State) Halt(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.halted {
		s.halted = true
		s.haltReason = reason
	}
}

// HaltReason returns the active halt reason, if any.
func (s *State) HaltReason() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.haltReason, s.halted
}

// DayKey returns the current risk day key (UTC date) and the day-start equity.
func (s *State) DayKey() (string, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dayKey, s.dayStartEquity
}

// DayKeyOrEmpty returns just the current risk day key.
func (s *State) DayKeyOrEmpty() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dayKey
}

// RollDay starts a new risk day if dayKey differs, resetting the baseline to
// the given equity. Returns true when a new day started.
func (s *State) RollDay(dayKey string, equity float64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dayKey == dayKey {
		return false
	}
	s.dayKey = dayKey
	s.dayStartEquity = equity
	return true
}

// DayStartEquity returns the baseline for the current day.
func (s *State) DayStartEquity() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dayStartEquity
}

// SetMark records the latest mark price (quote units per base).
func (s *State) SetMark(price float64) {
	if price <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markPrice = price
}

// Mark returns the latest mark price.
func (s *State) Mark() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markPrice
}

// AddRealizedPnL accumulates realized PnL (quote units).
func (s *State) AddRealizedPnL(v float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.realizedPnL += v
}

// RealizedPnL returns accumulated realized PnL.
func (s *State) RealizedPnL() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.realizedPnL
}

// SetLastClosedTs records the ts of the last closed candle.
func (s *State) SetLastClosedTs(ts int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ts > s.lastClosedTs {
		s.lastClosedTs = ts
	}
}

// LastClosedTs returns the last closed candle timestamp.
func (s *State) LastClosedTs() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastClosedTs
}
