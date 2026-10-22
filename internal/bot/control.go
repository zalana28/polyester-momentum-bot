package bot

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"polyester-momentum-bot/internal/num"
)

// Control is the local command channel: a unix socket in the state directory,
// mode 0600, speaking one JSON request per connection. It exists so an operator
// (or an agent acting for them) can place a manual order without touching the
// bot's process, while every command still runs on the single event loop that
// owns position state.
//
// Nothing here is reachable over TCP: the socket is a file, owned by the bot's
// user, and the directory is 0700. That is the whole auth boundary.

// controlRequest is the wire format for one command.
type controlRequest struct {
	Cmd    string `json:"cmd"`              // buy | sell | flatten | status
	Symbol string `json:"symbol,omitempty"` // HYPE or HYPE-USDT
	Qty    string `json:"qty,omitempty"`    // base units, sell only
	Quote  string `json:"quote,omitempty"`  // quote budget, buy only
}

// controlResponse is always JSON, success or failure.
type controlResponse struct {
	OK        bool           `json:"ok"`
	Message   string         `json:"message,omitempty"`
	Error     string         `json:"error,omitempty"`
	Positions []positionView `json:"positions,omitempty"`
}

type positionView struct {
	Symbol   string  `json:"symbol"`
	Qty      string  `json:"qty"`
	Entry    string  `json:"entry"`
	Mark     string  `json:"mark"`
	PnlBps   float64 `json:"pnl_bps"`
	SL       string  `json:"sl"`
	Trail    string  `json:"trail,omitempty"`
	Strategy string  `json:"strategy"`
}

// ServeControl starts the unix-socket server and blocks until ctx-style
// shutdown, which is the process exit: the listener dies with the bot.
func (b *Bot) ServeControl(socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(socketPath) // stale socket from a crash
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("control socket perms: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/cmd", b.handleControl)
	mux.HandleFunc("/status", b.handleControl)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	b.log.Printf("[CTL] control socket at %s (0600, unix only)", socketPath)
	go func() {
		<-b.runCtx.Done()
		_ = srv.Close()
		_ = os.Remove(socketPath)
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (b *Bot) handleControl(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, controlResponse{Error: "POST only"})
		return
	}
	var req controlRequest
	if r.Method == http.MethodPost {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		if err := dec.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, controlResponse{Error: "bad json: " + err.Error()})
			return
		}
	} else {
		req = controlRequest{Cmd: "status"}
	}
	msg, view, err := b.RunControl(req)
	if err != nil {
		writeJSON(w, http.StatusOK, controlResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{OK: true, Message: msg, Positions: view})
}

func writeJSON(w http.ResponseWriter, code int, v controlResponse) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// RunControl validates a command and executes it on the event loop. Validation
// happens first so a malformed command never reaches the trading path.
func (b *Bot) RunControl(req controlRequest) (string, []positionView, error) {
	cmd := strings.ToLower(strings.TrimSpace(req.Cmd))
	switch cmd {
	case "status", "":
		return b.submitStatus()
	case "buy":
		s, err := b.resolveSymbol(req.Symbol)
		if err != nil {
			return "", nil, err
		}
		budget, err := b.manualBudget(s, req.Quote)
		if err != nil {
			return "", nil, err
		}
		msg, err := b.submit(func() (string, error) { return b.doManualBuy(s, budget) })
		return msg, nil, err
	case "sell":
		s, err := b.resolveSymbol(req.Symbol)
		if err != nil {
			return "", nil, err
		}
		qty, err := b.manualQty(s, req.Qty)
		if err != nil {
			return "", nil, err
		}
		msg, err := b.submit(func() (string, error) { return b.doManualSell(s, qty) })
		return msg, nil, err
	case "flatten":
		s, err := b.resolveSymbol(req.Symbol)
		if err != nil {
			return "", nil, err
		}
		msg, err := b.submit(func() (string, error) { return b.doFlatten(s) })
		return msg, nil, err
	default:
		return "", nil, fmt.Errorf("command tidak dikenal: %q", req.Cmd)
	}
}

// resolveSymbol accepts HYPE or HYPE-USDT and refuses pairs the bot does not
// trade, so a typo cannot be routed anywhere.
func (b *Bot) resolveSymbol(raw string) (*symRT, error) {
	name := strings.ToUpper(strings.TrimSpace(raw))
	if name == "" {
		return nil, fmt.Errorf("symbol wajib diisi")
	}
	if !strings.Contains(name, "-") {
		name += "-USDT"
	}
	for _, s := range b.syms {
		if s.symbol == name {
			return s, nil
		}
	}
	return nil, fmt.Errorf("symbol %s tidak ditradingkan bot ini", name)
}

