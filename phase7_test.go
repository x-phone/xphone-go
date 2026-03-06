package xphone

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ==========================================================================
// MockCall — implements Call interface
// ==========================================================================

func TestMockCall_ImplementsCallInterface(t *testing.T) {
	var _ Call = NewMockCall()
}

func TestMockCall_DefaultState(t *testing.T) {
	c := NewMockCall()
	assert.Equal(t, StateRinging, c.State())
	assert.Equal(t, DirectionInbound, c.Direction())
}

func TestMockCall_SetState(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)
	assert.Equal(t, StateActive, c.State())
}

func TestMockCall_SetDirection(t *testing.T) {
	c := NewMockCall()
	c.SetDirection(DirectionOutbound)
	assert.Equal(t, DirectionOutbound, c.Direction())
}

func TestMockCall_ID(t *testing.T) {
	c := NewMockCall()
	assert.NotEmpty(t, c.ID())
}

func TestMockCall_SetRemoteURI(t *testing.T) {
	c := NewMockCall()
	c.SetRemoteURI("sip:1001@pbx.example.com")
	assert.Equal(t, "sip:1001@pbx.example.com", c.RemoteURI())
}

func TestMockCall_SetRemoteIP(t *testing.T) {
	c := NewMockCall()
	c.SetRemoteIP("192.168.1.100")
	assert.Equal(t, "192.168.1.100", c.RemoteIP())
}

func TestMockCall_SetRemotePort(t *testing.T) {
	c := NewMockCall()
	c.SetRemotePort(5060)
	assert.Equal(t, 5060, c.RemotePort())
}

func TestMockCall_AcceptTransitionsToActive(t *testing.T) {
	c := NewMockCall()
	require.NoError(t, c.Accept())
	assert.Equal(t, StateActive, c.State())
}

func TestMockCall_AcceptWhenNotRingingFails(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)
	assert.ErrorIs(t, c.Accept(), ErrInvalidState)
}

func TestMockCall_RejectTransitionsToEnded(t *testing.T) {
	c := NewMockCall()
	require.NoError(t, c.Reject(486, "Busy Here"))
	assert.Equal(t, StateEnded, c.State())
}

func TestMockCall_EndFromActive(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)
	require.NoError(t, c.End())
	assert.Equal(t, StateEnded, c.State())
}

func TestMockCall_Hold(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)
	require.NoError(t, c.Hold())
	assert.Equal(t, StateOnHold, c.State())
}

func TestMockCall_Resume(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateOnHold)
	require.NoError(t, c.Resume())
	assert.Equal(t, StateActive, c.State())
}

func TestMockCall_Mute(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)
	require.NoError(t, c.Mute())
	assert.True(t, c.Muted())
}

func TestMockCall_Unmute(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)
	c.Mute()
	require.NoError(t, c.Unmute())
	assert.False(t, c.Muted())
}

func TestMockCall_SendDTMF(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)
	require.NoError(t, c.SendDTMF("5"))
	assert.Equal(t, []string{"5"}, c.SentDTMF())
}

func TestMockCall_BlindTransfer(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)
	require.NoError(t, c.BlindTransfer("sip:1003@pbx"))
	assert.Equal(t, "sip:1003@pbx", c.LastTransferTarget())
}

func TestMockCall_SetCodec(t *testing.T) {
	c := NewMockCall()
	c.SetCodec(CodecG722)
	assert.Equal(t, CodecG722, c.Codec())
}

func TestMockCall_SDP(t *testing.T) {
	c := NewMockCall()
	c.SetLocalSDP("v=0\r\n")
	c.SetRemoteSDP("v=0\r\n")
	assert.Equal(t, "v=0\r\n", c.LocalSDP())
	assert.Equal(t, "v=0\r\n", c.RemoteSDP())
}

func TestMockCall_StartTime(t *testing.T) {
	c := NewMockCall()
	now := time.Now()
	c.SetStartTime(now)
	assert.Equal(t, now, c.StartTime())
}

func TestMockCall_Duration(t *testing.T) {
	c := NewMockCall()
	c.SetStartTime(time.Now().Add(-5 * time.Second))
	assert.InDelta(t, 5.0, c.Duration().Seconds(), 0.5)
}

func TestMockCall_Headers(t *testing.T) {
	c := NewMockCall()
	c.SetHeader("X-Custom", "value1")
	assert.Equal(t, []string{"value1"}, c.Header("X-Custom"))
}

func TestMockCall_RTPChannels(t *testing.T) {
	c := NewMockCall()
	assert.NotNil(t, c.RTPRawReader())
	assert.NotNil(t, c.RTPReader())
	assert.NotNil(t, c.RTPWriter())
	assert.NotNil(t, c.PCMReader())
	assert.NotNil(t, c.PCMWriter())
}

func TestMockCall_Callbacks(t *testing.T) {
	c := NewMockCall()

	dtmfFired := make(chan string, 1)
	c.OnDTMF(func(digit string) { dtmfFired <- digit })

	// Fire DTMF callback via SimulateDTMF.
	c.SimulateDTMF("9")
	select {
	case d := <-dtmfFired:
		assert.Equal(t, "9", d)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnDTMF not fired")
	}
}

func TestMockCall_OnEndedFired(t *testing.T) {
	c := NewMockCall()
	fired := make(chan EndReason, 1)
	c.OnEnded(func(r EndReason) { fired <- r })

	c.SetState(StateActive)
	c.End()

	select {
	case r := <-fired:
		assert.Equal(t, EndedByLocal, r)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnEnded not fired")
	}
}

func TestMockCall_OnStateFired(t *testing.T) {
	c := NewMockCall()
	fired := make(chan CallState, 1)
	c.OnState(func(s CallState) { fired <- s })

	c.Accept()

	select {
	case s := <-fired:
		assert.Equal(t, StateActive, s)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnState not fired")
	}
}

func TestMockCall_IDAndCallID(t *testing.T) {
	c := NewMockCall()
	assert.NotEmpty(t, c.ID())
	assert.True(t, strings.HasPrefix(c.ID(), "CA"), "ID should have CA prefix")
	assert.NotEmpty(t, c.CallID())
	assert.NotEqual(t, c.ID(), c.CallID(), "ID and CallID should be distinct")
}

// ==========================================================================
// MockPhone — implements Phone interface
// ==========================================================================

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

// ==========================================================================
// MockCall — RTP channel operations
// ==========================================================================

func TestMockCall_InjectRTPToReader(t *testing.T) {
	c := NewMockCall()
	pkt := &rtp.Packet{Header: rtp.Header{SequenceNumber: 42}}
	c.InjectRTP(pkt)

	select {
	case got := <-c.RTPReader():
		assert.Equal(t, uint16(42), got.Header.SequenceNumber)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("packet not received on RTPReader")
	}
}
