// dashboard: TUI cockpit untuk dip maker bot. Read-only — membaca logs/dip.log,
// state-dip/stats.json, dan state-dip/trades.csv, lalu menggambar panel
// berwarna + mascot anime yang bereaksi terhadap hasil trade. Bot tetap
// jalan headless; dashboard tinggal dibuka di terminal kedua: ./bin/dashboard
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	dir      = "/Users/jazeerahusna/.zcode/workspace/default/polyester-momentum-bot"
	logPath  = dir + "/logs/dip.log"
	statPath = dir + "/state-dip/stats.json"
	csvPath  = dir + "/state-dip/trades.csv"
)

const (
	cReset = "\x1b[0m"
	cBold  = "\x1b[1m"
	cRed   = "\x1b[31m"
	cGrn   = "\x1b[32m"
	cYel   = "\x1b[33m"
	cCyn   = "\x1b[36m"
	cMag   = "\x1b[35m"
	cGray  = "\x1b[90m"
)

type pairStat struct {
	Fills, Skips, Wins, Losses int
	Net                        float64
}
type dipStats struct {
	TotalNet float64             `json:"total_net"`
	Cycles   int                 `json:"cycles"`
	Wins     int                 `json:"wins"`
	Losses   int                 `json:"losses"`
	PerPair  map[string]pairStat `json:"per_pair"`
}

type trade struct {
	ts, sym, strat, net, reason string
	netF                        float64
}

type event struct {
	line  string
	color string
	mark  string // simbol mood: "+", "-", "!", ".", "z"
}

var lineRe = regexp.MustCompile(`^\[(\d\d:\d\d:\d\d)\] (.*)$`)

func readTail(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > 4000 {
			lines = lines[1:]
		}
	}
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

