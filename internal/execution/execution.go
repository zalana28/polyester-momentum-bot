// Package execution turns decisions into orders. Two implementations share one
// interface: LiveExecutor (real testnet orders via the Polyester SDK) and
// PaperExecutor (simulated fills from market data, no API key).
//
// Invariants enforced here:
//   - every create attempt is journaled with its ClientOrderID/ClientTriggerID
//     BEFORE the first attempt (idempotency for ambiguous retries);
//   - retries never change a client order ID;
//   - rate-limit errors retry with backoff, validation/auth errors never do;
//   - ambiguous transport failures reconcile by looking the ID up first.
package execution

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"connectrpc.com/connect"
	sdkerrors "github.com/Fabric-Labs/polyester-sdk-go/errors"
)

// ErrDust means the remaining quantity is below the pair's minimum and cannot
// be sold as its own order.
var ErrDust = errors.New("execution: quantity below pair minimum")

// ErrAmbiguous means an operation may or may not have been admitted and the
// caller must reconcile before retrying.
var ErrAmbiguous = errors.New("execution: ambiguous outcome, reconcile required")

// MarketInfo carries pair constants resolved from hydrated catalogs plus the
// empirically detected wire price scale.
type MarketInfo struct {
	Symbol                 string
	SymbolID               uint32 // pair id from the catalog (for stream routing)
	BaseScale              int    // base quantity scale (catalog)
	QuoteScale             int    // quote quantity scale (catalog)
	TickTicks              int64  // tick size in TRUE price ticks (1e6 scale)
	MinQtyScaled           int64  // minimum base quantity (base scale)
	MinNotionalQuoteScaled int64  // minimum notional (quote scale)
	BaseAssetID            uint32
	QuoteAssetID           uint32
	// WireScale is the exponent W with wirePriceTicks = truePrice × 10^W on
	// server responses (detected at boot; observed 9 on testnet). All
	// executor boundary prices are TRUE price ticks (1e6); wire conversion
	// happens inside LiveExecutor.
	WireScale int
}

// Fill is the outcome of an executed order. AvgPriceScaled6 is the average
// execution price in TRUE price ticks (1e6 scale) no matter which wire unit
// the backend used.
type Fill struct {
	ClientOrderID   string
	OrderID         string
	BaseQtyScaled   int64
	AvgPriceScaled6 int64
	Status          string
	Partial         bool
}

// TriggerRef identifies a trigger owned by this bot.
type TriggerRef struct {
	TriggerID       string
	ClientTriggerID string
	TriggerType     string
	Status          string
}

// OrderRef identifies an order owned by this bot.
type OrderRef struct {
	OrderID       string
	ClientOrderID string
	Status        string
}

// Trigger terminal-ish statuses.
const (
	StatusCreated   = "created"
	StatusArmed     = "armed"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
	StatusFailed    = "failed"
	StatusPaused    = "paused"
)

// ActiveTriggerStatuses are statuses in which a trigger may still fire.
func ActiveTriggerStatuses() []string {
	return []string{StatusCreated, StatusArmed, StatusRunning}
}

// IsTerminalTriggerStatus reports whether the trigger will never fire again.
func IsTerminalTriggerStatus(s string) bool {
	switch s {
	case StatusCompleted, StatusCancelled, StatusFailed:
		return true
	default:
		return false
	}
}

// Executor is the trading surface consumed by the bot loop.
type Executor interface {
	Kind() string

	// Buy spends up to budgetQuoteScaled quote (at quote scale) with a market
	// IOC buy. Returns the observed fill.
	Buy(ctx context.Context, budgetQuoteScaled int64, clientOrderID string) (*Fill, error)

	// Sell disposes baseQtyScaled base (at base scale) with a market IOC sell.
	// Returns ErrDust when the quantity is below the pair minimum.
	Sell(ctx context.Context, baseQtyScaled int64, clientOrderID string) (*Fill, error)

	// PlaceStopLoss arms ONE standalone sell stop_loss trigger for
	// baseQtyScaled at an absolute trigger price (TRUE price ticks, 1e6).
	// NOTE: an armed trigger RESERVES the base balance on the server, so two
	// full-size triggers (SL+TP) cannot coexist — take profit is enforced
	// bot-side instead.
	PlaceStopLoss(ctx context.Context, baseQtyScaled, slScaled6 int64, slClientID string) (*StopLoss, error)

	// CancelTrigger cancels one trigger. Cancelling an already-terminal
	// trigger succeeds (idempotent).
	CancelTrigger(ctx context.Context, triggerID string) error

	// TriggerStatus returns the current lifecycle status of a trigger.
	TriggerStatus(ctx context.Context, triggerID string) (string, error)

	// ActiveTriggers lists triggers owned by this bot (client ID prefix match)
	// that are in an active status.
	ActiveTriggers(ctx context.Context) ([]TriggerRef, error)

	// OpenOrders lists working orders owned by this bot.
	OpenOrders(ctx context.Context) ([]OrderRef, error)

	// CancelAllOrders cancels every open order for the account.
	CancelAllOrders(ctx context.Context) error

	// ArmDeadMan arms the server-side cancel-all-after window (live); paper
	// returns nil.
	ArmDeadMan(ctx context.Context) error

	// RefreshDeadMan re-arms the cancel-all-after window (live).
	RefreshDeadMan(ctx context.Context) error

	// QuoteAvailable returns available quote balance in quote units (float,
	// for risk math only). Paper returns the virtual balance.
	QuoteAvailable(ctx context.Context) (float64, error)

	// BaseBalance returns total base balance in base units (float, risk math).
	BaseBalance(ctx context.Context) (float64, error)

	// Mark returns the last known price in quote units per base.
	Mark() float64

	// SetMark updates the last known price (quote units per base).
	SetMark(price float64)
}

