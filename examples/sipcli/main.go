// sipcli is an interactive SIP client built with the xphone library.
// It demonstrates registration, inbound/outbound calls, hold, DTMF,
// mute, blind transfer, echo mode — all driven by the event-based API.
//
// Usage:
//
//	go run ./examples/sipcli -server pbx.example.com -user 1001 -pass secret
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xphone "github.com/x-phone/xphone-go"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Colors
// ---------------------------------------------------------------------------

var (
	accent    = lipgloss.Color("#6495ED") // cornflower blue
	accentDim = lipgloss.Color("#4A6FA5")
	green     = lipgloss.Color("#00FF00")
	red       = lipgloss.Color("#FF0000")
	yellow    = lipgloss.Color("#FFFF00")
	magenta   = lipgloss.Color("#FF00FF")
	dim       = lipgloss.Color("#666666")
	surface   = lipgloss.Color("#1a1a2e")
	barBG     = lipgloss.Color("#16213e")
	white     = lipgloss.Color("#FFFFFF")
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	maxEvents    = 200
	maxDebugLogs = 500
	maxHistory   = 500
	echoDelay    = 10 // frames (~200ms at 20ms/frame)
)

// ---------------------------------------------------------------------------
// App state (shared between TUI and xphone callbacks via prog.Send)
// ---------------------------------------------------------------------------

// trackedCall tracks one call in the calls panel.
type trackedCall struct {
	call   xphone.Call
	label  string
	status string
}

// blfEntry tracks one watched extension in the BLF panel.
type blfEntry struct {
	extension string
	state     xphone.ExtensionState
}

// prog is the running bubbletea program. Used by xphone callbacks and the
// custom slog handler to send messages into the TUI from any goroutine.
var prog *tea.Program

// --- messages ---

type msgLog string
type msgDebugLog string
type msgRegState string
type msgCallState struct {
	callID string
	status string
}
type msgCallEnded struct{ callID string }
type msgCallRef struct{ call xphone.Call }
type msgWatchAdded struct {
	ext string
	id  string
}
type msgBLFUpdate struct {
	ext   string
	state xphone.ExtensionState
}

// ---------------------------------------------------------------------------
// Custom slog handler that feeds into the TUI debug panel
// ---------------------------------------------------------------------------

type tuiHandler struct{ level slog.Level }

func (h *tuiHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *tuiHandler) Handle(_ context.Context, r slog.Record) error {
	if prog == nil {
		return nil
	}
	var b strings.Builder
	switch {
	case r.Level >= slog.LevelError:
		b.WriteString("ERR ")
	case r.Level >= slog.LevelWarn:
		b.WriteString("WRN ")
	case r.Level >= slog.LevelInfo:
		b.WriteString("INF ")
	default:
		b.WriteString("DBG ")
	}
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(a.Value.String())
		return true
	})
	prog.Send(msgDebugLog(flattenLine(b.String())))
	return nil
}

func (h *tuiHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *tuiHandler) WithGroup(_ string) slog.Handler      { return h }

// flattenLine collapses newlines into " | " for single-line display.
func flattenLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " | ")
	s = strings.ReplaceAll(s, "\n", " | ")
	s = strings.ReplaceAll(s, "\r", " | ")
	return s
}

// tuiWriter adapts the standard log package output into the TUI debug panel.
// This captures sipgo's transport-level SIP message logging.
type tuiWriter struct{}

func (w tuiWriter) Write(p []byte) (int, error) {
	if prog == nil {
		return len(p), nil
	}
	line := flattenLine(strings.TrimRight(string(p), "\n\r"))
	if line != "" {
		prog.Send(msgDebugLog("SIP " + line))
	}
	return len(p), nil
}

// ---------------------------------------------------------------------------
// Command history (persisted to ~/.sipcli_history)
// ---------------------------------------------------------------------------

func historyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".sipcli_history")
}

