package engine

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/aasumitro/iT2B/internal/pumpportal"
)

var errFake = errors.New("fake trade failure")
var bg = context.Background()

// testParams disables the momentum-confirmation gate — most tests exercise
// filter/risk/TP/SL/live-trade/follow logic and expect a pass to buy
// immediately, same as before that gate existed. Momentum-gate behavior
// itself is covered separately below.
func testParams() Params {
	p := DefaultParams()
	p.MomentumConfirmTrades = 0
	return p
}

func newTestEngine(startEquity float64) (*Engine, chan Snapshot) {
	client := pumpportal.New("")
	snapCh := make(chan Snapshot, 100)
	e := New(testParams(), Deps{Client: client}, startEquity, func(s Snapshot) {
		select {
		case snapCh <- s:
		default:
		}
	})
	return e, snapCh
}

func TestFilterRejectsOutOfRangeDevBuy(t *testing.T) {
	e, _ := newTestEngine(5)
	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 0.01, MarketCapSol: 10})
	if len(e.positions) != 0 {
		t.Fatalf("expected no position opened for tiny dev buy, got %d", len(e.positions))
	}
}

func TestFilterAndRiskOpenPosition(t *testing.T) {
	e, _ := newTestEngine(5)
	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 1, MarketCapSol: 10})
	if len(e.positions) != 1 {
		t.Fatalf("expected 1 open position, got %d", len(e.positions))
	}
	if e.equity != 5-e.params.PositionSizeSOL {
		t.Fatalf("equity not debited: got %v", e.equity)
	}
}

func TestRiskSkipsWhenMaxPositionsReached(t *testing.T) {
	e, _ := newTestEngine(100)
	e.params.MaxPositions = 1
	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 1, MarketCapSol: 10})
	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "B", Symbol: "BBB", SolAmount: 1, MarketCapSol: 10})
	if len(e.positions) != 1 {
		t.Fatalf("expected exactly 1 position (cap=1), got %d", len(e.positions))
	}
}

func TestTakeProfitClosesPositionAsWin(t *testing.T) {
	e, _ := newTestEngine(5)
	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 1, MarketCapSol: 10})
	entryMcap := 10.0
	tpMcap := entryMcap * (1 + e.params.TakeProfitPct + 0.01)
	e.onTrade(pumpportal.TradeEvent{Mint: "A", MarketCapSol: tpMcap})

	if len(e.positions) != 0 {
		t.Fatalf("expected position closed on take-profit, still open: %+v", e.positions)
	}
	if e.wins != 1 || e.losses != 0 {
		t.Fatalf("expected 1 win 0 losses, got wins=%d losses=%d", e.wins, e.losses)
	}
	if e.equity <= 5-e.params.PositionSizeSOL {
		t.Fatalf("expected equity to grow above entry debit after profitable exit, got %v", e.equity)
	}
}

func TestStopLossClosesPositionAsLoss(t *testing.T) {
	e, _ := newTestEngine(5)
	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 1, MarketCapSol: 10})
	entryMcap := 10.0
	slMcap := entryMcap * (1 - e.params.StopLossPct - 0.01)
	e.onTrade(pumpportal.TradeEvent{Mint: "A", MarketCapSol: slMcap})

	if len(e.positions) != 0 {
		t.Fatal("expected position closed on stop-loss")
	}
	if e.losses != 1 || e.wins != 0 {
		t.Fatalf("expected 1 loss 0 wins, got wins=%d losses=%d", e.wins, e.losses)
	}
}

type stubTrader struct {
	buyErr, sellErr     error
	buyCalls, sellCalls int
	fee                 float64
}

func (s *stubTrader) Buy(mint string, sol float64) (string, error) {
	s.buyCalls++
	if s.buyErr != nil {
		return "", s.buyErr
	}
	return "sig-buy", nil
}

func (s *stubTrader) SellAll(mint string) (string, error) {
	s.sellCalls++
	if s.sellErr != nil {
		return "", s.sellErr
	}
	return "sig-sell", nil
}

func (s *stubTrader) FeeSol() float64 { return s.fee }

func TestLiveBuyFailureOpensNoPosition(t *testing.T) {
	client := pumpportal.New("")
	trader := &stubTrader{buyErr: errFake}
	e := New(testParams(), Deps{Client: client, Trader: trader, Live: true}, 5, nil)

	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 1, MarketCapSol: 10})

	if len(e.positions) != 0 {
		t.Fatalf("expected no position after failed live buy, got %d", len(e.positions))
	}
	if e.equity != 5 {
		t.Fatalf("expected equity untouched after failed buy, got %v", e.equity)
	}
	if trader.buyCalls != 1 {
		t.Fatalf("expected exactly 1 buy attempt, got %d", trader.buyCalls)
	}
	if len(e.recent) != 1 || e.recent[0].Kind != "BUY_FAILED" {
		t.Fatalf("expected a BUY_FAILED log entry, got %+v", e.recent)
	}
}

