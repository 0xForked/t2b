package tui

import "github.com/charmbracelet/lipgloss"

var (
	colBg      = lipgloss.Color("#0A1612")
	colMint    = lipgloss.Color("#5EEAD4")
	colMintDim = lipgloss.Color("#2C6B62")
	colPink    = lipgloss.Color("#F472B6")
	colGold    = lipgloss.Color("#FBBF24")
	colRed     = lipgloss.Color("#F87171")
	colGreen   = lipgloss.Color("#4ADE80")
	colGray    = lipgloss.Color("#6B7B78")
	colWhite   = lipgloss.Color("#E7FBF7")

	styleTitle = lipgloss.NewStyle().Foreground(colMint).Bold(true)
	styleDim   = lipgloss.NewStyle().Foreground(colGray)
	styleLabel = lipgloss.NewStyle().Foreground(colGray)

	styleBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colMintDim).
			Padding(0, 1)

	styleBoxTitle = lipgloss.NewStyle().Foreground(colMint).Bold(true)

	styleBig = lipgloss.NewStyle().Foreground(colWhite).Bold(true)

	styleUp   = lipgloss.NewStyle().Foreground(colGreen).Bold(true)
	styleDown = lipgloss.NewStyle().Foreground(colRed).Bold(true)

	styleFocused = lipgloss.NewStyle().Foreground(colMint)
	styleError   = lipgloss.NewStyle().Foreground(colRed).Bold(true)
)

func statusColor(s string) lipgloss.Color {
	switch s {
	case "READY", "PASSED", "APPROVED", "FILLED", "AI_PASS", "CONFIRMED":
		return colMint
	case "LISTENING", "SCREENING", "SIZING", "BUYING", "TRACKING", "LOGGING", "SELLING", "WATCHING":
		return colGold
	case "THINKING":
		return colPink
	case "STRIKING":
		return colPink
	case "REJECTED", "SKIPPED", "AI_REJECT", "AI_BUDGET":
		return colGray
	case "ERROR", "AI_ERROR", "STUCK":
		return colRed
	default:
		return colGray
	}
}
