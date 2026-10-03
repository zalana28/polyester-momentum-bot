// Package num holds exact decimal/scaled-integer math used on the money path.
// Polyester protocol prices are fixed 1e6 ticks; quantities carry a per-pair
// catalog scale. Never use floats for sizing, budgets, or trigger prices.
package num

import (
	"fmt"
	"math/big"
	"strings"
)

// ParseDecimalToScaled parses a decimal string (e.g. "25.00") into an integer
// scaled by 10^scale. The value must be finite, non-negative, and fit int64
// after scaling.
func ParseDecimalToScaled(s string, scale int) (int64, error) {
	if scale < 0 || scale > 36 {
		return 0, fmt.Errorf("num: unsupported scale %d", scale)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("num: empty decimal string")
	}
	rat, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0, fmt.Errorf("num: %q is not a valid decimal", s)
	}
	if rat.Sign() < 0 {
		return 0, fmt.Errorf("num: negative value %q not allowed", s)
	}
	scaled := new(big.Rat).Mul(rat, pow10(scale))
	if !scaled.IsInt() {
		// Round down to the target scale: sizes must never round up.
		scaled = floorRat(scaled)
	}
	i := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	if !i.IsInt64() {
		return 0, fmt.Errorf("num: %q at scale %d overflows int64", s, scale)
	}
	return i.Int64(), nil
}

// FormatScaled renders a scaled integer as a plain decimal string.
func FormatScaled(scaled int64, scale int) string {
	if scale <= 0 {
		return fmt.Sprintf("%d", scaled)
	}
	neg := scaled < 0
	if neg {
		scaled = -scaled
	}
	unit := pow10Int(scale)
	whole := scaled / unit
	frac := scaled % unit
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	fmt.Fprintf(&b, "%d.%0*d", whole, scale, frac)
	s := b.String()
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// ScaledToFloat converts a scaled integer to float64. Only for indicators,
// logging, and risk thresholds — never for order sizing.
func ScaledToFloat(scaled int64, scale int) float64 {
	return float64(scaled) / pow10Float(scale)
}

// FloatToScaledRounded converts a float64 to a scaled integer, rounding to
// nearest. Only for indicators/logging; money inputs must use decimals.
func FloatToScaledRounded(f float64, scale int) int64 {
	v := f * pow10Float(scale)
	return int64(v + 0.5)
}

// Pow10 returns 10^scale as int64 (scale must be in [0,18]).
func Pow10(scale int) int64 {
	if scale < 0 || scale > 18 {
		return 1
	}
	return pow10Int(scale)
}

// ShiftUp returns scaled × 10^k (k >= 0). Overflow-safe for int64 inputs
// used in this bot (prices and bps-derived values stay far below int64).
func ShiftUp(scaled int64, k int) int64 {
	for i := 0; i < k; i++ {
		scaled *= 10
	}
	return scaled
}

// ShiftDown returns scaled / 10^k (k >= 0), rounding down.
func ShiftDown(scaled int64, k int) int64 {
	for i := 0; i < k; i++ {
		scaled /= 10
	}
	return scaled
}

// MulBpsFloor returns ticks*bps/10000, rounding down. Use for SL/TP trigger
// prices derived from an entry price: MulBpsFloor(entry, 10000-slBps) etc.
func MulBpsFloor(ticks int64, bps int64) int64 {
	if ticks < 0 || bps < 0 {
		return 0
	}
	return ticks * bps / 10000
}

// RoundToTickFloor rounds ticks down to a multiple of tickTicks so trigger
// prices respect the pair's tick size. tickTicks must be positive.
func RoundToTickFloor(ticks, tickTicks int64) int64 {
	if tickTicks <= 0 {
		return ticks
	}
	return ticks - ticks%tickTicks
}

// ScaledDiv computes a/b at arbitrary scales exactly, rounding down:
// result = floor(a * 10^resScale * 10^bScale / (b * 10^aScale)).
// It answers "how much base (baseScale) do I get for quote (quoteScale)
// at price ticks (1e6)?" via ScaledDiv(quote, quoteScale, ticks, 6, baseScale).
func ScaledDiv(a int64, aScale int, b int64, bScale int, resScale int) (int64, error) {
	if b <= 0 {
		return 0, fmt.Errorf("num: division by zero or negative")
	}
	if a < 0 || aScale < 0 || bScale < 0 || resScale < 0 || resScale > 36 {
		return 0, fmt.Errorf("num: bad ScaledDiv arguments a=%d aScale=%d bScale=%d resScale=%d", a, aScale, bScale, resScale)
	}
	num := new(big.Int).Mul(big.NewInt(a), pow10Big(resScale+bScale))
	den := big.NewInt(b)
	den.Mul(den, pow10Big(aScale))
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if r.Sign() < 0 {
		q.Sub(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, fmt.Errorf("num: ScaledDiv result overflows int64")
	}
	return q.Int64(), nil
}

// ScaledMulDiv computes floor(a*b/c) with per-value scales, for derived
// quantities such as proceeds = base * price.
func ScaledMulDiv(a int64, aScale int, b int64, bScale int, c int64, cScale int, resScale int) (int64, error) {
	if c <= 0 {
		return 0, fmt.Errorf("num: division by zero or negative")
	}
	if a < 0 || b < 0 || aScale < 0 || bScale < 0 || cScale < 0 || resScale < 0 || resScale > 36 {
		return 0, fmt.Errorf("num: bad ScaledMulDiv arguments")
	}
	num := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	num.Mul(num, pow10Big(resScale+cScale))
	den := new(big.Int).Mul(big.NewInt(c), pow10Big(aScale+bScale))
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if r.Sign() < 0 {
		q.Sub(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, fmt.Errorf("num: ScaledMulDiv result overflows int64")
	}
	return q.Int64(), nil
}

func pow10(scale int) *big.Rat {
	return new(big.Rat).SetInt(pow10Big(scale))
}

func pow10Big(scale int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
}

func pow10Int(scale int) int64 {
	v := int64(1)
	for i := 0; i < scale; i++ {
		v *= 10
	}
	return v
}

func pow10Float(scale int) float64 {
	f := 1.0
	for i := 0; i < scale; i++ {
		f *= 10
	}
	return f
}

func floorRat(r *big.Rat) *big.Rat {
	q, rem := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if rem.Sign() < 0 {
		q.Sub(q, big.NewInt(1))
	}
	return new(big.Rat).SetInt(q)
}
