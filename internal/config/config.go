// Package config loads bot configuration from the process environment with an
// optional .env fallback. Process env always wins over .env.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Mode selects paper (simulation) vs live (testnet orders) operation.
type Mode string

// Default API endpoints. The SDK's TestnetEnvironment preset still points at
// the retired api-testnet.polyester.com (NXDOMAIN); the live testnet host is
// the dotted api.testnet.polyester.com. Override with POLYESTER_API_URL /
// POLYESTER_WS_URL.
const (
	DefaultAPIURL = "https://api.testnet.polyester.com"
	DefaultWSURL  = "wss://api.testnet.polyester.com"
)

const (
	ModePaper Mode = "paper"
	ModeLive  Mode = "live"
)

type Config struct {
	Mode Mode

	// Live credentials. Required only for MODE=live.
	APIKeyID      string
	APIPrivateKey string
	AccountID     string

	Symbol    string // display symbol, e.g. BTC-USDT (single-symbol mode)
	Timeframe string // candle timeframe, e.g. 1m
	// Symbols enables multi-symbol mode: comma-separated list, or "all" for
	// every enabled *-USDT pair (USDC-USDT excluded as a stable pair).
	// Empty -> single-symbol mode using Symbol.
	Symbols []string
	// MaxOpenPositions caps concurrent open positions across all symbols.
	MaxOpenPositions int

	// Exit plan. The defaults reproduce the protection the live binary ran
	// with: arm the trail once a position is +150 bps, trail 100 bps behind
	// the peak in 10 bps steps, take half off at +100 bps, and close anything
	// still under +50 bps after 20 minutes.
	TrailActivateBps int
	TrailDistanceBps int
	TrailStepBps     int
	TrailPartialBps  int
	TrailPartialFrac float64
	TimeStopMinutes  int
	TimeStopMinBps   int
	// Brain gate: BRAIN_MIN_SCORE (0 = off) is the multi-factor score bar a
	// buy signal must clear; BRAIN_COOLDOWN_MIN pauses a symbol after a
	// losing cycle.
	BrainMinScore     float64
	BrainCooldownMin  float64
	EnsembleThreshold float64

	// Order sizing / protection.
	TradeBudgetQuote string // decimal quote budget per trade, e.g. "25.00"
	MaxSlippageBps   int32  // market IOC execution protection, 1..10000

	// Strategy params.
	StopLossBps     int     // SL distance below avg entry, bps
	TakeProfitBps   int     // TP distance above avg entry, bps
	ROCTimeframes   int     // ROC lookback in candles
	ROCThresholdPct float64 // minimum ROC to confirm momentum, in percent
	VolumeSurgeMult float64 // candle volume vs mean of previous N candles
	EMAFast         int
	EMASlow         int
	VolMAPeriod     int
	WarmupCandles   int // REST candles fetched at boot to seed the engine

	// Risk.
	DailyLossLimitPct float64 // % of day-start equity; trip => kill switch

	// Live-only protection.
	DeadManTimeoutSec int // CancelAllAfter window
	DeadManRefreshSec int // refresh interval
	ReconcilePollSec  int

	// Housekeeping.
	StateDir  string // journal directory
	FlattenOn bool   // FLATTEN_ON_SHUTDOWN: flatten open position on SIGINT/SIGTERM

	// PriceScale is the wire price exponent W (wireTicks = truePrice×10^W).
	// 0 = auto-detect from candle quote_volume/volume at boot.
	PriceScale int

	// Paper mode.
	PaperStartQuote string // virtual starting quote balance

	// API endpoints. Defaults point at the live testnet host; override with
	// POLYESTER_API_URL / POLYESTER_WS_URL (for example to run on devnet).
	APIURL string
	WSURL  string

	// ShadowOnly records approved signals and their outcomes but never sends
	// an order. It is how the bot earns the right to trade: run with it until
	// the edge report shows positive net expectancy, then turn it off.
	ShadowOnly bool

	// Control socket: a local unix socket for manual orders (chat-driven).
	// Off unless CONTROL_ENABLED=true; the socket is 0600 inside StateDir.
	ControlEnabled         bool
	ControlSocket          string
	ManualMaxNotionalQuote float64
}

