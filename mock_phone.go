package xphone

import (
	"context"
	"sync"
)

// MockPhone is a mock implementation of the Phone interface for testing.
// It simulates registration, dialing, and incoming calls without SIP transport.
type MockPhone struct {
	mu sync.Mutex

	state           PhoneState
	onIncomingFns   []func(Call)
	onRegisteredFns []func()
	onUnregFns      []func()
	onErrorFns      []func(error)
	onCallStateFns  []func(Call, CallState)
	onCallEndedFns  []func(Call, EndReason)
	onCallDTMFFns   []func(Call, string)
	onVoicemailFns  []func(VoicemailStatus)
	onMessageFns    []func(SipMessage)
	lastCall        Call
	calls           map[string]Call
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
	fns := make([]func(), len(p.onRegisteredFns))
	copy(fns, p.onRegisteredFns)
	p.mu.Unlock()
	for _, fn := range fns {
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
	unregFns := make([]func(), len(p.onUnregFns))
	copy(unregFns, p.onUnregFns)
	activeCalls := make([]*MockCall, 0, len(p.calls))
	for _, c := range p.calls {
		activeCalls = append(activeCalls, c.(*MockCall))
	}
	p.calls = make(map[string]Call)
	p.mu.Unlock()
	for _, c := range activeCalls {
		c.End()
	}
	for _, fn := range unregFns {
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
	p.onIncomingFns = append(p.onIncomingFns, fn)
}

func (p *MockPhone) OnRegistered(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onRegisteredFns = append(p.onRegisteredFns, fn)
}

func (p *MockPhone) OnUnregistered(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onUnregFns = append(p.onUnregFns, fn)
}

func (p *MockPhone) OnError(fn func(error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onErrorFns = append(p.onErrorFns, fn)
}

func (p *MockPhone) OnCallState(fn func(Call, CallState)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCallStateFns = append(p.onCallStateFns, fn)
}

func (p *MockPhone) OnCallEnded(fn func(Call, EndReason)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCallEndedFns = append(p.onCallEndedFns, fn)
}

func (p *MockPhone) OnCallDTMF(fn func(Call, string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCallDTMFFns = append(p.onCallDTMFFns, fn)
}

func (p *MockPhone) OnVoicemail(fn func(VoicemailStatus)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onVoicemailFns = append(p.onVoicemailFns, fn)
}

func (p *MockPhone) OnMessage(fn func(SipMessage)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onMessageFns = append(p.onMessageFns, fn)
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
	if len(p.onCallStateFns) > 0 {
		fns := make([]func(Call, CallState), len(p.onCallStateFns))
		copy(fns, p.onCallStateFns)
		c.onStatePhone = func(state CallState) {
			for _, fn := range fns {
				fn(c, state)
			}
		}
	}
	if len(p.onCallEndedFns) > 0 {
		fns := make([]func(Call, EndReason), len(p.onCallEndedFns))
		copy(fns, p.onCallEndedFns)
		c.onEndedPhone = func(reason EndReason) {
			for _, fn := range fns {
				fn(c, reason)
			}
		}
	}
	if len(p.onCallDTMFFns) > 0 {
		fns := make([]func(Call, string), len(p.onCallDTMFFns))
		copy(fns, p.onCallDTMFFns)
		c.onDTMFPhone = func(digit string) {
			for _, fn := range fns {
				fn(c, digit)
			}
		}
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
	fns := make([]func(Call), len(p.onIncomingFns))
	copy(fns, p.onIncomingFns)
	p.mu.Unlock()

	for _, fn := range fns {
		fn(c)
	}
}

// SimulateError fires the OnError callbacks with the given error.
func (p *MockPhone) SimulateError(err error) {
	p.mu.Lock()
	fns := make([]func(error), len(p.onErrorFns))
	copy(fns, p.onErrorFns)
	p.mu.Unlock()
	for _, fn := range fns {
		fn(err)
	}
}

// SimulateMWI fires the OnVoicemail callbacks with the given status.
func (p *MockPhone) SimulateMWI(status VoicemailStatus) {
	p.mu.Lock()
	fns := make([]func(VoicemailStatus), len(p.onVoicemailFns))
	copy(fns, p.onVoicemailFns)
	p.mu.Unlock()
	for _, fn := range fns {
		fn(status)
	}
}

// SimulateMessage fires the OnMessage callbacks with the given message.
func (p *MockPhone) SimulateMessage(msg SipMessage) {
	p.mu.Lock()
	fns := make([]func(SipMessage), len(p.onMessageFns))
	copy(fns, p.onMessageFns)
	p.mu.Unlock()
	for _, fn := range fns {
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
