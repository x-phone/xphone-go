package testutil

import (
	"strings"
	"sync"
)

// MockDialog is a mock SIP dialog for testing call behavior.
type MockDialog struct {
	mu sync.Mutex

	sdpAnswerSent      bool
	lastResponseCode   int
	lastResponseReason string
	cancelSent         bool
	byeSent            bool
	lastReInviteSDP    string
	referSent          bool
	lastReferTarget    string
	callID             string
	headers            map[string][]string
	onNotify           func(code int)
}

// NewMockDialog creates a new MockDialog with defaults.
func NewMockDialog() *MockDialog {
	return &MockDialog{
		callID:  "mock-call-id",
		headers: make(map[string][]string),
	}
}

// NewMockDialogWithCallID creates a MockDialog with a specific Call-ID.
func NewMockDialogWithCallID(callID string) *MockDialog {
	d := NewMockDialog()
	d.callID = callID
	return d
}

// NewMockDialogWithHeaders creates a MockDialog with specific headers.
func NewMockDialogWithHeaders(headers map[string][]string) *MockDialog {
	d := NewMockDialog()
	d.headers = headers
	return d
}

// SDPAnswerSent returns whether an SDP answer was sent.
func (d *MockDialog) SDPAnswerSent() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sdpAnswerSent
}

// SendSDPAnswer marks that an SDP answer was sent.
func (d *MockDialog) SendSDPAnswer() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sdpAnswerSent = true
}

// LastResponseCode returns the last SIP response code sent.
func (d *MockDialog) LastResponseCode() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastResponseCode
}

// LastResponseReason returns the last SIP response reason phrase.
func (d *MockDialog) LastResponseReason() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastResponseReason
}

// Respond records a SIP response.
func (d *MockDialog) Respond(code int, reason string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastResponseCode = code
	d.lastResponseReason = reason
}

// CancelSent returns whether a CANCEL was sent.
func (d *MockDialog) CancelSent() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancelSent
}

// SendCancel marks that a CANCEL was sent.
func (d *MockDialog) SendCancel() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelSent = true
}

// ByeSent returns whether a BYE was sent.
func (d *MockDialog) ByeSent() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.byeSent
}

// SendBye marks that a BYE was sent.
func (d *MockDialog) SendBye() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.byeSent = true
}

// LastReInviteSDP returns the SDP from the last re-INVITE.
func (d *MockDialog) LastReInviteSDP() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastReInviteSDP
}

// SendReInvite records a re-INVITE with the given SDP.
func (d *MockDialog) SendReInvite(sdp string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastReInviteSDP = sdp
}

// ReferSent returns whether a REFER was sent.
func (d *MockDialog) ReferSent() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.referSent
}

// LastReferTarget returns the target of the last REFER.
func (d *MockDialog) LastReferTarget() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastReferTarget
}

// SendRefer records a REFER to the given target.
func (d *MockDialog) SendRefer(target string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.referSent = true
	d.lastReferTarget = target
}

// OnNotify sets the callback for NOTIFY events.
func (d *MockDialog) OnNotify(fn func(code int)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onNotify = fn
}

// SimulateNotify simulates a NOTIFY with the given response code.
func (d *MockDialog) SimulateNotify(code int) {
	d.mu.Lock()
	fn := d.onNotify
	d.mu.Unlock()
	if fn != nil {
		fn(code)
	}
}

// CallID returns the SIP Call-ID.
func (d *MockDialog) CallID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.callID
}

// Header returns the values for a SIP header (case-insensitive).
func (d *MockDialog) Header(name string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	lower := strings.ToLower(name)
	for k, v := range d.headers {
		if strings.ToLower(k) == lower {
			cp := make([]string, len(v))
			copy(cp, v)
			return cp
		}
	}
	return nil
}

// Headers returns a copy of all SIP headers.
func (d *MockDialog) Headers() map[string][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := make(map[string][]string, len(d.headers))
	for k, v := range d.headers {
		vals := make([]string, len(v))
		copy(vals, v)
		cp[k] = vals
	}
	return cp
}
