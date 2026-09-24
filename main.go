package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aasumitro/iT2B/internal/brain"
	"github.com/aasumitro/iT2B/internal/config"
	"github.com/aasumitro/iT2B/internal/engine"
	"github.com/aasumitro/iT2B/internal/journal"
	"github.com/aasumitro/iT2B/internal/pumpportal"
	"github.com/aasumitro/iT2B/internal/tui"
)

// defaultSlippagePct/defaultPriorityFeeSol are Lightning trade parameters;
// bump these here if you're getting slipped out of fast-moving mints.
const (
	defaultSlippagePct    = 10
	defaultPriorityFeeSol = 0.0005
)

func main() {
	cmd := "dashboard"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "init":
		runInit()
	case "dashboard":
		fs := flag.NewFlagSet("dashboard", flag.ExitOnError)
		live := fs.Bool("live", false, "arm real trading via PumpPortal Lightning API (default: paper mode)")
		ai := fs.Bool("ai", false, "use an AI brain for the buy/reject + exit-timing calls, layered on top of the hard filters/rails")
		provider := fs.String("ai-provider", "claude", "which CLI agent to use with --ai: claude, gemini, antigravity, or applefm")
		fs.Parse(args)
		runDashboard(*live, *ai, *provider)
	case "follow":
		runFollow(args)
	case "stats":
		runStats()
	case "version":
		fmt.Println("t2b 0.1.0")
	default:
		fmt.Printf(`unknown command %q

usage:
  t2b init                    set up wallet + config
  t2b dashboard               run the desk in paper mode
  t2b dashboard --ai                          paper mode, Claude makes the buy/reject + exit calls
  t2b dashboard --ai --ai-provider gemini     same, via Gemini CLI instead
  t2b dashboard --ai --ai-provider antigravity  same, via Antigravity CLI (agy) instead
  t2b dashboard --ai --ai-provider applefm    same, via local Apple FoundationModels (t2bfm)
  t2b dashboard --live        real PumpPortal Lightning orders
  t2b follow list             show wallets currently copy-traded
  t2b follow add <address>    copy-trade this wallet's buys
  t2b follow remove <address> stop copy-trading this wallet
  t2b stats                   detailed win/loss/PnL/fee report across every run, from the journal
`, cmd)
		os.Exit(1)
	}
}

func runInit() {
	if !runInitWizard() {
		os.Exit(1)
	}
}

// runInitWizard runs the setup TUI and reports whether it finished (vs. the
// user quitting early or an error).
func runInitWizard() bool {
	p := tea.NewProgram(tui.NewInit(), tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		fmt.Println("error:", err)
		return false
	}
	return final.(tui.InitModel).Done()
}

