package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsAreValidPaper(t *testing.T) {
	t.Setenv("MODE", "")
	t.Setenv("API_KEY_ID", "")
	t.Setenv("API_PRIVATE_KEY", "")
	t.Setenv("ACCOUNT_ID", "")
	os.Unsetenv("MODE")
	os.Unsetenv("API_KEY_ID")
	os.Unsetenv("API_PRIVATE_KEY")
	os.Unsetenv("ACCOUNT_ID")
	cfg, err := FromEnv(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
	if cfg.Mode != ModePaper || cfg.Symbol != "BTC-USDT" || cfg.Timeframe != "1m" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLiveRequiresCredentials(t *testing.T) {
	t.Setenv("MODE", "live")
	t.Setenv("API_KEY_ID", "")
	t.Setenv("API_PRIVATE_KEY", "")
	t.Setenv("ACCOUNT_ID", "")
	if _, err := FromEnv(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("live mode without credentials must fail")
	}
}

func TestDotenvLoading(t *testing.T) {
	t.Setenv("API_KEY_ID", "") // clear ambient env influence
	t.Setenv("API_PRIVATE_KEY", "")
	t.Setenv("ACCOUNT_ID", "")
	t.Setenv("MODE", "live")
	path := writeEnv(t, "# comment\nAPI_KEY_ID=ak_123\nAPI_PRIVATE_KEY='deadbeef'\nACCOUNT_ID=acct-9\nMAX_SLIPPAGE_BPS=75\n")
	cfg, err := FromEnv(path)
	if err != nil {
		t.Fatalf("dotenv config invalid: %v", err)
	}
	if cfg.APIKeyID != "ak_123" || cfg.APIPrivateKey != "deadbeef" || cfg.AccountID != "acct-9" {
		t.Errorf("dotenv values not loaded: %+v", cfg)
	}
	if cfg.MaxSlippageBps != 75 {
		t.Errorf("slippage from dotenv: %d", cfg.MaxSlippageBps)
	}
}

func TestProcessEnvWinsOverDotenv(t *testing.T) {
	t.Setenv("MODE", "paper")
	t.Setenv("SYMBOL", "ETH-USDT")
	path := writeEnv(t, "SYMBOL=DOGE-USDT\nMODE=live\n")
	cfg, err := FromEnv(path)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.Symbol != "ETH-USDT" || cfg.Mode != ModePaper {
		t.Errorf("process env must win: symbol=%s mode=%s", cfg.Symbol, cfg.Mode)
	}
}

func TestValidationRejectsBadSlippage(t *testing.T) {
	t.Setenv("MODE", "paper")
	t.Setenv("MAX_SLIPPAGE_BPS", "20000")
	if _, err := FromEnv(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("slippage 20000 must be rejected")
	}
}

func TestEndpointDefaultsPointAtLiveTestnetHost(t *testing.T) {
	os.Unsetenv("POLYESTER_API_URL")
	os.Unsetenv("POLYESTER_WS_URL")
	cfg, err := FromEnv(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
	if cfg.APIURL != DefaultAPIURL || cfg.WSURL != DefaultWSURL {
		t.Fatalf("endpoint defaults: got %q/%q want %q/%q", cfg.APIURL, cfg.WSURL, DefaultAPIURL, DefaultWSURL)
	}
	if DefaultAPIURL != "https://api.testnet.polyester.com" {
		t.Fatalf("retired host regression: %q", DefaultAPIURL)
	}
}

func TestEndpointOverridesFromEnv(t *testing.T) {
	t.Setenv("POLYESTER_API_URL", "https://api-devnet.polyester.ai")
	t.Setenv("POLYESTER_WS_URL", "wss://api-devnet.polyester.ai")
	cfg, err := FromEnv(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("override must be valid: %v", err)
	}
	if cfg.APIURL != "https://api-devnet.polyester.ai" || cfg.WSURL != "wss://api-devnet.polyester.ai" {
		t.Fatalf("override ignored: %q/%q", cfg.APIURL, cfg.WSURL)
	}
}

func TestEndpointRejectsBadScheme(t *testing.T) {
	t.Setenv("POLYESTER_API_URL", "ftp://api.testnet.polyester.com")
	if _, err := FromEnv(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected a scheme validation error")
	}
	t.Setenv("POLYESTER_API_URL", "https://api.testnet.polyester.com")
	t.Setenv("POLYESTER_WS_URL", "https://api.testnet.polyester.com")
	if _, err := FromEnv(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected a websocket scheme validation error")
	}
}
