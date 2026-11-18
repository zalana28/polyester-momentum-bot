// Command backtest mereplay strategi dip (mean-reversion maker) di atas data
// candle historis tools/lab/candles/*.json, lalu membandingkan varian keputusan:
//
//	raw   — setiap sinyal dip dieksekusi (perilaku lama tanpa gate)
//	brain — gate kepala bot: skip downtrend/choppy, skor meanrev, cooldown
//	        adaptif setelah loss (paket brain+ensemble yang sudah ada di kode)
//
// Simulasi fill konservatif: maker entry terisi hanya jika candle berikutnya
// menembus harga entry (low <= entry), target terisi hanya jika high >= target,
// stop/timeout keluar taker pada harga terburuk yang terlihat.
//
//	go run ./cmd/backtest -pairs BTC-USDT,ETH-USDT -variant both
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"polyester-momentum-bot/internal/strategy"
)

// CandleRaw adalah satu baris di file tools/lab/candles.
type CandleRaw struct {
	Ts    int64   `json:"ts"`
	Open  float64 `json:"o"`
	High  float64 `json:"h"`
	Low   float64 `json:"l"`
	Close float64 `json:"c"`
	Vol   float64 `json:"v"`
}

type CandleFile struct {
	Candles   []CandleRaw `json:"candles"`
	Symbol    string      `json:"symbol"`
	Timeframe string      `json:"timeframe"`
}

type Config struct {
	BudgetUSD   float64 // modal per siklus
	OffsetBps   float64 // entry = mid - offset
	TargetBps   float64 // target = entry + target
	StopBps     float64 // taker stop di bawah entry
	MaxHoldBars int     // batas pegangan (candle 5m) sebelum time-stop
	MakerFee    float64 // 0.10%
	TakerFee    float64 // 0.15%
}

func defaultConfig() Config {
	return Config{
		BudgetUSD:   1000,
		OffsetBps:   30,
		TargetBps:   60,
		StopBps:     100,
		MaxHoldBars: 8, // ~40 menit, sekitar TimeStop live
		MakerFee:    0.0010,
		TakerFee:    0.0015,
	}
}

// Result adalah statistik satu varian di satu pair (atau total).
type Result struct {
	Cycles int     `json:"cycles"`
	Fills  int     `json:"fills"`
	Wins   int     `json:"wins"`
	Losses int     `json:"losses"`
	Gross  float64 `json:"gross_usdt"`
	Fees   float64 `json:"fees_usdt"`
	Net    float64 `json:"net_usdt"`
	MaxDD  float64 `json:"max_drawdown_usdt"`
}

func (r *Result) add(o Result) {
	r.Cycles += o.Cycles
	r.Fills += o.Fills
	r.Wins += o.Wins
	r.Losses += o.Losses
	r.Gross += o.Gross
	r.Fees += o.Fees
	r.Net += o.Net
	if o.MaxDD > r.MaxDD {
		r.MaxDD = o.MaxDD
	}
}

// btState melacak posisi terbuka dalam simulasi satu pair.
type btState struct {
	inPos    bool
	entryPx  float64
	entryBar int
	targetPx float64
	peakPx   float64
}

