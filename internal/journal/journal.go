// Package journal is a crash-safe append-only record of trading intents.
// It is the idempotency backbone: every client order/trigger ID is written
// BEFORE the first create attempt, so a restart can reconcile ambiguous
// outcomes without ever double-ordering.
package journal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry kinds used by the bot.
const (
	KindBuyIntent        = "buy_intent"
	KindBuyResult        = "buy_result"
	KindSellIntent       = "sell_intent"
	KindSellResult       = "sell_result"
	KindTriggersPlaced   = "triggers_placed"
	KindTriggerCancelled = "trigger_cancelled"
	KindFlat             = "flat"
	KindKillSwitch       = "kill_switch"
	KindReconcile        = "reconcile"
	KindHalt             = "halt"
)

type Entry struct {
	Ts              time.Time         `json:"ts"`
	Kind            string            `json:"kind"`
	ClientOrderID   string            `json:"client_order_id,omitempty"`
	ClientTriggerID string            `json:"client_trigger_id,omitempty"`
	Detail          map[string]string `json:"detail,omitempty"`
}

type Journal struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	writer *bufio.Writer
}

// Open creates (or appends to) <dir>/journal.jsonl with 0700 directory perms.
func Open(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("journal: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "journal.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("journal: open %s: %w", path, err)
	}
	return &Journal{path: path, file: f, writer: bufio.NewWriter(f)}, nil
}

// Path returns the journal file location.
func (j *Journal) Path() string { return j.path }

// Append writes one entry and fsyncs it before returning.
func (j *Journal) Append(e Entry) error {
	if e.Ts.IsZero() {
		e.Ts = time.Now().UTC()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("journal: marshal: %w", err)
	}
	b = append(b, '\n')
	if _, err := j.writer.Write(b); err != nil {
		return fmt.Errorf("journal: write: %w", err)
	}
	if err := j.writer.Flush(); err != nil {
		return fmt.Errorf("journal: flush: %w", err)
	}
	if err := j.file.Sync(); err != nil {
		return fmt.Errorf("journal: sync: %w", err)
	}
	return nil
}

// Close flushes and closes the underlying file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.writer.Flush(); err != nil {
		j.file.Close()
		return err
	}
	return j.file.Close()
}

// Load reads all parseable entries. Corrupt trailing lines (crash mid-write)
// are skipped; with fsync-before-return appends this should only happen for
// torn writes, which are safe to ignore during replay.
func Load(dir string) ([]Entry, error) {
	path := filepath.Join(dir, "journal.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("journal: open %s: %w", path, err)
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("journal: scan %s: %w", path, err)
	}
	return out, nil
}
