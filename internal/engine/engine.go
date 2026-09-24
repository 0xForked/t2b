// Package engine runs a trading pipeline over a live pump.fun feed:
// Listener -> Filter -> Risk -> Executor -> Position -> Logbook. Each stage
// maps to one "agent" shown on the dashboard. Filter/Position can optionally
// defer to a Brain (see brain.go) for the buy/reject and hold/sell calls;
// the hard threshold/TP/SL/timeout rails never go away, the Brain only adds
// an earlier, optional decision on top of them.
package engine

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/aasumitro/iT2B/internal/journal"
	"github.com/aasumitro/iT2B/internal/pumpportal"
)

// pump.fun tokens are minted with a fixed 1e9 supply; price per token in SOL
// is approximately marketcap / supply.
const tokenSupply = 1_000_000_000

// Params are the trading rules the Filter/Risk/Position agents enforce.
type Params struct {
	MinDevBuySOL    float64       // ignore creates with a suspiciously small dev buy
	MaxDevBuySOL    float64       // ignore creates that look already-pumped
	MaxEntryMcapSol float64       // don't chase tokens already past this mcap
	PositionSizeSOL float64       // paper size per trade
	MaxPositions    int           // concurrent open positions cap
	TakeProfitPct   float64       // e.g. 0.5 = +50%
	StopLossPct     float64       // e.g. 0.2 = -20%
	MaxHold         time.Duration // force-exit timeout
	AIReviewEvery   time.Duration // how often an open position gets an AI hold/sell review
	AIMinHold       time.Duration // don't bother asking the AI to review a position this fresh

	// AIMaxCallsPerMinute hard-caps total spend regardless of feed volume —
	// on a busy firehose the call count, not prompt size, is what runs the
	// bill up. 0 = unlimited. A candidate that gets skipped for budget is
	// logged as a rejection with that reason, never silently dropped.
	AIMaxCallsPerMinute int

	// Auto-follow: a token creator whose past launches (that we traded)
	// clear both thresholds gets auto-added to the follow set — their next
	// launches skip the hard filters entirely, same trust level as a
	// manually followed wallet.
	AutoFollowMinWins    int     // minimum closed wins on that dev's tokens before considering promotion
	AutoFollowMinWinRate float64 // wins / (wins+losses) required at that point

	// Momentum confirmation: a candidate that clears every filter still
	// doesn't get bought immediately — it's watched for real secondary
	// trading interest first (a buy from someone other than the token's
	// own creator). Most pump.fun mints get zero trades beyond the dev's
	// own initial buy, ever; buying those blind means holding a dead
	// position until MaxHold forces a flat exit. 0 disables this gate
	// (old behavior: buy immediately on passing filters).
	MomentumConfirmTrades int           // qualifying secondary buys required before actually buying
	MomentumWindow        time.Duration // how long to wait for that confirmation before dropping the candidate
}

func DefaultParams() Params {
	return Params{
		MinDevBuySOL:          0.5,
		MaxDevBuySOL:          5,
		MaxEntryMcapSol:       50,
		PositionSizeSOL:       0.1,
		MaxPositions:          5,
		TakeProfitPct:         0.5,
		StopLossPct:           0.2,
		MaxHold:               3 * time.Minute,
		AIReviewEvery:         20 * time.Second,
		AIMinHold:             15 * time.Second,
		AIMaxCallsPerMinute:   10,
		AutoFollowMinWins:     3,
		AutoFollowMinWinRate:  0.66,
		MomentumConfirmTrades: 1,
		MomentumWindow:        20 * time.Second,
	}
}

type AgentName string

const (
	AgentListener AgentName = "LISTENER"
	AgentFilter   AgentName = "FILTER"
	AgentRisk     AgentName = "RISK"
	AgentExecutor AgentName = "EXECUTOR"
	AgentPosition AgentName = "POSITION"
	AgentLogbook  AgentName = "LOGBOOK"
)

type Position struct {
	Mint       string
	Symbol     string
	DevPubkey  string // token creator, empty for a copy-trade-sourced position — we don't know their dev
	EntryPrice float64
	EntrySol   float64
	Tokens     float64
	OpenedAt   time.Time
	LastPrice  float64
	LastPnlPct float64
	BuySig     string  // tx signature, only set in live mode
	BuyFeeSol  float64 // paid at open — 0 in paper mode

	SellAttempts    int // consecutive failed sell attempts — capped and throttled, see closePosition
	LastSellAttempt time.Time
}