// FromEnv builds configuration. dotenvPaths are consulted (first match wins)
// for keys missing from the process environment.
func FromEnv(dotenvPaths ...string) (*Config, error) {
	vals := map[string]string{}
	if path := firstExisting(dotenvPaths); path != "" {
		kv, err := parseDotenv(path)
		if err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", path, err)
		}
		for k, v := range kv {
			vals[k] = v
		}
	}
	get := func(key string) string {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
		return vals[key]
	}

	c := &Config{
		Mode:              Mode(strings.ToLower(get("MODE"))),
		APIKeyID:          get("API_KEY_ID"),
		APIPrivateKey:     get("API_PRIVATE_KEY"),
		AccountID:         get("ACCOUNT_ID"),
		Symbol:            get("SYMBOL"),
		Timeframe:         get("TIMEFRAME"),
		TradeBudgetQuote:  get("TRADE_BUDGET_QUOTE"),
		StopLossBps:       intFrom(get, "STOP_LOSS_BPS", 100),
		TakeProfitBps:     intFrom(get, "TAKE_PROFIT_BPS", 200),
		ROCTimeframes:     intFrom(get, "ROC_PERIOD", 5),
		ROCThresholdPct:   floatFrom(get, "ROC_THRESHOLD_PCT", 0.3),
		VolumeSurgeMult:   floatFrom(get, "VOLUME_SURGE_MULT", 1.5),
		EMAFast:           intFrom(get, "EMA_FAST", 9),
		EMASlow:           intFrom(get, "EMA_SLOW", 21),
		VolMAPeriod:       intFrom(get, "VOL_MA_PERIOD", 20),
		WarmupCandles:     intFrom(get, "WARMUP_CANDLES", 150),
		DailyLossLimitPct: floatFrom(get, "DAILY_LOSS_LIMIT_PCT", 5.0),
		DeadManTimeoutSec: intFrom(get, "DEAD_MAN_TIMEOUT_SEC", 15),
		DeadManRefreshSec: intFrom(get, "DEAD_MAN_REFRESH_SEC", 5),
		ReconcilePollSec:  intFrom(get, "RECONCILE_POLL_SEC", 30),
		StateDir:          get("STATE_DIR"),
		PaperStartQuote:   get("PAPER_START_QUOTE"),
		PriceScale:        intFrom(get, "POLYESTER_PRICE_SCALE", 0),
		MaxOpenPositions:  intFrom(get, "MAX_OPEN_POSITIONS", 4),
		TrailActivateBps:  intFrom(get, "TRAIL_ACTIVATE_BPS", 150),
		TrailDistanceBps:  intFrom(get, "TRAIL_DISTANCE_BPS", 100),
		TrailStepBps:      intFrom(get, "TRAIL_STEP_BPS", 10),
		TrailPartialBps:   intFrom(get, "TRAIL_PARTIAL_BPS", 100),
		TrailPartialFrac:  floatFrom(get, "TRAIL_PARTIAL_FRAC", 0.50),
		TimeStopMinutes:   intFrom(get, "TIME_STOP_MINUTES", 20),
		TimeStopMinBps:    intFrom(get, "TIME_STOP_MIN_BPS", 50),
		BrainMinScore:     floatFrom(get, "BRAIN_MIN_SCORE", 60),
		BrainCooldownMin:  floatFrom(get, "BRAIN_COOLDOWN_MIN", 10),
		EnsembleThreshold: floatFrom(get, "ENSEMBLE_THRESHOLD", 0.5),
		APIURL:            get("POLYESTER_API_URL"),
		WSURL:             get("POLYESTER_WS_URL"),
	}
	if c.APIURL == "" {
		c.APIURL = DefaultAPIURL
	}
	if c.WSURL == "" {
		c.WSURL = DefaultWSURL
	}
	c.ShadowOnly = boolFrom(get, "SHADOW_ONLY", false)
	c.ControlEnabled = boolFrom(get, "CONTROL_ENABLED", false)
	c.ControlSocket = get("CONTROL_SOCKET")
	c.ManualMaxNotionalQuote = floatFrom(get, "MANUAL_MAX_NOTIONAL", 0)
	c.MaxSlippageBps = int32(intFrom(get, "MAX_SLIPPAGE_BPS", 50))
	c.FlattenOn = true
	if v := get("FLATTEN_ON_SHUTDOWN"); v != "" {
		c.FlattenOn = v != "false" && v != "0" && v != "no"
	}

	if c.Mode == "" {
		c.Mode = ModePaper
	}
	if c.Symbol == "" {
		c.Symbol = "BTC-USDT"
	}
	if c.Timeframe == "" {
		c.Timeframe = "1m"
	}
	if c.TradeBudgetQuote == "" {
		c.TradeBudgetQuote = "25.00"
	}
	if c.StateDir == "" {
		c.StateDir = "state"
	}
	if c.PaperStartQuote == "" {
		c.PaperStartQuote = "1000.00"
	}

	if raw := get("SYMBOLS"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.ToUpper(strings.TrimSpace(part))
			if part != "" {
				c.Symbols = append(c.Symbols, part)
			}
		}
	}
	if c.MaxOpenPositions <= 0 {
		c.MaxOpenPositions = 4
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// validateEndpoint rejects malformed or scheme-mismatched endpoint overrides.
func validateEndpoint(raw, key string, schemes ...string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("config: %s must be an absolute URL (got %q)", key, raw)
	}
	for _, scheme := range schemes {
		if parsed.Scheme == scheme {
			return nil
		}
	}
	return fmt.Errorf("config: %s scheme must be one of %s (got %q)", key, strings.Join(schemes, "/"), parsed.Scheme)
}

