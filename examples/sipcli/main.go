// sipcli is an interactive SIP client built with the xphone library.
// It demonstrates registration, inbound/outbound calls, hold, DTMF,
// mute, and blind transfer — all driven by the event-based API.
//
// Usage:
//
//	go run ./examples/sipcli -server pbx.example.com -user 1001 -pass secret
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	xphone "github.com/x-phone/xphone-go"
	"gopkg.in/yaml.v3"
)

// prog is the running bubbletea program. Used by xphone callbacks and the
// custom slog handler to send messages into the TUI from any goroutine.
var prog *tea.Program

// --- messages ---

type msgLog string
type msgDebugLog string
type msgRegState string
type msgCallState string
type msgCallCleared struct{}
type msgCallRef struct{ call xphone.Call }

// --- custom slog handler that feeds into the TUI ---

type tuiHandler struct{ level slog.Level }

func (h *tuiHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *tuiHandler) Handle(_ context.Context, r slog.Record) error {
	if prog == nil {
		return nil
	}
	var b strings.Builder
	// Level tag
	switch {
	case r.Level >= slog.LevelError:
		b.WriteString("\033[31mERR\033[0m ")
	case r.Level >= slog.LevelWarn:
		b.WriteString("\033[33mWRN\033[0m ")
	case r.Level >= slog.LevelInfo:
		b.WriteString("\033[36mINF\033[0m ")
	default:
		b.WriteString("\033[2mDBG\033[0m ")
	}
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" \033[2m")
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(a.Value.String())
		b.WriteString("\033[0m")
		return true
	})
	line := strings.ReplaceAll(b.String(), "\r\n", " | ")
	line = strings.ReplaceAll(line, "\n", " | ")
	line = strings.ReplaceAll(line, "\r", " | ")
	prog.Send(msgDebugLog(line))
	return nil
}

func (h *tuiHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *tuiHandler) WithGroup(name string) slog.Handler       { return h }

// --- model ---

type model struct {
	phone xphone.Phone
	call  xphone.Call

	regStatus  string
	callStatus string
	logs       []string
	debugLogs  []string
	input      string
	err        string
	quitting   bool
	width      int
	height     int
}

func initialModel(phone xphone.Phone) model {
	return model{
		phone:      phone,
		regStatus:  "disconnected",
		callStatus: "no active call",
		width:      80,
		height:     24,
	}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		return m.handleKey(msg)
	case msgLog:
		m.logs = append(m.logs, string(msg))
	case msgDebugLog:
		m.debugLogs = append(m.debugLogs, string(msg))
	case msgRegState:
		m.regStatus = string(msg)
	case msgCallState:
		m.callStatus = string(msg)
	case msgCallCleared:
		m.call = nil
		m.callStatus = "no active call"
	case msgCallRef:
		m.call = msg.call
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEnter:
		cmd := strings.TrimSpace(m.input)
		m.input = ""
		m.err = ""
		if cmd != "" {
			return m.execCommand(cmd)
		}
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.input += string(msg.Runes)
		} else if msg.Type == tea.KeySpace {
			m.input += " "
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.quitting {
		return "Bye!\n"
	}

	leftW := m.width * 40 / 100
	rightW := m.width - leftW - 1 // 1 for the separator
	if leftW < 20 {
		leftW = 20
	}
	if rightW < 20 {
		rightW = 20
	}
	panelH := m.height - 4 // status bar + error + input + help
	if panelH < 3 {
		panelH = 3
	}

	// Build left panel lines (events)
	leftHeader := invertText(" EVENTS", leftW)
	leftLines := renderLogPanel(m.logs, leftW, panelH)

	// Build right panel lines (debug)
	rightHeader := invertText(" DEBUG", rightW)
	rightLines := renderLogPanel(m.debugLogs, rightW, panelH)

	var b strings.Builder

	// Status bar (full width)
	bar := fmt.Sprintf(" REG: %s  |  CALL: %s", m.regStatus, m.callStatus)
	b.WriteString(invertText(bar, m.width))
	b.WriteByte('\n')

	// Panel headers
	b.WriteString(leftHeader)
	b.WriteString("\033[2m|\033[0m")
	b.WriteString(rightHeader)
	b.WriteByte('\n')

	// Panel body
	for i := 0; i < panelH; i++ {
		b.WriteString(leftLines[i])
		b.WriteString("\033[2m|\033[0m")
		b.WriteString(rightLines[i])
		b.WriteByte('\n')
	}

	// Error / input / help (full width)
	if m.err != "" {
		b.WriteString(" \033[31m" + m.err + "\033[0m\n")
	} else {
		b.WriteByte('\n')
	}
	b.WriteString(" > " + m.input + "\033[5m_\033[0m\n")
	b.WriteString("\033[2m dial|accept|reject|hangup|hold|resume|mute|unmute|dtmf|transfer|quit\033[0m")

	return b.String()
}

