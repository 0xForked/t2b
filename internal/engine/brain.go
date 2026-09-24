package engine

import (
	"context"

	"github.com/aasumitro/iT2B/internal/brain"
)

// Decision/ExitDecision are aliased here so the rest of the engine package
// doesn't need to import brain directly.
type Decision = brain.Decision
type ExitDecision = brain.ExitDecision

// Brain is how the Filter/Position agents get a second opinion beyond the
// hard thresholds. *brain.ClaudeCLI satisfies this; nil means "no AI, hard
// thresholds only" and the engine never calls it.
type Brain interface {
	EntryDecision(ctx context.Context, symbol, mint string, devBuySol, mcapSol float64) (Decision, error)
	ExitDecision(ctx context.Context, symbol string, entryPrice, currentPrice, pnlPct float64, heldSeconds int) (ExitDecision, error)
}
