package brain

import "fmt"

// entryPrompt/exitPrompt are shared across every provider (Claude, Gemini,
// Antigravity, ...) so a calibration tweak only needs to happen once.

func entryPrompt(symbol, mint string, devBuySol, mcapSol float64) string {
	devPct := 0.0
	if mcapSol > 0 {
		devPct = devBuySol / mcapSol * 100
	}
	return fmt.Sprintf(`You are a pump.fun memecoin entry filter running in PAPER TRADING mode — no real capital, small simulated size, purpose is to collect enough sample trades to measure whether this strategy profits at all. A near-100%% reject rate produces zero data and defeats the point. Only reject on a genuinely alarming signal; when it's ambiguous, buy.

A new token just appeared:
symbol: %s
mint: %s
dev's own initial buy: %.3f SOL (%.1f%% of the token's starting market cap)
market cap at creation: %.2f SOL

Calibration — this is normal on pump.fun, NOT a red flag by itself:
- dev buy under ~20%% of starting mcap
- generic/joke/meme symbol names (most legitimate launches look exactly like this)

Actually alarming (reject for these):
- dev buy over ~40%% of starting mcap (dev can dump most of the supply on early buyers)
- symbol/name containing an explicit scam tell (e.g. impersonating a real token, "airdrop", "claim", a contract-address-looking string)

Respond with ONLY this JSON object, nothing else, no markdown fences, no explanation outside the JSON:
{"buy": true, "reasoning": "one short sentence"}`, symbol, mint, devBuySol, devPct, mcapSol)
}

func exitPrompt(symbol string, entryPrice, currentPrice, pnlPct float64, heldSeconds int) string {
	return fmt.Sprintf(`You manage one open pump.fun position:

symbol: %s
entry price: %.10f SOL
current price: %.10f SOL
unrealized PnL: %+.2f%%
held for: %ds

A hard stop-loss (-20%%) and take-profit (+50%%) already protect this position automatically — you are being asked whether to exit EARLY, before either of those fires. This is a brand-new pump.fun mint; %ds is not enough time for real momentum to show up, and "price hasn't moved yet" is the default state for almost every position at this age, not a signal.

Only say sell if there is an ACTUAL reason to exit now rather than let the hard rails run: pnl is already meaningfully negative (worse than roughly -10%%) without yet hitting the stop-loss, or pnl is comfortably positive and you'd rather lock in a partial win than risk giving it back. Flat/near-zero pnl with a short hold time is NOT a reason to sell — hold. When in doubt, hold.

Respond with ONLY this JSON object, nothing else, no markdown fences:
{"sell": true, "reasoning": "one short sentence"}`, symbol, entryPrice, currentPrice, pnlPct*100, heldSeconds, heldSeconds)
}