// runPair mereplay satu pair dengan satu varian gate, mengembalikan statistik.
func runPair(cfg Config, candles []CandleRaw, useBrain bool, minScore float64, cooldownMin float64) Result {
	var res Result
	warmup := 60 // candle untuk EMA/RSI/regime stabil

	// meanrev stateful: satu instance per replay, diberi makan tiap candle
	// tertutup tepat satu kali (mirip aliran live).
	meanrev := strategy.NewMeanReversionStrategy()

	st := btState{}
	equity := 0.0
	peakEq := 0.0
	lastLossIdx := -9999
	cooldownBars := int(cooldownMin/5.0) + 1 // candle 5m: 10 menit = 2 bar

	makerFeeUSD := cfg.BudgetUSD * cfg.MakerFee
	takerFeeUSD := cfg.BudgetUSD * cfg.TakerFee

	for i := warmup; i < len(candles); i++ {
		c := candles[i]
		// Strategi melihat candle tertutup ini duluan (state maju tiap bar).
		mrVote := meanrev.OnCandle(strategy.Candle{TsSec: c.Ts, High: c.High, Low: c.Low, Close: c.Close, Volume: c.Vol})

		if st.inPos {
			// 1) target maker: high >= target
			if c.High >= st.targetPx {
				gross := (st.targetPx - st.entryPx) / st.entryPx * cfg.BudgetUSD
				net := gross - makerFeeUSD - makerFeeUSD
				res.Fills++
				res.Wins++
				res.Gross += gross
				res.Fees += makerFeeUSD + makerFeeUSD
				res.Net += net
				equity += net
				peakEq = math.Max(peakEq, equity)
				res.MaxDD = math.Max(res.MaxDD, peakEq-equity)
				if net < 0 {
					lastLossIdx = i
				}
				meanrev.NoteExit()
				st = btState{}
				continue
			}
			// 2) stop taker: low menembus stop (asumsi terisi di harga stop)
			stopPx := st.entryPx * (1 - cfg.StopBps/10000)
			if c.Low <= stopPx {
				gross := (stopPx - st.entryPx) / st.entryPx * cfg.BudgetUSD // negatif
				net := gross - makerFeeUSD - takerFeeUSD
				res.Fills++
				res.Losses++
				res.Gross += gross
				res.Fees += makerFeeUSD + takerFeeUSD
				res.Net += net
				equity += net
				peakEq = math.Max(peakEq, equity)
				res.MaxDD = math.Max(res.MaxDD, peakEq-equity)
				if net < 0 {
					lastLossIdx = i
				}
				meanrev.NoteExit()
				st = btState{}
				continue
			}
			// 3) time-stop taker di close
			if i-st.entryBar >= cfg.MaxHoldBars {
				gross := (c.Close - st.entryPx) / st.entryPx * cfg.BudgetUSD
				net := gross - makerFeeUSD - takerFeeUSD
				res.Fills++
				if net > 0 {
					res.Wins++
				} else {
					res.Losses++
				}
				res.Gross += gross
				res.Fees += makerFeeUSD + takerFeeUSD
				res.Net += net
				equity += net
				peakEq = math.Max(peakEq, equity)
				res.MaxDD = math.Max(res.MaxDD, peakEq-equity)
				if net < 0 {
					lastLossIdx = i
				}
				meanrev.NoteExit()
				st = btState{}
				continue
			}
			st.peakPx = math.Max(st.peakPx, c.High)
			continue
		}

		// === Fase entry: keputusan di candle close, order di bawah mid ===
		window := toStrategyCandles(candles[max(0, i-120) : i+1])
		regime := strategy.ClassifyRegime(window, 12, 40)

		if useBrain {
			// Falling-knife guard persis rule live dip.
			if regime == strategy.RegimeTrendDown {
				continue
			}
			// Choppy: mean-reversion rawan whipsaw — skip.
			if regime == strategy.RegimeChoppy {
				continue
			}
			if i-lastLossIdx < cooldownBars {
				continue
			}
			if mrVote.Kind != "buy" || mrVote.Confidence*100 < minScore {
				continue
			}
		}

		// Entry maker di bawah mid (close dipakai sebagai proxy mid).
		mid := c.Close
		entry := mid * (1 - cfg.OffsetBps/10000)
		// Fill dicek mulai candle BERIKUTNYA (order dipasang di akhir candle i).
		for j := i + 1; j < len(candles); j++ {
			fc := candles[j]
			if fc.Low <= entry {
				// Terisi. Stop dicari dalam candle yang sama (worst case:
				// candle menukik ke stop sebelum rebound).
				stopPx := entry * (1 - cfg.StopBps/10000)
				targetPx := entry * (1 + cfg.TargetBps/10000)
				if fc.Low <= stopPx {
					// Terisi lalu langsung stop pada candle yang sama.
					gross := (stopPx - entry) / entry * cfg.BudgetUSD
					net := gross - makerFeeUSD - takerFeeUSD
					res.Cycles++
					res.Fills++
					res.Losses++
					res.Gross += gross
					res.Fees += makerFeeUSD + takerFeeUSD
					res.Net += net
					equity += net
					peakEq = math.Max(peakEq, equity)
					res.MaxDD = math.Max(res.MaxDD, peakEq-equity)
					meanrev.NoteExit()
					i = j
				} else if fc.High >= targetPx {
					// Terisi lalu langsung kena target pada candle yang sama.
					gross := (targetPx - entry) / entry * cfg.BudgetUSD
					net := gross - makerFeeUSD - makerFeeUSD
					res.Cycles++
					res.Fills++
					res.Wins++
					res.Gross += gross
					res.Fees += makerFeeUSD + makerFeeUSD
					res.Net += net
					equity += net
					peakEq = math.Max(peakEq, equity)
					res.MaxDD = math.Max(res.MaxDD, peakEq-equity)
					meanrev.NoteExit()
					i = j
				} else {
					st = btState{inPos: true, entryPx: entry, entryBar: j, targetPx: targetPx, peakPx: fc.High}
					res.Cycles++
				}
				break
			}
			if fc.Close < entry*(1-cfg.StopBps/10000) {
				// Harga kabur ke bawah jauh sebelum order terisi: order
				// realistis ditarik (chasing ke bawah dihindari).
				i = j
				break
			}
		}
	}
	return res
}