// sellRetryInterval/maxSellAttempts stop a position whose sell keeps
// failing (e.g. PumpPortal can't find the token account yet) from
// re-triggering an API call on every 2s timeout sweep or trade tick
// forever. After maxSellAttempts the engine gives up automatically —
// the position stays open (we don't know if we still hold it) but stops
// hammering the API.
const (
	sellRetryInterval = 15 * time.Second
	maxSellAttempts   = 5
)

type LogEntry struct {
	At          time.Time
	Kind        string // BUY, SELL_TP, SELL_SL, SELL_TIMEOUT, SELL_AI, BUY_FAILED, SELL_FAILED
	Symbol      string
	Mint        string
	PnlSOL      float64 // net of both buy and sell fees — the real result
	GrossPnlSOL float64 // before fees, on SELL_* kinds only — lets you see fees eating into a nominal win
	FeeSol      float64 // total fee paid on this trade (buy+sell combined on the closing entry)
	PnlPct      float64
	Sig         string // tx signature, only set in live mode
	Err         string // set on *_FAILED entries
	Reason      string // AI reasoning, set on SELL_AI
}

// ScanEntry is every token the Listener sees, whether or not the Filter/Risk
// stages act on it — this is what makes the live feed visible even when
// nothing is tradeable yet.
type ScanEntry struct {
	At           time.Time
	Symbol       string
	Mint         string
	DevBuySOL    float64
	MarketCapSol float64
	Passed       bool
	AI           bool    // true if an AI call (not just hard thresholds) produced this row
	Follow       bool    // true if this came from a followed wallet's buy, not the new-mint scanner
	Reason       string  // why rejected/approved
	CostUSD      float64 // this call's cost, if AI and the Brain reports one
}

// Snapshot is an immutable view of engine state for rendering.
type Snapshot struct {
	Equity       float64 // free cash only — does NOT include the value of open positions
	TotalEquity  float64 // Equity + mark-to-market value of every open position, at last known price
	StartEquity  float64
	Trades       int
	Wins         int
	Losses       int
	AIPending    int     // entry/exit decisions currently in flight
	AICostUSD    float64 // running total of every AI call's cost this run
	FollowCount  int     // manually + auto-followed wallets, live count
	GrossPnlSol  float64 // realized PnL before fees, across all closed trades
	TotalFeesSol float64 // every fee paid so far (0 in paper mode) — GrossPnlSol - TotalFeesSol = net realized PnL
	OpenPos      []Position
	Recent       []LogEntry  // newest first, capped
	Scans        []ScanEntry // newest first, capped
	AgentStatus  map[AgentName]string
	AgentUpdated map[AgentName]time.Time
}

// decisionMsg carries an async Brain result back into the single-threaded
// event loop.
type decisionMsg struct {
	kind     string // "entry" | "exit"
	mint     string
	ev       pumpportal.TokenEvent // entry only
	entryDec Decision
	exitDec  ExitDecision
	err      error
}

// devStat tracks how a token creator's past launches (that we actually
// traded) have played out, for auto-follow promotion.
type devStat struct {
	wins, losses int
}

// watchCandidate is a token that cleared every filter but hasn't been
// bought yet — waiting for proof someone other than the dev is actually
// trading it.
type watchCandidate struct {
	ev            pumpportal.TokenEvent
	deadline      time.Time
	qualifying    int
	anyTradesSeen int // every trade event for this mint, qualifying or not — diagnostic for "is the subscription even delivering data"
	devTradesSeen int // trades from the dev's own pubkey specifically — distinguishes "dead token" from "only the dev is active"
	ai, follow    bool
	reason        string // AI reasoning or follow note, carried through to the eventual scan log entry
}

type Engine struct {
	params       Params
	client       *pumpportal.Client
	trader       Trader
	live         bool
	brain        Brain
	journal      *journal.Journal
	onAutoFollow func(wallet string) // fired outside the lock when a dev crosses the promotion threshold
	notify       func(Snapshot)

	mu           sync.Mutex
	equity       float64
	startEquity  float64
	totalAICost  float64
	totalFeesSol float64
	grossPnlSol  float64 // realized PnL before fees — lets Snapshot show fees eating into gains separately
	positions    map[string]*Position
	recent       []LogEntry
	scans        []ScanEntry
	wins         int
	losses       int
	trades       int
	status       map[AgentName]string
	updated      map[AgentName]time.Time
	pendingEntry map[string]bool
	pendingExit  map[string]bool
	followSet    map[string]bool // manually + auto-followed wallets — mutates at runtime, so guarded by mu
	devStats     map[string]*devStat
	watching     map[string]*watchCandidate // mint -> candidate waiting on momentum confirmation

	aiWindowStart time.Time // fixed-window budget: reset every minute
	aiWindowCalls int

	decisions chan decisionMsg
}

