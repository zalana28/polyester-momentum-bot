// Command bot is the Polyester testnet momentum trading bot.
//
// Modes:
//   - paper (default): simulated fills on live testnet market data, no API key
//   - live: real testnet orders via the Polyester Go SDK (API key required)
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"polyester-momentum-bot/internal/bot"
	"polyester-momentum-bot/internal/config"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	exe, _ := os.Executable()
	localEnv := filepath.Join(filepath.Dir(exe), ".env")
	cwdEnv, _ := filepath.Abs(".env")

	cfg, err := config.FromEnv(cwdEnv, localEnv)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds)
	if err := bot.Run(ctx, cfg, logger); err != nil {
		fmt.Fprintf(os.Stderr, "bot exited with error: %v\n", err)
		os.Exit(1)
	}
	log.Println("bye")
}