func loadStats() dipStats {
	var st dipStats
	raw, err := os.ReadFile(statPath)
	if err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

func loadTrades(n int) []trade {
	var out []trade
	for _, l := range readTail(csvPath, n+1) {
		p := strings.Split(l, ",")
		if len(p) < 11 || p[0] == "ts_close" {
			continue
		}
		var net float64
		fmt.Sscan(p[9], &net)
		out = append(out, trade{p[0], p[1], p[2], p[9], p[10], net})
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func colorize(ev string) (string, string) {
	switch {
	case strings.Contains(ev, "FILLED"):
		return cGrn, "+"
	case strings.Contains(ev, "MOMENTUM"):
		return cYel, "!"
	case strings.Contains(ev, "SL") && strings.Contains(ev, "hit"),
		strings.Contains(ev, "taker exit"), strings.Contains(ev, "TAKER"):
		return cRed, "-"
	case strings.Contains(ev, "skip"), strings.Contains(ev, "falling knives"):
		return cGray, "."
	case strings.Contains(ev, "cycle done"):
		if strings.Contains(ev, "net=-") {
			return cRed, "-"
		}
		return cGrn, "+"
	case strings.Contains(ev, "SELL LIMIT"), strings.Contains(ev, "BUY LIMIT"):
		return cCyn, "."
	case strings.Contains(ev, "stopped"), strings.Contains(ev, "shutdown"):
		return cMag, "."
	}
	return cGray, "."
}

func MathAbs(f float64) float64 { if f < 0 { return -f }; return f }
func pad(s string, w int) string {
	s = strings.ReplaceAll(s, "\x1b", "")
	if len([]rune(s)) > w {
		r := []rune(s)
		return string(r[:w])
	}
	return s + strings.Repeat(" ", w-len([]rune(s)))
}

var quotesHappy = []string{"PROFIT! Nice one! ✧", "GANBATTE! We won! (★^▽^★)", "Look! All green!"}

func mascot(mood string) []string {
	switch mood {
	case "happy":
		return []string{`  \ (^▽^) /`, `    |   |  `, `   dD  bD  `}
	case "sad":
		return []string{`   (╥﹏╥)   `, `   /|   |\  `, `    dD bD   `}
	case "fight":
		return []string{`  ᕦ(ò_óˇ)ᕤ  `, `    |   |   `, `   dD   bD  `}
	case "sleep":
		return []string{`   (-.-) zZ`, `    |   |   `, `   dD  bD   `}
	}
	return []string{`   (•_•)    `, `    |   |   `, `   dD  bD   `}
}

func main() {
	rand.Seed(time.Now().UnixNano())
	fmt.Print("\x1b[?1049h\x1b[?25l") // alternate screen, sembunyikan kursor
	defer fmt.Print("\x1b[?25h\x1b[?1049l")
	for {
		st := loadStats()
		trades := loadTrades(5)
		logs := readTail(logPath, 10)

		// mood dari event & trade terakhir
		mood := "idle"
		var moodText, quote string
		if len(trades) > 0 {
			last := trades[len(trades)-1]
			age := time.Since(modTime(csvPath))
			if last.netF > 0 && age < 60*time.Second {
				mood = "happy"
			} else if last.netF < 0 && age < 60*time.Second {
				mood = "sad"
			}
		}
		if mood == "idle" {
			for i := len(logs) - 1; i >= 0; i-- {
				_, mk := colorize(logs[i])
				if mk == "!" {
					mood = "fight"
					break
				}
			}
			if mt := modTime(logPath); time.Since(mt) > 3*time.Minute {
				mood = "sleep"
			}
		}
		switch mood {
		case "happy":
			quote = quotesHappy[rand.Intn(len(quotesHappy))]
			moodText = "PROFIT!"
		case "sad":
			quote = "Nooo... SL hit (╥﹏╥)"
			moodText = "small loss, stay strong!"
		case "fight":
			quote = "GANBATTE! Momentum entry!"
			moodText = "momentum action!"
		case "sleep":
			quote = "zzz... quiet market..."
			moodText = "waiting for a dip..."
		default:
			quote = "Patience... a maker waits~"
			moodText = "hunting dips..."
		}

		// ---- BOT LIVE: baca state-dip/live.json (ditulis bot tiap 2-6 dtk) ----
		type liveState struct {
			Ts         int64   `json:"ts"`
			Phase      string  `json:"phase"`
			Symbol     string  `json:"symbol"`
			OrderPrice float64 `json:"order_price"`
			Mid        float64 `json:"mid"`
			DistBps    float64 `json:"dist_bps"`
			Qty        float64 `json:"qty"`
			Entry      float64 `json:"entry"`
			Target     float64 `json:"target"`
			Sl         float64 `json:"sl"`
		}
		var lv liveState
		lvFresh := false
		if raw, err := os.ReadFile(dir + "/state-dip/live.json"); err == nil {
			if json.Unmarshal(raw, &lv) == nil && time.Now().Unix()-lv.Ts < 30 {
				lvFresh = true
			}
		}

		var b strings.Builder
		b.WriteString("\x1b[H")
		line := func(s string) { b.WriteString(pad(s, 100) + "\r\n") }
		line(cBold + cMag + "  ╔═══════════════════ COCKPIT DIP MAKER v2 ═══════════════════╗" + cReset)
		line(cBold + cMag + "  ║" + cReset + "  💹 Polyester Testnet — maker mean-reversion + momentum AI  " + cBold + cMag + "║" + cReset)

		wr := 0.0
		if st.Cycles > 0 {
			wr = float64(st.Wins) / float64(st.Cycles) * 100
		}
		netStr := fmt.Sprintf("%+.4f", st.TotalNet)
		netCol := cGrn
		if st.TotalNet < 0 {
			netCol = cRed
		}
		line(fmt.Sprintf("  ║  PnL total strategi: %s$%s%s   siklus: %d (✓%d ✗%d)  winrate: %.0f%%  ",
			netCol, netStr, cReset, st.Cycles, st.Wins, st.Losses, wr) + cReset)
		line("  ║                                                            " + cReset)

		ms := mascot(mood)
		for i, ml := range ms {
			txt := ""
			switch i {
			case 0:
				txt = fmt.Sprintf("saldo terakhir tercatat & status: %s", moodText)
			case 1:
				txt = quote
			case 2:
				txt = fmt.Sprintf("%s%s%s", cGray, "\"bot tetap headless di belakang saya~\"", cReset)
			}
			line("  ║  " + cCyn + ml + cReset + "   " + txt)
		}
		line("  ║                                                            " + cReset)
		// ---- panel BOT LIVE ----
		line(cBold + cMag + "  ╠══════════════════════════ BOT LIVE ════════════════════════╣" + cReset)
		switch {
		case lvFresh && lv.Phase == "WAIT_BUY":
			line(fmt.Sprintf("  ║  ⏳ MENUNGGU DIP %s", lv.Symbol) + cReset)
			dir := "turun"
			if lv.DistBps < 0 {
				dir = "naik"
			}
			line(fmt.Sprintf("  ║  order @ %s%.6g%s | pasar @ %s%.6g%s | butuh %s %.0f bps%s",
				cYel, lv.OrderPrice, cReset, cCyn, lv.Mid, cReset, cYel, dir, MathAbs(lv.DistBps), cReset) + cReset)
			line("  ║  maker sabar menunggu harga turun ke order...              " + cReset)
		case lvFresh && lv.Phase == "HOLD":
			unrl := (lv.Mid - lv.Entry) * lv.Qty
			uc := cGrn
			if unrl < 0 {
				uc = cRed
			}
			line(fmt.Sprintf("  ║  💼 PEGANG %s  qty %.6g @ %.6g", lv.Symbol, lv.Qty, lv.Entry) + cReset)
			line(fmt.Sprintf("  ║  mid %.6g | TP %.6g | SL %.6g | P/L mengambang %s$%+.2f%s",
				lv.Mid, lv.Target, lv.Sl, uc, unrl, cReset) + cReset)
		case lvFresh:
			line("  ║  fase: " + lv.Phase + " " + lv.Symbol + "                                        " + cReset)
		default:
			line("  ║  (bot idle / tidak ada data live)                          " + cReset)
		}
		line("  ║                                                            " + cReset)
		line(cBold + cMag + "  ╠══════════════════════ TRADES TERAKHIR ═════════════════════╣" + cReset)
		if len(trades) == 0 {
			line("  ║  (belum ada trade tertutup)                                " + cReset)
		}
		for _, t := range trades {
			col := cGrn
			ico := "✅"
			if t.netF < 0 {
				col, ico = cRed, "❌"
			}
			line(fmt.Sprintf("  ║  %s %s  %-12s %-11s net %s$%s%s   [%s]",
				ico, cGray+t.ts[11:19]+cReset, t.sym, t.strat, col, t.net, cReset, t.reason) + cReset)
		}
		line(cBold + cMag + "  ╠══════════════════════════ LOG LIVE ════════════════════════╣" + cReset)
		for _, l := range logs {
			m := lineRe.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			col, _ := colorize(m[2])
			line(fmt.Sprintf("  ║  %s%s %s%s", cGray+m[1]+cReset, col, m[2], cReset))
		}
		line(cBold + cMag + "  ╚════════════════════════════════════════════════════════════╝" + cReset)
		spin := []string{"|", "/", "-", "\\"}[int(time.Now().Unix())%4]
		line(cGray + "  " + spin + " LIVE " + time.Now().UTC().Format("15:04:05") + " UTC · memantau logs/dip.log · Ctrl+C keluar (bot tidak terganggu)" + cReset)

		fmt.Print(b.String())
		time.Sleep(1 * time.Second)
	}
}

func modTime(path string) time.Time {
	fi, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}