// Deps groups the engine's external wiring — everything here is optional
// except Client:
//   - Trader: nil means PaperTrader (simulated fills, the default)
//   - Brain: nil means pure hard-threshold behavior, zero AI calls
//   - Journal: nil means no on-disk trade log, in-memory only
//   - FollowWallets: empty means no copy-trading to start (auto-follow can still add to it)
//   - OnAutoFollow: called when a dev gets auto-promoted, so the caller can persist it (e.g. to config)
type Deps struct {
	Client        *pumpportal.Client
	Trader        Trader
	Live          bool
	Brain         Brain
	Journal       *journal.Journal
	FollowWallets []string
	OnAutoFollow  func(wallet string)
}

// New wires up the engine. live only affects status/labeling — pass it
// true alongside a LiveTrader in deps.
func New(params Params, deps Deps, startEquity float64, notify func(Snapshot)) *Engine {
	trader := deps.Trader
	if trader == nil {
		trader = PaperTrader{}
	}
	followSet := make(map[string]bool, len(deps.FollowWallets))
	for _, addr := range deps.FollowWallets {
		followSet[addr] = true
	}
	e := &Engine{
		params:       params,
		client:       deps.Client,
		trader:       trader,
		live:         deps.Live,
		brain:        deps.Brain,
		journal:      deps.Journal,
		onAutoFollow: deps.OnAutoFollow,
		notify:       notify,
		equity:       startEquity,
		startEquity:  startEquity,
		positions:    make(map[string]*Position),
		status: map[AgentName]string{
			AgentListener: "READY",
			AgentFilter:   "READY",
			AgentRisk:     "READY",
			AgentExecutor: "READY",
			AgentPosition: "READY",
			AgentLogbook:  "READY",
		},
		updated:      make(map[AgentName]time.Time),
		pendingEntry: make(map[string]bool),
		pendingExit:  make(map[string]bool),
		followSet:    followSet,
		devStats:     make(map[string]*devStat),
		watching:     make(map[string]*watchCandidate),
		decisions:    make(chan decisionMsg, 32),
	}
	return e
}

func (e *Engine) setStatus(a AgentName, s string) {
	e.mu.Lock()
	e.status[a] = s
	e.updated[a] = time.Now()
	e.mu.Unlock()
}

// allowAICall enforces AIMaxCallsPerMinute with a fixed one-minute window —
// good enough to put a hard ceiling on spend, doesn't need sliding-window
// precision. Call once per AI dispatch (entry or exit), before spawning the
// goroutine.
func (e *Engine) allowAICall() bool {
	if e.params.AIMaxCallsPerMinute <= 0 {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Since(e.aiWindowStart) >= time.Minute {
		e.aiWindowStart = time.Now()
		e.aiWindowCalls = 0
	}
	if e.aiWindowCalls >= e.params.AIMaxCallsPerMinute {
		return false
	}
	e.aiWindowCalls++
	return true
}

// Run consumes the pumpportal feed until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	sweepTicker := time.NewTicker(2 * time.Second) // MaxHold timeout sweep
	defer sweepTicker.Stop()

	var aiTickerC <-chan time.Time
	if e.brain != nil {
		aiTicker := time.NewTicker(e.params.AIReviewEvery)
		defer aiTicker.Stop()
		aiTickerC = aiTicker.C
	}

	e.emit() // fire an initial snapshot so the dashboard shows correct starting state, not blank, before the first feed event

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-e.client.NewTokens:
			e.setStatus(AgentListener, "LISTENING")
			e.onNewToken(ctx, ev)
		case tr := <-e.client.Trades:
			e.onTrade(tr)
		case <-sweepTicker.C:
			e.sweepTimeouts()
			e.sweepMomentumTimeouts()
		case <-aiTickerC:
			e.sweepAIExitReview(ctx)
		case res := <-e.decisions:
			e.handleDecision(res)
		}
		e.emit()
	}
}

