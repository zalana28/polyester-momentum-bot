// Command keycheck identifies which value from the API-key creation dialog is
// what — completely offline, no key ever leaves this machine.
//
// Background: Polyester API keys are an Ed25519 keypair. The dialog shows the
// private key (64 hex chars = 32-byte seed) and its public key — and both are
// 64 hex characters, so they are easy to mix up. The bot needs:
//
//	API_PRIVATE_KEY : the 64-hex Ed25519 seed (the secret one)
//	API_KEY_ID      : the key's ID — a DIFFERENT identifier, not the public key
//
// Usage: put the private key in .env first, then run with the other values
// from the dialog as candidates (quoted, space separated):
//
//	go run ./cmd/keycheck "<value1>" "<value2>"
//
// For every candidate it prints whether it is (a) the public key derived from
// your private key, (b) the private key itself (wrong box!), or (c) unrelated
// (that is what likely belongs in API_KEY_ID). It also prints the derived
// public key so you can eyeball-match it against the dialog.
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	candidates := os.Args[1:]
	if len(candidates) == 0 {
		fmt.Println("usage: go run ./cmd/keycheck \"<value1>\" \"<value2>\" ...")
		fmt.Println("pass the other values from the creation dialog as candidates")
		os.Exit(2)
	}

	privHex, err := readPrivateKeyFromEnv()
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}

	seed, err := hex.DecodeString(privHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		fmt.Println("error: API_PRIVATE_KEY in .env is not a valid 64-char hex seed")
		os.Exit(1)
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	pubHex := hex.EncodeToString(pub)

	fmt.Println("derived public key from your private key (safe to share):")
	fmt.Println("  " + pubHex)
	fmt.Println()

	for i, c := range candidates {
		label := fmt.Sprintf("candidate %d", i+1)
		if len(c) > 12 {
			label += fmt.Sprintf(" (…%s, %d chars)", c[len(c)-6:], len(c))
		} else {
			label += fmt.Sprintf(" (%q, %d chars)", c, len(c))
		}
		switch norm(c) {
		case pubHex:
			fmt.Printf("%s = the Ed25519 PUBLIC KEY derived from your private key.\n", label)
			fmt.Println("   -> NOT needed in .env. The server already knows it via your key ID.")
		case privHex:
			fmt.Printf("%s = the PRIVATE KEY itself (same value as API_PRIVATE_KEY).\n", label)
			fmt.Println("   -> Wrong box! Never put this anywhere except API_PRIVATE_KEY.")
		default:
			fmt.Printf("%s = unrelated identifier -> this is what API_KEY_ID likely is.\n", label)
			fmt.Println("   -> Confirm against the API key LIST in the app (the ID shown per key).")
		}
	}
}

// readPrivateKeyFromEnv reads API_PRIVATE_KEY from ./ .env, ./.env, or the
// process environment. The value is never printed.
func readPrivateKeyFromEnv() (string, error) {
	if v := strings.TrimSpace(os.Getenv("API_PRIVATE_KEY")); v != "" {
		return v, nil
	}
	for _, dir := range []string{".", ".."} {
		raw, err := os.ReadFile(filepath.Join(dir, ".env"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "API_PRIVATE_KEY=") {
				v := strings.TrimSpace(strings.TrimPrefix(line, "API_PRIVATE_KEY="))
				if v != "" {
					return v, nil
				}
			}
		}
	}
	return "", fmt.Errorf("API_PRIVATE_KEY not found: fill .env first or export API_PRIVATE_KEY")
}

// norm strips whitespace and 0x prefix, lowercases hex.
func norm(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	return strings.ToLower(s)
}
