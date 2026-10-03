// office: server lokal (127.0.0.1:8787) yang menyajikan kantor 3D tim bot.
// Read-only: membaca logs/dip.log, stats.json, trades.csv -> /api/state.
// Jalankan: ./bin/office lalu buka http://127.0.0.1:8787 di browser.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const dir = "/Users/jazeerahusna/.zcode/workspace/default/polyester-momentum-bot"

type ev struct {
	Ts   string `json:"ts"`
	Kind string `json:"kind"` // filled|momentum|sl|skip|sell|profit|loss|info
	Sym  string `json:"sym"`
	Txt  string `json:"txt"`
}

type state struct {
	TotalNet float64  `json:"total_net"`
	Cycles   int      `json:"cycles"`
	Wins     int      `json:"wins"`
	Losses   int      `json:"losses"`
	Events   []ev     `json:"events"`
	Trades   []string `json:"trades"`
}

var lineRe = regexp.MustCompile(`^\[(\d\d:\d\d:\d\d)\] (.*)$`)

func kindOf(s string) string {
	switch {
	case strings.Contains(s, "MOMENTUM FILLED"), strings.Contains(s, "MOMENTUM:"):
		return "momentum"
	case strings.Contains(s, "FILLED"):
		return "filled"
	case strings.Contains(s, "SELL LIMIT"):
		return "sell"
	case strings.Contains(s, "SL bot-side kena"), strings.Contains(s, "taker exit"), strings.Contains(s, "TAKER EXIT"):
		return "sl"
	case strings.Contains(s, "jangan gotong"), strings.Contains(s, "skip"):
		return "skip"
	case strings.Contains(s, "cycle done"):
		if strings.Contains(s, "net=-") {
			return "loss"
		}
		return "profit"
	}
	return "info"
}

func tail(path string, n int) []string {
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
		if len(lines) > 2000 {
			lines = lines[1:]
		}
	}
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

func apiState(w http.ResponseWriter, r *http.Request) {
	var st state
	if raw, err := os.ReadFile(dir + "/state-dip/stats.json"); err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	for _, l := range tail(dir+"/logs/dip.log", 60) {
		m := lineRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		k := kindOf(m[2])
		if k == "info" {
			continue
		}
		sym := ""
		for _, tok := range strings.Fields(m[2]) {
			if strings.Contains(tok, "-USDT") {
				sym = tok
				break
			}
		}
		st.Events = append(st.Events, ev{Ts: m[1], Kind: k, Sym: sym, Txt: m[2]})
	}
	// newest first, max 25
	for i, j := 0, len(st.Events)-1; i < j; i, j = i+1, j-1 {
		st.Events[i], st.Events[j] = st.Events[j], st.Events[i]
	}
	if len(st.Events) > 25 {
		st.Events = st.Events[:25]
	}
	for _, l := range tail(dir+"/state-dip/trades.csv", 6) {
		if strings.HasPrefix(l, "ts_close") {
			continue
		}
		st.Trades = append(st.Trades, l)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", apiState)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, dir+"/cmd/office/index.html")
	})
	fmt.Println("Kantor 3D tim bot: http://127.0.0.1:8787  (Ctrl+C untuk keluar)")
	srv := &http.Server{Addr: "127.0.0.1:8787", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Println("server:", err)
		os.Exit(1)
	}
	_ = strconv.Itoa // keep import if unused later
}
