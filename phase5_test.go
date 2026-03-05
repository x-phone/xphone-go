package xphone

import (
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/testutil"
)

// ==========================================================================
// Mute / Unmute
// ==========================================================================

func TestCall_Mute_SuppressesOutboundPCM(t *testing.T) {
	c := activeCall()
	defer c.stopMedia()

	require.NoError(t, c.Mute())

	// Write a PCM frame — should be silently dropped (muted).
	frame := make([]int16, 160)
	frame[0] = 9999
	select {
	case c.PCMWriter() <- frame:
	case <-time.After(100 * time.Millisecond):
	}

	// No packet should appear on sentRTP.
	time.Sleep(50 * time.Millisecond)
	pkts := drainPackets(c.sentRTP)
	assert.Empty(t, pkts, "PCMWriter output must be suppressed while muted")
}

func TestCall_Mute_SuppressesOutboundRTPWriter(t *testing.T) {
	c := activeCall()
	defer c.stopMedia()

	require.NoError(t, c.Mute())

	pkt := &rtp.Packet{Header: rtp.Header{SequenceNumber: 42, PayloadType: 0}}
	select {
	case c.RTPWriter() <- pkt:
	case <-time.After(100 * time.Millisecond):
	}

	time.Sleep(50 * time.Millisecond)
	pkts := drainPackets(c.sentRTP)
	assert.Empty(t, pkts, "RTPWriter output must be suppressed while muted")
}

func TestCall_Unmute_RestoresOutboundPCM(t *testing.T) {
	c := activeCall()
	defer c.stopMedia()

	require.NoError(t, c.Mute())
	require.NoError(t, c.Unmute())

	// PCM frame should now produce outbound RTP again.
	frame := make([]int16, 160)
	select {
	case c.PCMWriter() <- frame:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("PCMWriter blocked")
	}

	sent := readPacket(t, c.sentRTP, 200*time.Millisecond)
	assert.NotNil(t, sent, "PCMWriter should produce packets after Unmute")
}

func TestCall_Unmute_RestoresOutboundRTPWriter(t *testing.T) {
	c := activeCall()
	defer c.stopMedia()

	require.NoError(t, c.Mute())
	require.NoError(t, c.Unmute())

	pkt := &rtp.Packet{Header: rtp.Header{SequenceNumber: 77, PayloadType: 0}}
	select {
	case c.RTPWriter() <- pkt:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RTPWriter blocked")
	}

	sent := readPacket(t, c.sentRTP, 200*time.Millisecond)
	assert.Equal(t, uint16(77), sent.SequenceNumber, "RTPWriter should forward packets after Unmute")
}

func TestCall_Mute_InboundStillFlows(t *testing.T) {
	c := activeCall()
	defer c.stopMedia()

	require.NoError(t, c.Mute())

	// Inject inbound RTP — should still arrive on readers.
	c.injectRTP(&rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, PayloadType: 0},
		Payload: make([]byte, 160),
	})

	raw := readPacket(t, c.RTPRawReader(), 200*time.Millisecond)
	assert.NotNil(t, raw, "inbound RTP must still flow while muted")
}

func TestCall_Mute_WhenNotActiveReturnsError(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	assert.ErrorIs(t, c.Mute(), ErrInvalidState)
}

func TestCall_Unmute_WhenNotActiveReturnsError(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	assert.ErrorIs(t, c.Unmute(), ErrInvalidState)
}

func TestCall_Mute_WhenAlreadyMutedReturnsError(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.Accept()
	require.NoError(t, c.Mute())
	assert.ErrorIs(t, c.Mute(), ErrAlreadyMuted)
}

func TestCall_Unmute_WhenNotMutedReturnsError(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.Accept()
	assert.ErrorIs(t, c.Unmute(), ErrNotMuted)
}

func TestCall_Mute_WhenOnHoldReturnsError(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.Accept()
	c.Hold()
	assert.ErrorIs(t, c.Mute(), ErrInvalidState)
}

func TestCall_Unmute_WhenEndedReturnsError(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.Accept()
	c.End()
	assert.ErrorIs(t, c.Unmute(), ErrInvalidState)
}

func TestCall_OnMute_CallbackFires(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.Accept()

	fired := make(chan struct{}, 1)
	c.OnMute(func() { fired <- struct{}{} })

	c.Mute()

	select {
	case <-fired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnMute callback never fired")
	}
}

func TestCall_OnUnmute_CallbackFires(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.Accept()

	fired := make(chan struct{}, 1)
	c.OnUnmute(func() { fired <- struct{}{} })

	c.Mute()
	c.Unmute()

	select {
	case <-fired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnUnmute callback never fired")
	}
}

// ==========================================================================
// RemoteURI / RemoteIP / RemotePort
// ==========================================================================

func TestCall_RemoteURI_FromDialogHeader(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"From": {"<sip:1001@pbx.example.com>"},
	})
	c := newInboundCall(dialog)
	assert.Equal(t, "sip:1001@pbx.example.com", c.RemoteURI())
}

