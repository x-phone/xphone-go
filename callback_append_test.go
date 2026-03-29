package xphone

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestCallOnEndedAppends verifies that calling OnEnded twice registers both
// callbacks, and both fire when the call ends.
func TestCallOnEndedAppends(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)

	var count1, count2 int32
	c.OnEnded(func(EndReason) { atomic.AddInt32(&count1, 1) })
	c.OnEnded(func(EndReason) { atomic.AddInt32(&count2, 1) })

	c.End()

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnEnded callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnEnded callback was not called")
	}
}

// TestCallOnStateAppends verifies that calling OnState twice registers both
// callbacks, and both fire on state transitions.
func TestCallOnStateAppends(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateRinging)

	var count1, count2 int32
	c.OnState(func(CallState) { atomic.AddInt32(&count1, 1) })
	c.OnState(func(CallState) { atomic.AddInt32(&count2, 1) })

	c.Accept()

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnState callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnState callback was not called")
	}
}

// TestCallOnDTMFAppends verifies multiple OnDTMF callbacks all fire.
func TestCallOnDTMFAppends(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)

	var count1, count2 int32
	c.OnDTMF(func(string) { atomic.AddInt32(&count1, 1) })
	c.OnDTMF(func(string) { atomic.AddInt32(&count2, 1) })

	c.SimulateDTMF("5")

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnDTMF callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnDTMF callback was not called")
	}
}

// TestCallOnHoldAppends verifies multiple OnHold callbacks all fire.
func TestCallOnHoldAppends(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)

	var count1, count2 int32
	c.OnHold(func() { atomic.AddInt32(&count1, 1) })
	c.OnHold(func() { atomic.AddInt32(&count2, 1) })

	c.Hold()

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnHold callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnHold callback was not called")
	}
}

// TestCallOnResumeAppends verifies multiple OnResume callbacks all fire.
func TestCallOnResumeAppends(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateOnHold)

	var count1, count2 int32
	c.OnResume(func() { atomic.AddInt32(&count1, 1) })
	c.OnResume(func() { atomic.AddInt32(&count2, 1) })

	c.Resume()

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnResume callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnResume callback was not called")
	}
}

// TestCallOnMuteUnmuteAppends verifies multiple OnMute/OnUnmute callbacks all fire.
func TestCallOnMuteUnmuteAppends(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)

	var muteCount1, muteCount2 int32
	c.OnMute(func() { atomic.AddInt32(&muteCount1, 1) })
	c.OnMute(func() { atomic.AddInt32(&muteCount2, 1) })

	c.Mute()

	if atomic.LoadInt32(&muteCount1) != 1 {
		t.Error("first OnMute callback was not called")
	}
	if atomic.LoadInt32(&muteCount2) != 1 {
		t.Error("second OnMute callback was not called")
	}

	var unmuteCount1, unmuteCount2 int32
	c.OnUnmute(func() { atomic.AddInt32(&unmuteCount1, 1) })
	c.OnUnmute(func() { atomic.AddInt32(&unmuteCount2, 1) })

	c.Unmute()

	if atomic.LoadInt32(&unmuteCount1) != 1 {
		t.Error("first OnUnmute callback was not called")
	}
	if atomic.LoadInt32(&unmuteCount2) != 1 {
		t.Error("second OnUnmute callback was not called")
	}
}

// TestPhoneOnRegisteredAppends verifies calling OnRegistered twice fires both.
func TestPhoneOnRegisteredAppends(t *testing.T) {
	p := NewMockPhone()

	var count1, count2 int32
	p.OnRegistered(func() { atomic.AddInt32(&count1, 1) })
	p.OnRegistered(func() { atomic.AddInt32(&count2, 1) })

	p.Connect(context.Background())

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnRegistered callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnRegistered callback was not called")
	}
}

// TestPhoneOnUnregisteredAppends verifies calling OnUnregistered twice fires both.
func TestPhoneOnUnregisteredAppends(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())

	var count1, count2 int32
	p.OnUnregistered(func() { atomic.AddInt32(&count1, 1) })
	p.OnUnregistered(func() { atomic.AddInt32(&count2, 1) })

	p.Disconnect()

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnUnregistered callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnUnregistered callback was not called")
	}
}

