package engine

import "github.com/aasumitro/iT2B/internal/pumpportal"

// Trader is how the Executor agent actually moves money. PaperTrader never
// touches the chain; LiveTrader fires real orders through PumpPortal's
// Lightning API. Equity/PnL bookkeeping in the engine is identical either
// way — it estimates fills from the feed price, same as paper mode, since
// confirming exact on-chain fill amounts would mean parsing the settled tx.
// ponytail: feed-price-estimated fills, upgrade to parsing the tx's actual
// swap amounts if slippage tracking needs to be exact.
type Trader interface {
	Buy(mint string, solAmount float64) (signature string, err error)
	SellAll(mint string) (signature string, err error)

	// FeeSol is what one successful Buy or SellAll call costs on top of the
	// trade amount itself — 0 for paper, the configured priority fee for
	// live. Without this, tracked equity/PnL silently ignores real fees
	// paid, making a live run look more profitable than the wallet actually is.
	FeeSol() float64
}

// PaperTrader is the default — no order ever leaves this process.
type PaperTrader struct{}

func (PaperTrader) Buy(mint string, solAmount float64) (string, error) { return "", nil }
func (PaperTrader) SellAll(mint string) (string, error)                { return "", nil }
func (PaperTrader) FeeSol() float64                                    { return 0 }

// LiveTrader places real orders. Only constructed when the caller has
// explicitly armed live mode (see main.go) — the engine itself does not
// gate this any further.
type LiveTrader struct {
	Client         *pumpportal.TradeClient
	SlippagePct    float64
	PriorityFeeSol float64
}

func (t LiveTrader) Buy(mint string, solAmount float64) (string, error) {
	return t.Client.Buy(mint, solAmount, t.SlippagePct, t.PriorityFeeSol)
}

func (t LiveTrader) SellAll(mint string) (string, error) {
	return t.Client.SellAll(mint, t.SlippagePct, t.PriorityFeeSol)
}

// FeeSol reports only the priority fee we configure and send — PumpPortal
// may take its own additional cut we don't have documented numbers for, so
// this is a floor on real fee cost, not necessarily the exact total.
func (t LiveTrader) FeeSol() float64 { return t.PriorityFeeSol }
