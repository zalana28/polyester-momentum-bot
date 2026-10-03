package journal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	j, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := j.Append(Entry{Kind: KindBuyIntent, ClientOrderID: "pmom-p-buy-1",
		Detail: map[string]string{"budget": "25"}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := j.Append(Entry{Kind: KindBuyResult, ClientOrderID: "pmom-p-buy-1"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if entries[0].Kind != KindBuyIntent || entries[0].ClientOrderID != "pmom-p-buy-1" {
		t.Errorf("entry 0 wrong: %+v", entries[0])
	}
	if entries[0].Detail["budget"] != "25" {
		t.Errorf("detail lost: %+v", entries[0])
	}
	if entries[1].Kind != KindBuyResult {
		t.Errorf("entry 1 wrong: %+v", entries[1])
	}
}

func TestLoadMissingDirIsEmpty(t *testing.T) {
	entries, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("load on missing dir must not error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want 0 entries, got %d", len(entries))
	}
}

func TestLoadToleratesTornWrite(t *testing.T) {
	dir := t.TempDir()
	j, _ := Open(dir)
	_ = j.Append(Entry{Kind: KindBuyIntent, ClientOrderID: "a"})
	_ = j.Close()
	// Simulate a torn trailing write.
	f, err := os.OpenFile(filepath.Join(dir, "journal.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_, _ = f.WriteString(`{"kind":"buy_re`)
	_ = f.Close()

	entries, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(entries) != 1 || entries[0].ClientOrderID != "a" {
		t.Fatalf("torn write must be skipped: %+v", entries)
	}
}