func loadHistory() []string {
	p := historyPath()
	if p == "" {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > maxHistory {
		lines = lines[len(lines)-maxHistory:]
	}
	return lines
}

func saveHistory(history []string) {
	p := historyPath()
	if p == "" {
		return
	}
	h := history
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	f, err := os.Create(p)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, line := range h {
		w.WriteString(line)
		w.WriteByte('\n')
	}
	w.Flush()
}

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type model struct {
	phone xphone.Phone

	regStatus string
	calls     []trackedCall
	selected  int
	events    []string
	debugLogs []string
	input     string
	err       string
	quitting  bool
	width     int
	height    int

	// Audio handler for speaker/mic/echo (nil if no active call)
	audio *audioHandler

	// BLF state
	blf     []blfEntry        // watched extensions for BLF panel
	watches map[string]string // subscription IDs keyed by extension

	// Command history
	history      []string
	historyPos   int // -1 = not browsing
	historyDraft string
}

func initialModel(phone xphone.Phone) model {
	return model{
		phone:      phone,
		regStatus:  "disconnected",
		width:      80,
		height:     24,
		watches:    make(map[string]string),
		history:    loadHistory(),
		historyPos: -1,
	}
}

func (m model) Init() tea.Cmd { return nil }

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		return m.handleKey(msg)
	case msgLog:
		m.pushEvent(string(msg))
	case msgDebugLog:
		m.debugLogs = append(m.debugLogs, string(msg))
		if len(m.debugLogs) > maxDebugLogs {
			m.debugLogs = m.debugLogs[len(m.debugLogs)-maxDebugLogs:]
		}
	case msgRegState:
		m.regStatus = string(msg)
	case msgCallState:
		for i := range m.calls {
			if m.calls[i].call.CallID() == msg.callID {
				m.calls[i].status = msg.status
				// Auto-start audio (speaker ON) when a call becomes active
				if msg.status == "active" && m.audio == nil {
					m.audio = newAudioHandler(m.calls[i].call)
					m.pushEvent("speaker: ON (auto)")
				}
				break
			}
		}
	case msgCallEnded:
		for i := range m.calls {
			if m.calls[i].call.CallID() == msg.callID {
				// Stop audio if it was for this call
				if m.audio != nil && m.audio.call.CallID() == msg.callID {
					m.stopAudio()
				}
				m.calls = append(m.calls[:i], m.calls[i+1:]...)
				if m.selected >= len(m.calls) && m.selected > 0 {
					m.selected = len(m.calls) - 1
				}
				break
			}
		}
	case msgWatchAdded:
		m.watches[msg.ext] = msg.id
		// Add to BLF list with Unknown state (will be updated by first NOTIFY).
		m.blf = append(m.blf, blfEntry{extension: msg.ext, state: xphone.ExtensionUnknown})
	case msgBLFUpdate:
		for i := range m.blf {
			if m.blf[i].extension == msg.ext {
				m.blf[i].state = msg.state
				break
			}
		}
	case msgCallRef:
		isFirst := len(m.calls) == 0
		m.calls = append(m.calls, trackedCall{
			call:   msg.call,
			label:  callLabel(msg.call),
			status: callStateName(msg.call.State()),
		})
		if isFirst {
			m.selected = 0
		}
	}
	return m, nil
}

func callLabel(c xphone.Call) string {
	if c.Direction() == xphone.DirectionOutbound {
		return c.To()
	}
	from := c.From()
	if name := c.FromName(); name != "" {
		return fmt.Sprintf("%s (%s)", name, from)
	}
	return from
}

func (m *model) pushEvent(s string) {
	m.events = append(m.events, s)
	if len(m.events) > maxEvents {
		m.events = m.events[len(m.events)-maxEvents:]
	}
}

// ---------------------------------------------------------------------------
// Key handling
// ---------------------------------------------------------------------------

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m.shutdown()
	case tea.KeyEscape:
		m.input = ""
	case tea.KeyEnter:
		cmd := strings.TrimSpace(m.input)
		m.input = ""
		m.err = ""
		m.historyPos = -1
		m.historyDraft = ""
		if cmd != "" {
			m.history = append(m.history, cmd)
			return m.execCommand(cmd)
		}
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	case tea.KeyUp:
		m.historyUp()
	case tea.KeyDown:
		m.historyDown()
	default:
		if msg.Type == tea.KeyRunes {
			m.input += string(msg.Runes)
		} else if msg.Type == tea.KeySpace {
			m.input += " "
		}
	}
	return m, nil
}

func (m *model) historyUp() {
	if len(m.history) == 0 {
		return
	}
	if m.historyPos == -1 {
		m.historyDraft = m.input
		m.historyPos = len(m.history) - 1
	} else if m.historyPos > 0 {
		m.historyPos--
	}
	m.input = m.history[m.historyPos]
}

