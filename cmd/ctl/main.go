// Command ctl sends one command to the running bot's control socket.
//
// The bot must be started with CONTROL_ENABLED=true; the socket is a unix
// socket (0600) inside the bot's state directory, so this only works for the
// user who runs the bot.
//
//	ctl status
//	ctl sell HYPE 3            # 3 HYPE at market, stop re-armed on the rest
//	ctl buy LTC 500            # 500 USDT budget; drops to TRADE_BUDGET_QUOTE
//	ctl flatten HYPE
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type request struct {
	Cmd    string `json:"cmd"`
	Symbol string `json:"symbol,omitempty"`
	Qty    string `json:"qty,omitempty"`
	Quote  string `json:"quote,omitempty"`
}

type response struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	Error     string `json:"error"`
	Positions []struct {
		Symbol string  `json:"symbol"`
		Qty    string  `json:"qty"`
		Entry  string  `json:"entry"`
		Mark   string  `json:"mark"`
		PnlBps float64 `json:"pnl_bps"`
		SL     string  `json:"sl"`
		Trail  string  `json:"trail"`
	} `json:"positions"`
}

func main() {
	sock := flag.String("sock", "", "control socket path (default: state/control.sock)")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
	}
	path := *sock
	if path == "" {
		path = os.Getenv("CONTROL_SOCKET")
	}
	if path == "" {
		path = "state/control.sock"
	}

	req := request{Cmd: strings.ToLower(args[0])}
	switch req.Cmd {
	case "status":
	case "flatten":
		if len(args) < 2 {
			usage()
		}
		req.Symbol = args[1]
	case "sell":
		if len(args) < 3 {
			usage()
		}
		req.Symbol, req.Qty = args[1], args[2]
	case "buy":
		if len(args) < 2 {
			usage()
		}
		req.Symbol = args[1]
		if len(args) > 2 {
			req.Quote = args[2]
		}
	default:
		usage()
	}

	resp, err := send(path, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctl: %v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "ditolak: %s\n", resp.Error)
		os.Exit(2)
	}
	fmt.Println(resp.Message)
	for _, p := range resp.Positions {
		fmt.Printf("  %-10s qty=%s entry=%s mark=%s pnl=%+.0fbps sl=%s",
			p.Symbol, p.Qty, p.Entry, p.Mark, p.PnlBps, p.SL)
		if p.Trail != "" && p.Trail != "0" {
			fmt.Printf(" trail=%s", p.Trail)
		}
		fmt.Println()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `pakai:
  ctl status
  ctl sell <SYMBOL> <qty>
  ctl buy <SYMBOL> [quote]
  ctl flatten <SYMBOL>

opsi:
  -sock <path>   socket kontrol (default state/control.sock)
`)
	os.Exit(1)
}

func send(path string, req request) (*response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		},
	}
	res, err := client.Post("http://unix/cmd", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("tidak bisa menghubungi bot di %s: %w", path, err)
	}
	defer res.Body.Close()
	var out response
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("respons tidak valid: %w", err)
	}
	return &out, nil
}
