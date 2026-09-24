package journal

import (
	"strings"
	"testing"
)

func TestComputeStatsAggregation(t *testing.T) {
	// two live wins, one live loss, one paper win, plus scanner/AI/failure
	// noise that should be counted separately, not folded into trade stats.
	lines := strings.Join([]string{
		`{"at":"2026-09-22T10:00:00Z","kind":"BUY","symbol":"A","mint":"mA","live":true}`,
		`{"at":"2026-09-22T10:01:00Z","kind":"SELL_TP","symbol":"A","mint":"mA","live":true,"pnl_sol":0.05,"gross_pnl_sol":0.07,"fee_sol":0.02}`,
		`{"at":"2026-09-22T10:02:00Z","kind":"SELL_SL","symbol":"B","mint":"mB","live":true,"pnl_sol":-0.03,"gross_pnl_sol":-0.01,"fee_sol":0.02}`,
		`{"at":"2026-09-22T10:03:00Z","kind":"SELL_TIMEOUT","symbol":"C","mint":"mC","live":false,"pnl_sol":0.01,"gross_pnl_sol":0.01}`,
		`{"at":"2026-09-22T10:04:00Z","kind":"BUY_FAILED","symbol":"D","mint":"mD"}`,
		`{"at":"2026-09-22T10:05:00Z","kind":"SELL_FAILED","symbol":"E","mint":"mE"}`,
		`{"at":"2026-09-22T10:06:00Z","kind":"SELL_STUCK","symbol":"F","mint":"mF"}`,
		`{"at":"2026-09-22T10:07:00Z","kind":"SCAN","symbol":"G","mint":"mG","passed":true,"ai":true,"cost_usd":0.02}`,
		`{"at":"2026-09-22T10:08:00Z","kind":"SCAN","symbol":"H","mint":"mH","passed":false}`,
	}, "\n")

	s, err := computeStats(strings.NewReader(lines))
	if err != nil {
		t.Fatal(err)
	}

	if s.TotalTrades != 3 {
		t.Fatalf("expected 3 closed trades, got %d", s.TotalTrades)
	}
	if s.LiveTrades != 2 || s.PaperTrades != 1 {
		t.Fatalf("expected 2 live 1 paper, got live=%d paper=%d", s.LiveTrades, s.PaperTrades)
	}
	if s.Wins != 2 || s.Losses != 1 {
		t.Fatalf("expected 2 wins 1 loss, got wins=%d losses=%d", s.Wins, s.Losses)
	}
	if got, want := s.GrossPnlSol, 0.07-0.01+0.01; abs(got-want) > 1e-9 {
		t.Fatalf("expected gross pnl %.4f, got %.4f", want, got)
	}
	if got, want := s.FeeSol, 0.04; abs(got-want) > 1e-9 {
		t.Fatalf("expected total fees %.4f, got %.4f", want, got)
	}
	if got, want := s.NetPnlSol, 0.05-0.03+0.01; abs(got-want) > 1e-9 {
		t.Fatalf("expected net pnl %.4f, got %.4f", want, got)
	}
	if s.BestSymbol != "A" || s.WorstSymbol != "B" {
		t.Fatalf("expected best=A worst=B, got best=%s worst=%s", s.BestSymbol, s.WorstSymbol)
	}
	if s.BuyFailed != 1 || s.SellFailed != 1 || s.SellStuck != 1 {
		t.Fatalf("expected 1 each of buy/sell failed and stuck, got %+v", s)
	}
	if s.AICalls != 1 || abs(s.AICostUSD-0.02) > 1e-9 {
		t.Fatalf("expected 1 AI call costing 0.02, got calls=%d cost=%v", s.AICalls, s.AICostUSD)
	}
	if s.ScansTotal != 2 || s.ScansPassed != 1 {
		t.Fatalf("expected 2 scans 1 passed, got total=%d passed=%d", s.ScansTotal, s.ScansPassed)
	}
	if got, want := s.WinRate(), 200.0/3; abs(got-want) > 1e-6 {
		t.Fatalf("expected win rate %.4f%%, got %.4f%%", want, got)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
