package testutil

import (
	"crypto/rand"
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
	infoSent           bool
	lastInfoDigit      string
	lastInfoDuration   int
	callID             string
	headers            map[string][]string
	onNotify           func(code int)
}

// NewMockDialog creates a new MockDialog with defaults.
func NewMockDialog() *MockDialog {
	return &MockDialog{
		callID:  mockCallID(),
		headers: make(map[string][]string),
	}
}

func mockCallID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("mock-%x", b)
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

// NewMockDialogWithCallIDAndHeaders creates a MockDialog with a specific Call-ID and headers.
func NewMockDialogWithCallIDAndHeaders(callID string, headers map[string][]string) *MockDialog {
	d := NewMockDialog()
	d.callID = callID
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

// SendInfoDTMF records a SIP INFO DTMF request.
func (d *MockDialog) SendInfoDTMF(digit string, duration int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.infoSent = true
	d.lastInfoDigit = digit
	d.lastInfoDuration = duration
	return nil
}

// OnNotify sets the callback for NOTIFY events.
func (d *MockDialog) OnNotify(fn func(code int)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onNotify = fn
}

// FireNotify fires the on_notify callback with the given status code.
func (d *MockDialog) FireNotify(code int) {
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

// InfoSent returns whether a SIP INFO DTMF was sent.
func (d *MockDialog) InfoSent() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.infoSent
}

// LastInfoDigit returns the digit from the last SIP INFO DTMF.
func (d *MockDialog) LastInfoDigit() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastInfoDigit
}

// LastInfoDuration returns the duration from the last SIP INFO DTMF.
func (d *MockDialog) LastInfoDuration() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastInfoDuration
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