func TestLiveSellFailureKeepsPositionOpen(t *testing.T) {
	client := pumpportal.New("")
	trader := &stubTrader{sellErr: errFake}
	e := New(testParams(), Deps{Client: client, Trader: trader, Live: true}, 5, nil)

	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 1, MarketCapSol: 10})
	if len(e.positions) != 1 {
		t.Fatalf("setup failed: expected position opened, got %d", len(e.positions))
	}

	e.closePosition("A", "SELL_TP", "")

	if len(e.positions) != 1 {
		t.Fatalf("expected position to remain open after failed sell, got %d", len(e.positions))
	}
	if e.trades != 0 {
		t.Fatalf("expected no trade counted on failed sell, got %d", e.trades)
	}
	if e.recent[0].Kind != "SELL_FAILED" {
		t.Fatalf("expected a SELL_FAILED log entry, got %+v", e.recent[0])
	}
}

// TestRepeatedSellFailureGivesUpAndStopsRetrying is a regression test for a
// self-deadlock: closePosition's error branch used to call e.setStatus (which
// locks e.mu) while e.mu was already held. It hung forever under any repeated
// failure — exactly this scenario — until fixed. Also proves the throttle:
// immediately-repeated calls within sellRetryInterval don't re-invoke the
// trader at all, and calls stop entirely once maxSellAttempts is reached.
func TestRepeatedSellFailureGivesUpAndStopsRetrying(t *testing.T) {
	client := pumpportal.New("")
	trader := &stubTrader{sellErr: errFake}
	e := New(testParams(), Deps{Client: client, Trader: trader, Live: true}, 5, nil)

	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 1, MarketCapSol: 10})

	done := make(chan struct{})
	go func() {
		for i := 0; i < maxSellAttempts+3; i++ {
			e.closePosition("A", "SELL_TP", "")
			// bypass the interval throttle between iterations so this test
			// doesn't take sellRetryInterval*maxSellAttempts to run
			e.mu.Lock()
			if pos, ok := e.positions["A"]; ok {
				pos.LastSellAttempt = time.Time{}
			}
			e.mu.Unlock()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("closePosition hung — deadlock regression")
	}

	if len(e.positions) != 1 {
		t.Fatalf("expected the stuck position to remain open, got %d", len(e.positions))
	}
	if trader.sellCalls != maxSellAttempts {
		t.Fatalf("expected exactly %d sell attempts (capped, then given up), got %d", maxSellAttempts, trader.sellCalls)
	}
	if e.recent[0].Kind != "SELL_STUCK" {
		t.Fatalf("expected the final log entry to be SELL_STUCK, got %+v", e.recent[0])
	}
}

func TestSweepTimeoutForceCloses(t *testing.T) {
	e, _ := newTestEngine(5)
	e.params.MaxHold = 1 * time.Millisecond
	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 1, MarketCapSol: 10})
	time.Sleep(5 * time.Millisecond)
	e.sweepTimeouts()
	if len(e.positions) != 0 {
		t.Fatal("expected timeout sweep to close stale position")
	}
	if e.trades != 1 {
		t.Fatalf("expected 1 closed trade, got %d", e.trades)
	}
}

type stubBrain struct {
	entryBuy    bool
	entryErr    error
	entryReason string
	exitSell    bool
	exitErr     error
}

func (s stubBrain) EntryDecision(ctx context.Context, symbol, mint string, devBuySol, mcapSol float64) (Decision, error) {
	if s.entryErr != nil {
		return Decision{}, s.entryErr
	}
	return Decision{Buy: s.entryBuy, Reasoning: s.entryReason}, nil
}

func (s stubBrain) ExitDecision(ctx context.Context, symbol string, entryPrice, currentPrice, pnlPct float64, heldSeconds int) (ExitDecision, error) {
	if s.exitErr != nil {
		return ExitDecision{}, s.exitErr
	}
	return ExitDecision{Sell: s.exitSell, Reasoning: "ai says exit"}, nil
}

