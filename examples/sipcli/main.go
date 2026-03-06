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
	"log/slog"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	xphone "github.com/x-phone/xphone-go"
)

// prog is the running bubbletea program. Used by xphone callbacks to send
// messages into the TUI from any goroutine.
var prog *tea.Program

// --- messages sent from xphone callbacks into the TUI ---

type msgLog string
type msgRegState string
type msgCallState string
type msgCallCleared struct{}
type msgCallRef struct{ call xphone.Call }

// --- model ---

type model struct {
	phone xphone.Phone
	call  xphone.Call

	regStatus  string
	callStatus string
	logs       []string
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

	var b strings.Builder

	// Status bar
	bar := fmt.Sprintf(" REG: %s  |  CALL: %s", m.regStatus, m.callStatus)
	b.WriteString(invertText(bar, m.width))
	b.WriteByte('\n')

	// Event log
	logLines := m.height - 5
	if logLines < 3 {
		logLines = 3
	}
	start := 0
	if len(m.logs) > logLines {
		start = len(m.logs) - logLines
	}
	for i := start; i < len(m.logs); i++ {
		b.WriteString(" " + m.logs[i] + "\n")
	}
	for i := len(m.logs) - start; i < logLines; i++ {
		b.WriteByte('\n')
	}

	// Error / input / help
	if m.err != "" {
		b.WriteString(" \033[31m" + m.err + "\033[0m\n")
	} else {
		b.WriteByte('\n')
	}
	b.WriteString(" > " + m.input + "\033[5m_\033[0m\n")
	b.WriteString("\033[2m dial|accept|reject|hangup|hold|resume|mute|unmute|dtmf|transfer|quit\033[0m")

	return b.String()
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
		if m.call != nil {
			m.call.End()
		}
		m.phone.Disconnect()
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

// callAction executes a call operation, returning an error log if it fails.
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
		// Wire events before sending ref so no events are missed.
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
		// Wire call events immediately so no events are missed.
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

// --- main ---

func main() {
	server := flag.String("server", "", "SIP server hostname or IP (required)")
	user := flag.String("user", "", "SIP username (required)")
	pass := flag.String("pass", "", "SIP password")
	transport := flag.String("transport", "udp", "SIP transport: udp, tcp, tls")
	flag.Parse()

	if *server == "" || *user == "" {
		fmt.Fprintln(os.Stderr, "Usage: sipcli -server <host> -user <username> [-pass <password>] [-transport udp|tcp|tls]")
		os.Exit(1)
	}

	// Silence library logs — they leak to the terminal and break the TUI.
	// Events are surfaced through callbacks in the event log panel instead.
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))

	phone := xphone.New(
		xphone.WithCredentials(*user, *pass, *server),
		xphone.WithTransport(*transport, nil),
		xphone.WithLogger(silent),
	)

	// Wire callbacks before Connect.
	wirePhoneEvents(phone)

	prog = tea.NewProgram(initialModel(phone), tea.WithAltScreen())

	// Connect in background so the TUI renders immediately.
	go func() {
		prog.Send(msgLog("[info] connecting to " + *server + "..."))
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