func (e *Engine) onNewToken(ctx context.Context, ev pumpportal.TokenEvent) {
	e.setStatus(AgentFilter, "SCREENING")

	e.mu.Lock()
	trustedDev := e.followSet[ev.TraderPubkey]
	e.mu.Unlock()
	if trustedDev {
		e.setStatus(AgentFilter, "PASSED")
		e.startMomentumWatch(ev, false, true, "followed dev "+shortAddr(ev.TraderPubkey))
		return
	}

	scan := ScanEntry{At: time.Now(), Symbol: ev.Symbol, Mint: ev.Mint, DevBuySOL: ev.SolAmount, MarketCapSol: ev.MarketCapSol}

	if ev.SolAmount < e.params.MinDevBuySOL || ev.SolAmount > e.params.MaxDevBuySOL {
		e.setStatus(AgentFilter, "REJECTED")
		scan.Reason = "dev buy out of range"
		e.logScan(scan)
		return
	}
	if ev.MarketCapSol > e.params.MaxEntryMcapSol {
		e.setStatus(AgentFilter, "REJECTED")
		scan.Reason = "mcap too high"
		e.logScan(scan)
		return
	}

	e.mu.Lock()
	openCount := len(e.positions)
	canAfford := e.equity >= e.params.PositionSizeSOL
	_, already := e.positions[ev.Mint]
	e.mu.Unlock()

	e.setStatus(AgentFilter, "PASSED")
	if already || openCount >= e.params.MaxPositions || !canAfford {
		e.setStatus(AgentRisk, "SKIPPED")
		scan.Reason = "risk skip: position cap/funds"
		e.logScan(scan)
		return
	}
	e.setStatus(AgentRisk, "READY")

	if e.brain == nil {
		e.startMomentumWatch(ev, false, false, "")
		return
	}

	if !e.allowAICall() {
		e.setStatus(AgentFilter, "AI_BUDGET")
		scan.Reason = "AI call budget exhausted this minute"
		e.logScan(scan)
		return
	}

	e.mu.Lock()
	if e.pendingEntry[ev.Mint] {
		e.mu.Unlock()
		return
	}
	e.pendingEntry[ev.Mint] = true
	e.mu.Unlock()

	e.setStatus(AgentFilter, "THINKING")
	go e.requestEntryDecision(ctx, ev)
}

// requestEntryDecision runs off the event loop goroutine — an AI subprocess
// call takes seconds, and blocking the loop for that long would stall the
// whole feed.
func (e *Engine) requestEntryDecision(ctx context.Context, ev pumpportal.TokenEvent) {
	dec, err := e.brain.EntryDecision(ctx, ev.Symbol, ev.Mint, ev.SolAmount, ev.MarketCapSol)
	select {
	case e.decisions <- decisionMsg{kind: "entry", mint: ev.Mint, ev: ev, entryDec: dec, err: err}:
	case <-ctx.Done():
	}
}

func (e *Engine) handleDecision(res decisionMsg) {
	switch res.kind {
	case "entry":
		e.mu.Lock()
		delete(e.pendingEntry, res.mint)
		e.mu.Unlock()

		scan := ScanEntry{At: time.Now(), Symbol: res.ev.Symbol, Mint: res.mint, DevBuySOL: res.ev.SolAmount, MarketCapSol: res.ev.MarketCapSol, AI: true, CostUSD: res.entryDec.CostUSD}
		if res.entryDec.CostUSD > 0 {
			e.mu.Lock()
			e.totalAICost += res.entryDec.CostUSD
			e.mu.Unlock()
		}
		if res.err != nil {
			e.setStatus(AgentFilter, "AI_ERROR")
			scan.Reason = "AI error: " + res.err.Error()
			e.logScan(scan)
			return
		}
		scan.Reason = res.entryDec.Reasoning
		if !res.entryDec.Buy {
			e.setStatus(AgentFilter, "AI_REJECT")
			e.logScan(scan)
			return
		}
		e.setStatus(AgentFilter, "AI_PASS")
		e.startMomentumWatch(res.ev, true, false, res.entryDec.Reasoning)

	case "exit":
		if res.exitDec.CostUSD > 0 {
			e.mu.Lock()
			e.totalAICost += res.exitDec.CostUSD
			e.mu.Unlock()
		}
		e.mu.Lock()
		delete(e.pendingExit, res.mint)
		_, stillOpen := e.positions[res.mint]
		e.mu.Unlock()
		if !stillOpen || res.err != nil {
			return // hard rails may have already closed it, or the call failed — either way, nothing to do
		}
		if res.exitDec.Sell {
			e.closePosition(res.mint, "SELL_AI", res.exitDec.Reasoning)
		}
	}
}

