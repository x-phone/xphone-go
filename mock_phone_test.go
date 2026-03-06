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
