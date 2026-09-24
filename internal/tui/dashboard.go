package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/aasumitro/iT2B/internal/engine"
)

var agentOrder = []engine.AgentName{
	engine.AgentListener,
	engine.AgentFilter,
	engine.AgentRisk,
	engine.AgentExecutor,
	engine.AgentPosition,
	engine.AgentLogbook,
}

type snapshotMsg engine.Snapshot

// WaitForSnapshot turns a channel receive into a tea.Cmd; the dashboard
// model re-issues it after every message so the feed keeps flowing.
func WaitForSnapshot(ch <-chan engine.Snapshot) tea.Cmd {
	return func() tea.Msg {
		return snapshotMsg(<-ch)
	}
}

type DashboardModel struct {
	snapCh  <-chan engine.Snapshot
	snap    engine.Snapshot
	history []float64
	wallet  string
	paper   bool
	width   int
	height  int
}

func NewDashboard(snapCh <-chan engine.Snapshot, walletAddr string, paper bool) DashboardModel {
	return DashboardModel{snapCh: snapCh, wallet: walletAddr, paper: paper}
}

func (m DashboardModel) Init() tea.Cmd {
	return WaitForSnapshot(m.snapCh)
}

func (m DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
	case snapshotMsg:
		m.snap = engine.Snapshot(msg)
		m.history = append(m.history, m.snap.TotalEquity)
		if len(m.history) > 200 {
			m.history = m.history[len(m.history)-200:]
		}
		return m, WaitForSnapshot(m.snapCh)
	}
	return m, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

func short(addr string) string {
	if len(addr) <= 10 {
		return addr
	}
	return addr[:4] + "…" + addr[len(addr)-4:]
}

// boxOverhead is how much wider a styleBox renders than the Width() you
// pass it: 1 border + 1 padding char on each side.
const boxOverhead = 4

// contentWidth clamps to the real terminal width (falling back to 80 before
// the first WindowSizeMsg arrives) so nothing ever wraps mid-line — an
// unaccounted terminal auto-wrap is what corrupts bubbletea's alt-screen
// diff renderer and produces the stacked/duplicated frames.
func (m DashboardModel) contentWidth() int {
	w := m.width
	if w <= 0 {
		w = 80
	}
	if w < 60 {
		w = 60
	}
	return w
}

func (m DashboardModel) View() string {
	cw := m.contentWidth()
	mode := "LIVE"
	if m.paper {
		mode = "PAPER"
	}
	headerText := "pump.fun · " + mode + " · " + short(m.wallet)
	if m.snap.FollowCount > 0 {
		headerText += fmt.Sprintf(" · following %d wallet", m.snap.FollowCount)
		if m.snap.FollowCount != 1 {
			headerText += "s"
		}
	}
	if m.snap.AICostUSD > 0 {
		headerText += fmt.Sprintf(" · AI cost $%.4f", m.snap.AICostUSD)
	}
	if m.snap.AIPending > 0 {
		headerText += fmt.Sprintf(" · thinking ×%d", m.snap.AIPending)
	}
	header := lipgloss.JoinHorizontal(lipgloss.Top,
		styleTitle.Render("T2B"), "  ",
		styleDim.Render(headerText),
	)

	statW := max((cw-2-boxOverhead*2)/2, 18)
	stats := lipgloss.JoinHorizontal(lipgloss.Top,
		m.statCard(statW), "  ", m.tradeCard(statW),
	)

	agents := m.agentRow(cw)
	scanner := m.scannerBox(cw - boxOverhead)

	posWRaw := cw*4/10 - boxOverhead
	feedW := max(cw-posWRaw-boxOverhead*2-2, 16)
	posW := max(posWRaw, 16)
	positions := m.positionsBox(posW)
	feed := m.feedBox(feedW)

	spark := m.sparkBox(cw - boxOverhead)

	body := lipgloss.JoinVertical(lipgloss.Left,
		header, "",
		stats, "",
		agents, "",
		scanner, "",
		lipgloss.JoinHorizontal(lipgloss.Top, positions, "  ", feed), "",
		spark, "",
		styleDim.Render("q  quit"),
	)
	// Belt-and-braces: never let a rounding slip push a line past the
	// terminal edge.
	return lipgloss.NewStyle().MaxWidth(cw).Render(body)
}

