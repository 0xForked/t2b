// Package brain calls out to the `claude` CLI in headless print mode
// (`claude -p`) to get buy/reject and hold/sell judgments, reusing whatever
// Claude Code auth/plan is already active — no separate Anthropic API key.
package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Decision is the FILTER-stage call: should the engine open a paper (or
// live) position in this brand-new mint. CostUSD is filled in by ask() from
// the CLI's own accounting, not part of what we ask Claude to output.
type Decision struct {
	Buy       bool    `json:"buy"`
	Reasoning string  `json:"reasoning"`
	CostUSD   float64 `json:"-"`
}

// ExitDecision is the periodic POSITION-stage call for an already-open
// position: hold or sell now. This is additive to the hard TP/SL/timeout
// rails in engine.go, never a replacement for them.
type ExitDecision struct {
	Sell      bool    `json:"sell"`
	Reasoning string  `json:"reasoning"`
	CostUSD   float64 `json:"-"`
}

type ClaudeCLI struct {
	Timeout time.Duration

	mu           sync.Mutex
	totalCostUSD float64
	totalCalls   int
}

func NewClaudeCLI() *ClaudeCLI {
	return &ClaudeCLI{Timeout: 25 * time.Second}
}

// TotalCostUSD is the running sum of every claude -p call this instance has
// made, per the CLI's own reported total_cost_usd.
func (c *ClaudeCLI) TotalCostUSD() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalCostUSD
}

func (c *ClaudeCLI) TotalCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalCalls
}

// envelope is claude -p --output-format json's wrapper around the model's
// actual text reply, plus the CLI's own cost accounting for that call.
type envelope struct {
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	IsError      bool    `json:"is_error"`
}

// defaultModel is deliberately Haiku, not the CLI's default — this is a
// binary buy/reject classification, not a task that needs Sonnet-level
// reasoning, and every call already pays a fixed ~$0.05 session/cache
// overhead regardless of model (see Timeout doc below), so a cheaper model
// is the only lever that actually reduces cost per call.
const defaultModel = "claude-haiku-4-5-20251001"

// ask returns the model's raw text reply and that single call's cost.
//
// Each invocation is a fresh `claude -p` process with no session reuse, so
// every single call pays full cache-creation cost for the CLI's own
// context (observed: ~11k tokens, ~$0.05, even for a trivial prompt) on
// top of whatever the prompt itself costs. That fixed overhead — not the
// prompt — is the dominant cost driver when calling this in a loop.
// ponytail: no session reuse across calls; `--resume <session_id>` would
// let later calls hit cache_read instead of cache_creation if this still
// isn't cheap enough after the Haiku swap.
func (c *ClaudeCLI) ask(ctx context.Context, prompt string) (string, float64, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--output-format", "json", "--model", defaultModel)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return "", 0, fmt.Errorf("claude -p failed: %w (%s)", err, strings.TrimSpace(errOut.String()))
	}

	var env envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || env.Result == "" {
		// Unexpected envelope shape — fall back to treating stdout as the
		// raw reply directly, no cost figure available for this call.
		return out.String(), 0, nil
	}

	c.mu.Lock()
	c.totalCostUSD += env.TotalCostUSD
	c.totalCalls++
	c.mu.Unlock()

	if env.IsError {
		return env.Result, env.TotalCostUSD, fmt.Errorf("claude -p returned an error result: %s", truncate(env.Result, 200))
	}
	return env.Result, env.TotalCostUSD, nil
}

// extractJSON pulls the first balanced {...} object out of raw CLI output,
// tolerating stray prose or markdown fences around it.
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start == -1 || end == -1 || end < start {
		return ""
	}
	return s[start : end+1]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (c *ClaudeCLI) EntryDecision(ctx context.Context, symbol, mint string, devBuySol, mcapSol float64) (Decision, error) {
	raw, cost, err := c.ask(ctx, entryPrompt(symbol, mint, devBuySol, mcapSol))
	if err != nil {
		return Decision{}, err
	}
	js := extractJSON(raw)
	if js == "" {
		return Decision{}, fmt.Errorf("no JSON in claude response: %s", truncate(strings.TrimSpace(raw), 200))
	}
	var d Decision
	if err := json.Unmarshal([]byte(js), &d); err != nil {
		return Decision{}, fmt.Errorf("bad JSON from claude: %w", err)
	}
	d.CostUSD = cost
	return d, nil
}

func (c *ClaudeCLI) ExitDecision(ctx context.Context, symbol string, entryPrice, currentPrice, pnlPct float64, heldSeconds int) (ExitDecision, error) {
	raw, cost, err := c.ask(ctx, exitPrompt(symbol, entryPrice, currentPrice, pnlPct, heldSeconds))
	if err != nil {
		return ExitDecision{}, err
	}
	js := extractJSON(raw)
	if js == "" {
		return ExitDecision{}, fmt.Errorf("no JSON in claude response: %s", truncate(strings.TrimSpace(raw), 200))
	}
	var d ExitDecision
	if err := json.Unmarshal([]byte(js), &d); err != nil {
		return ExitDecision{}, fmt.Errorf("bad JSON from claude: %w", err)
	}
	d.CostUSD = cost
	return d, nil
}