// tryOpenPosition re-validates risk capacity (state may have moved since
// onNewToken's cheap pre-check, especially across an AI call's latency)
// then places the buy.
func (e *Engine) tryOpenPosition(ev pumpportal.TokenEvent) {
	buyFee := e.trader.FeeSol()
	e.mu.Lock()
	openCount := len(e.positions)
	canAfford := e.equity >= e.params.PositionSizeSOL+buyFee
	_, already := e.positions[ev.Mint]
	e.mu.Unlock()

	e.setStatus(AgentRisk, "SIZING")
	if already || openCount >= e.params.MaxPositions || !canAfford {
		e.setStatus(AgentRisk, "SKIPPED")
		return
	}
	e.setStatus(AgentRisk, "APPROVED")

	price := ev.MarketCapSol / tokenSupply
	if price <= 0 {
		return
	}
	tokens := e.params.PositionSizeSOL / price

	e.setStatus(AgentExecutor, "BUYING")
	sig, err := e.trader.Buy(ev.Mint, e.params.PositionSizeSOL)
	if err != nil {
		e.setStatus(AgentExecutor, "ERROR")
		e.mu.Lock()
		e.log(LogEntry{At: time.Now(), Kind: "BUY_FAILED", Symbol: ev.Symbol, Mint: ev.Mint, Err: err.Error()})
		e.mu.Unlock()
		return
	}

	e.mu.Lock()
	e.equity -= e.params.PositionSizeSOL + buyFee
	e.totalFeesSol += buyFee
	e.positions[ev.Mint] = &Position{
		Mint:       ev.Mint,
		Symbol:     ev.Symbol,
		DevPubkey:  ev.TraderPubkey,
		EntryPrice: price,
		EntrySol:   e.params.PositionSizeSOL,
		Tokens:     tokens,
		OpenedAt:   time.Now(),
		LastPrice:  price,
		BuySig:     sig,
		BuyFeeSol:  buyFee,
	}
	e.log(LogEntry{At: time.Now(), Kind: "BUY", Symbol: ev.Symbol, Mint: ev.Mint, Sig: sig})
	e.mu.Unlock()
	e.setStatus(AgentExecutor, "FILLED")
	e.setStatus(AgentPosition, "TRACKING")
	e.client.WatchTrades(ev.Mint)
}

func (e *Engine) onTrade(tr pumpportal.TradeEvent) {
	e.mu.Lock()
	pos, held := e.positions[tr.Mint]
	var pnlPct float64
	if held {
		price := tr.MarketCapSol / tokenSupply
		if price > 0 {
			pos.LastPrice = price
			pos.LastPnlPct = (price - pos.EntryPrice) / pos.EntryPrice
		}
		pnlPct = pos.LastPnlPct
	}
	e.mu.Unlock()

	if held {
		e.setStatus(AgentPosition, "STRIKING")
		if pnlPct >= e.params.TakeProfitPct {
			e.closePosition(tr.Mint, "SELL_TP", "")
		} else if pnlPct <= -e.params.StopLossPct {
			e.closePosition(tr.Mint, "SELL_SL", "")
		}
	}

	if !held {
		e.mu.Lock()
		cand, isWatching := e.watching[tr.Mint]
		confirmed := false
		if isWatching {
			// count every trade seen regardless of type/trader — this is
			// the diagnostic for whether the subscription is delivering
			// data at all, distinct from whether it qualifies as momentum.
			cand.anyTradesSeen++
			if tr.TraderPubkey == cand.ev.TraderPubkey {
				cand.devTradesSeen++
			}
			if tr.TxType == "buy" && tr.TraderPubkey != cand.ev.TraderPubkey {
				cand.qualifying++
				if cand.qualifying >= e.params.MomentumConfirmTrades {
					confirmed = true
					delete(e.watching, tr.Mint)
				}
			}
		}
		e.mu.Unlock()
		if confirmed {
			reason := cand.reason
			if reason == "" {
				reason = "momentum confirmed"
			}
			e.logScan(ScanEntry{
				At: time.Now(), Symbol: cand.ev.Symbol, Mint: cand.ev.Mint, DevBuySOL: cand.ev.SolAmount, MarketCapSol: cand.ev.MarketCapSol,
				Passed: true, AI: cand.ai, Follow: cand.follow, Reason: reason,
			})
			e.setStatus(AgentRisk, "CONFIRMED")
			e.tryOpenPosition(cand.ev)
		}
	}

	if tr.TxType == "buy" {
		e.mu.Lock()
		followed := e.followSet[tr.TraderPubkey]
		e.mu.Unlock()
		if followed {
			e.onFollowedBuy(tr)
		}
	}
}