func (m *model) historyDown() {
	if m.historyPos == -1 {
		return
	}
	if m.historyPos < len(m.history)-1 {
		m.historyPos++
		m.input = m.history[m.historyPos]
	} else {
		m.historyPos = -1
		m.input = m.historyDraft
		m.historyDraft = ""
	}
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

var (
	borderStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(accentDim)

	titleStyle = lipgloss.NewStyle().
			Foreground(accent).
			Bold(true)

	dimStyle    = lipgloss.NewStyle().Foreground(dim)
	whiteStyle  = lipgloss.NewStyle().Foreground(white)
	promptStyle = lipgloss.NewStyle().Foreground(green).Bold(true)
	cursorStyle = lipgloss.NewStyle().Foreground(green).Blink(true)
	errStyle    = lipgloss.NewStyle().Foreground(red).Bold(true)

	// Event/debug color styles (cached to avoid per-line allocations)
	redStyle      = lipgloss.NewStyle().Foreground(red)
	yellowStyle   = lipgloss.NewStyle().Foreground(yellow)
	greenStyle    = lipgloss.NewStyle().Foreground(green)
	magentaStyle  = lipgloss.NewStyle().Foreground(magenta)
	accentStyle   = lipgloss.NewStyle().Foreground(accent)
	incomingStyle = lipgloss.NewStyle().Foreground(yellow).Bold(true)
)

func (m model) View() string {
	if m.quitting {
		return "Bye!\n"
	}

	w := m.width
	h := m.height

	// --- Status bar ---
	statusBar := m.renderStatusBar(w)

	// --- Command area (5 lines with border) ---
	cmdArea := m.renderCommandArea(w)
	cmdH := lipgloss.Height(cmdArea)

	// Available height for panels
	statusH := lipgloss.Height(statusBar)
	panelH := h - statusH - cmdH
	if panelH < 4 {
		panelH = 4
	}

	// --- Left panels (40%) ---
	leftW := w * 40 / 100
	if leftW < 20 {
		leftW = 20
	}
	rightW := w - leftW
	if rightW < 20 {
		rightW = 20
	}

	// Calls panel height: dynamic based on number of calls
	callsInnerH := len(m.calls)
	if callsInnerH == 0 {
		callsInnerH = 1
	}
	if callsInnerH > 6 {
		callsInnerH = 6
	}
	callsPanelH := callsInnerH + 2 // +2 for border

	// BLF panel height: conditional, only when watching extensions
	blfPanelH := 0
	if len(m.blf) > 0 {
		blfInnerH := len(m.blf)
		if blfInnerH > 4 {
			blfInnerH = 4
		}
		blfPanelH = blfInnerH + 2 // +2 for border
	}

	eventsPanelH := panelH - callsPanelH - blfPanelH
	if eventsPanelH < 4 {
		eventsPanelH = 4
		callsPanelH = panelH - eventsPanelH - blfPanelH
	}

	callsPanel := m.renderCallsPanel(leftW, callsPanelH)
	var leftPanels []string
	leftPanels = append(leftPanels, callsPanel)
	if blfPanelH > 0 {
		leftPanels = append(leftPanels, m.renderBLFPanel(leftW, blfPanelH))
	}
	leftPanels = append(leftPanels, m.renderEventsPanel(leftW, eventsPanelH))
	leftColumn := lipgloss.NewStyle().Height(panelH).Render(
		lipgloss.JoinVertical(lipgloss.Left, leftPanels...),
	)

	// --- Right panel (60%) ---
	debugPanel := lipgloss.NewStyle().Height(panelH).Render(
		m.renderDebugPanel(rightW, panelH),
	)

	// --- Compose ---
	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, debugPanel)

	return lipgloss.JoinVertical(lipgloss.Left, statusBar, panels, cmdArea)
}

func (m model) renderStatusBar(w int) string {
	regDot, regColor := regStatusIndicator(m.regStatus)

	colorStyle := lipgloss.NewStyle().Foreground(regColor)
	regLine := dimStyle.Render("  REG") +
		colorStyle.Render(fmt.Sprintf(" %s ", regDot)) +
		colorStyle.Bold(true).Render(m.regStatus)

	return borderStyle.
		Width(w-2).
		Background(barBG).
		Padding(0, 0).
		Render(
			titleStyle.Render(" sipcli (go) ") + "\n" + regLine,
		)
}

func regStatusIndicator(status string) (string, lipgloss.Color) {
	switch status {
	case "registered":
		return "●", green
	case "registering":
		return "◌", yellow
	case "error", "failed":
		return "●", red
	default:
		return "○", dim
	}
}

func (m model) renderCallsPanel(w, h int) string {
	innerW := w - 2
	innerH := h - 2
	if innerH < 1 {
		innerH = 1
	}

	var lines []string
	if len(m.calls) == 0 {
		lines = append(lines, dimStyle.Render("  (no calls)"))
	} else {
		for i, tc := range m.calls {
			selected := i == m.selected
			color, indicator := callStatusStyle(tc.status)

			marker := " "
			markerColor := dim
			if selected {
				marker = ">"
				markerColor = green
			}

			statusStyle := lipgloss.NewStyle().Foreground(color)
			line := lipgloss.NewStyle().Foreground(markerColor).Bold(selected).Render(fmt.Sprintf(" %s", marker)) +
				dimStyle.Render(fmt.Sprintf("#%d ", i+1)) +
				statusStyle.Render(fmt.Sprintf("%s ", indicator)) +
				whiteStyle.Bold(selected).Render(tc.label) +
				statusStyle.Render(fmt.Sprintf("  %s", tc.status))

			lines = append(lines, truncate(line, innerW))
		}
	}

	// Pad to innerH
	for len(lines) < innerH {
		lines = append(lines, "")
	}
	if len(lines) > innerH {
		lines = lines[len(lines)-innerH:]
	}

	content := strings.Join(lines, "\n")
	return borderStyle.
		Width(innerW).
		Height(innerH).
		Background(surface).
		Render(titleStyle.Render(" Calls ") + "\n" + content)
}

