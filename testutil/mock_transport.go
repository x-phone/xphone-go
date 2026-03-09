package testutil

import (
	"context"
	"errors"
	"sync"
	"time"
)

// SentMessage represents a sent SIP message for inspection.
type SentMessage struct {
	Method  string
	URI     string
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
// It satisfies the sipTransport interface and provides test helpers
// for queueing responses, inspecting sent messages, and simulating events.
type MockTransport struct {
	mu sync.Mutex

	responses  []Response // general response queue (RespondWith)
	sequence   []Response // ordered sequence queue (RespondSequence)
	seqIndex   int
	failRemain int

	sent       []SentMessage
	keepalives int
	dropped    bool
	closed     bool

	inviteFunc      func()
	dropHandler     func()
	incomingHandler func(from, to string)
	mwiNotifyFn     func(body string)
	responseCh      map[int][]chan bool

	// responseReady is signaled when a new response is queued,
	// unblocking any goroutine waiting in SendRequest or ReadResponse.
	responseReady chan struct{}
}

// NewMockTransport creates a new MockTransport.
func NewMockTransport() *MockTransport {
	return &MockTransport{
		responseCh:    make(map[int][]chan bool),
		responseReady: make(chan struct{}, 1),
	}
}

// Close implements sipTransport.
func (m *MockTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// Closed returns whether Close was called.
func (m *MockTransport) Closed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// --- Test setup helpers ---

// RespondWith queues a response for the next SIP request.
func (m *MockTransport) RespondWith(code int, reason string) {
	m.mu.Lock()
	m.responses = append(m.responses, Response{Code: code, Header: reason})
	m.mu.Unlock()
	// Signal that a response is available.
	select {
	case m.responseReady <- struct{}{}:
	default:
	}
}

// RespondSequence queues an ordered sequence of responses.
// Responses are consumed in order by successive SendRequest calls.
func (m *MockTransport) RespondSequence(responses ...Response) {
	m.mu.Lock()
	m.sequence = append(m.sequence, responses...)
	m.seqIndex = 0
	m.mu.Unlock()
	// Signal any goroutine blocked in awaitResponse.
	select {
	case m.responseReady <- struct{}{}:
	default:
	}
}

// FailNext causes the next n send attempts to fail with a transport error.
func (m *MockTransport) FailNext(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failRemain = n
}

// OnInvite sets a callback that fires when SendRequest is called with method "INVITE".
// Typically used to queue responses for the INVITE inside the callback.
func (m *MockTransport) OnInvite(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inviteFunc = fn
}

// --- sipTransport interface ---

// SendRequest sends a SIP request and waits for a response.
// It records the sent message, handles FailNext failures, triggers OnInvite
// for INVITE requests, and blocks until a response is available or ctx expires.
// Returns (response code, response header/reason, error).
func (m *MockTransport) SendRequest(ctx context.Context, method string, headers map[string]string) (int, string, error) {
	m.mu.Lock()

	// Copy headers to avoid the caller mutating our stored reference.
	var hdrCopy map[string]string
	if headers != nil {
		hdrCopy = make(map[string]string, len(headers))
		for k, v := range headers {
			hdrCopy[k] = v
		}
	}

	// Record the sent message (even for failed attempts, so CountSent is accurate).
	m.sent = append(m.sent, SentMessage{Method: method, headers: hdrCopy})

	// Check for simulated transport failures.
	if m.failRemain > 0 {
		m.failRemain--
		m.mu.Unlock()
		return 0, "", errors.New("transport error")
	}

	// For INVITE, trigger the inviteFunc callback which typically queues responses.
	if method == "INVITE" && m.inviteFunc != nil {
		fn := m.inviteFunc
		m.mu.Unlock()
		fn()
	} else {
		m.mu.Unlock()
	}

	return m.awaitResponse(ctx)
}

// ReadResponse reads the next queued response without sending a new request.
// Used after SendRequest("INVITE") to consume subsequent provisional/final responses.
// Returns (response code, response header/reason, error).
func (m *MockTransport) ReadResponse(ctx context.Context) (int, string, error) {
	return m.awaitResponse(ctx)
}

// awaitResponse blocks until a response is available from the sequence or response
// queue, or until ctx is cancelled.
func (m *MockTransport) awaitResponse(ctx context.Context) (int, string, error) {
	for {
		m.mu.Lock()
		// Sequence responses take priority (used by RespondSequence).
		if m.seqIndex < len(m.sequence) {
			resp := m.sequence[m.seqIndex]
			m.seqIndex++
			m.mu.Unlock()
			return resp.Code, resp.Header, nil
		}
		// Then check the general response queue (used by RespondWith).
		if len(m.responses) > 0 {
			resp := m.responses[0]
			m.responses = m.responses[1:]
			m.mu.Unlock()
			return resp.Code, resp.Header, nil
		}
		m.mu.Unlock()

		// No response available — wait for one to be queued or context to expire.
		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-m.responseReady:
			// A response was queued; loop to check.
		}
	}
}

// SendKeepalive records a NAT keepalive.
func (m *MockTransport) SendKeepalive() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keepalives++
	return nil
}

