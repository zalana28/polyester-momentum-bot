// Package ledgerx wraps ledger reads with a bounded retry policy.
//
// The ledger read service answers 503 with a health-shaped body
// ({"ok":false,"service":"ledger-api-read",...}) while one of its downstream
// dependencies recovers. Those failures are transient; surfacing them on the
// first attempt makes the bot treat a healthy account as "equity unavailable"
// (or as a zero base balance) and skip its daily-loss guard. Callers should go
// through this package instead of calling Balances.List directly.
package ledgerx

import (
	"context"
	"errors"
	"math/rand"
	"time"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
	sdkerrors "github.com/Fabric-Labs/polyester-sdk-go/errors"
	"github.com/Fabric-Labs/polyester-sdk-go/models"
)

// retryDelays is the wait before each retry; len(retryDelays) is the retry
// budget. A 503 burst usually clears within a second or two.
var retryDelays = []time.Duration{250 * time.Millisecond, 750 * time.Millisecond, 1500 * time.Millisecond}

// List returns the account's balances, retrying transient upstream failures.
func List(ctx context.Context, client *polyester.Client) (models.BalancesList, error) {
	return Retry(ctx, func(ctx context.Context) (models.BalancesList, error) {
		return client.Balances.List(ctx, nil, nil)
	})
}

// Retry runs fetch until it succeeds, returns a non-retryable error, exhausts
// the retry budget, or ctx is done.
func Retry(ctx context.Context, fetch func(context.Context) (models.BalancesList, error)) (models.BalancesList, error) {
	for attempt := 0; ; attempt++ {
		out, err := fetch(ctx)
		if err == nil {
			return out, nil
		}
		if !Retryable(err) || attempt >= len(retryDelays) {
			return models.BalancesList{}, err
		}
		select {
		case <-ctx.Done():
			return models.BalancesList{}, err
		case <-time.After(jitter(retryDelays[attempt])):
		}
	}
}

// jitter spreads retry waits by ±20% so a fleet of symbols that failed on the
// same ledger hiccup does not retry in lockstep. Sub-10ms waits are left alone
// so tests stay fast and deterministic.
func jitter(d time.Duration) time.Duration {
	if d <= 10*time.Millisecond {
		return d
	}
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// Retryable reports whether err is a transient failure worth another attempt.
// Auth, validation, routing and response-contract failures are deterministic:
// retrying them only burns rate limit.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	var (
		authErr     *sdkerrors.AuthError
		valErr      *sdkerrors.ValidationError
		routeErr    *sdkerrors.RouteNotFoundError
		contractErr *sdkerrors.ResponseContractError
	)
	if errors.As(err, &authErr) ||
		errors.As(err, &valErr) ||
		errors.As(err, &routeErr) ||
		errors.As(err, &contractErr) {
		return false
	}
	var (
		serverErr    *sdkerrors.ServerError
		transportErr *sdkerrors.TransportError
		rateErr      *sdkerrors.RateLimitError
	)
	if errors.As(err, &serverErr) || errors.As(err, &transportErr) || errors.As(err, &rateErr) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}
