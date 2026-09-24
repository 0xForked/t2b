package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/aasumitro/iT2B/internal/config"
	"github.com/aasumitro/iT2B/internal/wallet"
)

type initStep int

const (
	stepWelcome initStep = iota
	stepWalletChoice
	stepImportKey
	stepPassphrase
	stepPassphraseConfirm
	stepPumpPortalKey
	stepRPC
	stepStartingEquity
	stepSaving
	stepDone
	stepError
)

type InitModel struct {
	step   initStep
	choice int // 0 = generate, 1 = import

	importInput textinput.Model
	passInput   textinput.Model
	pass2Input  textinput.Model
	keyInput    textinput.Model
	rpcInput    textinput.Model
	equityInput textinput.Model

	wallet *wallet.Wallet
	pass   string
	err    error

	width int
}

func mkInput(placeholder string, mask bool) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	// 500, not 200 — PumpPortal API keys observed at 200+ chars; 200 was
	// silently truncating them mid-paste with no error anywhere.
	ti.CharLimit = 500
	ti.Width = 60
	if mask {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
	}
	return ti
}

func NewInit() InitModel {
	m := InitModel{
		importInput: mkInput("base58 private key", true),
		passInput:   mkInput("passphrase", true),
		pass2Input:  mkInput("confirm passphrase", true),
		keyInput:    mkInput("PumpPortal API key (optional, blank = paper mode only)", false),
		rpcInput:    mkInput("RPC endpoint", false),
		equityInput: mkInput("starting paper equity in SOL", false),
	}
	m.rpcInput.SetValue("https://api.mainnet-beta.solana.com")
	m.equityInput.SetValue("5")
	m.step = stepWelcome
	return m
}

func (m InitModel) Init() tea.Cmd { return textinput.Blink }

// Done reports whether the wizard finished and wrote config+wallet to disk.
func (m InitModel) Done() bool { return m.step == stepDone }

func (m InitModel) focused() *textinput.Model {
	switch m.step {
	case stepImportKey:
		return &m.importInput
	case stepPassphrase:
		return &m.passInput
	case stepPassphraseConfirm:
		return &m.pass2Input
	case stepPumpPortalKey:
		return &m.keyInput
	case stepRPC:
		return &m.rpcInput
	case stepStartingEquity:
		return &m.equityInput
	}
	return nil
}

func (m InitModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			if m.step == stepDone || m.step == stepError {
				return m, tea.Quit
			}
		case "enter":
			if m.step == stepDone {
				return m, tea.Quit
			}
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m InitModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.step {
	case stepWelcome:
		if msg.String() == "enter" {
			m.step = stepWalletChoice
		}
		return m, nil

	case stepWalletChoice:
		switch msg.String() {
		case "up", "down", "tab":
			m.choice = 1 - m.choice
		case "enter":
			if m.choice == 0 {
				w, err := wallet.Generate()
				if err != nil {
					m.err = err
					m.step = stepError
					return m, nil
				}
				m.wallet = w
				m.step = stepPassphrase
				m.passInput.Focus()
				return m, textinput.Blink
			}
			m.step = stepImportKey
			m.importInput.Focus()
			return m, textinput.Blink
		}
		return m, nil

	case stepImportKey:
		if msg.String() == "enter" {
			w, err := wallet.ImportBase58(m.importInput.Value())
			if err != nil {
				m.err = err
				m.step = stepError
				return m, nil
			}
			m.wallet = w
			m.importInput.Blur()
			m.step = stepPassphrase
			m.passInput.Focus()
			return m, textinput.Blink
		}
		var cmd tea.Cmd
		m.importInput, cmd = m.importInput.Update(msg)
		return m, cmd

	case stepPassphrase:
		if msg.String() == "enter" && m.passInput.Value() != "" {
			m.passInput.Blur()
			m.step = stepPassphraseConfirm
			m.pass2Input.Focus()
			return m, textinput.Blink
		}
		var cmd tea.Cmd
		m.passInput, cmd = m.passInput.Update(msg)
		return m, cmd

	case stepPassphraseConfirm:
		if msg.String() == "enter" {
			if m.pass2Input.Value() != m.passInput.Value() {
				m.err = fmt.Errorf("passphrases do not match")
				m.step = stepError
				return m, nil
			}
			m.pass = m.passInput.Value()
			m.pass2Input.Blur()
			m.step = stepPumpPortalKey
			m.keyInput.Focus()
			return m, textinput.Blink
		}
		var cmd tea.Cmd
		m.pass2Input, cmd = m.pass2Input.Update(msg)
		return m, cmd

	case stepPumpPortalKey:
		if msg.String() == "enter" {
			m.keyInput.Blur()
			m.step = stepRPC
			m.rpcInput.Focus()
			return m, textinput.Blink
		}
		var cmd tea.Cmd
		m.keyInput, cmd = m.keyInput.Update(msg)
		return m, cmd

	case stepRPC:
		if msg.String() == "enter" {
			m.rpcInput.Blur()
			m.step = stepStartingEquity
			m.equityInput.Focus()
			return m, textinput.Blink
		}
		var cmd tea.Cmd
		m.rpcInput, cmd = m.rpcInput.Update(msg)
		return m, cmd

	case stepStartingEquity:
		if msg.String() == "enter" {
			m.equityInput.Blur()
			return m.save()
		}
		var cmd tea.Cmd
		m.equityInput, cmd = m.equityInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m InitModel) save() (tea.Model, tea.Cmd) {
	if err := m.wallet.Save(m.pass); err != nil {
		m.err = err
		m.step = stepError
		return m, nil
	}
	var startEquity float64
	fmt.Sscanf(m.equityInput.Value(), "%f", &startEquity)
	if startEquity <= 0 {
		startEquity = 5
	}
	cfg := &config.Config{
		RPCEndpoint:    m.rpcInput.Value(),
		PumpPortalKey:  m.keyInput.Value(),
		PaperMode:      true,
		StartingEquity: startEquity,
		WalletPubkey:   m.wallet.PublicBase58(),
	}
	if err := config.Save(cfg); err != nil {
		m.err = err
		m.step = stepError
		return m, nil
	}
	m.step = stepDone
	return m, nil
}

var wizardBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colMintDim).Padding(1, 2).Width(72)

