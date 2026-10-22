// Package report turns the bot's shadow signal log and trade log into an
// edge report: does the score predict anything, which symbols pay, and what
// did the gate cost or save.
//
// It reads both signal schemas. v1 (signals.csv) records mark-to-mark returns;
// v2 (signals-v2.csv) adds the signal candle timestamp and the book at signal
// and exit time, so returns net of the spread are available.
package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Signal is one shadow outcome at one horizon.
type Signal struct {
	SignalTs string
	Symbol   string
	Strategy string
	Approved bool
	Score    float64
	Regime   string
	Horizon  int
	Ret      float64 // mark-to-mark bps
	Net      float64 // round-trip bps (ask in, bid out); equals Ret for v1 rows
	HasNet   bool
}

// Trade is one closed position.
type Trade struct {
	Symbol     string
	Score      float64
	NetQuote   float64
	ExitReason string
	HoldSecs   int
}

// LoadSignals reads every existing signal file, oldest schema first.
func LoadSignals(paths ...string) ([]Signal, error) {
	var out []Signal
	for _, path := range paths {
		sigs, err := loadSignalFile(path)
		if err != nil {
			return nil, err
		}
		out = append(out, sigs...)
	}
	return out, nil
}

func loadSignalFile(path string) ([]Signal, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // a missing log is not an error: v2 starts empty
		}
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	col := map[string]int{}
	for i, name := range head {
		col[strings.TrimSpace(name)] = i
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return rec[i]
		}
		return ""
	}

	var out []Signal
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue // skip malformed line rather than lose the whole report
		}
		score, err1 := strconv.ParseFloat(get(rec, "score"), 64)
		horizon, err2 := strconv.Atoi(get(rec, "horizon"))
		ret, err3 := strconv.ParseFloat(get(rec, "ret_bps"), 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		s := Signal{
			SignalTs: get(rec, "signal_ts"),
			Symbol:   get(rec, "symbol"),
			Strategy: get(rec, "strategy"),
			Approved: get(rec, "approved") == "true",
			Score:    score,
			Regime:   get(rec, "regime"),
			Horizon:  horizon,
			Ret:      ret,
			Net:      ret,
		}
		if v := get(rec, "ret_net_bps"); v != "" {
			if net, err := strconv.ParseFloat(v, 64); err == nil {
				s.Net, s.HasNet = net, true
			}
		}
		if s.SignalTs == "" { // v1 has no signal timestamp; fall back to write time
			s.SignalTs = get(rec, "ts")
		}
		out = append(out, s)
	}
	return out, nil
}

// Stats summarises one group of outcomes.
type Stats struct {
	N       int
	Mean    float64 // mean Net bps
	Median  float64
	WinRate float64 // share of Net > 0, in percent
	Gross   float64 // mean mark-to-mark bps
}

// Summarise computes Stats for rows handled by keep.
func Summarise(sigs []Signal, keep func(Signal) bool) Stats {
	var vals, gross []float64
	for _, s := range sigs {
		if !keep(s) {
			continue
		}
		vals = append(vals, s.Net)
		gross = append(gross, s.Ret)
	}
	st := Stats{N: len(vals)}
	if len(vals) == 0 {
		return st
	}
	for _, v := range vals {
		st.Mean += v
		if v > 0 {
			st.WinRate++
		}
	}
	st.Mean /= float64(len(vals))
	st.WinRate = st.WinRate / float64(len(vals)) * 100

	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		st.Median = (sorted[mid-1] + sorted[mid]) / 2
	} else {
		st.Median = sorted[mid]
	}
	for _, v := range gross {
		st.Gross += v
	}
	st.Gross /= float64(len(gross))
	return st
}

// Bucket is one score band of the report.
type Bucket struct {
	Lo, Hi int
	Stats  Stats
}

// Buckets are the score bands the gate decision turns on.
var Buckets = []Bucket{
	{Lo: 0, Hi: 24}, {Lo: 25, Hi: 34}, {Lo: 35, Hi: 44}, {Lo: 45, Hi: 54}, {Lo: 55, Hi: 100},
}

// ByScore fills Buckets with outcomes at one horizon.
func ByScore(sigs []Signal, horizon int) []Bucket {
	out := make([]Bucket, len(Buckets))
	for i, b := range Buckets {
		b.Stats = Summarise(sigs, func(s Signal) bool {
			return s.Horizon == horizon && s.Score >= float64(b.Lo) && s.Score <= float64(b.Hi)
		})
		out[i] = b
	}
	return out
}