// drainDecision waits for the async entry/exit goroutine to post its
// result, then feeds it through the same handler the event loop uses —
// exercising the real integration without needing Run().
func drainDecision(t *testing.T, e *Engine) {
	t.Helper()
	select {
	case res := <-e.decisions:
		e.handleDecision(res)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for decision")
	}
}

func TestAIEntryApprovalOpensPosition(t *testing.T) {
	client := pumpportal.New("")
	e := New(testParams(), Deps{Client: client, Brain: stubBrain{entryBuy: true, entryReason: "looks fine"}}, 5, nil)

	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 1, MarketCapSol: 10})
	drainDecision(t, e)

	if len(e.positions) != 1 {
		t.Fatalf("expected AI-approved buy to open a position, got %d", len(e.positions))
	}
	if len(e.scans) != 1 || !e.scans[0].AI || !e.scans[0].Passed {
		t.Fatalf("expected an AI-tagged, passed scan entry, got %+v", e.scans)
	}
}

func TestAIEntryRejectionOpensNoPosition(t *testing.T) {
	client := pumpportal.New("")
	e := New(testParams(), Deps{Client: client, Brain: stubBrain{entryBuy: false, entryReason: "smells like a rug"}}, 5, nil)

	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 1, MarketCapSol: 10})
	drainDecision(t, e)

	if len(e.positions) != 0 {
		t.Fatalf("expected AI rejection to open no position, got %d", len(e.positions))
	}
	if len(e.scans) != 1 || !e.scans[0].AI || e.scans[0].Passed || e.scans[0].Reason != "smells like a rug" {
		t.Fatalf("expected an AI-tagged rejected scan entry with reasoning, got %+v", e.scans)
	}
}

func TestAIExitReviewClosesPosition(t *testing.T) {
	client := pumpportal.New("")
	e := New(testParams(), Deps{Client: client, Brain: stubBrain{exitSell: true}}, 5, nil)

	e.positions["A"] = &Position{
		Mint: "A", Symbol: "AAA", EntryPrice: 1, EntrySol: 0.1, Tokens: 0.1,
		OpenedAt: time.Now().Add(-time.Minute), LastPrice: 1.1, LastPnlPct: 0.1,
	}

	e.sweepAIExitReview(bg)
	drainDecision(t, e)

	if len(e.positions) != 0 {
		t.Fatalf("expected AI sell decision to close the position, got %d open", len(e.positions))
	}
	if e.recent[0].Kind != "SELL_AI" {
		t.Fatalf("expected a SELL_AI log entry, got %+v", e.recent[0])
	}
}

func TestFollowedWalletBuyOpensPositionBypassingHardFilters(t *testing.T) {
	client := pumpportal.New("")
	e := New(testParams(), Deps{Client: client, FollowWallets: []string{"whale1"}}, 5, nil)

	// dev buy of 0.001 SOL would fail MinDevBuySOL on the normal scanner
	// path — copy-trading is supposed to skip that filter entirely.
	e.onTrade(pumpportal.TradeEvent{Mint: "A", Symbol: "AAA", TxType: "buy", TraderPubkey: "whale1", SolAmount: 0.001, MarketCapSol: 10})

	if len(e.positions) != 1 {
		t.Fatalf("expected a followed wallet's buy to open a position, got %d", len(e.positions))
	}
	if len(e.scans) != 1 || !e.scans[0].Follow || !e.scans[0].Passed {
		t.Fatalf("expected a Follow-tagged, passed scan entry, got %+v", e.scans)
	}
}

func TestAutoFollowPromotesDevAfterWinStreak(t *testing.T) {
	client := pumpportal.New("")
	var promoted string
	e := New(testParams(), Deps{Client: client, OnAutoFollow: func(w string) { promoted = w }}, 100, nil)
	e.params.AutoFollowMinWins = 2
	e.params.AutoFollowMinWinRate = 0.5

	for _, mint := range []string{"A", "B"} {
		e.positions[mint] = &Position{Mint: mint, Symbol: "X", DevPubkey: "dev1", EntryPrice: 1, EntrySol: 0.1, Tokens: 0.1, LastPrice: 2, OpenedAt: time.Now()}
		e.closePosition(mint, "SELL_TP", "")
	}

	if !e.followSet["dev1"] {
		t.Fatal("expected dev1 to be auto-followed after clearing the win streak threshold")
	}
	if promoted != "dev1" {
		t.Fatalf("expected OnAutoFollow callback fired with dev1, got %q", promoted)
	}

	// the promoted dev's next launch should now bypass the hard filters —
	// this dev-buy size would fail MinDevBuySOL on the normal scanner path.
	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "C", Symbol: "CCC", TraderPubkey: "dev1", SolAmount: 0.0001, MarketCapSol: 10})
	if _, ok := e.positions["C"]; !ok {
		t.Fatal("expected auto-followed dev's next launch to open a position despite failing hard filters")
	}
}