func (m InitModel) View() string {
	switch m.step {
	case stepWelcome:
		return wizardBox.Render(lipgloss.JoinVertical(lipgloss.Left,
			styleTitle.Render("T2B init — pump.fun agentic trading desk"),
			"",
			styleDim.Render("Sets up an encrypted wallet + paper-trading config."),
			styleDim.Render("Nothing here touches real funds until you flip to live mode."),
			"",
			"enter  continue",
		))

	case stepWalletChoice:
		opts := []string{"Generate a new wallet", "Import an existing private key"}
		var lines []string
		for i, o := range opts {
			if i == m.choice {
				lines = append(lines, styleFocused.Render("› "+o))
			} else {
				lines = append(lines, styleDim.Render("  "+o))
			}
		}
		return wizardBox.Render(lipgloss.JoinVertical(lipgloss.Left,
			styleTitle.Render("Wallet"), "",
			lipgloss.JoinVertical(lipgloss.Left, lines...), "",
			styleDim.Render("↑/↓ select · enter confirm"),
		))

	case stepImportKey:
		return wizardBox.Render(lipgloss.JoinVertical(lipgloss.Left,
			styleTitle.Render("Import wallet"), "",
			styleLabel.Render("Base58 private key (solana-cli / Phantom export format):"),
			m.importInput.View(), "",
			styleDim.Render("enter  continue"),
		))

	case stepPassphrase:
		return wizardBox.Render(lipgloss.JoinVertical(lipgloss.Left,
			styleTitle.Render("Encrypt wallet"), "",
			styleLabel.Render(fmt.Sprintf("Public key: %s", m.wallet.PublicBase58())),
			styleLabel.Render("Choose a passphrase to encrypt the key at rest:"),
			m.passInput.View(), "",
			styleDim.Render("enter  continue"),
		))

	case stepPassphraseConfirm:
		return wizardBox.Render(lipgloss.JoinVertical(lipgloss.Left,
			styleTitle.Render("Confirm passphrase"), "",
			m.pass2Input.View(), "",
			styleDim.Render("enter  continue"),
		))

	case stepPumpPortalKey:
		return wizardBox.Render(lipgloss.JoinVertical(lipgloss.Left,
			styleTitle.Render("PumpPortal API key"), "",
			styleLabel.Render("Only needed for live trade execution later."),
			styleLabel.Render("Leave blank to run paper mode off the public data feed."),
			m.keyInput.View(), "",
			styleDim.Render("enter  continue"),
		))

	case stepRPC:
		return wizardBox.Render(lipgloss.JoinVertical(lipgloss.Left,
			styleTitle.Render("Solana RPC endpoint"), "",
			m.rpcInput.View(), "",
			styleDim.Render("enter  continue"),
		))

	case stepStartingEquity:
		return wizardBox.Render(lipgloss.JoinVertical(lipgloss.Left,
			styleTitle.Render("Starting paper equity"), "",
			m.equityInput.View(), "",
			styleDim.Render("enter  finish"),
		))

	case stepDone:
		return wizardBox.Render(lipgloss.JoinVertical(lipgloss.Left,
			styleTitle.Render("✓ Setup complete"), "",
			styleLabel.Render("Wallet:  ")+m.wallet.PublicBase58(),
			styleLabel.Render("Mode:    paper"), "",
			styleDim.Render("enter  open dashboard · q  quit"),
		))

	case stepError:
		msg := ""
		if m.err != nil {
			msg = m.err.Error()
		}
		return wizardBox.Render(lipgloss.JoinVertical(lipgloss.Left,
			styleError.Render("Error"), "",
			msg, "",
			styleDim.Render("q  quit"),
		))
	}
	return ""
}