// startMomentumWatch registers a filter-passing candidate to wait for proof
// someone other than the token's own creator is actually trading it, rather
// than buying blind. ai/follow/reason are carried through so the eventual
// scan log entry (on confirmation or timeout) keeps the same context a
// direct buy would have logged immediately.
func (e *Engine) startMomentumWatch(ev pumpportal.TokenEvent, ai, follow bool, reason string) {
	if e.params.MomentumConfirmTrades <= 0 {
		// gate disabled — old behavior, buy immediately
		e.logScan(ScanEntry{At: time.Now(), Symbol: ev.Symbol, Mint: ev.Mint, DevBuySOL: ev.SolAmount, MarketCapSol: ev.MarketCapSol, Passed: true, AI: ai, Follow: follow, Reason: reason})
		e.tryOpenPosition(ev)
		return
	}
	e.setStatus(AgentRisk, "WATCHING")
	e.mu.Lock()
	e.watching[ev.Mint] = &watchCandidate{
		ev: ev, deadline: time.Now().Add(e.params.MomentumWindow), ai: ai, follow: follow, reason: reason,
	}
	e.mu.Unlock()
	e.client.WatchTrades(ev.Mint)
}

// sweepMomentumTimeouts drops any candidate that never got its momentum
// confirmed within the window — logged as a rejection, not silently lost.
func (e *Engine) sweepMomentumTimeouts() {
	e.mu.Lock()
	var expired []*watchCandidate
	now := time.Now()
	for mint, c := range e.watching {
		if now.After(c.deadline) {
			expired = append(expired, c)
			delete(e.watching, mint)
		}
	}
	e.mu.Unlock()
	for _, c := range expired {
		e.client.UnwatchTrades(c.ev.Mint)
		reason := "no momentum confirmed within " + e.params.MomentumWindow.String()
		switch {
		case c.anyTradesSeen == 0:
			// diagnostic: zero trade events arrived for this mint at all —
			// either genuinely dead, or the subscription isn't delivering.
			reason += " (0 trades seen)"
		case c.qualifying == 0:
			reason += " (" + strconv.Itoa(c.anyTradesSeen) + " trades seen, " + strconv.Itoa(c.devTradesSeen) + " from dev, 0 qualifying)"
		}
		e.logScan(ScanEntry{
			At: time.Now(), Symbol: c.ev.Symbol, Mint: c.ev.Mint, DevBuySOL: c.ev.SolAmount, MarketCapSol: c.ev.MarketCapSol,
			Passed: false, AI: c.ai, Follow: c.follow, Reason: reason,
		})
	}
}

// onFollowedBuy copy-trades a followed wallet's buy. Unlike onNewToken, it
// skips the dev-buy/mcap hard filters entirely — the whole point of
// following a wallet is trusting its judgment over the generic scanner
// rules. Risk capacity (position cap, funds) still applies, and the normal
// TP/SL/timeout/AI exit rails manage the resulting position exactly like
// any other.
func (e *Engine) onFollowedBuy(tr pumpportal.TradeEvent) {
	e.mu.Lock()
	_, already := e.positions[tr.Mint]
	e.mu.Unlock()
	if already {
		return
	}

	e.logScan(ScanEntry{
		At: time.Now(), Symbol: tr.Symbol, Mint: tr.Mint,
		DevBuySOL: tr.SolAmount, MarketCapSol: tr.MarketCapSol,
		Follow: true, Passed: true, Reason: "copied " + shortAddr(tr.TraderPubkey),
	})

	e.tryOpenPosition(pumpportal.TokenEvent{
		Mint: tr.Mint, Symbol: tr.Symbol, SolAmount: tr.SolAmount, MarketCapSol: tr.MarketCapSol,
	})
}

func shortAddr(addr string) string {
	if len(addr) <= 8 {
		return addr
	}
	return addr[:4] + "…" + addr[len(addr)-4:]
}