func runDashboard(live, aiEnabled bool, aiProvider string) {
	if !config.Exists() {
		if !runInitWizard() {
			os.Exit(1)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("error loading config:", err)
		os.Exit(1)
	}

	var brainImpl engine.Brain
	if aiEnabled {
		var bin string
		switch aiProvider {
		case "claude":
			bin = "claude"
		case "gemini":
			bin = "gemini"
		case "antigravity":
			bin = "agy"
		case "applefm":
			bin = "t2bfm"
		default:
			fmt.Printf("unknown --ai-provider %q — use claude, gemini, antigravity, or applefm\n", aiProvider)
			os.Exit(1)
		}
		if _, err := exec.LookPath(bin); err != nil {
			fmt.Printf("--ai-provider %s requires the `%s` CLI on PATH — not found, continuing without AI\n", aiProvider, bin)
			if aiProvider == "applefm" {
				fmt.Println("build it: cd swift/t2bfm && swift build -c release, then put .build/release/t2bfm on PATH")
			}
		} else {
			switch aiProvider {
			case "claude":
				brainImpl = brain.NewClaudeCLI()
			case "gemini":
				brainImpl = brain.NewGeminiCLI()
			case "antigravity":
				brainImpl = brain.NewAntigravityCLI()
			case "applefm":
				brainImpl = brain.NewAppleFMCLI()
			}
		}
	}

	jrnl, err := journal.Open()
	if err != nil {
		fmt.Println("warning: couldn't open ~/.t2b/logs/trades.jsonl, continuing without a persistent journal:", err)
	}
	defer jrnl.Close()

	var trader engine.Trader = engine.PaperTrader{}
	if live {
		if cfg.PumpPortalKey == "" {
			fmt.Println("--live requires a PumpPortal API key — run `t2b init` and set one, or add \"pumpportal_api_key\" to ~/.t2b/config.json")
			os.Exit(1)
		}
		if !armLiveTrading(cfg.WalletPubkey) {
			fmt.Println("aborted — not armed")
			os.Exit(1)
		}
		trader = engine.LiveTrader{
			Client:         pumpportal.NewTradeClient(cfg.PumpPortalKey),
			SlippagePct:    defaultSlippagePct,
			PriorityFeeSol: defaultPriorityFeeSol,
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := pumpportal.New(cfg.PumpPortalKey)
	go client.Run(ctx)
	client.WatchWallets(cfg.FollowWallets)

	snapCh := make(chan engine.Snapshot, 4)
	notify := func(s engine.Snapshot) {
		select {
		case snapCh <- s:
		default:
			// drop stale snapshot, UI will catch up on next tick
			select {
			case <-snapCh:
			default:
			}
			snapCh <- s
		}
	}
	eng := engine.New(engine.DefaultParams(), engine.Deps{
		Client: client, Trader: trader, Live: live, Brain: brainImpl, Journal: jrnl,
		FollowWallets: cfg.FollowWallets, OnAutoFollow: persistAutoFollow,
	}, cfg.StartingEquity, notify)
	go eng.Run(ctx)

	p := tea.NewProgram(tui.NewDashboard(snapCh, cfg.WalletPubkey, !live), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}

// persistAutoFollow writes an auto-promoted wallet back to config.json so
// it survives a restart, exactly like `t2b follow add` would. Reloads
// config fresh rather than reusing the in-memory cfg, since this fires
// asynchronously mid-run.
func persistAutoFollow(wallet string) {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	for _, existing := range cfg.FollowWallets {
		if existing == wallet {
			return
		}
	}
	cfg.FollowWallets = append(cfg.FollowWallets, wallet)
	config.Save(cfg)
}

func runFollow(args []string) {
	if !config.Exists() {
		fmt.Println("no config found — run `t2b init` first")
		os.Exit(1)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("error loading config:", err)
		os.Exit(1)
	}

	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		if len(cfg.FollowWallets) == 0 {
			fmt.Println("no followed wallets — add one with `t2b follow add <address>`")
			return
		}
		for _, addr := range cfg.FollowWallets {
			fmt.Println(addr)
		}

	case "add":
		if len(args) < 2 {
			fmt.Println("usage: t2b follow add <wallet address>")
			os.Exit(1)
		}
		addr := args[1]
		for _, existing := range cfg.FollowWallets {
			if existing == addr {
				fmt.Println("already following", addr)
				return
			}
		}
		cfg.FollowWallets = append(cfg.FollowWallets, addr)
		if err := config.Save(cfg); err != nil {
			fmt.Println("error saving config:", err)
			os.Exit(1)
		}
		fmt.Println("now following", addr)

	case "remove":
		if len(args) < 2 {
			fmt.Println("usage: t2b follow remove <wallet address>")
			os.Exit(1)
		}
		addr := args[1]
		out := cfg.FollowWallets[:0]
		found := false
		for _, existing := range cfg.FollowWallets {
			if existing == addr {
				found = true
				continue
			}
			out = append(out, existing)
		}
		if !found {
			fmt.Println("not following", addr)
			return
		}
		cfg.FollowWallets = out
		if err := config.Save(cfg); err != nil {
			fmt.Println("error saving config:", err)
			os.Exit(1)
		}
		fmt.Println("unfollowed", addr)

	default:
		fmt.Printf("unknown `t2b follow` subcommand %q — use list/add/remove\n", args[0])
		os.Exit(1)
	}
}

// runStats prints a detailed, honest report from the journal — net of
// fees, not just a win/loss count. This is what actually answers "is the
// AI (or the strategy generally) profitable", since a nominal win rate
// alone can hide fees eating every gain.
func runStats() {
	s, err := journal.ReadStats()
	if err != nil {
		fmt.Println("error reading journal:", err)
		os.Exit(1)
	}
	if s.TotalTrades == 0 && s.ScansTotal == 0 {
		fmt.Println("no data yet — nothing in ~/.t2b/logs/trades.jsonl")
		return
	}

	fmt.Println()
	if !s.From.IsZero() {
		fmt.Printf("T2B REPORT · %s → %s\n", s.From.Local().Format("2006-01-02 15:04"), s.To.Local().Format("2006-01-02 15:04"))
	}
	fmt.Println(strings.Repeat("─", 60))

	fmt.Println("REALIZED (closed trades)")
	fmt.Printf("  Trades       %d  (%d live, %d paper)\n", s.TotalTrades, s.LiveTrades, s.PaperTrades)
	if s.TotalTrades > 0 {
		fmt.Printf("  Win rate     %.1f%%  (%d W / %d L)\n", s.WinRate(), s.Wins, s.Losses)
		fmt.Printf("  Gross PnL    %+.4f SOL\n", s.GrossPnlSol)
		fmt.Printf("  Fees paid    -%.4f SOL\n", s.FeeSol)
		fmt.Printf("  Net PnL      %+.4f SOL  <- the real number\n", s.NetPnlSol)
		fmt.Printf("  Avg win      %+.4f SOL\n", s.AvgWinSol())
		fmt.Printf("  Avg loss     %+.4f SOL\n", s.AvgLossSol())
		if s.BestSymbol != "" {
			fmt.Printf("  Best trade   %s  %+.4f SOL  (%s)\n", s.BestSymbol, s.BestPnlSol, shortAddrMain(s.BestMint))
		}
		if s.WorstSymbol != "" {
			fmt.Printf("  Worst trade  %s  %+.4f SOL  (%s)\n", s.WorstSymbol, s.WorstPnlSol, shortAddrMain(s.WorstMint))
		}
		fmt.Println()
		fmt.Println("BY EXIT REASON")
		for _, kind := range []string{"SELL_TP", "SELL_SL", "SELL_TIMEOUT", "SELL_AI"} {
			if n := s.ByKind[kind]; n > 0 {
				fmt.Printf("  %-14s %d\n", kind, n)
			}
		}
	} else {
		fmt.Println("  no closed trades yet")
	}

	fmt.Println()
	fmt.Println("EXECUTION ISSUES")
	fmt.Printf("  BUY_FAILED   %d\n", s.BuyFailed)
	fmt.Printf("  SELL_FAILED  %d\n", s.SellFailed)
	if s.SellStuck > 0 {
		fmt.Printf("  SELL_STUCK   %d  <- capital possibly still locked in these, check OPEN POSITIONS\n", s.SellStuck)
	}

	if s.AICalls > 0 {
		fmt.Println()
		fmt.Println("AI")
		fmt.Printf("  Calls        %d\n", s.AICalls)
		fmt.Printf("  Cost         $%.4f\n", s.AICostUSD)
	}

	fmt.Println()
	fmt.Println("SCANNER")
	fmt.Printf("  Scanned      %d\n", s.ScansTotal)
	if s.ScansTotal > 0 {
		fmt.Printf("  Passed       %d  (%.1f%%)\n", s.ScansPassed, float64(s.ScansPassed)/float64(s.ScansTotal)*100)
	}
	fmt.Println()
}

func shortAddrMain(addr string) string {
	if len(addr) <= 8 {
		return addr
	}
	return addr[:4] + "…" + addr[len(addr)-4:]
}

// armLiveTrading is the explicit, out-of-band confirmation gate: having a
// PumpPortal key configured is not enough by itself to fire real orders.
func armLiveTrading(walletPubkey string) bool {
	fmt.Println()
	fmt.Println("⚠  LIVE TRADING — this will place real orders with real SOL")
	fmt.Println("   wallet:", walletPubkey)
	fmt.Println("   type ARM to continue, anything else aborts:")
	fmt.Print("   > ")

	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line) == "ARM"
}