func toStrategyCandles(raw []CandleRaw) []strategy.Candle {
	out := make([]strategy.Candle, len(raw))
	for i, r := range raw {
		out[i] = strategy.Candle{TsSec: r.Ts, High: r.High, Low: r.Low, Close: r.Close, Volume: r.Vol}
	}
	return out
}

func main() {
	dir := flag.String("dir", "tools/lab/candles", "folder candle JSON")
	pairs := flag.String("pairs", "", "koma-pisah pair (kosong = semua)")
	variant := flag.String("variant", "both", "raw|brain|both")
	budget := flag.Float64("budget", 1000, "modal per siklus USD")
	offset := flag.Float64("offset-bps", 30, "entry di bawah mid (bps)")
	target := flag.Float64("target-bps", 60, "target profit (bps)")
	stop := flag.Float64("stop-bps", 100, "stop loss (bps)")
	minScore := flag.Float64("min-score", 55, "batas skor meanrev utk brain variant")
	cooldown := flag.Float64("cooldown", 10, "menit cooldown setelah loss")
	flag.Parse()

	cfg := defaultConfig()
	cfg.BudgetUSD = *budget
	cfg.OffsetBps = *offset
	cfg.TargetBps = *target
	cfg.StopBps = *stop

	files, err := filepath.Glob(filepath.Join(*dir, "*.json"))
	if err != nil || len(files) == 0 {
		fmt.Println("tidak ada candle:", err)
		os.Exit(1)
	}
	want := map[string]bool{}
	if *pairs != "" {
		for _, p := range strings.Split(*pairs, ",") {
			want[strings.ToUpper(strings.TrimSpace(p))] = true
		}
	}

	type pairRow struct {
		name       string
		raw, brain Result
	}
	rows := []pairRow{}
	for _, f := range files {
		var cf CandleFile
		b, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(b, &cf) != nil || len(cf.Candles) < 200 {
			continue
		}
		if len(want) > 0 && !want[cf.Symbol] {
			continue
		}
		var rawRes, brainRes Result
		if *variant == "raw" || *variant == "both" {
			rawRes = runPair(cfg, cf.Candles, false, 0, 0)
		}
		if *variant == "brain" || *variant == "both" {
			brainRes = runPair(cfg, cf.Candles, true, *minScore, *cooldown)
		}
		rows = append(rows, pairRow{cf.Symbol, rawRes, brainRes})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	var totRaw, totBrain Result
	fmt.Printf("%-12s %8s %6s %5s %5s %9s  | %6s %5s %5s %9s\n",
		"PAIR", "", "cyc", "win", "loss", "NET USDT", "cyc", "win", "loss", "NET USDT")
	fmt.Printf("%-12s %8s %6s %5s %5s %9s  | %6s %5s %5s %9s\n",
		"", "RAW", "", "", "", "", "BRAIN", "", "", "")
	for _, r := range rows {
		totRaw.add(r.raw)
		totBrain.add(r.brain)
		fmt.Printf("%-12s %8s %6d %5d %5d %9.2f  | %6d %5d %5d %9.2f\n",
			r.name, "", r.raw.Cycles, r.raw.Wins, r.raw.Losses, r.raw.Net,
			r.brain.Cycles, r.brain.Wins, r.brain.Losses, r.brain.Net)
	}
	fmt.Printf("%-12s %8s %6d %5d %5d %9.2f  | %6d %5d %5d %9.2f\n",
		"TOTAL", "", totRaw.Cycles, totRaw.Wins, totRaw.Losses, totRaw.Net,
		totBrain.Cycles, totBrain.Wins, totBrain.Losses, totBrain.Net)
	fmt.Printf("\nRAW   : fill=%d winrate=%.1f%% fee=%.2f maxDD=%.2f\n",
		totRaw.Fills, pct(totRaw.Wins, totRaw.Fills), totRaw.Fees, totRaw.MaxDD)
	fmt.Printf("BRAIN : fill=%d winrate=%.1f%% fee=%.2f maxDD=%.2f (minScore=%.0f cooldown=%.0fm)\n",
		totBrain.Fills, pct(totBrain.Wins, totBrain.Fills), totBrain.Fees, totBrain.MaxDD, *minScore, *cooldown)
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}