func TestCall_RemoteURI_EmptyWhenNoFromHeader(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	assert.Equal(t, "", c.RemoteURI())
}

func TestCall_RemoteURI_StripsDisplayName(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"From": {"\"Alice\" <sip:alice@example.com>"},
	})
	c := newInboundCall(dialog)
	assert.Equal(t, "sip:alice@example.com", c.RemoteURI())
}

func TestCall_RemoteIP_FromRemoteSDP(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.remoteSDP = testSDP("192.168.1.200", 5004, "sendrecv", 0)
	assert.Equal(t, "192.168.1.200", c.RemoteIP())
}

func TestCall_RemoteIP_EmptyBeforeSDP(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	assert.Equal(t, "", c.RemoteIP())
}

func TestCall_RemotePort_FromRemoteSDP(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.remoteSDP = testSDP("192.168.1.200", 5004, "sendrecv", 0)
	assert.Equal(t, 5004, c.RemotePort())
}

func TestCall_RemotePort_ZeroBeforeSDP(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	assert.Equal(t, 0, c.RemotePort())
}

func TestCall_RemoteMedia_UpdatesAfterReInvite(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.Accept()
	c.remoteSDP = testSDP("192.168.1.100", 5000, "sendrecv", 0)
	assert.Equal(t, "192.168.1.100", c.RemoteIP())

	// Re-INVITE changes remote IP and port.
	c.simulateReInvite(testSDP("10.0.0.50", 6000, "sendrecv", 0))
	assert.Equal(t, "10.0.0.50", c.RemoteIP())
	assert.Equal(t, 6000, c.RemotePort())
}

// ==========================================================================
// OnState callback
// ==========================================================================

func TestCall_OnState_FiresOnAccept(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.Accept()

	select {
	case s := <-states:
		assert.Equal(t, StateActive, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on Accept")
	}
}

func TestCall_OnState_FiresOnReject(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.Reject(486, "Busy Here")

	select {
	case s := <-states:
		assert.Equal(t, StateEnded, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on Reject")
	}
}

func TestCall_OnState_FiresOnEnd(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.Accept()

	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.End()

	select {
	case s := <-states:
		assert.Equal(t, StateEnded, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on End")
	}
}

func TestCall_OnState_FiresOnHold(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.Accept()

	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.Hold()

	select {
	case s := <-states:
		assert.Equal(t, StateOnHold, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on Hold")
	}
}

func TestCall_OnState_FiresOnResume(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.Accept()
	c.Hold()

	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.Resume()

	select {
	case s := <-states:
		assert.Equal(t, StateActive, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on Resume")
	}
}

func TestCall_OnState_FiresOnRemoteBye(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.Accept()

	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.simulateBye()

	select {
	case s := <-states:
		assert.Equal(t, StateEnded, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on remote BYE")
	}
}

func TestCall_OnState_FiresOnOutboundRinging(t *testing.T) {
	c := newOutboundCall(testutil.NewMockDialog())
	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.simulateResponse(180, "Ringing")

	select {
	case s := <-states:
		assert.Equal(t, StateRemoteRinging, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on 180")
	}
}

func TestCall_OnState_FiresOnOutbound200(t *testing.T) {
	c := newOutboundCall(testutil.NewMockDialog())
	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.simulateResponse(200, "OK")

	select {
	case s := <-states:
		assert.Equal(t, StateActive, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on 200 OK")
	}
}

func TestCall_OnState_FiresOnEarlyMedia(t *testing.T) {
	c := newOutboundCall(testutil.NewMockDialog(), WithEarlyMedia())
	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.simulateResponse(183, "Session Progress")

	select {
	case s := <-states:
		assert.Equal(t, StateEarlyMedia, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on 183 EarlyMedia")
	}
}

func TestCall_OnState_DoesNotFireOnMute(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	c.Accept()

	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.Mute()

	// Mute does not change CallState — no OnState should fire.
	time.Sleep(100 * time.Millisecond)
	select {
	case s := <-states:
		t.Fatalf("OnState should not fire on Mute, got %v", s)
	default:
		// correct
	}
}

func TestCall_OnState_TracksFullLifecycle(t *testing.T) {
	c := newOutboundCall(testutil.NewMockDialog())
	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	// Read each state after each operation to avoid goroutine ordering assumptions.
	c.simulateResponse(180, "Ringing")
	select {
	case s := <-states:
		assert.Equal(t, StateRemoteRinging, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired for StateRemoteRinging")
	}

	c.simulateResponse(200, "OK")
	select {
	case s := <-states:
		assert.Equal(t, StateActive, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired for StateActive")
	}

	c.Hold()
	select {
	case s := <-states:
		assert.Equal(t, StateOnHold, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired for StateOnHold")
	}

	c.Resume()
	select {
	case s := <-states:
		assert.Equal(t, StateActive, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired for StateActive after Resume")
	}

	c.End()
	select {
	case s := <-states:
		assert.Equal(t, StateEnded, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired for StateEnded")
	}
}