// Respond records a SIP response (e.g. 100 Trying, 180 Ringing) and notifies
// any WaitForResponse listeners.
func (m *MockTransport) Respond(code int, reason string) {
	m.mu.Lock()
	if chs, ok := m.responseCh[code]; ok {
		for _, ch := range chs {
			select {
			case ch <- true:
			default:
			}
		}
		delete(m.responseCh, code)
	}
	m.mu.Unlock()
}

// OnDrop registers a handler that fires when SimulateDrop is called.
func (m *MockTransport) OnDrop(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropHandler = fn
}

// OnIncoming registers a handler for incoming SIP requests (e.g. INVITE).
func (m *MockTransport) OnIncoming(fn func(from, to string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.incomingHandler = fn
}

// SendSubscribe records a SUBSCRIBE request and returns the next queued response.
func (m *MockTransport) SendSubscribe(ctx context.Context, uri string, headers map[string]string) (int, string, error) {
	m.mu.Lock()

	// Copy headers to avoid the caller mutating our stored reference.
	var hdrCopy map[string]string
	if headers != nil {
		hdrCopy = make(map[string]string, len(headers))
		for k, v := range headers {
			hdrCopy[k] = v
		}
	}

	m.sent = append(m.sent, SentMessage{Method: "SUBSCRIBE", URI: uri, headers: hdrCopy})

	if m.failRemain > 0 {
		m.failRemain--
		m.mu.Unlock()
		return 0, "", errors.New("transport error")
	}
	m.mu.Unlock()

	return m.awaitResponse(ctx)
}

// OnMWINotify registers a handler for incoming MWI NOTIFY bodies.
func (m *MockTransport) OnMWINotify(fn func(body string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mwiNotifyFn = fn
}

// SimulateMWINotify simulates an incoming MWI NOTIFY with the given body.
func (m *MockTransport) SimulateMWINotify(body string) {
	m.mu.Lock()
	fn := m.mwiNotifyFn
	m.mu.Unlock()
	if fn != nil {
		fn(body)
	}
}

// --- Test simulation methods ---

// SimulateDrop simulates a transport connection drop.
// It fires the registered drop handler so the registry can react.
func (m *MockTransport) SimulateDrop() {
	m.mu.Lock()
	m.dropped = true
	fn := m.dropHandler
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// SimulateInvite simulates an incoming INVITE from a remote party.
func (m *MockTransport) SimulateInvite(from, to string) {
	m.mu.Lock()
	fn := m.incomingHandler
	m.mu.Unlock()
	if fn != nil {
		fn(from, to)
	}
}

// --- Test inspection methods ---

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

// LastSent returns a copy of the last sent message with the given method.
// Returns a pointer to the copy (safe from slice reallocation).
func (m *MockTransport) LastSent(method string) *SentMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		if m.sent[i].Method == method {
			cp := m.sent[i]
			return &cp
		}
	}
	return nil
}

// WaitForResponse returns a channel that receives true when Respond is called
// with the given code, or false after the timeout.
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