func (m DashboardModel) statCard(w int) string {
	// TotalEquity (free cash + mark-to-market of open positions) is the
	// real "am I profitable" number. Equity alone dips whenever a position
	// opens — that's capital deployed, not lost — and showing only that
	// number reads as a loss even when the portfolio is flat or up.
	total := m.snap.TotalEquity
	ret := 0.0
	if m.snap.StartEquity > 0 {
		ret = (total - m.snap.StartEquity) / m.snap.StartEquity * 100
	}
	retStyle := styleUp
	sign := "+"
	if ret < 0 {
		retStyle = styleDown
		sign = ""
	}
	content := lipgloss.JoinVertical(lipgloss.Left,
		styleLabel.Render("TOTAL EQUITY (SOL)"),
		styleBig.Render(fmt.Sprintf("%.3f", total)),
		styleDim.Render(fmt.Sprintf("start %.3f  ", m.snap.StartEquity))+retStyle.Render(fmt.Sprintf("%s%.2f%%", sign, ret)),
		styleDim.Render(fmt.Sprintf("free %.3f · %d open", m.snap.Equity, len(m.snap.OpenPos))),
	)
	return styleBox.Width(w).Render(content)
}

func (m DashboardModel) tradeCard(w int) string {
	winPct := 0.0
	if m.snap.Trades > 0 {
		winPct = float64(m.snap.Wins) / float64(m.snap.Trades) * 100
	}
	content := lipgloss.JoinVertical(lipgloss.Left,
		styleLabel.Render("TRADES"),
		styleBig.Render(fmt.Sprintf("%d", m.snap.Trades)),
		styleDim.Render(fmt.Sprintf("win %.0f%%  ", winPct))+
			styleUp.Render(fmt.Sprintf("%d W", m.snap.Wins))+" "+
			styleDown.Render(fmt.Sprintf("%d L", m.snap.Losses)),
	)
	return styleBox.Width(w).Render(content)
}

// pillWidth is sized for the widest status word the engine emits
// ("SELL_TIMEOUT" never appears here, worst case is "SCREENING"/"TRACKING").
const pillWidth = 21

func (m DashboardModel) agentRow(cw int) string {
	perPill := pillWidth + boxOverhead + 2 // pill box + join gap
	cols := min(max(cw/perPill, 1), len(agentOrder))

	var rows []string
	for i := 0; i < len(agentOrder); i += cols {
		end := min(i+cols, len(agentOrder))
		var pills []string
		for _, name := range agentOrder[i:end] {
			status := m.snap.AgentStatus[name]
			if status == "" {
				status = "IDLE"
			}
			c := statusColor(status)
			pill := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(c).
				Foreground(c).
				Padding(0, 1).
				Width(pillWidth).
				Render(fmt.Sprintf("%-9s %s", name, status))
			pills = append(pills, pill)
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, pills...))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m DashboardModel) scannerBox(w int) string {
	var b strings.Builder
	b.WriteString(styleBoxTitle.Render("SCANNER · new pump.fun mints") + "\n")
	if len(m.snap.Scans) == 0 {
		b.WriteString(styleDim.Render("waiting for the first token…"))
	}
	for _, s := range m.snap.Scans {
		tag := styleDown.Render("REJECT")
		detail := s.Reason
		if s.Passed {
			tag = styleUp.Render("PASS  ")
			if detail == "" {
				detail = "entered position"
			}
		}
		badge := "  "
		switch {
		case s.Follow:
			badge = lipgloss.NewStyle().Foreground(colGold).Render("CP")
		case s.AI:
			badge = lipgloss.NewStyle().Foreground(colPink).Render("AI")
		}
		sym := truncate(s.Symbol, 10)
		if sym == "" {
			sym = "?"
		}

		prefix := fmt.Sprintf("%s  %s %-10s dev %.2f  mcap %6.1f  %s  ",
			s.At.Format("15:04:05"), badge, sym, s.DevBuySOL, s.MarketCapSol, tag)
		maxDetail := max(w-2-lipgloss.Width(prefix), 6) // -2: styleBox's horizontal padding, already inside w
		b.WriteString(prefix)
		b.WriteString(truncate(detail, maxDetail))
		b.WriteString("\n")
	}
	return styleBox.Width(w).Height(6).Render(b.String())
}

