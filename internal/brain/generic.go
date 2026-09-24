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

// GenericCLI drives any headless CLI agent that follows the
// `<bin> -p "<prompt>" --output-format json` convention and replies with a
// {"response": "..."} envelope — this is Gemini CLI's, Antigravity CLI's
// (`agy`), and t2bfm's (the local FoundationModels wrapper, see
// swift/t2bfm/) shared shape. None of the three report a dollar cost in
// that envelope, so Decision.CostUSD stays 0 — TotalCalls is the honest
// metric available here.
type GenericCLI struct {
	Bin     string // "gemini", "agy", or "t2bfm"
	Model   string // passed as --model if set; empty = CLI's own default
	Timeout time.Duration

	mu         sync.Mutex
	totalCalls int
}

func NewGeminiCLI() *GenericCLI {
	return &GenericCLI{Bin: "gemini", Timeout: 25 * time.Second}
}

func NewAntigravityCLI() *GenericCLI {
	return &GenericCLI{Bin: "agy", Timeout: 25 * time.Second}
}

// NewAppleFMCLI drives t2bfm (swift/t2bfm/) — Apple's on-device
// FoundationModels, wrapped to speak the same CLI convention. Free, fully
// local, macOS 26+/Apple Silicon/Apple Intelligence only. Build it with
// `swift build -c release` in swift/t2bfm/ and put the resulting binary on
// PATH as `t2bfm`.
func NewAppleFMCLI() *GenericCLI {
	return &GenericCLI{Bin: "t2bfm", Timeout: 25 * time.Second}
}

func (c *GenericCLI) TotalCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalCalls
}

type genericEnvelope struct {
	Response string `json:"response"`
}

func (c *GenericCLI) ask(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	args := []string{"-p", prompt, "--output-format", "json"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s -p failed: %w (%s)", c.Bin, err, strings.TrimSpace(errOut.String()))
	}

	c.mu.Lock()
	c.totalCalls++
	c.mu.Unlock()

	var env genericEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || env.Response == "" {
		// Unexpected envelope shape — fall back to raw stdout as the reply.
		return out.String(), nil
	}
	return env.Response, nil
}

func (c *GenericCLI) EntryDecision(ctx context.Context, symbol, mint string, devBuySol, mcapSol float64) (Decision, error) {
	raw, err := c.ask(ctx, entryPrompt(symbol, mint, devBuySol, mcapSol))
	if err != nil {
		return Decision{}, err
	}
	js := extractJSON(raw)
	if js == "" {
		return Decision{}, fmt.Errorf("no JSON in %s response: %s", c.Bin, truncate(strings.TrimSpace(raw), 200))
	}
	var d Decision
	if err := json.Unmarshal([]byte(js), &d); err != nil {
		return Decision{}, fmt.Errorf("bad JSON from %s: %w", c.Bin, err)
	}
	return d, nil
}

func (c *GenericCLI) ExitDecision(ctx context.Context, symbol string, entryPrice, currentPrice, pnlPct float64, heldSeconds int) (ExitDecision, error) {
	raw, err := c.ask(ctx, exitPrompt(symbol, entryPrice, currentPrice, pnlPct, heldSeconds))
	if err != nil {
		return ExitDecision{}, err
	}
	js := extractJSON(raw)
	if js == "" {
		return ExitDecision{}, fmt.Errorf("no JSON in %s response: %s", c.Bin, truncate(strings.TrimSpace(raw), 200))
	}
	var d ExitDecision
	if err := json.Unmarshal([]byte(js), &d); err != nil {
		return ExitDecision{}, fmt.Errorf("bad JSON from %s: %w", c.Bin, err)
	}
	return d, nil
}