// StopLoss describes the single armed protective trigger for a position.
// Prices are TRUE price ticks (1e6 scale).
type StopLoss struct {
	TriggerID       string
	ClientTriggerID string
	PriceScaled6    int64
	BaseQtyScaled   int64
}

// retryOnRateLimit runs fn, retrying only sdkerrors.RateLimitError with the
// server-provided delay plus jitter. Everything else returns immediately.
func retryOnRateLimit(ctx context.Context, attempts int, fn func(attempt int) error) error {
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn(attempt)
		if err == nil {
			return nil
		}
		lastErr = err
		var rl *sdkerrors.RateLimitError
		if !errors.As(err, &rl) {
			return err
		}
		delay := rateLimitDelay(rl)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return lastErr
}

// rateLimitDelay prefers the server hint, then 1s, plus up to 250ms jitter.
func rateLimitDelay(rl *sdkerrors.RateLimitError) time.Duration {
	base := 1.0
	if rl.RetryAfter != nil && *rl.RetryAfter > 0 {
		base = *rl.RetryAfter
	} else if rl.Detail != nil {
		if s := rl.Detail.RetryAfterSeconds(); s != nil && *s > 0 {
			base = *s
		}
	}
	jitter := rand.Float64() * 0.25
	return time.Duration((base + jitter) * float64(time.Second))
}

// isConnectCode reports whether err carries one of the given Connect codes.
func isConnectCode(err error, codes ...connect.Code) bool {
	if err == nil {
		return false
	}
	c := connect.CodeOf(err)
	for _, want := range codes {
		if c == want {
			return true
		}
	}
	return false
}

// IsNotFound reports whether the error means "object does not exist".
// Exported for callers (reconcile, journal replay) that treat absence as a
// meaningful answer.
func IsNotFound(err error) bool {
	return isConnectCode(err, connect.CodeNotFound)
}

// notFound reports whether the error means "object does not exist".
func notFound(err error) bool {
	return IsNotFound(err)
}

// previewUnavailable reports whether preview can be skipped per the SDK
// contract (not deployed on every host): unimplemented/not-found transport.
func previewUnavailable(err error) bool {
	return isConnectCode(err, connect.CodeUnimplemented, connect.CodeNotFound)
}

// fatalValidationError reports whether a preview error is a real admission
// rejection (as opposed to transport/auth noise). Those must abort the create.
func fatalValidationError(err error) bool {
	var ve *sdkerrors.ValidationError
	return errors.As(err, &ve)
}

// classifyCreateError maps a create error to a retry decision.
type createErrClass int

const (
	createClassOK createErrClass = iota
	createClassRetryRateLimit
	createClassRejected // definitive validation/policy rejection
	createClassAmbiguous
	createClassFatal // auth/permission etc.
)

func classifyCreateError(err error) createErrClass {
	if err == nil {
		return createClassOK
	}
	var rl *sdkerrors.RateLimitError
	if errors.As(err, &rl) {
		return createClassRetryRateLimit
	}
	var ve *sdkerrors.ValidationError
	if errors.As(err, &ve) {
		return createClassRejected
	}
	var ae *sdkerrors.AuthError
	if errors.As(err, &ae) {
		return createClassFatal
	}
	switch {
	case isConnectCode(err, connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeInvalidArgument):
		return createClassFatal
	case isConnectCode(err, connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeCanceled,
		connect.CodeInternal, connect.CodeUnknown, connect.CodeResourceExhausted, connect.CodeDataLoss):
		return createClassAmbiguous
	default:
		// Transport-level failures (connection reset etc.) are ambiguous.
		return createClassAmbiguous
	}
}