// renderLogPanel renders a scrolling log into fixed-width lines.
// Long entries wrap onto continuation lines indented by 2 spaces.
func renderLogPanel(logs []string, width, height int) []string {
	// Expand all log entries into visual lines.
	var visual []string
	for _, entry := range logs {
		wrapped := wrapLine(" "+entry, width)
		visual = append(visual, wrapped...)
	}
	// Take the last `height` visual lines.
	start := 0
	if len(visual) > height {
		start = len(visual) - height
	}
	lines := make([]string, height)
	for i := 0; i < height; i++ {
		idx := start + i
		if idx < len(visual) {
			lines[i] = pad(visual[idx], width)
		} else {
			lines[i] = strings.Repeat(" ", width)
		}
	}
	return lines
}

// wrapLine splits s into lines of at most width visible characters.
// Continuation lines are indented with 3 spaces.
func wrapLine(s string, width int) []string {
	if width < 5 {
		width = 5
	}
	var result []string
	for len(s) > 0 {
		cut := truncateIndex(s, width)
		result = append(result, s[:cut])
		s = s[cut:]
		if len(s) > 0 {
			s = "   " + s // indent continuation
		}
	}
	return result
}

// truncateIndex returns the byte index where s should be cut to fit
// within maxW visible characters, respecting ANSI escape sequences.
func truncateIndex(s string, maxW int) int {
	visible := 0
	inEscape := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z') {
				inEscape = false
			}
			continue
		}
		visible++
		if visible >= maxW {
			return i + 1
		}
	}
	return len(s)
}

func pad(s string, width int) string {
	// Count visible characters (ignoring ANSI escapes).
	visible := 0
	inEscape := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z') {
				inEscape = false
			}
			continue
		}
		visible++
	}
	if visible < width {
		return s + strings.Repeat(" ", width-visible)
	}
	return s
}

func invertText(text string, width int) string {
	if len(text) < width {
		text += strings.Repeat(" ", width-len(text))
	}
	return "\033[7m" + text[:min(len(text), width)] + "\033[0m"
}

// --- command dispatch ---

func (m model) execCommand(input string) (model, tea.Cmd) {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = strings.Join(parts[1:], " ")
	}

	switch cmd {
	case "quit", "q", "exit":
		call := m.call
		phone := m.phone
		go func() {
			if call != nil {
				call.End()
			}
			phone.Disconnect()
		}()
		m.quitting = true
		return m, tea.Quit

	case "dial", "d":
		if arg == "" {
			m.err = "usage: dial <target>"
			return m, nil
		}
		if m.call != nil {
			m.err = "already in a call — hangup first"
			return m, nil
		}
		m.logs = append(m.logs, fmt.Sprintf("[dial] calling %s...", arg))
		return m, m.cmdDial(arg)

	case "accept", "a":
		return m, m.callAction("accept", func(c xphone.Call) error { return c.Accept() })

	case "reject":
		return m, m.callAction("reject", func(c xphone.Call) error { return c.Reject(486, "Busy Here") })

	case "hangup", "h":
		return m, m.callAction("hangup", func(c xphone.Call) error { return c.End() })

	case "hold":
		return m, m.callAction("hold", func(c xphone.Call) error { return c.Hold() })

	case "resume":
		return m, m.callAction("resume", func(c xphone.Call) error { return c.Resume() })

	case "mute":
		return m, m.callAction("mute", func(c xphone.Call) error { return c.Mute() })

	case "unmute":
		return m, m.callAction("unmute", func(c xphone.Call) error { return c.Unmute() })

	case "dtmf":
		if m.call == nil {
			m.err = "no active call"
			return m, nil
		}
		if arg == "" {
			m.err = "usage: dtmf <digits>"
			return m, nil
		}
		for _, ch := range arg {
			if err := m.call.SendDTMF(string(ch)); err != nil {
				m.err = err.Error()
				return m, nil
			}
		}
		m.logs = append(m.logs, fmt.Sprintf("[dtmf] sent: %s", arg))
		return m, nil

	case "transfer":
		if arg == "" {
			m.err = "usage: transfer <target>"
			return m, nil
		}
		return m, m.callAction("transfer to "+arg, func(c xphone.Call) error { return c.BlindTransfer(arg) })

	default:
		m.err = fmt.Sprintf("unknown: %s", cmd)
		return m, nil
	}
}

func (m model) callAction(name string, fn func(xphone.Call) error) tea.Cmd {
	if m.call == nil {
		m.err = "no active call"
		return nil
	}
	call := m.call
	return func() tea.Msg {
		if err := fn(call); err != nil {
			return msgLog(fmt.Sprintf("[error] %s: %s", name, err))
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
			return msgLog(fmt.Sprintf("[error] dial failed: %s", err))
		}
		wireCallEvents(call)
		return msgCallRef{call: call}
	}
}

// --- xphone event wiring ---