func (m model) renderBLFPanel(w, h int) string {
	innerW := w - 2
	innerH := h - 2
	if innerH < 1 {
		innerH = 1
	}

	var lines []string
	for _, b := range m.blf {
		indicator, style := blfIndicator(b.state)
		line := "  " + style.Render(indicator) + " " + whiteStyle.Render(b.extension) +
			"  " + style.Render(extStateName(b.state))
		lines = append(lines, truncate(line, innerW))
	}

	for len(lines) < innerH {
		lines = append(lines, "")
	}
	if len(lines) > innerH {
		lines = lines[:innerH]
	}

	content := strings.Join(lines, "\n")
	return borderStyle.
		Width(innerW).
		Height(innerH).
		Background(surface).
		Render(titleStyle.Render(" BLF ") + "\n" + content)
}

func blfIndicator(state xphone.ExtensionState) (string, lipgloss.Style) {
	switch state {
	case xphone.ExtensionAvailable:
		return "●", greenStyle
	case xphone.ExtensionRinging:
		return "◌", yellowStyle
	case xphone.ExtensionOnThePhone:
		return "●", redStyle
	case xphone.ExtensionOffline:
		return "○", dimStyle
	default:
		return "○", dimStyle
	}
}

func (m model) renderEventsPanel(w, h int) string {
	return renderLogPanel(" Events ", m.events, w, h, styleEvent)
}

func (m model) renderDebugPanel(w, h int) string {
	return renderLogPanel(" SIP Debug ", m.debugLogs, w, h, styleDebug)
}

func renderLogPanel(title string, logs []string, w, h int, styleFn func(string) string) string {
	innerW := w - 2
	innerH := h - 2
	if innerH < 1 {
		innerH = 1
	}

	// Expand log entries into visual lines with soft wrapping,
	// working backwards from the end so we fill exactly innerH lines.
	var visual []string
	for i := len(logs) - 1; i >= 0 && len(visual) < innerH*2; i-- {
		wrapped := wrapLine(logs[i], innerW)
		// Prepend wrapped lines (reversed, we'll reverse later)
		for j := len(wrapped) - 1; j >= 0; j-- {
			visual = append(visual, styleFn(wrapped[j]))
		}
	}
	// Reverse to get chronological order
	for i, j := 0, len(visual)-1; i < j; i, j = i+1, j-1 {
		visual[i], visual[j] = visual[j], visual[i]
	}
	// Take last innerH lines
	if len(visual) > innerH {
		visual = visual[len(visual)-innerH:]
	}
	// Pad
	for len(visual) < innerH {
		visual = append(visual, "")
	}

	content := strings.Join(visual, "\n")
	return borderStyle.
		Width(innerW).
		Height(innerH).
		Background(surface).
		Render(titleStyle.Render(title) + "\n" + content)
}

// wrapLine splits a string into lines that fit within maxW visible characters.
// Continuation lines are indented with 2 spaces.
func wrapLine(s string, maxW int) []string {
	if maxW < 5 {
		maxW = 5
	}
	if lipgloss.Width(s) <= maxW {
		return []string{s}
	}
	var result []string
	runes := []rune(s)
	for len(runes) > 0 {
		// Find the cut point
		cut := len(runes)
		if lipgloss.Width(string(runes)) > maxW {
			// Binary-ish search for the right cut
			cut = maxW
			if cut > len(runes) {
				cut = len(runes)
			}
			for cut > 0 && lipgloss.Width(string(runes[:cut])) > maxW {
				cut--
			}
			if cut == 0 {
				cut = 1 // always make progress
			}
		}
		result = append(result, string(runes[:cut]))
		runes = runes[cut:]
		if len(runes) > 0 {
			// Indent continuation lines
			runes = append([]rune("  "), runes...)
		}
	}
	return result
}

func (m model) renderCommandArea(w int) string {
	innerW := w - 2

	inputLine := promptStyle.Render(" > ") + m.input + cursorStyle.Render("_")

	helpText := "dial(d) accept(a) reject hangup(h) hold resume mute unmute dtmf transfer(xfer) msg watch(w) unwatch(uw) echo speaker mic quit(q)"
	helpLine := "   " + dimStyle.Render(helpText)

	cmdTitle := titleStyle.Render(" Command ")
	if m.err != "" {
		cmdTitle = errStyle.Render(fmt.Sprintf(" Command  --  %s ", m.err))
	}

	content := inputLine + "\n\n" + truncate(helpLine, innerW)

	return borderStyle.
		Width(innerW).
		Background(surface).
		Render(cmdTitle + "\n" + content)
}

// ---------------------------------------------------------------------------
// Style helpers
// ---------------------------------------------------------------------------

func callStatusStyle(status string) (lipgloss.Color, string) {
	switch status {
	case "active":
		return green, "●"
	case "ringing", "ringing remote", "dialing", "early media":
		return yellow, "◌"
	case "on hold":
		return magenta, "◊"
	default:
		return dim, "○"
	}
}