// shortMint renders a mint address as "xxx...xxx" — enough to tell two
// same-named tokens apart (pump.fun enforces no symbol uniqueness) without
// eating the whole line.
func shortMint(addr string) string {
	if len(addr) <= 9 {
		return addr
	}
	return addr[:3] + "..." + addr[len(addr)-3:]
}

func (m DashboardModel) positionsBox(w int) string {
	var b strings.Builder
	b.WriteString(styleBoxTitle.Render("OPEN POSITIONS") + "\n")
	if len(m.snap.OpenPos) == 0 {
		b.WriteString(styleDim.Render("none"))
	}
	avail := w - 2 // styleBox's horizontal padding, already inside w
	for _, p := range m.snap.OpenPos {
		pnlStyle := styleUp
		if p.LastPnlPct < 0 {
			pnlStyle = styleDown
		}
		pnlPlain := fmt.Sprintf("%+.1f%%", p.LastPnlPct*100)
		pnlStyled := pnlStyle.Render(pnlPlain)

		sym := p.Symbol
		if sym == "" {
			sym = "?"
		}
		prefix := fmt.Sprintf("%s (%s) ", sym, shortMint(p.Mint))
		maxPrefix := max(avail-lipgloss.Width(pnlPlain), 4)
		if lipgloss.Width(prefix) > maxPrefix {
			// too narrow for symbol + mint both — drop the mint, keep the symbol
			prefix = truncate(sym, maxPrefix) + " "
		}
		b.WriteString(prefix)
		b.WriteString(pnlStyled)
		b.WriteString("\n")
	}
	return styleBox.Width(w).Height(8).Render(b.String())
}

func (m DashboardModel) feedBox(w int) string {
	var b strings.Builder
	b.WriteString(styleBoxTitle.Render("LOGBOOK") + "\n")
	if len(m.snap.Recent) == 0 {
		b.WriteString(styleDim.Render("waiting for signals…"))
	}
	for _, e := range m.snap.Recent {
		style := styleDim
		switch e.Kind {
		case "BUY":
			style = lipgloss.NewStyle().Foreground(colGold)
		case "SELL_TP":
			style = styleUp
		case "SELL_SL", "SELL_TIMEOUT":
			style = styleDown
		case "SELL_AI":
			style = styleUp
			if e.PnlSOL < 0 {
				style = styleDown
			}
		case "BUY_FAILED", "SELL_FAILED", "SELL_STUCK":
			style = styleError
		}
		line := fmt.Sprintf("%s  %-12s %s", e.At.Format("15:04:05"), e.Kind, truncate(e.Symbol, 12))
		switch {
		case e.Err != "":
			line += "  " + e.Err
		case e.Kind != "BUY":
			line += fmt.Sprintf("  %+.3f SOL", e.PnlSOL)
		}
		if e.Reason != "" {
			line += "  " + e.Reason
		}
		if e.Sig != "" {
			line += "  sig " + truncate(e.Sig, 10)
		}
		line = truncate(line, w-2)
		b.WriteString(style.Render(line) + "\n")
	}
	return styleBox.Width(w).Height(8).Render(b.String())
}

func (m DashboardModel) sparkBox(w int) string {
	line := sparkline(m.history, w)
	if line == "" {
		line = styleDim.Render("collecting equity history…")
	} else {
		line = lipgloss.NewStyle().Foreground(colMint).Render(line)
	}
	title := fmt.Sprintf("EQUITY TRACE · %s", time.Now().Format("15:04:05"))
	content := lipgloss.JoinVertical(lipgloss.Left, styleBoxTitle.Render(title), line)
	return styleBox.Width(w).Render(content)
}