func (e *Engine) sweepTimeouts() {
	e.mu.Lock()
	var expired []string
	for mint, pos := range e.positions {
		if time.Since(pos.OpenedAt) > e.params.MaxHold {
			expired = append(expired, mint)
		}
	}
	e.mu.Unlock()
	for _, mint := range expired {
		e.closePosition(mint, "SELL_TIMEOUT", "")
	}
}

// sweepAIExitReview asks the Brain for a hold/sell opinion on any open
// position that's cleared AIMinHold and isn't already awaiting a review.
// This is purely additive to the hard TP/SL/timeout rails above, which keep
// firing on their own regardless of AI availability or latency.
func (e *Engine) sweepAIExitReview(ctx context.Context) {
	e.mu.Lock()
	var candidates []Position
	for mint, pos := range e.positions {
		if e.pendingExit[mint] || time.Since(pos.OpenedAt) < e.params.AIMinHold {
			continue
		}
		e.pendingExit[mint] = true
		candidates = append(candidates, *pos)
	}
	e.mu.Unlock()

	for _, pos := range candidates {
		if !e.allowAICall() {
			// Budget exhausted for this window — leave pendingExit unset so
			// it's picked up again next sweep; hard TP/SL/timeout still
			// protect the position in the meantime regardless.
			e.mu.Lock()
			delete(e.pendingExit, pos.Mint)
			e.mu.Unlock()
			continue
		}
		go e.requestExitDecision(ctx, pos)
	}
}

func (e *Engine) requestExitDecision(ctx context.Context, pos Position) {
	held := int(time.Since(pos.OpenedAt).Seconds())
	dec, err := e.brain.ExitDecision(ctx, pos.Symbol, pos.EntryPrice, pos.LastPrice, pos.LastPnlPct, held)
	select {
	case e.decisions <- decisionMsg{kind: "exit", mint: pos.Mint, exitDec: dec, err: err}:
	case <-ctx.Done():
	}
}

// closePosition blocks the engine's event loop for the duration of the sell
// call in live mode. Acceptable for now — trades are infrequent relative to
// the feed, and running it synchronously avoids a whole class of concurrent
// double-sell bugs on the same position.
// ponytail: single-threaded event loop stalls on each live order; move to a
// per-mint worker goroutine if trade volume ever makes that latency matter.
func (e *Engine) closePosition(mint, kind, reason string) {
	e.mu.Lock()
	pos, ok := e.positions[mint]
	if !ok {
		e.mu.Unlock()
		return
	}
	if pos.SellAttempts >= maxSellAttempts {
		e.mu.Unlock()
		return // gave up already — stay quiet, don't keep hammering the API
	}
	if pos.SellAttempts > 0 && time.Since(pos.LastSellAttempt) < sellRetryInterval {
		e.mu.Unlock()
		return // too soon since the last attempt
	}
	symbol := pos.Symbol
	pos.SellAttempts++
	pos.LastSellAttempt = time.Now()
	attempt := pos.SellAttempts
	e.mu.Unlock()

	e.setStatus(AgentPosition, "SELLING")
	sig, err := e.trader.SellAll(mint)
	if err != nil {
		gaveUp := attempt >= maxSellAttempts
		e.mu.Lock()
		if gaveUp {
			e.log(LogEntry{At: time.Now(), Kind: "SELL_STUCK", Symbol: symbol, Mint: mint, Err: "gave up after " + strconv.Itoa(attempt) + " attempts: " + err.Error()})
		} else {
			e.log(LogEntry{At: time.Now(), Kind: "SELL_FAILED", Symbol: symbol, Mint: mint, Err: err.Error()})
		}
		e.mu.Unlock()
		if gaveUp {
			e.setStatus(AgentPosition, "STUCK")
		} else {
			e.setStatus(AgentPosition, "ERROR")
		}
		return // leave the position open; next trigger retries until the cap above
	}

	sellFee := e.trader.FeeSol()

	e.mu.Lock()
	pos, ok = e.positions[mint]
	if !ok {
		e.mu.Unlock()
		return
	}
	exitSol := pos.Tokens * pos.LastPrice
	grossPnl := exitSol - pos.EntrySol
	totalFee := pos.BuyFeeSol + sellFee
	pnlSol := grossPnl - totalFee // net of both legs' fees — the real result
	pnlPct := pos.LastPnlPct

	e.equity += exitSol - sellFee
	e.totalFeesSol += sellFee
	e.grossPnlSol += grossPnl
	e.trades++
	win := pnlSol >= 0
	if win {
		e.wins++
	} else {
		e.losses++
	}
	delete(e.positions, mint)
	e.log(LogEntry{At: time.Now(), Kind: kind, Symbol: pos.Symbol, Mint: mint, PnlSOL: pnlSol, GrossPnlSOL: grossPnl, FeeSol: totalFee, PnlPct: pnlPct, Sig: sig, Reason: reason})

	promoted := ""
	if pos.DevPubkey != "" && !e.followSet[pos.DevPubkey] {
		st := e.devStats[pos.DevPubkey]
		if st == nil {
			st = &devStat{}
			e.devStats[pos.DevPubkey] = st
		}
		if win {
			st.wins++
		} else {
			st.losses++
		}
		total := st.wins + st.losses
		if st.wins >= e.params.AutoFollowMinWins && float64(st.wins)/float64(total) >= e.params.AutoFollowMinWinRate {
			e.followSet[pos.DevPubkey] = true
			promoted = pos.DevPubkey
		}
	}
	e.mu.Unlock()

	if promoted != "" {
		e.logScan(ScanEntry{At: time.Now(), Mint: mint, Follow: true, Passed: true, Reason: "auto-followed: track record cleared threshold"})
		if e.onAutoFollow != nil {
			e.onAutoFollow(promoted)
		}
	}

	e.setStatus(AgentPosition, "READY")
	e.setStatus(AgentLogbook, "LOGGING")
	e.client.UnwatchTrades(mint)
}

