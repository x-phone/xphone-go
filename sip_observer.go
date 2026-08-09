package xphone

import (
	"net/textproto"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
)

// SIPDirection reports whether an observed SIP message was received or sent.
type SIPDirection string

const (
	// SIPInbound marks a message received off the wire.
	SIPInbound SIPDirection = "inbound"
	// SIPOutbound marks a message xphone-go originated.
	SIPOutbound SIPDirection = "outbound"
)

// SIPMessage is a structured, redacted snapshot of a single SIP message
// delivered to a SIPObserver. It is an immutable copy: retaining or mutating
// it never affects the live signaling.
//
// Credential headers (Authorization, Proxy-Authorization) are masked before
// the message reaches the observer — see WithSIPObserver.
type SIPMessage struct {
	Direction  SIPDirection      // inbound or outbound
	IsResponse bool              // true for status responses, false for requests
	Method     string            // request method, or the CSeq method of a response
	StatusCode int               // 0 for requests; 1xx–6xx for responses
	Reason     string            // response reason phrase; "" for requests
	From       string            // From header value
	To         string            // To header value
	CallID     string            // Call-ID
	CSeq       string            // CSeq (e.g. "1 INVITE")
	Transport  string            // udp/tcp/tls/ws/wss
	Timestamp  time.Time         // observation time
	Headers    map[string]string // header keys canonicalized (RFC 3261 §7.3.1), values comma-joined when repeated; credentials redacted
}

// SIPObserver receives a structured copy of SIP messages flowing through the
// stack. Register one with WithSIPObserver (Phone) or the ServerConfig field
// of the same name.
//
// Coverage: inbound messages are captured in full at the transport layer
// (every received request and response, including retransmissions). Outbound
// coverage is for messages xphone-go constructs itself — in-dialog re-INVITE,
// its ACK, REFER, and INFO/DTMF. Messages sipgo builds internally (the initial
// INVITE, BYE, non-2xx ACKs, digest-auth retries, and transport retransmits)
// are not surfaced as outbound events; their effect is still visible through
// the inbound responses they elicit. sipgo v1.3.1 exposes no transport-level
// write hook, so faithful outbound capture is bounded to our own send sites.
//
// Outbound events reflect send attempts: the message is snapshotted after
// sipgo has populated its dialog headers, and is delivered even if the
// underlying transport write returns an error — a debugger wants failed
// attempts to remain visible, not hidden.
//
// The observer is called synchronously on the transport/TU goroutine. It MUST
// NOT block or panic; offload anything expensive (I/O, WebSocket fan-out) to a
// buffered channel. A nil observer disables observation with zero overhead.
type SIPObserver func(SIPMessage)

// observerNow is indirected so tests can pin the timestamp.
var observerNow = time.Now

// redactedValue replaces credential header values before they reach an observer.
const redactedValue = "REDACTED"

// credentialHeaders carry digest secrets (the computed response=) and are
// masked. Challenge headers (WWW-Authenticate/Proxy-Authenticate) are left
// intact: they carry realm/nonce, which are not secret and are useful to see.
var credentialHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
}

// headerLister is satisfied by *sip.Request and *sip.Response, which both embed
// the header store. The sip.Message interface itself exposes only GetHeaders.
type headerLister interface {
	Headers() []sip.Header
}

// buildSIPMessage projects a live sipgo message into an immutable, redacted
// SIPMessage. now is passed in (rather than read here) so callers control the
// clock and tests stay deterministic.
func buildSIPMessage(msg sip.Message, dir SIPDirection, now time.Time) SIPMessage {
	m := SIPMessage{
		Direction: dir,
		Transport: msg.Transport(),
		Timestamp: now,
		Headers:   map[string]string{},
	}

	switch v := msg.(type) {
	case *sip.Request:
		m.Method = string(v.Method)
	case *sip.Response:
		m.IsResponse = true
		m.StatusCode = v.StatusCode
		m.Reason = v.Reason
		if cseq := v.CSeq(); cseq != nil {
			m.Method = string(cseq.MethodName)
		}
	}

	if h := msg.CallID(); h != nil {
		m.CallID = h.Value()
	}
	if h := msg.From(); h != nil {
		m.From = h.Value()
	}
	if h := msg.To(); h != nil {
		m.To = h.Value()
	}
	if h := msg.CSeq(); h != nil {
		m.CSeq = h.Value()
	}

	if hl, ok := msg.(headerLister); ok {
		for _, h := range hl.Headers() {
			// SIP header names are case-insensitive (RFC 3261 §7.3.1);
			// canonicalize so callers can look them up by a stable key.
			name := textproto.CanonicalMIMEHeaderKey(h.Name())
			val := h.Value()
			if credentialHeaders[strings.ToLower(name)] {
				val = redactedValue
			}
			if existing, ok := m.Headers[name]; ok {
				m.Headers[name] = existing + ", " + val
			} else {
				m.Headers[name] = val
			}
		}
	}

	return m
}

// emitSIP delivers one observation, guarding against a nil observer or message.
func emitSIP(obs SIPObserver, msg sip.Message, dir SIPDirection) {
	if obs == nil || msg == nil {
		return
	}
	obs(buildSIPMessage(msg, dir, observerNow()))
}
