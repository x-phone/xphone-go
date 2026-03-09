package xphone

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- MockPhone — implements Phone interface ---

func TestMockPhone_ImplementsPhoneInterface(t *testing.T) {
	var _ Phone = NewMockPhone()
}

func TestMockPhone_DefaultState(t *testing.T) {
	p := NewMockPhone()
	assert.Equal(t, PhoneStateDisconnected, p.State())
}

func TestMockPhone_Connect(t *testing.T) {
	p := NewMockPhone()
	require.NoError(t, p.Connect(context.Background()))
	assert.Equal(t, PhoneStateRegistered, p.State())
}

func TestMockPhone_ConnectTwiceFails(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())
	assert.ErrorIs(t, p.Connect(context.Background()), ErrAlreadyConnected)
}

func TestMockPhone_Disconnect(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())
	require.NoError(t, p.Disconnect())
	assert.Equal(t, PhoneStateDisconnected, p.State())
}

func TestMockPhone_DisconnectWhenDisconnectedFails(t *testing.T) {
	p := NewMockPhone()
	assert.ErrorIs(t, p.Disconnect(), ErrNotConnected)
}

func TestMockPhone_OnRegisteredFires(t *testing.T) {
	p := NewMockPhone()
	fired := make(chan struct{}, 1)
	p.OnRegistered(func() { fired <- struct{}{} })

	p.Connect(context.Background())

	select {
	case <-fired:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnRegistered not fired")
	}
}

func TestMockPhone_OnUnregisteredFires(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())

	fired := make(chan struct{}, 1)
	p.OnUnregistered(func() { fired <- struct{}{} })

	p.Disconnect()

	select {
	case <-fired:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnUnregistered not fired")
	}
}

func TestMockPhone_OnErrorFires(t *testing.T) {
	p := NewMockPhone()
	fired := make(chan error, 1)
	p.OnError(func(err error) { fired <- err })

	p.SimulateError(ErrRegistrationFailed)

	select {
	case err := <-fired:
		assert.ErrorIs(t, err, ErrRegistrationFailed)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnError not fired")
	}
}

func TestMockPhone_SimulateIncoming(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())

	var received Call
	p.OnIncoming(func(c Call) { received = c })

	p.SimulateIncoming("sip:1001@pbx")

	require.NotNil(t, received)
	assert.Equal(t, StateRinging, received.State())
}

func TestMockPhone_Dial(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())

	c, err := p.Dial(context.Background(), "sip:1002@pbx")
	require.NoError(t, err)
	assert.Equal(t, StateActive, c.State())
	assert.Equal(t, DirectionOutbound, c.Direction())
}

func TestMockPhone_DialWhenDisconnectedFails(t *testing.T) {
	p := NewMockPhone()
	_, err := p.Dial(context.Background(), "sip:1002@pbx")
	assert.ErrorIs(t, err, ErrNotRegistered)
}

func TestMockPhone_LastCall(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())

	c, _ := p.Dial(context.Background(), "sip:1002@pbx")
	assert.Equal(t, c.ID(), p.LastCall().ID())
}

// --- Phone-level call callbacks on MockPhone ---

func TestMockPhone_OnCallStateFiresOnDial(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())

	type stateEvent struct {
		callID string
		state  CallState
	}
	ch := make(chan stateEvent, 4)
	p.OnCallState(func(c Call, s CallState) { ch <- stateEvent{c.ID(), s} })

	c, err := p.Dial(context.Background(), "sip:1002@pbx")
	require.NoError(t, err)

	// Hold the call to trigger a state change.
	require.NoError(t, c.Hold())

	select {
	case ev := <-ch:
		assert.Equal(t, c.ID(), ev.callID)
		assert.Equal(t, StateOnHold, ev.state)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnCallState never fired")
	}
}

func TestMockPhone_OnCallEndedFiresOnEnd(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())

	type endEvent struct {
		callID string
		reason EndReason
	}
	ch := make(chan endEvent, 1)
	p.OnCallEnded(func(c Call, r EndReason) { ch <- endEvent{c.ID(), r} })

	c, _ := p.Dial(context.Background(), "sip:1002@pbx")
	require.NoError(t, c.End())

	select {
	case ev := <-ch:
		assert.Equal(t, c.ID(), ev.callID)
		assert.Equal(t, EndedByLocal, ev.reason)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnCallEnded never fired")
	}
}

func TestMockPhone_OnCallDTMFFiresOnIncoming(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())

	type dtmfEvent struct {
		callID string
		digit  string
	}
	ch := make(chan dtmfEvent, 1)
	p.OnCallDTMF(func(c Call, d string) { ch <- dtmfEvent{c.ID(), d} })

	p.SimulateIncoming("sip:1001@pbx")
	mc := p.LastCall().(*MockCall)
	mc.SimulateDTMF("5")

	select {
	case ev := <-ch:
		assert.Equal(t, mc.ID(), ev.callID)
		assert.Equal(t, "5", ev.digit)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnCallDTMF never fired")
	}
}

// --- FindCall ---

func TestMockPhone_FindCall(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())

	c, _ := p.Dial(context.Background(), "sip:1002@pbx")
	found := p.FindCall(c.CallID())
	require.NotNil(t, found)
	assert.Equal(t, c.ID(), found.ID())
}

func TestMockPhone_FindCallReturnsNilForUnknown(t *testing.T) {
	p := NewMockPhone()
	assert.Nil(t, p.FindCall("nonexistent"))
}

// --- Calls() ---

func TestMockPhone_CallsReturnsEmpty(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())
	assert.Empty(t, p.Calls())
}

func TestMockPhone_CallsReturnsConcurrentCalls(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())

	c1, _ := p.Dial(context.Background(), "sip:1001@pbx")
	c2, _ := p.Dial(context.Background(), "sip:1002@pbx")

	calls := p.Calls()
	assert.Len(t, calls, 2)

	ids := map[string]bool{c1.ID(): true, c2.ID(): true}
	for _, c := range calls {
		assert.True(t, ids[c.ID()])
	}
}

func TestMockPhone_CallsIncludesIncoming(t *testing.T) {
	p := NewMockPhone()
	p.Connect(context.Background())

	p.OnIncoming(func(Call) {})
	p.SimulateIncoming("sip:1001@pbx")
	p.SimulateIncoming("sip:1003@pbx")

	assert.Len(t, p.Calls(), 2)
}