// manualBudget parses an optional quote budget and caps it. A command may not
// size a position larger than MANUAL_MAX_NOTIONAL (default: 2x the normal
// per-trade budget).
func (b *Bot) manualBudget(s *symRT, raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = b.cfg.TradeBudgetQuote
	}
	quote, err := strconv.ParseFloat(raw, 64)
	if err != nil || quote <= 0 {
		return 0, fmt.Errorf("quote tidak valid: %q", raw)
	}
	maxQuote := b.cfg.ManualMaxNotionalQuote
	if maxQuote <= 0 {
		base, _ := strconv.ParseFloat(b.cfg.TradeBudgetQuote, 64)
		maxQuote = base * 2
	}
	if quote > maxQuote {
		return 0, fmt.Errorf("quote %.2f melebihi batas manual %.2f", quote, maxQuote)
	}
	budget := num.FloatToScaledRounded(quote, s.mi.QuoteScale)
	if budget < s.mi.MinNotionalQuoteScaled {
		return 0, fmt.Errorf("quote %.2f di bawah minimum %s", quote,
			num.FormatScaled(s.mi.MinNotionalQuoteScaled, s.mi.QuoteScale))
	}
	return budget, nil
}

// manualQty parses a base quantity, rejecting anything that is not a positive
// decimal or carries more precision than the pair allows.
func (b *Bot) manualQty(s *symRT, raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("qty wajib diisi untuk sell")
	}
	if strings.ContainsFunc(raw, func(r rune) bool {
		return !(unicode.IsDigit(r) || r == '.')
	}) {
		return 0, fmt.Errorf("qty tidak valid: %q", raw)
	}
	q, err := strconv.ParseFloat(raw, 64)
	if err != nil || q <= 0 {
		return 0, fmt.Errorf("qty tidak valid: %q", raw)
	}
	return num.FloatToScaledRounded(q, s.mi.BaseScale), nil
}

// submit runs f on the event loop and waits for its answer. The timeout keeps a
// wedged loop from hanging an operator's shell forever.
func (b *Bot) submit(f func() (string, error)) (string, error) {
	type res struct {
		msg string
		err error
	}
	ch := make(chan res, 1)
	select {
	case b.acts <- func() {
		msg, err := f()
		ch <- res{msg, err}
	}:
	case <-b.runCtx.Done():
		return "", fmt.Errorf("bot sudah berhenti")
	}
	select {
	case r := <-ch:
		return r.msg, r.err
	case <-time.After(15 * time.Second):
		return "", fmt.Errorf("command timeout menunggu event loop")
	case <-b.runCtx.Done():
		return "", fmt.Errorf("bot sudah berhenti")
	}
}

func (b *Bot) submitStatus() (string, []positionView, error) {
	ch := make(chan []positionView, 1)
	select {
	case b.acts <- func() { ch <- b.positionsView() }:
	case <-b.runCtx.Done():
		return "", nil, fmt.Errorf("bot sudah berhenti")
	}
	select {
	case v := <-ch:
		return fmt.Sprintf("%d posisi terbuka", len(v)), v, nil
	case <-time.After(15 * time.Second):
		return "", nil, fmt.Errorf("status timeout")
	}
}

// positionsView snapshots open positions; loop-owned, so no locking.
func (b *Bot) positionsView() []positionView {
	out := make([]positionView, 0, len(b.syms))
	for _, s := range b.syms {
		pos := s.pos
		if pos == nil {
			continue
		}
		pnl := bpsBetween(pos.AvgEntryScaled6, num.FloatToScaledRounded(s.mark, 6))
		out = append(out, positionView{
			Symbol:   s.symbol,
			Qty:      num.FormatScaled(pos.BaseQtyScaled, s.mi.BaseScale),
			Entry:    num.FormatScaled(pos.AvgEntryScaled6, 6),
			Mark:     fmt.Sprintf("%.8g", s.mark),
			PnlBps:   pnl,
			SL:       num.FormatScaled(pos.SLPriceScaled6, 6),
			Trail:    num.FormatScaled(pos.TrailStopScaled6, 6),
			Strategy: pos.Strategy,
		})
	}
	return out
}

// doManualBuy runs on the loop: same gates as a signal entry, then the shared
// execution path so the stop-loss and exit plan are always attached.
func (b *Bot) doManualBuy(s *symRT, budget int64) (string, error) {
	if s.pos != nil {
		return "", fmt.Errorf("%s sudah punya posisi terbuka", s.symbol)
	}
	if b.cfg.ShadowOnly {
		return "", fmt.Errorf("SHADOW_ONLY aktif: order tidak dikirim")
	}
	if b.st.Halted() {
		return "", fmt.Errorf("bot halted: periksa penyebab sebelum order manual")
	}
	if n := b.openPositionCount(); n >= b.cfg.MaxOpenPositions {
		return "", fmt.Errorf("max posisi terbuka tercapai (%d)", n)
	}
	if b.cfg.Mode == configModeLive && !b.equityFresh(time.Now()) {
		return "", fmt.Errorf("equity tidak terbaca: daily-loss guard tidak bisa dievaluasi")
	}
	if err := b.executeEntry(s, budget, "manual", -1, "manual"); err != nil {
		return "", err
	}
	return fmt.Sprintf("BUY %s budget %s (SL terpasang)", s.symbol, num.FormatScaled(budget, s.mi.QuoteScale)), nil
}

