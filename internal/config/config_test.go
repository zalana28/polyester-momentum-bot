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