// TestPhoneOnErrorAppends verifies calling OnError twice fires both.
func TestPhoneOnErrorAppends(t *testing.T) {
	p := NewMockPhone()

	var count1, count2 int32
	p.OnError(func(error) { atomic.AddInt32(&count1, 1) })
	p.OnError(func(error) { atomic.AddInt32(&count2, 1) })

	p.SimulateError(errors.New("test"))

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnError callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnError callback was not called")
	}
}

// TestPhoneOnIncomingAppends verifies calling OnIncoming twice fires both.
func TestPhoneOnIncomingAppends(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())

	var count1, count2 int32
	p.OnIncoming(func(Call) { atomic.AddInt32(&count1, 1) })
	p.OnIncoming(func(Call) { atomic.AddInt32(&count2, 1) })

	p.SimulateIncoming("sip:alice@example.com")

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnIncoming callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnIncoming callback was not called")
	}
}

// TestPhoneOnCallStateAppends verifies calling OnCallState twice fires both
// when a call state changes.
func TestPhoneOnCallStateAppends(t *testing.T) {
	p := NewMockPhone()

	var count1, count2 int32
	p.OnCallState(func(Call, CallState) { atomic.AddInt32(&count1, 1) })
	p.OnCallState(func(Call, CallState) { atomic.AddInt32(&count2, 1) })

	p.Connect(context.Background())
	p.SimulateIncoming("sip:bob@example.com")

	c := p.LastCall().(*MockCall)
	c.Accept()

	if atomic.LoadInt32(&count1) < 1 {
		t.Error("first OnCallState callback was not called")
	}
	if atomic.LoadInt32(&count2) < 1 {
		t.Error("second OnCallState callback was not called")
	}
}

// TestPhoneOnCallEndedAppends verifies calling OnCallEnded twice fires both.
func TestPhoneOnCallEndedAppends(t *testing.T) {
	p := NewMockPhone()

	var count1, count2 int32
	p.OnCallEnded(func(Call, EndReason) { atomic.AddInt32(&count1, 1) })
	p.OnCallEnded(func(Call, EndReason) { atomic.AddInt32(&count2, 1) })

	p.Connect(context.Background())
	p.SimulateIncoming("sip:bob@example.com")

	c := p.LastCall().(*MockCall)
	c.Accept()
	c.End()

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnCallEnded callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnCallEnded callback was not called")
	}
}

// TestPhoneOnCallDTMFAppends verifies calling OnCallDTMF twice fires both.
func TestPhoneOnCallDTMFAppends(t *testing.T) {
	p := NewMockPhone()

	var count1, count2 int32
	p.OnCallDTMF(func(Call, string) { atomic.AddInt32(&count1, 1) })
	p.OnCallDTMF(func(Call, string) { atomic.AddInt32(&count2, 1) })

	p.Connect(context.Background())
	p.SimulateIncoming("sip:bob@example.com")

	c := p.LastCall().(*MockCall)
	c.Accept()
	c.SimulateDTMF("1")

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnCallDTMF callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnCallDTMF callback was not called")
	}
}

// TestPhoneOnMessageAppends verifies calling OnMessage twice fires both.
func TestPhoneOnMessageAppends(t *testing.T) {
	p := NewMockPhone()

	var count1, count2 int32
	p.OnMessage(func(SipMessage) { atomic.AddInt32(&count1, 1) })
	p.OnMessage(func(SipMessage) { atomic.AddInt32(&count2, 1) })

	p.SimulateMessage(SipMessage{From: "alice", Body: "hi"})

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnMessage callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnMessage callback was not called")
	}
}

// TestPhoneOnVoicemailAppends verifies calling OnVoicemail twice fires both.
func TestPhoneOnVoicemailAppends(t *testing.T) {
	p := NewMockPhone()

	var count1, count2 int32
	p.OnVoicemail(func(VoicemailStatus) { atomic.AddInt32(&count1, 1) })
	p.OnVoicemail(func(VoicemailStatus) { atomic.AddInt32(&count2, 1) })

	p.SimulateMWI(VoicemailStatus{MessagesWaiting: true})

	if atomic.LoadInt32(&count1) != 1 {
		t.Error("first OnVoicemail callback was not called")
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Error("second OnVoicemail callback was not called")
	}
}