// BySymbol summarises outcomes per symbol at one horizon, best mean first.
func BySymbol(sigs []Signal, horizon int) map[string]Stats {
	syms := map[string]bool{}
	for _, s := range sigs {
		if s.Horizon == horizon {
			syms[s.Symbol] = true
		}
	}
	out := map[string]Stats{}
	for sym := range syms {
		sym := sym
		out[sym] = Summarise(sigs, func(s Signal) bool {
			return s.Horizon == horizon && s.Symbol == sym
		})
	}
	return out
}

// ByDay summarises outcomes per UTC day at one horizon.
func ByDay(sigs []Signal, horizon int) map[string]Stats {
	days := map[string]bool{}
	for _, s := range sigs {
		if s.Horizon == horizon && len(s.SignalTs) >= 10 {
			days[s.SignalTs[:10]] = true
		}
	}
	out := map[string]Stats{}
	for d := range days {
		d := d
		out[d] = Summarise(sigs, func(s Signal) bool {
			return s.Horizon == horizon && len(s.SignalTs) >= 10 && s.SignalTs[:10] == d
		})
	}
	return out
}

// Verdict states, in one line, what the score buckets imply for the gate.
//
// It is deliberately blunt: the report exists to stop threshold changes made on
// vibes, so it only claims an edge when the data actually shows one.
func Verdict(buckets []Bucket) string {
	var informative []Bucket
	for _, b := range buckets {
		if b.Stats.N >= 20 {
			informative = append(informative, b)
		}
	}
	if len(informative) < 2 {
		return "sampel terlalu kecil — belum bisa menyimpulkan apa pun"
	}
	positive := 0
	for _, b := range informative {
		if b.Stats.Mean > 0 {
			positive++
		}
	}
	// Returns falling as scores rise is the anti-predictive pattern: the
	// scorer ranks the worst setups highest.
	monotonicDown := true
	for i := 1; i < len(informative); i++ {
		if informative[i].Stats.Mean > informative[i-1].Stats.Mean {
			monotonicDown = false
		}
	}
	switch {
	case positive == 0 && monotonicDown:
		return "ANTI-PREDIKTIF: semua bucket negatif dan skor tinggi justru return lebih buruk — menurunkan ambang hanya memperbanyak trade rugi"
	case positive == 0:
		return "TIDAK ADA EDGE: semua bucket skor negatif — menurunkan ambang hanya memperbanyak trade rugi"
	case positive*2 < len(informative) && monotonicDown:
		return "ANTI-PREDIKTIF: skor tinggi justru return lebih buruk — gate benar menahan entry"
	case positive == len(informative):
		return "EDGE POSITIF di semua bucket — layak menurunkan ambang dan mengukur ulang"
	default:
		return "EDGE CAMPURAN: periksa bucket yang positif sebelum mengubah ambang"
	}
}

// LoadTrades reads the closed-trade log; missing fields degrade to zero.
func LoadTrades(path string) ([]Trade, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil {
		return nil, nil
	}
	col := map[string]int{}
	for i, name := range head {
		col[strings.TrimSpace(name)] = i
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return rec[i]
		}
		return ""
	}
	var out []Trade
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		net, _ := strconv.ParseFloat(get(rec, "net_usdt"), 64)
		score, _ := strconv.ParseFloat(get(rec, "signal_score"), 64)
		hold, _ := strconv.Atoi(get(rec, "hold_secs"))
		out = append(out, Trade{
			Symbol: get(rec, "symbol"), Score: score, NetQuote: net,
			ExitReason: get(rec, "exit_reason"), HoldSecs: hold,
		})
	}
	return out, nil
}

// TradeSummary aggregates the closed-trade log.
type TradeSummary struct {
	N        int
	Net      float64
	Wins     int
	ByReason map[string]int
}

// SummariseTrades totals net PnL and groups exits by reason.
func SummariseTrades(trades []Trade) TradeSummary {
	sum := TradeSummary{ByReason: map[string]int{}}
	for _, t := range trades {
		sum.N++
		sum.Net += t.NetQuote
		if t.NetQuote > 0 {
			sum.Wins++
		}
		sum.ByReason[t.ExitReason]++
	}
	return sum
}

// FmtStats renders one row of the table.
func FmtStats(s Stats) string {
	if s.N == 0 {
		return "n=0"
	}
	return fmt.Sprintf("n=%4d  net %+7.1fbps  med %+7.1f  win %3.0f%%  (gross %+7.1f)",
		s.N, s.Mean, s.Median, s.WinRate, s.Gross)
}
