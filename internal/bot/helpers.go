package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Fabric-Labs/polyester-sdk-go/models"

	"polyester-momentum-bot/internal/config"
	"polyester-momentum-bot/internal/execution"
	"polyester-momentum-bot/internal/journal"
	"polyester-momentum-bot/internal/num"
)

// isDust reports whether the error is the dust sentinel.
func isDust(err error) bool { return errors.Is(err, execution.ErrDust) }

const configModeLive = config.ModeLive

// journalAdapter adapts the bot's journal to the executor interface.
type journalAdapter struct{ jrnl *journal.Journal }

func (a journalAdapter) Append(e execution.Entry) error {
	return a.jrnl.Append(journal.Entry{
		Kind:            e.Kind,
		ClientOrderID:   e.ClientOrderID,
		ClientTriggerID: e.ClientTriggerID,
		Detail:          e.Detail,
	})
}

// pairForSymbol finds a pair object in the raw spot config.
func pairForSymbol(spotRaw map[string]any, symbol string) (map[string]any, error) {
	for _, key := range []string{"pairs", "symbols"} {
		raw, _ := spotRaw[key].([]any)
		for _, item := range raw {
			pair, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if sym, _ := pair["symbol"].(string); sym == symbol {
				return pair, nil
			}
		}
	}
	return nil, fmt.Errorf("symbol %q not found in spot config", symbol)
}

// uintField returns the first present key as uint32.
func uintField(pair map[string]any, keys ...string) *uint32 {
	for _, k := range keys {
		switch v := pair[k].(type) {
		case float64:
			id := uint32(v)
			return &id
		case uint32:
			return &v
		case uint64:
			id := uint32(v)
			return &id
		case int:
			id := uint32(v)
			return &id
		case json.Number:
			if n, err := v.Int64(); err == nil {
				id := uint32(n)
				return &id
			}
		}
	}
	return nil
}

// strField returns the first present key as string.
func strField(pair map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := pair[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// baseAssetSymbol reads the pair's base asset symbol, falling back to the
// part before "-" in the display symbol (same convention as the official
// examples).
func baseAssetSymbol(pair map[string]any, symbol string) string {
	if v := strField(pair, "baseAsset", "base_asset"); v != "" {
		return v
	}
	if parts := strings.SplitN(symbol, "-", 2); len(parts) > 0 {
		return parts[0]
	}
	return symbol
}

// quoteAssetSymbol reads the pair's quote asset symbol, falling back to the
// part after "-" in the display symbol.
func quoteAssetSymbol(pair map[string]any, symbol string) string {
	if v := strField(pair, "quoteAsset", "quote_asset"); v != "" {
		return v
	}
	if parts := strings.SplitN(symbol, "-", 2); len(parts) == 2 {
		return parts[1]
	}
	return "USDT"
}

// minNotionalScaled converts the pair's decimal min-notional into quote-scaled
// integer space via big.Rat-safe parsing.
func minNotionalScaled(pc models.PairConstraints, quoteScale int) int64 {
	v, err := num.ParseDecimalToScaled(pc.MinNotionalQuote, quoteScale)
	if err != nil || v < 0 {
		return 0
	}
	return v
}
