// Package journal appends every trade-relevant event to a JSONL file on
// disk so a paper (or live) run can be reviewed after the process exits —
// the in-memory engine state is a capped ring buffer and doesn't survive a
// restart.
package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Entry struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // BUY, SELL_TP, SELL_SL, SELL_TIMEOUT, SELL_AI, BUY_FAILED, SELL_FAILED, SCAN
	Symbol string    `json:"symbol"`
	Mint   string    `json:"mint"`
	Live   bool      `json:"live"`

	PnlSOL      float64 `json:"pnl_sol,omitempty"`       // net of fees — the real result
	GrossPnlSOL float64 `json:"gross_pnl_sol,omitempty"` // before fees
	FeeSol      float64 `json:"fee_sol,omitempty"`
	PnlPct      float64 `json:"pnl_pct,omitempty"`
	Sig         string  `json:"sig,omitempty"`
	Err         string  `json:"err,omitempty"`

	// SCAN-only fields
	DevBuySOL    float64 `json:"dev_buy_sol,omitempty"`
	MarketCapSol float64 `json:"mcap_sol,omitempty"`
	Passed       bool    `json:"passed,omitempty"`
	AI           bool    `json:"ai,omitempty"`
	Reason       string  `json:"reason,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

type Journal struct {
	mu   sync.Mutex
	file *os.File
}

// Open appends to ~/.t2b/logs/trades.jsonl, creating the directory/file if
// needed.
func Open() (*Journal, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".t2b", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "trades.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Journal{file: f}, nil
}

func (j *Journal) Write(e Entry) {
	if j == nil || j.file == nil {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.file.Write(append(b, '\n'))
}

func (j *Journal) Close() error {
	if j == nil || j.file == nil {
		return nil
	}
	return j.file.Close()
}
