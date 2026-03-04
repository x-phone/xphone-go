package testutil

import (
	"sync"
	"time"
)

// SentMessage represents a sent SIP message for inspection.
type SentMessage struct {
	Method  string
	headers map[string]string
}

// Header returns the value of a header on the sent message.
func (m *SentMessage) Header(name string) string {
	if m == nil || m.headers == nil {
		return ""
	}
	return m.headers[name]
}

// MockTransport is a mock SIP transport for testing.
type MockTransport struct {
	mu sync.Mutex

	responses    []Response
	sequence     []Response
	seqIndex     int
	failCount    int
	failRemain   int
	sent         []SentMessage
	keepalives   int
	dropped      bool
	inviteFunc   func()
	responseCh   map[int][]chan bool
}

// NewMockTransport creates a new MockTransport.
func NewMockTransport() *MockTransport {
	return &MockTransport{
		responseCh: make(map[int][]chan bool),
	}
}

// Close implements sipTransport.
func (m *MockTransport) Close() error {
	return nil
}

// RespondWith queues a response for the next SIP request.
func (m *MockTransport) RespondWith(code int, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, Response{Code: code, Header: reason})
}

// RespondSequence queues a sequence of responses.
func (m *MockTransport) RespondSequence(responses ...Response) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sequence = append(m.sequence, responses...)
	m.seqIndex = 0
}

// FailNext causes the next n send attempts to fail.
func (m *MockTransport) FailNext(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failRemain = n
}

// CountSent returns the number of messages sent with the given method.
func (m *MockTransport) CountSent(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, msg := range m.sent {
		if msg.Method == method {
			count++
		}
	}
	return count
}

// CountSentKeepalives returns the number of keepalive messages sent.
func (m *MockTransport) CountSentKeepalives() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keepalives
}

// LastSent returns the last sent message with the given method.
func (m *MockTransport) LastSent(method string) *SentMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		if m.sent[i].Method == method {
			return &m.sent[i]
		}
	}
	return nil
}

// SimulateDrop simulates a transport drop.
func (m *MockTransport) SimulateDrop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropped = true
}

// OnInvite sets a callback for when an INVITE is sent.
func (m *MockTransport) OnInvite(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inviteFunc = fn
}

// SimulateInvite simulates an incoming INVITE.
func (m *MockTransport) SimulateInvite(from, to string) {
	// Stub — will be wired up during implementation
}

// WaitForResponse returns a channel that receives true when a response
// with the given code is sent, or false on timeout.
func (m *MockTransport) WaitForResponse(code int, timeout time.Duration) <-chan bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan bool, 1)
	m.responseCh[code] = append(m.responseCh[code], ch)

	go func() {
		time.Sleep(timeout)
		select {
		case ch <- false:
		default:
		}
	}()

	return ch
}
