package ledgerx

import (
	"context"
	"errors"
	"testing"
	"time"

	sdkerrors "github.com/Fabric-Labs/polyester-sdk-go/errors"
	"github.com/Fabric-Labs/polyester-sdk-go/models"
)

func withFastRetries(t *testing.T) {
	t.Helper()
	prev := retryDelays
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryDelays = prev })
}

func TestRetryRecoversFromTransientServerError(t *testing.T) {
	withFastRetries(t)
	calls := 0
	out, err := Retry(context.Background(), func(context.Context) (models.BalancesList, error) {
		calls++
		if calls < 3 {
			return models.BalancesList{}, &sdkerrors.ServerError{Msg: "request failed without server error details"}
		}
		return models.BalancesList{Balances: []models.AssetBalance{{AssetID: 7}}}, nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
	if len(out.Balances) != 1 || out.Balances[0].AssetID != 7 {
		t.Fatalf("unexpected balances: %+v", out.Balances)
	}
}

func TestRetryStopsAtBudget(t *testing.T) {
	withFastRetries(t)
	calls := 0
	_, err := Retry(context.Background(), func(context.Context) (models.BalancesList, error) {
		calls++
		return models.BalancesList{}, &sdkerrors.ServerError{Msg: "boom"}
	})
	if err == nil {
		t.Fatal("expected the last error")
	}
	if want := len(retryDelays) + 1; calls != want {
		t.Fatalf("expected %d attempts, got %d", want, calls)
	}
}

func TestRetryDoesNotRetryAuthErrors(t *testing.T) {
	withFastRetries(t)
	calls := 0
	_, err := Retry(context.Background(), func(context.Context) (models.BalancesList, error) {
		calls++
		return models.BalancesList{}, &sdkerrors.AuthError{Msg: "401 Unauthorized", Status: 401}
	})
	if err == nil {
		t.Fatal("expected auth error")
	}
	if calls != 1 {
		t.Fatalf("auth errors must not be retried, got %d attempts", calls)
	}
}

func TestRetryStopsWhenContextDone(t *testing.T) {
	prev := retryDelays
	retryDelays = []time.Duration{time.Hour}
	t.Cleanup(func() { retryDelays = prev })

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := Retry(ctx, func(context.Context) (models.BalancesList, error) {
		calls++
		return models.BalancesList{}, &sdkerrors.ServerError{Msg: "boom"}
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Fatalf("expected to stop after cancellation, got %d attempts", calls)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation did not interrupt the retry wait (%s)", elapsed)
	}
}

func TestRetryableClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"server", &sdkerrors.ServerError{Msg: "503"}, true},
		{"transport", &sdkerrors.TransportError{Msg: "reset"}, true},
		{"rate limit", &sdkerrors.RateLimitError{Msg: "429"}, true},
		{"deadline", context.DeadlineExceeded, true},
		{"auth", &sdkerrors.AuthError{Msg: "401"}, false},
		{"validation", &sdkerrors.ValidationError{Msg: "bad"}, false},
		{"route", &sdkerrors.RouteNotFoundError{Procedure: "x"}, false},
		{"contract", &sdkerrors.ResponseContractError{Msg: "bad shape"}, false},
		{"wrapped plain", errors.New("other"), false},
	}
	for _, tc := range cases {
		if got := Retryable(tc.err); got != tc.want {
			t.Fatalf("%s: Retryable=%v want %v", tc.name, got, tc.want)
		}
	}
}