// doManualSell sells at market. A qty at or above the position closes it; a
// smaller qty reduces the position and re-arms the stop for the remainder.
// With no tracked position, it sells the base balance the account actually
// holds (a manual/spot holding the bot does not manage).
func (b *Bot) doManualSell(s *symRT, qty int64) (string, error) {
	pos := s.pos
	if pos == nil {
		return b.doSpotSell(s, qty)
	}
	if qty <= 0 {
		return "", fmt.Errorf("qty harus positif")
	}
	if qty >= pos.BaseQtyScaled {
		return b.doFlatten(s)
	}
	b.cancelExits(s, pos)
	sold, _, exitScaled6, err := b.sellWithOneRetry(s, qty)
	if err != nil {
		b.log.Printf("[CTL] CRITICAL: %s manual sell failed (%v); re-arming", s.symbol, err)
		if _, rerr := b.rearmSL(s, pos.BaseQtyScaled, pos.AvgEntryScaled6); rerr != nil {
			b.log.Printf("[CTL] CRITICAL: %s re-arm after failed manual sell (%v)", s.symbol, rerr)
		}
		return "", fmt.Errorf("sell gagal: %w", err)
	}
	b.bookPartialPnL(pos, sold, exitScaled6)
	remaining := pos.BaseQtyScaled - sold
	if remaining < s.mi.MinQtyScaled {
		s.pos = nil
		return fmt.Sprintf("SELL %s %s (posisi lunas)", s.symbol, num.FormatScaled(sold, s.mi.BaseScale)), nil
	}
	pos.BaseQtyScaled = remaining
	if sl, rerr := b.rearmSL(s, remaining, pos.AvgEntryScaled6); rerr != nil {
		b.log.Printf("[CTL] CRITICAL: %s re-arm on remainder failed (%v)", s.symbol, rerr)
	} else {
		pos.SLTriggerID, pos.SLClientTriggerID = sl.TriggerID, sl.ClientTriggerID
		pos.SLPriceScaled6 = sl.PriceScaled6
	}
	return fmt.Sprintf("SELL %s %s (sisa %s, SL di-arm ulang)", s.symbol,
		num.FormatScaled(sold, s.mi.BaseScale), num.FormatScaled(remaining, s.mi.BaseScale)), nil
}

func (b *Bot) doFlatten(s *symRT) (string, error) {
	if s.pos == nil {
		return "", fmt.Errorf("%s tidak punya posisi terbuka", s.symbol)
	}
	qty := s.pos.BaseQtyScaled
	b.flatten(s, "manual")
	if s.pos != nil {
		return "", fmt.Errorf("%s gagal close penuh; cek log", s.symbol)
	}
	return fmt.Sprintf("FLATTEN %s %s", s.symbol, num.FormatScaled(qty, s.mi.BaseScale)), nil
}

// doSpotSell sells base the bot holds but does not manage as a position (a
// manual holding). It deliberately touches no position state: there is no stop
// to cancel and no strategy PnL to book. The venue still enforces reservation,
// so an amount locked by an order or withdrawal fails here rather than
// silently selling something else.
func (b *Bot) doSpotSell(s *symRT, qty int64) (string, error) {
	if qty <= 0 {
		return "", fmt.Errorf("qty harus positif")
	}
	bal, err := s.exec.BaseBalance(b.opCtx)
	if err != nil {
		return "", fmt.Errorf("saldo %s tidak terbaca: %w", s.symbol, err)
	}
	have := num.FloatToScaledRounded(bal, s.mi.BaseScale)
	if qty > have {
		return "", fmt.Errorf("qty %s melebihi saldo %s", num.FormatScaled(qty, s.mi.BaseScale),
			num.FormatScaled(have, s.mi.BaseScale))
	}
	fill, err := s.exec.Sell(b.opCtx, qty, b.newID(s, "spot"))
	if err != nil {
		return "", fmt.Errorf("sell gagal: %w", err)
	}
	return fmt.Sprintf("SELL %s %s @ %s (dari saldo, bukan posisi bot)", s.symbol,
		num.FormatScaled(fill.BaseQtyScaled, s.mi.BaseScale),
		num.FormatScaled(fill.AvgPriceScaled6, 6)), nil
}
