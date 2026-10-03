package strategy

import "testing"

func upSeries(n int, start, step float64) []Candle {
	out := make([]Candle, n)
	p := start
	for i := 0; i < n; i++ {
		p += step
		out[i] = Candle{TsSec: int64(i), High: p * 1.002, Low: p * 0.998, Close: p, Volume: 100 + float64(i)}
	}
	return out
}

func feedAll(s Strategy, cs []Candle) []Vote {
	var votes []Vote
	for _, c := range cs {
		v := s.OnCandle(c)
		if v.Kind != "" {
			votes = append(votes, v)
		}
	}
	return votes
}

func TestClassifyRegime(t *testing.T) {
	up := upSeries(60, 100, 0.5)
	if got := ClassifyRegime(up, 9, 21); got != RegimeTrendUp {
		t.Errorf("steady up = %s, want trend-up", got)
	}
	flat := make([]Candle, 60)
	for i := range flat {
		p := 100 + float64(i%2)*0.01 - float64((i+1)%2)*0.01
		flat[i] = Candle{TsSec: int64(i), High: p + 0.01, Low: p - 0.01, Close: p, Volume: 100}
	}
	if got := ClassifyRegime(flat, 9, 21); got == RegimeTrendUp {
		t.Errorf("flat series must not be trend-up (got %s)", got)
	}
}

func TestMeanReversionBuysOversold(t *testing.T) {
	s := NewMeanReversionStrategy()
	// Build a range then crash below the lower band.
	for i := 0; i < 30; i++ {
		s.OnCandle(Candle{TsSec: int64(i), High: 100.5, Low: 99.5, Close: 100, Volume: 100})
	}
	v := s.OnCandle(Candle{TsSec: 31, High: 97.5, Low: 95, Close: 95.5, Volume: 100})
	if v.Kind != "buy" {
		t.Fatalf("oversold crash should produce a buy, got %+v", v)
	}
	// Reversion back to the middle triggers exit.
	for i := 32; i < 35; i++ {
		s.OnCandle(Candle{TsSec: int64(i), High: 99.9, Low: 99, Close: 99.8, Volume: 100})
	}
	v = s.OnCandle(Candle{TsSec: 36, High: 100.3, Low: 99.6, Close: 100.1, Volume: 100})
	if v.Kind != "exit" {
		t.Fatalf("reversion to mid should exit, got %+v", v)
	}
}

func TestBreakoutBuysOnNewHigh(t *testing.T) {
	b := NewBreakoutStrategy()
	// Consolidate 25 candles at 100-101.
	for i := 0; i < 25; i++ {
		b.OnCandle(Candle{TsSec: int64(i), High: 101, Low: 100, Close: 100.5, Volume: 100})
	}
	// Break above the 20-high.
	v := b.OnCandle(Candle{TsSec: 26, High: 103, Low: 101.5, Close: 102.8, Volume: 300})
	if v.Kind != "buy" {
		t.Fatalf("breakout above channel high should buy, got %+v", v)
	}
	// Fall back to channel mid: exit.
	v = b.OnCandle(Candle{TsSec: 27, High: 102, Low: 100.5, Close: 100.4, Volume: 100})
	if v.Kind != "exit" {
		t.Fatalf("fall back to mid should exit, got %+v", v)
	}
}

func TestPullbackBuysDipInUptrend(t *testing.T) {
	p := NewPullbackStrategy()
	// Uptrend: +0.3% per candle for 40 candles.
	var lastBuy Vote
	fired := false
	price := 100.0
	for i := 0; i < 40; i++ {
		price *= 1.003
		p.OnCandle(Candle{TsSec: int64(i), High: price * 1.001, Low: price * 0.999, Close: price, Volume: 100})
	}
	// Pullback candle: -2% dip closing above the EMA21 (which lags ~3% behind
	// in a +0.3%/candle trend).
	dip := price * 0.98
	lastBuy = p.OnCandle(Candle{TsSec: 41, High: price, Low: dip * 0.997, Close: dip, Volume: 200})
	if lastBuy.Kind != "buy" {
		t.Fatalf("pullback in uptrend should buy, got %+v (close %.4f)", lastBuy, dip)
	}
	fired = true
	_ = fired
}

func TestEnsembleWeightsAdapt(t *testing.T) {
	e := NewEnsemble(0.5, NewMeanReversionStrategy(), NewBreakoutStrategy())
	if w := e.Weights()["meanrev"]; w != 1.0 {
		t.Fatalf("initial weight = %f, want 1.0", w)
	}
	// Two losses shrink the meanrev voice.
	e.Report("meanrev", -0.8)
	e.Report("meanrev", -0.8)
	w := e.Weights()["meanrev"]
	if w >= 1.0 {
		t.Fatalf("losing strategy must lose weight, got %f", w)
	}
	// Wins restore it (0.6 + 2x0.25 = 1.1).
	e.Report("meanrev", +1.0)
	e.Report("meanrev", +1.0)
	w = e.Weights()["meanrev"]
	if w <= 1.0 {
		t.Fatalf("wins must restore weight above 1.0, got %f", w)
	}
	// Clamp at the floor.
	for i := 0; i < 20; i++ {
		e.Report("meanrev", -1.0)
	}
	if w = e.Weights()["meanrev"]; w < 0.2 {
		t.Fatalf("weight below floor: %f", w)
	}
}

func TestEnsembleSuitedWeighting(t *testing.T) {
	mr := NewMeanReversionStrategy()
	mo := NewMomentumStrategy(Params{FastEMA: 9, SlowEMA: 21, ROCPeriod: 5, VolMAPeriod: 20})
	e := NewEnsemble(0.4, mr, mo)
	// In a range regime, a meanrev buy with conf 0.8 gets weight 1.0*0.8=0.8 ≥ 0.5 → buy.
	candles := choppyCandles(30)
	// Push below the lower band on the last candle.
	last := candles[len(candles)-1]
	last.Close *= 0.9
	last.High = last.Close * 1.005
	candles[len(candles)-1] = last
	// Feed the series through the ensemble, then read the last candle's vote.
	var buy *Vote
	var votes []Vote
	for i, c := range candles {
		regime := RegimeRange
		if i == len(candles)-1 {
			regime = RegimeRange
		}
		b, ex, vs := e.OnCandle(c, regime)
		if i == len(candles)-1 {
			buy, votes = b, vs
		}
		_ = ex
	}
	if buy == nil {
		t.Fatalf("meanrev buy should clear the bar in range regime: %+v", votes)
	}
	if buy.Strategy != "meanrev" {
		t.Fatalf("attribution = %s, want meanrev", buy.Strategy)
	}
}

func choppyCandles(n int) []Candle {
	out := make([]Candle, n)
	p := 100.0
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			p *= 1.004
		} else {
			p /= 1.004
		}
		out[i] = Candle{TsSec: int64(i), High: p * 1.002, Low: p * 0.998, Close: p, Volume: 100}
	}
	return out
}
