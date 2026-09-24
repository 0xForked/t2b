package journal

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// closeKinds are the LogEntry kinds that represent an actual closed trade —
// distinct from BUY, and from the *_FAILED/*_STUCK execution-issue kinds.
var closeKinds = map[string]bool{
	"SELL_TP": true, "SELL_SL": true, "SELL_TIMEOUT": true, "SELL_AI": true,
}

// Stats is an aggregate read back over every entry ever written to the
// journal — the answer to "is this thing actually profitable", net of fees,
// not just a win/loss count.
type Stats struct {
	From, To time.Time

	TotalTrades, LiveTrades, PaperTrades int
	Wins, Losses                         int

	GrossPnlSol float64 // before fees
	FeeSol      float64
	NetPnlSol   float64 // GrossPnlSol - FeeSol — the real result

	SumWinSol, SumLossSol float64 // for averages; SumLossSol is negative

	BestSymbol, BestMint   string
	BestPnlSol             float64
	WorstSymbol, WorstMint string
	WorstPnlSol            float64

	ByKind map[string]int // SELL_TP/SELL_SL/SELL_TIMEOUT/SELL_AI -> count

	BuyFailed, SellFailed, SellStuck int

	AICalls   int
	AICostUSD float64

	ScansTotal, ScansPassed int
}

func (s Stats) WinRate() float64 {
	if s.TotalTrades == 0 {
		return 0
	}
	return float64(s.Wins) / float64(s.TotalTrades) * 100
}

func (s Stats) AvgWinSol() float64 {
	if s.Wins == 0 {
		return 0
	}
	return s.SumWinSol / float64(s.Wins)
}

func (s Stats) AvgLossSol() float64 {
	if s.Losses == 0 {
		return 0
	}
	return s.SumLossSol / float64(s.Losses)
}

// ReadStats aggregates ~/.t2b/logs/trades.jsonl. Returns an empty Stats, no
// error, if the journal doesn't exist yet (nothing run so far).
func ReadStats() (Stats, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Stats{}, err
	}
	f, err := os.Open(filepath.Join(home, ".t2b", "logs", "trades.jsonl"))
	if os.IsNotExist(err) {
		return Stats{ByKind: map[string]int{}}, nil
	}
	if err != nil {
		return Stats{}, err
	}
	defer f.Close()
	return computeStats(f)
}

func computeStats(r io.Reader) (Stats, error) {
	s := Stats{ByKind: map[string]int{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024) // journal lines can be long (raw API errors)

	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // one malformed line shouldn't sink the whole report
		}
		if s.From.IsZero() || e.At.Before(s.From) {
			s.From = e.At
		}
		if e.At.After(s.To) {
			s.To = e.At
		}

		switch {
		case e.Kind == "SCAN" || e.Kind == "SCAN_FOLLOW":
			s.ScansTotal++
			if e.Passed {
				s.ScansPassed++
			}
			if e.AI && e.CostUSD > 0 {
				s.AICalls++
				s.AICostUSD += e.CostUSD
			}
		case e.Kind == "BUY_FAILED":
			s.BuyFailed++
		case e.Kind == "SELL_FAILED":
			s.SellFailed++
		case e.Kind == "SELL_STUCK":
			s.SellStuck++
		case closeKinds[e.Kind]:
			s.TotalTrades++
			s.ByKind[e.Kind]++
			if e.Live {
				s.LiveTrades++
			} else {
				s.PaperTrades++
			}
			s.GrossPnlSol += e.GrossPnlSOL
			s.FeeSol += e.FeeSol
			s.NetPnlSol += e.PnlSOL
			if e.PnlSOL >= 0 {
				s.Wins++
				s.SumWinSol += e.PnlSOL
			} else {
				s.Losses++
				s.SumLossSol += e.PnlSOL
			}
			if s.TotalTrades == 1 || e.PnlSOL > s.BestPnlSol {
				s.BestPnlSol, s.BestSymbol, s.BestMint = e.PnlSOL, e.Symbol, e.Mint
			}
			if s.TotalTrades == 1 || e.PnlSOL < s.WorstPnlSol {
				s.WorstPnlSol, s.WorstSymbol, s.WorstMint = e.PnlSOL, e.Symbol, e.Mint
			}
		}
	}
	return s, sc.Err()
}
