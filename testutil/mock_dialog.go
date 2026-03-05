package testutil

import (
	"fmt"
	"strings"
	"sync"
)

// MockDialog is a mock SIP dialog for testing call behavior.
// It satisfies the dialog interface and also exposes test inspection methods
// (ByeSent, LastResponseCode, etc.) on the concrete type.
type MockDialog struct {
	mu sync.Mutex

	lastResponseCode   int
	lastResponseReason string
	lastResponseBody   []byte
	cancelSent         bool
	byeSent            bool
	lastReInviteSDP    []byte
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

// NewMockDialogWithSessionExpires creates a MockDialog with a Session-Expires header.
func NewMockDialogWithSessionExpires(seconds int) *MockDialog {
	return NewMockDialogWithHeaders(map[string][]string{
		"Session-Expires": {fmt.Sprintf("%d", seconds)},
	})
}

// --- dialog interface methods (return error) ---

// Respond records a SIP response.
func (d *MockDialog) Respond(code int, reason string, body []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastResponseCode = code
	d.lastResponseReason = reason
	d.lastResponseBody = body
	return nil
}

// SendBye marks that a BYE was sent.
func (d *MockDialog) SendBye() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.byeSent = true
	return nil
}

// SendCancel marks that a CANCEL was sent.
func (d *MockDialog) SendCancel() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelSent = true
	return nil
}

// SendReInvite records a re-INVITE with the given SDP.
func (d *MockDialog) SendReInvite(sdp []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastReInviteSDP = sdp
	return nil
}

// SendRefer records a REFER to the given target.
func (d *MockDialog) SendRefer(target string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.referSent = true
	d.lastReferTarget = target
	return nil
}

// OnNotify sets the callback for NOTIFY events.
func (d *MockDialog) OnNotify(fn func(code int)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onNotify = fn
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

// --- Test inspection methods (concrete type only, not on dialog interface) ---

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

// LastResponseBody returns the body from the last Respond call.
func (d *MockDialog) LastResponseBody() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastResponseBody
}

// ByeSent returns whether a BYE was sent.
func (d *MockDialog) ByeSent() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.byeSent
}

// CancelSent returns whether a CANCEL was sent.
func (d *MockDialog) CancelSent() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancelSent
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

// LastReInviteSDP returns the SDP from the last re-INVITE.
func (d *MockDialog) LastReInviteSDP() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.lastReInviteSDP)
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
