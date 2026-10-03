package num

import "testing"

func TestParseDecimalToScaled(t *testing.T) {
	cases := []struct {
		in    string
		scale int
		want  int64
		err   bool
	}{
		{"25", 2, 2500, false},
		{"25.00", 2, 2500, false},
		{"25.005", 2, 2500, false}, // rounds down, never up
		{"0.000001", 6, 1, false},
		{"1000000", 6, 1_000_000_000_000, false},
		{"-1", 2, 0, true},
		{"abc", 2, 0, true},
		{"", 2, 0, true},
	}
	for _, c := range cases {
		got, err := ParseDecimalToScaled(c.in, c.scale)
		if c.err {
			if err == nil {
				t.Errorf("ParseDecimalToScaled(%q,%d): expected error", c.in, c.scale)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDecimalToScaled(%q,%d): unexpected error %v", c.in, c.scale, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseDecimalToScaled(%q,%d) = %d, want %d", c.in, c.scale, got, c.want)
		}
	}
}

func TestFormatScaled(t *testing.T) {
	cases := []struct {
		in    int64
		scale int
		want  string
	}{
		{2500, 2, "25"},
		{2505, 2, "25.05"},
		{1, 6, "0.000001"},
		{1_000_000, 6, "1"},
		{0, 2, "0"},
	}
	for _, c := range cases {
		if got := FormatScaled(c.in, c.scale); got != c.want {
			t.Errorf("FormatScaled(%d,%d) = %q, want %q", c.in, c.scale, got, c.want)
		}
	}
}

func TestScaledRoundTrip(t *testing.T) {
	for _, s := range []string{"25", "25.5", "0.000123", "12345.6789"} {
		v, err := ParseDecimalToScaled(s, 6)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", s, err)
		}
		if got := FormatScaled(v, 6); got != TrimZeros(s) {
			t.Errorf("round trip %q -> %d -> %q", s, v, got)
		}
	}
}

func TrimZeros(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out := s
			for len(out) > 0 && (out[len(out)-1] == '0') {
				out = out[:len(out)-1]
			}
			if len(out) > 0 && out[len(out)-1] == '.' {
				out = out[:len(out)-1]
			}
			return out
		}
	}
	return s
}

func TestMulBpsFloorAndTick(t *testing.T) {
	entry := int64(100_000_000) // 100.000000
	if got := MulBpsFloor(entry, 9900); got != 99_000_000 {
		t.Errorf("SL 1%% = %d, want 99000000", got)
	}
	if got := MulBpsFloor(entry, 10200); got != 102_000_000 {
		t.Errorf("TP 2%% = %d, want 102000000", got)
	}
	if got := RoundToTickFloor(100_123_456, 100); got != 100_123_400 {
		t.Errorf("tick floor = %d, want 100123400", got)
	}
}

func TestScaledDiv(t *testing.T) {
	// 25 USDT (scale 2) at price 100.5 ticks=100_500_000 (1e6) -> base qty at scale 8.
	got, err := ScaledDiv(2500, 2, 100_500_000, 6, 8)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := int64(24_875_621) // 0.24875621 base units (floor of 0.2487562189...)
	if got != want {
		t.Errorf("ScaledDiv = %d, want %d", got, want)
	}
}