func styleEvent(line string) string {
	// Strip "[label] " prefix for matching.
	content := line
	if strings.HasPrefix(line, "[") {
		if i := strings.Index(line, "] "); i >= 0 {
			content = line[i+2:]
		}
	}

	switch {
	case strings.HasPrefix(content, "ERROR"):
		return redStyle.Render(line)
	case strings.HasPrefix(content, "ended:"):
		return accentStyle.Render(line)
	case strings.HasPrefix(content, "incoming"):
		return incomingStyle.Render(line)
	case strings.HasPrefix(content, "active"), strings.HasPrefix(content, "connected"):
		return greenStyle.Render(line)
	case strings.HasPrefix(content, "DTMF"):
		return magentaStyle.Render(line)
	case strings.HasPrefix(content, "MSG from"):
		return incomingStyle.Render(line)
	case strings.HasPrefix(content, "BLF"):
		return magentaStyle.Render(line)
	case strings.HasPrefix(content, "watching"):
		return accentStyle.Render(line)
	case strings.HasPrefix(content, "unwatched"):
		return accentStyle.Render(line)
	case strings.HasPrefix(content, "dialing"),
		strings.HasPrefix(content, "ringing"),
		strings.HasPrefix(content, "early media"):
		return yellowStyle.Render(line)
	case strings.HasPrefix(content, "held"), strings.HasPrefix(content, "resumed"):
		return magentaStyle.Render(line)
	case strings.HasPrefix(content, "registered"):
		return greenStyle.Render(line)
	default:
		return whiteStyle.Render(line)
	}
}

func styleDebug(line string) string {
	switch {
	case strings.HasPrefix(line, "ERR"):
		return redStyle.Render(line)
	case strings.HasPrefix(line, "WRN"):
		return yellowStyle.Render(line)
	case strings.HasPrefix(line, "INF"):
		return accentStyle.Render(line)
	default:
		return dimStyle.Render(line)
	}
}