func wirePhoneEvents(phone xphone.Phone) {
	phone.OnRegistered(func() {
		prog.Send(msgRegState("registered"))
		prog.Send(msgLog("[event] registered with server"))
	})
	phone.OnUnregistered(func() {
		prog.Send(msgRegState("unregistered"))
		prog.Send(msgLog("[event] registration lost"))
	})
	phone.OnError(func(err error) {
		prog.Send(msgRegState("error"))
		prog.Send(msgLog(fmt.Sprintf("[error] %s", err)))
	})
	phone.OnIncoming(func(call xphone.Call) {
		wireCallEvents(call)

		from := call.From()
		if name := call.FromName(); name != "" {
			from = fmt.Sprintf("%s (%s)", name, from)
		}
		prog.Send(msgLog(fmt.Sprintf("[incoming] from %s — type 'accept' or 'reject'", from)))
		prog.Send(msgCallState(fmt.Sprintf("ringing from %s", from)))
		prog.Send(msgCallRef{call: call})
	})
}

func wireCallEvents(call xphone.Call) {
	call.OnState(func(state xphone.CallState) {
		name := callStateName(state)
		prog.Send(msgLog(fmt.Sprintf("[state] %s", name)))
		if state == xphone.StateEnded {
			prog.Send(msgCallCleared{})
		} else {
			prog.Send(msgCallState(name))
		}
	})
	call.OnEnded(func(reason xphone.EndReason) {
		prog.Send(msgLog(fmt.Sprintf("[ended] %s", endReasonName(reason))))
		prog.Send(msgCallCleared{})
	})
	call.OnDTMF(func(digit string) {
		prog.Send(msgLog(fmt.Sprintf("[dtmf] received: %s", digit)))
	})
	call.OnHold(func() {
		prog.Send(msgLog("[event] put on hold by remote"))
	})
	call.OnResume(func() {
		prog.Send(msgLog("[event] resumed by remote"))
	})
}

// --- display helpers ---

func callStateName(s xphone.CallState) string {
	names := map[xphone.CallState]string{
		xphone.StateIdle:          "idle",
		xphone.StateRinging:       "ringing",
		xphone.StateDialing:       "dialing",
		xphone.StateRemoteRinging: "remote ringing",
		xphone.StateEarlyMedia:    "early media",
		xphone.StateActive:        "active",
		xphone.StateOnHold:        "on hold",
		xphone.StateEnded:         "ended",
	}
	if name, ok := names[s]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", s)
}

func endReasonName(r xphone.EndReason) string {
	names := map[xphone.EndReason]string{
		xphone.EndedByLocal:     "local hangup",
		xphone.EndedByRemote:    "remote hangup",
		xphone.EndedByTimeout:   "media timeout",
		xphone.EndedByError:     "error",
		xphone.EndedByTransfer:  "transferred",
		xphone.EndedByRejected:  "rejected",
		xphone.EndedByCancelled: "cancelled",
	}
	if name, ok := names[r]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", r)
}

// --- profiles ---

type profile struct {
	Server    string `yaml:"server"`
	User      string `yaml:"user"`
	Pass      string `yaml:"pass"`
	Transport string `yaml:"transport"`
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

// --- main ---

func main() {
	profileName := flag.String("profile", "", "load settings from ~/.sipcli.yaml profile")
	server := flag.String("server", "", "SIP server hostname or IP")
	user := flag.String("user", "", "SIP username")
	pass := flag.String("pass", "", "SIP password")
	transport := flag.String("transport", "", "SIP transport: udp, tcp, tls")
	flag.Parse()

	var p profile
	if *profileName != "" {
		var err error
		p, err = loadProfile(*profileName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
	if *server != "" {
		p.Server = *server
	}
	if *user != "" {
		p.User = *user
	}
	if *pass != "" {
		p.Pass = *pass
	}
	if *transport != "" {
		p.Transport = *transport
	}
	if p.Transport == "" {
		p.Transport = "udp"
	}

	if p.Server == "" || p.User == "" {
		fmt.Fprintln(os.Stderr, "Usage: sipcli -profile <name>")
		fmt.Fprintln(os.Stderr, "       sipcli -server <host> -user <username> [-pass <password>] [-transport udp|tcp|tls]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Profiles are loaded from ~/.sipcli.yaml. Flags override profile values.")
		os.Exit(1)
	}

	// Silence the standard log package (used by sipgo's transport layer)
	// so it doesn't leak through bubbletea's alt screen.
	log.SetOutput(io.Discard)

	// Route library logs into the TUI debug panel instead of stderr.
	debugLogger := slog.New(&tuiHandler{level: slog.LevelDebug})

	phone := xphone.New(
		xphone.WithCredentials(p.User, p.Pass, p.Server),
		xphone.WithTransport(p.Transport, nil),
		xphone.WithLogger(debugLogger),
	)

	wirePhoneEvents(phone)

	prog = tea.NewProgram(initialModel(phone), tea.WithAltScreen())

	go func() {
		prog.Send(msgLog("[info] connecting to " + p.Server + " as " + p.User + "..."))
		prog.Send(msgRegState("registering"))
		if err := phone.Connect(context.Background()); err != nil {
			prog.Send(msgLog(fmt.Sprintf("[error] connect: %s", err)))
			prog.Send(msgRegState("failed"))
		}
	}()

	if _, err := prog.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
