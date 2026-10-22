// Command report prints an edge report from the bot's shadow signal log.
//
// It answers one question the live bot cannot: is the gate earning its keep?
// Every buy signal the bot sees is logged (approved or vetoed) with its
// outcome at +1/+3/+10 candles, so score bands can be compared directly.
//
//	go run ./cmd/report                 # horizon 3, state/ next to the binary
//	go run ./cmd/report -h 10 -state ~/polyester-bot-testnet2/state
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"polyester-momentum-bot/internal/report"
)

func main() {
	state := flag.String("state", "state", "directory holding signals.csv / signals-v2.csv / trades.csv")
	horizon := flag.Int("h", 3, "candle horizon to report (1, 3, 10)")
	top := flag.Int("top", 6, "worst/best symbols to list")
	flag.Parse()

	sigs, err := report.LoadSignals(
		filepath.Join(*state, "signals.csv"),
		filepath.Join(*state, "signals-v2.csv"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
		os.Exit(1)
	}
	trades, err := report.LoadTrades(filepath.Join(*state, "trades.csv"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
		os.Exit(1)
	}
	if len(sigs) == 0 {
		fmt.Printf("belum ada sinyal di %s (signals.csv / signals-v2.csv)\n", *state)
		return
	}

	fmt.Printf("SHADOW EDGE REPORT — horizon %d candle\n", *horizon)
	fmt.Printf("sumber: %s\n\n", *state)

	buckets := report.ByScore(sigs, *horizon)
	fmt.Println("SKOR → HASIL")
	for _, b := range buckets {
		fmt.Printf("  %2d-%3d  %s\n", b.Lo, b.Hi, report.FmtStats(b.Stats))
	}
	fmt.Printf("\nVERDICT: %s\n\n", report.Verdict(buckets))

	fmt.Println("PER SIMBOL (urut terburuk)")
	syms := report.BySymbol(sigs, *horizon)
	names := make([]string, 0, len(syms))
	for name := range syms {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return syms[names[i]].Mean < syms[names[j]].Mean })
	for i, name := range names {
		if i >= *top && i < len(names)-*top {
			continue
		}
		fmt.Printf("  %-10s %s\n", name, report.FmtStats(syms[name]))
	}

	fmt.Println("\nPER HARI (UTC)")
	days := report.ByDay(sigs, *horizon)
	dayNames := make([]string, 0, len(days))
	for d := range days {
		dayNames = append(dayNames, d)
	}
	sort.Strings(dayNames)
	for _, d := range dayNames {
		fmt.Printf("  %s  %s\n", d, report.FmtStats(days[d]))
	}

	if sum := report.SummariseTrades(trades); sum.N > 0 {
		fmt.Printf("\nTRADE NYATA: %d posisi, net %+.2f quote, win %d/%d\n", sum.N, sum.Net, sum.Wins, sum.N)
		reasons := make([]string, 0, len(sum.ByReason))
		for r := range sum.ByReason {
			reasons = append(reasons, r)
		}
		sort.Strings(reasons)
		for _, r := range reasons {
			fmt.Printf("  exit %-14s %d\n", r, sum.ByReason[r])
		}
	}
}