func TestTotalEquityIncludesOpenPositionMarkToMarket(t *testing.T) {
	e, snapCh := newTestEngine(5)
	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 1, MarketCapSol: 10})
	// price unchanged since entry (10 mcap / 1e9 supply) -> position's
	// mark-to-market value should equal what was spent on it.
	e.emit()

	var snap Snapshot
	select {
	case snap = <-snapCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for snapshot")
	}

	if len(snap.OpenPos) != 1 {
		t.Fatalf("expected 1 open position, got %d", len(snap.OpenPos))
	}
	// Free cash should be down by PositionSizeSOL, but total equity — cash
	// plus the position's current value — should be back near the start,
	// not showing a loss just because capital moved into a position.
	if snap.Equity != 5-e.params.PositionSizeSOL {
		t.Fatalf("expected free equity debited by position size, got %v", snap.Equity)
	}
	if got, want := snap.TotalEquity, 5.0; got < want-0.001 || got > want+0.001 {
		t.Fatalf("expected total equity ~%.3f (flat, position at entry price), got %.3f", want, got)
	}
}

func TestAllowAICallEnforcesPerMinuteCap(t *testing.T) {
	client := pumpportal.New("")
	e := New(testParams(), Deps{Client: client}, 5, nil)
	e.params.AIMaxCallsPerMinute = 2

	if !e.allowAICall() {
		t.Fatal("expected call 1 to be allowed")
	}
	if !e.allowAICall() {
		t.Fatal("expected call 2 to be allowed")
	}
	if e.allowAICall() {
		t.Fatal("expected call 3 to be rejected — over the per-minute cap")
	}
}

func TestAllowAICallUnlimitedWhenCapIsZero(t *testing.T) {
	client := pumpportal.New("")
	e := New(testParams(), Deps{Client: client}, 5, nil)
	e.params.AIMaxCallsPerMinute = 0

	for i := 0; i < 50; i++ {
		if !e.allowAICall() {
			t.Fatalf("expected unlimited calls with cap=0, rejected at call %d", i+1)
		}
	}
}

func TestUnfollowedWalletBuyIsIgnored(t *testing.T) {
	client := pumpportal.New("")
	e := New(testParams(), Deps{Client: client, FollowWallets: []string{"whale1"}}, 5, nil)

	e.onTrade(pumpportal.TradeEvent{Mint: "A", Symbol: "AAA", TxType: "buy", TraderPubkey: "someone-else", SolAmount: 1, MarketCapSol: 10})

	if len(e.positions) != 0 {
		t.Fatalf("expected an unfollowed wallet's buy to be ignored, got %d positions", len(e.positions))
	}
}

// TestFeesFlipGrossWinIntoNetLoss is exactly the scenario a user flagged:
// a trade can look like a win by price alone but be a real loss once fees
// are counted — the win/loss label and PnL must reflect that, not just the
// gross price move.
func TestFeesFlipGrossWinIntoNetLoss(t *testing.T) {
	client := pumpportal.New("")
	trader := &stubTrader{fee: 0.01}
	e := New(testParams(), Deps{Client: client, Trader: trader, Live: true}, 5, nil)

	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", SolAmount: 1, MarketCapSol: 10})
	if got, want := e.equity, 5-e.params.PositionSizeSOL-0.01; math.Abs(got-want) > 1e-9 {
		t.Fatalf("expected equity debited by position size + buy fee, got %v want %v", got, want)
	}

	// 5% gross gain on a 0.1 SOL position = 0.005 SOL gross profit —
	// smaller than the 0.02 SOL round-trip fee (0.01 buy + 0.01 sell).
	pos := e.positions["A"]
	pos.LastPrice = pos.EntryPrice * 1.05
	pos.LastPnlPct = 0.05

	e.closePosition("A", "SELL_TP", "")

	if e.wins != 0 || e.losses != 1 {
		t.Fatalf("expected fees to flip a gross-positive trade into a net loss, got wins=%d losses=%d", e.wins, e.losses)
	}
	entry := e.recent[0]
	if entry.GrossPnlSOL <= 0 {
		t.Fatalf("expected gross pnl positive, got %v", entry.GrossPnlSOL)
	}
	if entry.PnlSOL >= 0 {
		t.Fatalf("expected net pnl negative once fees applied, got %v", entry.PnlSOL)
	}
	if math.Abs(entry.FeeSol-0.02) > 1e-9 {
		t.Fatalf("expected total fee 0.02 (buy+sell), got %v", entry.FeeSol)
	}
}

