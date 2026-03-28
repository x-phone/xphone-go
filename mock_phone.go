package xphone

import (
	"context"
	"sync"
)

// MockPhone is a mock implementation of the Phone interface for testing.
// It simulates registration, dialing, and incoming calls without SIP transport.
type MockPhone struct {
	mu sync.Mutex

	state          PhoneState
	onIncomingFn   func(Call)
	onRegisteredFn func()
	onUnregFn      func()
	onErrorFn      func(error)
	onCallStateFn  func(Call, CallState)
	onCallEndedFn  func(Call, EndReason)
	onCallDTMFFn   func(Call, string)
	onVoicemailFn  func(VoicemailStatus)
	onMessageFn    func(SipMessage)
	lastCall       Call
	calls          map[string]Call
}

// NewMockPhone creates a new MockPhone in the Disconnected state.
func NewMockPhone() *MockPhone {
	return &MockPhone{
		state: PhoneStateDisconnected,
		calls: make(map[string]Call),
	}
}

func (p *MockPhone) Connect(_ context.Context) error {
	p.mu.Lock()
	if p.state != PhoneStateDisconnected {
		p.mu.Unlock()
		return ErrAlreadyConnected
	}
	p.state = PhoneStateRegistered
	fn := p.onRegisteredFn
	p.mu.Unlock()
	if fn != nil {
		fn()
	}
	return nil
}

func (p *MockPhone) Disconnect() error {
	p.mu.Lock()
	if p.state == PhoneStateDisconnected {
		p.mu.Unlock()
		return ErrNotConnected
	}
	p.state = PhoneStateDisconnected
	fn := p.onUnregFn
	activeCalls := make([]*MockCall, 0, len(p.calls))
	for _, c := range p.calls {
		activeCalls = append(activeCalls, c.(*MockCall))
	}
	p.calls = make(map[string]Call)
	p.mu.Unlock()
	for _, c := range activeCalls {
		c.End()
	}
	if fn != nil {
		fn()
	}
	return nil
}

func (p *MockPhone) Dial(_ context.Context, target string, opts ...DialOption) (Call, error) {
	p.mu.Lock()
	if p.state != PhoneStateRegistered {
		p.mu.Unlock()
		return nil, ErrNotRegistered
	}

	c := NewMockCall()
	c.state = StateActive
	c.direction = DirectionOutbound
	c.remoteURI = target
	p.lastCall = c
	p.calls[c.CallID()] = c
	p.wireCallCallbacks(c)
	callID := c.CallID()
	c.onEndedCleanup = func(_ EndReason) { p.untrackCall(callID) }
	p.mu.Unlock()

	return c, nil
}

func (p *MockPhone) OnIncoming(fn func(Call)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onIncomingFn = fn
}

func (p *MockPhone) OnRegistered(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onRegisteredFn = fn
}

func (p *MockPhone) OnUnregistered(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onUnregFn = fn
}

func (p *MockPhone) OnError(fn func(error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onErrorFn = fn
}

func (p *MockPhone) OnCallState(fn func(Call, CallState)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCallStateFn = fn
}

func (p *MockPhone) OnCallEnded(fn func(Call, EndReason)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCallEndedFn = fn
}

func (p *MockPhone) OnCallDTMF(fn func(Call, string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCallDTMFFn = fn
}

func (p *MockPhone) OnVoicemail(fn func(VoicemailStatus)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onVoicemailFn = fn
}

func (p *MockPhone) OnMessage(fn func(SipMessage)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onMessageFn = fn
}

func (p *MockPhone) SendMessage(_ context.Context, _ string, _ string) error {
	return nil
}

func (p *MockPhone) SendMessageWithType(_ context.Context, _ string, _ string, _ string) error {
	return nil
}

func (p *MockPhone) Watch(_ context.Context, _ string, _ func(string, ExtensionState, ExtensionState)) (string, error) {
	return newCallID(), nil
}

func (p *MockPhone) Unwatch(_ string) error {
	return nil
}

func (p *MockPhone) SubscribeEvent(_ context.Context, _ string, _ string, _ int, _ func(NotifyEvent)) (string, error) {
	return newCallID(), nil
}

func (p *MockPhone) UnsubscribeEvent(_ string) error {
	return nil
}

// wireCallCallbacks hooks phone-level call callbacks onto a MockCall's
// internal fields so they coexist with user-set per-call callbacks.
// Must be called with p.mu held.
func (p *MockPhone) wireCallCallbacks(c *MockCall) {
	if p.onCallStateFn != nil {
		fn := p.onCallStateFn
		c.onStatePhone = func(state CallState) { fn(c, state) }
	}
	if p.onCallEndedFn != nil {
		fn := p.onCallEndedFn
		c.onEndedPhone = func(reason EndReason) { fn(c, reason) }
	}
	if p.onCallDTMFFn != nil {
		fn := p.onCallDTMFFn
		c.onDTMFPhone = func(digit string) { fn(c, digit) }
	}
}

func (p *MockPhone) AttendedTransfer(callA Call, callB Call) error {
	return callA.AttendedTransfer(callB)
}

func (p *MockPhone) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]Call, 0, len(p.calls))
	for _, c := range p.calls {
		result = append(result, c)
	}
	return result
}

func (p *MockPhone) FindCall(callID string) Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.calls[callID]
	if !ok {
		return nil
	}
	return c
}

func (p *MockPhone) State() PhoneState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// untrackCall removes a call from the active calls map.
func (p *MockPhone) untrackCall(callID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.calls, callID)
}

// --- Test simulation methods ---

// SimulateIncoming creates an incoming MockCall and fires the OnIncoming callback.
func (p *MockPhone) SimulateIncoming(from string) {
	c := NewMockCall()
	c.SetRemoteURI(from)

	p.mu.Lock()
	p.lastCall = c
	p.calls[c.CallID()] = c
	p.wireCallCallbacks(c)
	callID := c.CallID()
	c.onEndedCleanup = func(_ EndReason) { p.untrackCall(callID) }
	fn := p.onIncomingFn
	p.mu.Unlock()

	if fn != nil {
		fn(c)
	}
}

// SimulateError fires the OnError callback with the given error.
func (p *MockPhone) SimulateError(err error) {
	p.mu.Lock()
	fn := p.onErrorFn
	p.mu.Unlock()
	if fn != nil {
		fn(err)
	}
}

// SimulateMWI fires the OnVoicemail callback with the given status.
func (p *MockPhone) SimulateMWI(status VoicemailStatus) {
	p.mu.Lock()
	fn := p.onVoicemailFn
	p.mu.Unlock()
	if fn != nil {
		fn(status)
	}
}

// SimulateMessage fires the OnMessage callback with the given message.
func (p *MockPhone) SimulateMessage(msg SipMessage) {
	p.mu.Lock()
	fn := p.onMessageFn
	p.mu.Unlock()
	if fn != nil {
		fn(msg)
	}
}

// LastCall returns the most recent call (dialed or incoming).
func (p *MockPhone) LastCall() Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastCall
}

// Ensure MockPhone satisfies Phone at compile time.
var _ Phone = (*MockPhone)(nil)