func (c *Config) validate() error {
	switch c.Mode {
	case ModePaper, ModeLive:
	default:
		return fmt.Errorf("config: MODE must be paper or live (got %q)", c.Mode)
	}
	if err := validateEndpoint(c.APIURL, "POLYESTER_API_URL", "http", "https"); err != nil {
		return err
	}
	if err := validateEndpoint(c.WSURL, "POLYESTER_WS_URL", "ws", "wss"); err != nil {
		return err
	}
	if c.Mode == ModeLive {
		if c.APIKeyID == "" || c.APIPrivateKey == "" {
			return fmt.Errorf("config: MODE=live requires API_KEY_ID and API_PRIVATE_KEY")
		}
		if c.AccountID == "" {
			return fmt.Errorf("config: MODE=live requires ACCOUNT_ID (Profile -> Account ID)")
		}
		if strings.TrimSpace(c.APIPrivateKey) != c.APIPrivateKey {
			return fmt.Errorf("config: API_PRIVATE_KEY has surrounding whitespace")
		}
	}
	if _, err := strconv.ParseFloat(c.TradeBudgetQuote, 64); err != nil {
		return fmt.Errorf("config: TRADE_BUDGET_QUOTE must be a decimal number (got %q)", c.TradeBudgetQuote)
	}
	if c.MaxSlippageBps < 1 || c.MaxSlippageBps > 10000 {
		return fmt.Errorf("config: MAX_SLIPPAGE_BPS must be 1..10000 (got %d)", c.MaxSlippageBps)
	}
	if c.StopLossBps <= 0 || c.TakeProfitBps <= 0 {
		return fmt.Errorf("config: STOP_LOSS_BPS and TAKE_PROFIT_BPS must be positive")
	}
	if c.EMAFast <= 0 || c.EMASlow <= c.EMAFast {
		return fmt.Errorf("config: EMA_FAST must be > 0 and < EMA_SLOW")
	}
	if c.ROCTimeframes <= 0 || c.VolMAPeriod <= 0 {
		return fmt.Errorf("config: ROC_PERIOD and VOL_MA_PERIOD must be positive")
	}
	if c.ROCThresholdPct < 0 || c.VolumeSurgeMult <= 0 {
		return fmt.Errorf("config: ROC_THRESHOLD_PCT must be >= 0 and VOLUME_SURGE_MULT > 0")
	}
	if c.DailyLossLimitPct <= 0 || c.DailyLossLimitPct > 100 {
		return fmt.Errorf("config: DAILY_LOSS_LIMIT_PCT must be in (0,100]")
	}
	if c.WarmupCandles < c.EMASlow+2 {
		return fmt.Errorf("config: WARMUP_CANDLES must be >= EMA_SLOW+2 (%d)", c.EMASlow+2)
	}
	if c.PriceScale != 0 && (c.PriceScale < 6 || c.PriceScale > 12) {
		return fmt.Errorf("config: POLYESTER_PRICE_SCALE must be 0 (auto) or 6..12 (got %d)", c.PriceScale)
	}
	if c.Mode == ModeLive {
		if c.DeadManTimeoutSec <= 0 || c.DeadManRefreshSec <= 0 || c.DeadManRefreshSec >= c.DeadManTimeoutSec {
			return fmt.Errorf("config: DEAD_MAN_REFRESH_SEC must be < DEAD_MAN_TIMEOUT_SEC and both positive")
		}
		if c.ReconcilePollSec <= 0 {
			return fmt.Errorf("config: RECONCILE_POLL_SEC must be positive")
		}
	}
	return nil
}

func firstExisting(paths []string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// parseDotenv reads KEY=VALUE lines, ignoring blank lines and # comments.
// Inline "export " prefixes are tolerated; values may be single/double quoted.
func parseDotenv(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	return out, nil
}

func intFrom(get func(string) string, key string, def int) int {
	v := get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func floatFrom(get func(string) string, key string, def float64) float64 {
	v := get(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

// boolFrom parses the usual truthy spellings. Anything unrecognised falls back
// to def so a typo in .env cannot silently arm a mode the user did not mean.
func boolFrom(get func(string) string, key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(get(key))) {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