func TestMomentumGateDoesNotBuyImmediately(t *testing.T) {
	client := pumpportal.New("")
	e := New(DefaultParams(), Deps{Client: client}, 5, nil) // gate enabled (default)

	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", TraderPubkey: "dev1", SolAmount: 1, MarketCapSol: 10})

	if len(e.positions) != 0 {
		t.Fatalf("expected no immediate buy while awaiting momentum confirmation, got %d positions", len(e.positions))
	}
	if _, watching := e.watching["A"]; !watching {
		t.Fatal("expected the candidate to be registered as watching")
	}
}

func TestMomentumGateBuysOnConfirmedSecondaryTrade(t *testing.T) {
	client := pumpportal.New("")
	e := New(DefaultParams(), Deps{Client: client}, 5, nil)

	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", TraderPubkey: "dev1", SolAmount: 1, MarketCapSol: 10})

	// the dev buying again should NOT count as momentum
	e.onTrade(pumpportal.TradeEvent{Mint: "A", TxType: "buy", TraderPubkey: "dev1", MarketCapSol: 10})
	if len(e.positions) != 0 {
		t.Fatal("expected the dev's own follow-up buy to not count as momentum confirmation")
	}

	// someone else buying should
	e.onTrade(pumpportal.TradeEvent{Mint: "A", TxType: "buy", TraderPubkey: "stranger", MarketCapSol: 10})
	if len(e.positions) != 1 {
		t.Fatalf("expected a real secondary buy to confirm momentum and open a position, got %d", len(e.positions))
	}
	if _, watching := e.watching["A"]; watching {
		t.Fatal("expected the candidate to be removed from watching once confirmed")
	}
	if len(e.scans) != 1 || !e.scans[0].Passed {
		t.Fatalf("expected a passed scan entry logged on confirmation, got %+v", e.scans)
	}
}

func TestMomentumGateDropsCandidateAfterTimeout(t *testing.T) {
	client := pumpportal.New("")
	params := DefaultParams()
	params.MomentumWindow = 1 * time.Millisecond
	e := New(params, Deps{Client: client}, 5, nil)

	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", TraderPubkey: "dev1", SolAmount: 1, MarketCapSol: 10})
	time.Sleep(5 * time.Millisecond)
	e.sweepMomentumTimeouts()

	if _, watching := e.watching["A"]; watching {
		t.Fatal("expected the candidate to be dropped after the momentum window expired")
	}
	if len(e.positions) != 0 {
		t.Fatal("expected no position opened for a candidate that never confirmed")
	}
	if len(e.scans) != 1 || e.scans[0].Passed {
		t.Fatalf("expected a rejected scan entry logged on timeout, got %+v", e.scans)
	}
	if !strings.Contains(e.scans[0].Reason, "0 trades seen") {
		t.Fatalf("expected the timeout reason to report zero trades seen (subscription diagnostic), got %q", e.scans[0].Reason)
	}
}

// TestMomentumTimeoutReportsDevOnlyActivity distinguishes "subscription
// isn't delivering data" (0 trades seen) from "data's arriving but it's
// only the dev trading their own token" — the diagnostic that would have
// answered the real production question directly instead of guessing.
func TestMomentumTimeoutReportsDevOnlyActivity(t *testing.T) {
	client := pumpportal.New("")
	params := DefaultParams()
	params.MomentumWindow = 1 * time.Millisecond
	e := New(params, Deps{Client: client}, 5, nil)

	e.onNewToken(bg, pumpportal.TokenEvent{Mint: "A", Symbol: "AAA", TraderPubkey: "dev1", SolAmount: 1, MarketCapSol: 10})
	// the dev buying their own token again shouldn't count as momentum,
	// but it should still register as "data is arriving"
	e.onTrade(pumpportal.TradeEvent{Mint: "A", TxType: "buy", TraderPubkey: "dev1", MarketCapSol: 10})

	time.Sleep(5 * time.Millisecond)
	e.sweepMomentumTimeouts()

	if len(e.scans) != 1 {
		t.Fatalf("expected 1 scan entry, got %d", len(e.scans))
	}
	reason := e.scans[0].Reason
	if !strings.Contains(reason, "1 trades seen") || !strings.Contains(reason, "1 from dev") {
		t.Fatalf("expected the timeout reason to report 1 trade seen from dev, got %q", reason)
	}
}