// truncate cuts a string to fit within maxW visible characters,
// accounting for ANSI escape sequences.
func truncate(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	// Cut runes until we fit; ANSI codes are zero-width.
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)) > maxW {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

// ---------------------------------------------------------------------------
// Command dispatch
// ---------------------------------------------------------------------------

func (m model) execCommand(input string) (model, tea.Cmd) {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return m, nil
	}
	cmd := strings.ToLower(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = strings.Join(parts[1:], " ")
	}

	switch cmd {
	case "quit", "q", "exit":
		return m.shutdown()

	case "dial", "d":
		if arg == "" {
			m.err = "usage: dial <target>"
			return m, nil
		}
		m.pushEvent(fmt.Sprintf("dialing %s...", arg))
		return m, m.cmdDial(arg)

	case "accept", "a":
		num := parseCallNum(arg)
		return m, callAction(&m, "accept", num, func(c xphone.Call) error {
			return c.Accept()
		})

	case "reject":
		num := parseCallNum(arg)
		return m, callAction(&m, "reject", num, func(c xphone.Call) error {
			return c.Reject(486, "Busy Here")
		})

	case "hangup", "h":
		num := parseCallNum(arg)
		return m, callAction(&m, "hangup", num, func(c xphone.Call) error {
			return c.End()
		})

	case "hold":
		num := parseCallNum(arg)
		return m, callAction(&m, "hold", num, func(c xphone.Call) error {
			return c.Hold()
		})

	case "resume":
		num := parseCallNum(arg)
		return m, callAction(&m, "resume", num, func(c xphone.Call) error {
			return c.Resume()
		})

	case "mute":
		num := parseCallNum(arg)
		return m, callAction(&m, "mute", num, func(c xphone.Call) error {
			return c.Mute()
		})

	case "unmute":
		num := parseCallNum(arg)
		return m, callAction(&m, "unmute", num, func(c xphone.Call) error {
			return c.Unmute()
		})

	case "dtmf":
		if arg == "" {
			m.err = "usage: dtmf <digits>"
			return m, nil
		}
		c := m.resolveCall(-1)
		if c == nil {
			m.err = "no active call"
			return m, nil
		}
		for _, ch := range arg {
			if err := c.SendDTMF(string(ch)); err != nil {
				m.err = fmt.Sprintf("dtmf error: %s", err)
				return m, nil
			}
		}
		m.pushEvent(fmt.Sprintf("DTMF sent: %s", arg))
		return m, nil

	case "transfer", "xfer":
		if arg == "" {
			m.err = "usage: transfer <target>"
			return m, nil
		}
		target := arg
		return m, callAction(&m, "transfer to "+arg, -1, func(c xphone.Call) error {
			return c.BlindTransfer(target)
		})

	case "msg":
		// msg <target> <body...>
		if len(parts) < 3 {
			m.err = "usage: msg <target> <message>"
			return m, nil
		}
		target := parts[1]
		body := strings.Join(parts[2:], " ")
		m.pushEvent(fmt.Sprintf("sending message to %s...", target))
		ph := m.phone
		return m, func() tea.Msg {
			if err := ph.SendMessage(context.Background(), target, body); err != nil {
				return msgLog(fmt.Sprintf("ERROR msg: %s", err))
			}
			return msgLog(fmt.Sprintf("message sent to %s", target))
		}

	case "watch", "w":
		if arg == "" {
			m.err = "usage: watch <extension>"
			return m, nil
		}
		ext := arg
		if _, ok := m.watches[ext]; ok {
			m.err = fmt.Sprintf("already watching %s", ext)
			return m, nil
		}
		m.pushEvent(fmt.Sprintf("watching %s...", ext))
		ph := m.phone
		return m, func() tea.Msg {
			id, err := ph.Watch(context.Background(), ext, func(extension string, state, prev xphone.ExtensionState) {
				prog.Send(msgBLFUpdate{ext: extension, state: state})
				prog.Send(msgLog(fmt.Sprintf("BLF %s: %s (was %s)", extension, extStateName(state), extStateName(prev))))
			})
			if err != nil {
				return msgLog(fmt.Sprintf("ERROR watch %s: %s", ext, err))
			}
			prog.Send(msgLog(fmt.Sprintf("watching %s", ext)))
			return msgWatchAdded{ext: ext, id: id}
		}

	case "unwatch", "uw":
		if arg == "" {
			m.err = "usage: unwatch <extension>"
			return m, nil
		}
		ext := arg
		id, ok := m.watches[ext]
		if !ok {
			m.err = fmt.Sprintf("not watching %s", ext)
			return m, nil
		}
		delete(m.watches, ext)
		// Remove from BLF list.
		for i := range m.blf {
			if m.blf[i].extension == ext {
				m.blf = append(m.blf[:i], m.blf[i+1:]...)
				break
			}
		}
		ph := m.phone
		return m, func() tea.Msg {
			if err := ph.Unwatch(id); err != nil {
				return msgLog(fmt.Sprintf("ERROR unwatch %s: %s", ext, err))
			}
			return msgLog(fmt.Sprintf("unwatched %s", ext))
		}

	case "echo":
		m.ensureAudio()
		if m.audio == nil {
			m.err = "no active call"
			return m, nil
		}
		on := !m.audio.EchoActive()
		m.audio.SetEcho(on)
		if on {
			m.pushEvent("echo: ON")
			if m.audio.MicActive() {
				m.pushEvent("mic: OFF (echo takes priority)")
			}
		} else {
			m.pushEvent("echo: OFF")
		}
		return m, nil

	case "speaker":
		m.ensureAudio()
		if m.audio == nil {
			m.err = "no active call"
			return m, nil
		}
		on := !m.audio.SpeakerActive()
		m.audio.SetSpeaker(on)
		if on {
			m.pushEvent("speaker: ON")
		} else {
			m.pushEvent("speaker: OFF")
		}
		return m, nil

	case "mic":
		m.ensureAudio()
		if m.audio == nil {
			m.err = "no active call"
			return m, nil
		}
		on := !m.audio.MicActive()
		m.audio.SetMic(on)
		if on {
			m.pushEvent("mic: ON")
			if m.audio.EchoActive() {
				m.pushEvent("echo: OFF (mic takes priority)")
			}
		} else {
			m.pushEvent("mic: OFF")
		}
		return m, nil

	default:
		// Try to select a call by number
		if n, err := strconv.Atoi(cmd); err == nil {
			if n >= 1 && n <= len(m.calls) {
				m.selected = n - 1
			} else {
				m.err = fmt.Sprintf("no call #%d", n)
			}
			return m, nil
		}
		m.err = fmt.Sprintf("unknown command: %s", cmd)
		return m, nil
	}
}