const maxRecent = 12
const maxScans = 12

// log assumes the caller already holds e.mu.
func (e *Engine) log(entry LogEntry) {
	e.recent = append([]LogEntry{entry}, e.recent...)
	if len(e.recent) > maxRecent {
		e.recent = e.recent[:maxRecent]
	}
	e.journal.Write(journal.Entry{
		At: entry.At, Kind: entry.Kind, Symbol: entry.Symbol, Mint: entry.Mint, Live: e.live,
		PnlSOL: entry.PnlSOL, GrossPnlSOL: entry.GrossPnlSOL, FeeSol: entry.FeeSol, PnlPct: entry.PnlPct,
		Sig: entry.Sig, Err: entry.Err, Reason: entry.Reason,
	})
}

// logScan takes its own lock since, unlike log(), it's called from paths
// that aren't already holding e.mu.
func (e *Engine) logScan(entry ScanEntry) {
	e.mu.Lock()
	e.scans = append([]ScanEntry{entry}, e.scans...)
	if len(e.scans) > maxScans {
		e.scans = e.scans[:maxScans]
	}
	e.mu.Unlock()
	kind := "SCAN"
	if entry.Follow {
		kind = "SCAN_FOLLOW"
	}
	e.journal.Write(journal.Entry{
		At: entry.At, Kind: kind, Symbol: entry.Symbol, Mint: entry.Mint, Live: e.live,
		DevBuySOL: entry.DevBuySOL, MarketCapSol: entry.MarketCapSol, Passed: entry.Passed, AI: entry.AI, Reason: entry.Reason, CostUSD: entry.CostUSD,
	})
}

func (e *Engine) emit() {
	if e.notify == nil {
		return
	}
	e.mu.Lock()
	snap := Snapshot{
		Equity:       e.equity,
		StartEquity:  e.startEquity,
		Trades:       e.trades,
		Wins:         e.wins,
		Losses:       e.losses,
		AIPending:    len(e.pendingEntry) + len(e.pendingExit),
		AICostUSD:    e.totalAICost,
		FollowCount:  len(e.followSet),
		GrossPnlSol:  e.grossPnlSol,
		TotalFeesSol: e.totalFeesSol,
		Recent:       append([]LogEntry(nil), e.recent...),
		Scans:        append([]ScanEntry(nil), e.scans...),
	}
	snap.TotalEquity = e.equity
	for _, p := range e.positions {
		snap.OpenPos = append(snap.OpenPos, *p)
		snap.TotalEquity += p.Tokens * p.LastPrice
	}
	snap.AgentStatus = make(map[AgentName]string, len(e.status))
	snap.AgentUpdated = make(map[AgentName]time.Time, len(e.updated))
	for k, v := range e.status {
		snap.AgentStatus[k] = v
	}
	for k, v := range e.updated {
		snap.AgentUpdated[k] = v
	}
	e.mu.Unlock()
	e.notify(snap)
}