func parseCallNum(arg string) int {
	if arg == "" {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil {
		return -1
	}
	return n
}

func (m *model) resolveCall(num int) xphone.Call {
	if len(m.calls) == 0 {
		return nil
	}
	idx := m.selected
	if num >= 1 && num <= len(m.calls) {
		idx = num - 1
	} else if num >= 1 {
		return nil
	}
	if idx >= 0 && idx < len(m.calls) {
		return m.calls[idx].call
	}
	return nil
}

func callAction(m *model, name string, num int, fn func(xphone.Call) error) tea.Cmd {
	c := m.resolveCall(num)
	if c == nil {
		if num >= 1 {
			m.err = fmt.Sprintf("no call #%d", num)
		} else {
			m.err = "no active call"
		}
		return nil
	}
	return func() tea.Msg {
		if err := fn(c); err != nil {
			return msgLog(fmt.Sprintf("ERROR %s: %s", name, err))
		}
		return nil
	}
}

func (m model) cmdDial(target string) tea.Cmd {
	phone := m.phone
	return func() tea.Msg {
		call, err := phone.Dial(context.Background(), target,
			xphone.WithDialTimeout(30*time.Second),
		)
		if err != nil {
			return msgLog(fmt.Sprintf("ERROR dial failed: %s", err))
		}
		wireCallEvents(call)
		return msgCallRef{call: call}
	}
}

// ---------------------------------------------------------------------------
// Audio management
// ---------------------------------------------------------------------------

// ensureAudio starts the audio handler for the selected call if not already running.
func (m *model) ensureAudio() {
	c := m.resolveCall(-1)
	if c == nil {
		return
	}
	if m.audio != nil {
		return
	}
	m.audio = newAudioHandler(c)
}

// shutdown cleanly stops audio, ends all calls, and disconnects the phone.
func (m model) shutdown() (model, tea.Cmd) {
	m.quitting = true
	m.stopAudio()
	calls := m.calls
	phone := m.phone
	return m, tea.Batch(tea.Quit, func() tea.Msg {
		for _, tc := range calls {
			tc.call.End()
		}
		phone.Disconnect()
		return nil
	})
}

// stopAudio stops and cleans up the audio handler.
func (m *model) stopAudio() {
	if m.audio != nil {
		m.audio.Stop()
		m.audio = nil
	}
}

// ---------------------------------------------------------------------------
// xphone event wiring
// ---------------------------------------------------------------------------

func wirePhoneEvents(phone xphone.Phone) {
	phone.OnRegistered(func() {
		prog.Send(msgRegState("registered"))
		prog.Send(msgLog("registered with server"))
	})
	phone.OnUnregistered(func() {
		prog.Send(msgRegState("unregistered"))
		prog.Send(msgLog("registration lost"))
	})
	phone.OnError(func(err error) {
		prog.Send(msgRegState("error"))
		prog.Send(msgLog(fmt.Sprintf("ERROR %s", err)))
	})
	phone.OnIncoming(func(call xphone.Call) {
		wireCallEvents(call)
		prog.Send(msgLog(fmt.Sprintf("incoming from %s — type 'accept' or 'reject'", callLabel(call))))
		prog.Send(msgCallRef{call: call})
	})
	phone.OnMessage(func(msg xphone.SipMessage) {
		prog.Send(msgLog(fmt.Sprintf("MSG from %s: %s", msg.From, msg.Body)))
	})
}

func wireCallEvents(call xphone.Call) {
	callID := call.CallID()
	call.OnState(func(state xphone.CallState) {
		name := callStateName(state)
		prog.Send(msgLog(fmt.Sprintf("[%s] %s", callLabel(call), name)))
		if state != xphone.StateEnded {
			prog.Send(msgCallState{callID: callID, status: name})
		}
	})
	call.OnEnded(func(reason xphone.EndReason) {
		prog.Send(msgLog(fmt.Sprintf("ended: %s", endReasonName(reason))))
		prog.Send(msgCallEnded{callID: callID})
	})
	call.OnDTMF(func(digit string) {
		prog.Send(msgLog(fmt.Sprintf("DTMF received: %s", digit)))
	})
	call.OnHold(func() {
		prog.Send(msgLog("held by remote"))
	})
	call.OnResume(func() {
		prog.Send(msgLog("resumed by remote"))
	})
}

// ---------------------------------------------------------------------------
// Display helpers
// ---------------------------------------------------------------------------

func callStateName(s xphone.CallState) string {
	switch s {
	case xphone.StateIdle:
		return "idle"
	case xphone.StateRinging:
		return "ringing"
	case xphone.StateDialing:
		return "dialing"
	case xphone.StateRemoteRinging:
		return "ringing remote"
	case xphone.StateEarlyMedia:
		return "early media"
	case xphone.StateActive:
		return "active"
	case xphone.StateOnHold:
		return "on hold"
	case xphone.StateEnded:
		return "ended"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

func extStateName(s xphone.ExtensionState) string {
	switch s {
	case xphone.ExtensionAvailable:
		return "available"
	case xphone.ExtensionRinging:
		return "ringing"
	case xphone.ExtensionOnThePhone:
		return "on the phone"
	case xphone.ExtensionOffline:
		return "offline"
	case xphone.ExtensionUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("state(%d)", s)
	}
}

func endReasonName(r xphone.EndReason) string {
	switch r {
	case xphone.EndedByLocal:
		return "local hangup"
	case xphone.EndedByRemote:
		return "remote hangup"
	case xphone.EndedByTimeout:
		return "media timeout"
	case xphone.EndedByError:
		return "error"
	case xphone.EndedByTransfer:
		return "transferred"
	case xphone.EndedByTransferFailed:
		return "transfer failed"
	case xphone.EndedByRejected:
		return "rejected"
	case xphone.EndedByCancelled:
		return "cancelled"
	default:
		return fmt.Sprintf("unknown(%d)", r)
	}
}

// ---------------------------------------------------------------------------
// Profiles
// ---------------------------------------------------------------------------

type profile struct {
	Server    string `yaml:"server"`
	User      string `yaml:"user"`
	Pass      string `yaml:"pass"`
	Transport string `yaml:"transport"`
	Port      int    `yaml:"port"`
	Stun      string `yaml:"stun"`
	LocalIP   string `yaml:"local_ip"`
}

type profileFile struct {
	Profiles map[string]profile `yaml:"profiles"`
}

func loadProfile(name string) (profile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return profile{}, fmt.Errorf("cannot find home directory: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".sipcli.yaml"))
	if err != nil {
		return profile{}, fmt.Errorf("cannot read ~/.sipcli.yaml: %w", err)
	}
	var f profileFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return profile{}, fmt.Errorf("invalid ~/.sipcli.yaml: %w", err)
	}
	p, ok := f.Profiles[name]
	if !ok {
		available := make([]string, 0, len(f.Profiles))
		for k := range f.Profiles {
			available = append(available, k)
		}
		return profile{}, fmt.Errorf("profile %q not found (available: %s)", name, strings.Join(available, ", "))
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// CLI flags
// ---------------------------------------------------------------------------

type cliFlags struct {
	profile   string
	server    string
	user      string
	pass      string
	transport string
	port      int
	stun      string
	localIP   string
}

func parseFlags() cliFlags {
	var f cliFlags
	// Manual flag parsing to support --flag syntax like the Rust version,
	// while also supporting Go's -flag syntax.
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		arg = strings.TrimLeft(arg, "-")
		if i+1 >= len(args) {
			break
		}
		val := args[i+1]
		switch arg {
		case "profile":
			f.profile = val
			i++
		case "server":
			f.server = val
			i++
		case "user":
			f.user = val
			i++
		case "pass":
			f.pass = val
			i++
		case "transport":
			f.transport = val
			i++
		case "port":
			f.port, _ = strconv.Atoi(val)
			i++
		case "stun":
			f.stun = val
			i++
		case "local-ip", "local_ip", "localip":
			f.localIP = val
			i++
		}
	}
	return f
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	cli := parseFlags()

	// Resolve profile + CLI overrides.
	var p profile
	if cli.profile != "" {
		var err error
		p, err = loadProfile(cli.profile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
	if cli.server != "" {
		p.Server = cli.server
	}
	if cli.user != "" {
		p.User = cli.user
	}
	if cli.pass != "" {
		p.Pass = cli.pass
	}
	if cli.transport != "" {
		p.Transport = cli.transport
	}
	if cli.port != 0 {
		p.Port = cli.port
	}
	if cli.stun != "" {
		p.Stun = cli.stun
	}
	if cli.localIP != "" {
		p.LocalIP = cli.localIP
	}
	if p.Transport == "" {
		p.Transport = "udp"
	}

	if p.Server == "" || p.User == "" {
		fmt.Fprintln(os.Stderr, "Usage: sipcli -profile <name>")
		fmt.Fprintln(os.Stderr, "       sipcli -server <host> -user <username> [-pass <password>] [-transport udp|tcp|tls]")
		fmt.Fprintln(os.Stderr, "             [-port <port>] [-stun <host:port>] [-local-ip <ip>]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Profiles are loaded from ~/.sipcli.yaml. Flags override profile values.")
		os.Exit(1)
	}

	// Route sipgo's standard log output into the TUI debug panel
	// instead of letting it leak through bubbletea's alt screen.
	log.SetOutput(tuiWriter{})
	log.SetFlags(0) // no timestamp prefix — we handle formatting

	// Route library logs into the TUI debug panel.
	debugLogger := slog.New(&tuiHandler{level: slog.LevelDebug})

	// Build phone options
	opts := []xphone.PhoneOption{
		xphone.WithCredentials(p.User, p.Pass, p.Server),
		xphone.WithTransport(p.Transport, nil),
		xphone.WithLogger(debugLogger),
	}
	if p.Port != 0 {
		opts = append(opts, xphone.WithRTPPorts(p.Port, p.Port+100))
	}
	if p.Stun != "" {
		opts = append(opts, xphone.WithStunServer(p.Stun))
	}

	phone := xphone.New(opts...)

	wirePhoneEvents(phone)

	prog = tea.NewProgram(initialModel(phone), tea.WithAltScreen())

	go func() {
		msg := fmt.Sprintf("connecting to %s as %s...", p.Server, p.User)
		if p.Stun != "" {
			msg += fmt.Sprintf(" (STUN: %s)", p.Stun)
		}
		prog.Send(msgLog(msg))
		prog.Send(msgRegState("registering"))
		if err := phone.Connect(context.Background()); err != nil {
			prog.Send(msgLog(fmt.Sprintf("ERROR connect: %s", err)))
			prog.Send(msgRegState("failed"))
		}
	}()

	finalModel, err := prog.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Save command history on exit.
	if m, ok := finalModel.(model); ok {
		saveHistory(m.history)
	}
}
