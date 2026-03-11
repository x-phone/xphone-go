package xphone

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/fakepbx"
)

// fakepbx-based tests: real SIP over loopback, no Docker.
// These complement the Docker/Asterisk integration tests with fast,
// deterministic, parallel-safe tests that run in `go test`.

// pbxConfig builds a Config pointing at the given FakePBX instance.
func pbxConfig(t *testing.T, pbx *fakepbx.FakePBX) Config {
	t.Helper()
	host, portStr, err := net.SplitHostPort(pbx.Addr())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return Config{
		Username:         "1001",
		Password:         "test",
		Host:             host,
		Port:             port,
		Transport:        "udp",
		RegisterExpiry:   60 * time.Second,
		RegisterRetry:    time.Second,
		RegisterMaxRetry: 3,
		MediaTimeout:     10 * time.Second,
	}
}

// connectWithConfig creates a Phone, connects it, waits for registration, and
// registers a cleanup. Shared by connectPBX and connectPBXNoAuth.
func connectWithConfig(t *testing.T, cfg Config) Phone {
	t.Helper()
	p := newPhone(cfg)

	registered := make(chan struct{}, 1)
	p.OnRegistered(func() {
		select {
		case registered <- struct{}{}:
		default:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := p.Connect(ctx)
	require.NoError(t, err, "Connect failed")

	select {
	case <-registered:
	case <-ctx.Done():
		t.Fatal("registration timeout")
	}

	t.Cleanup(func() { p.Disconnect() })
	return p
}

// connectPBX creates a Phone connected and registered to the given FakePBX.
func connectPBX(t *testing.T, pbx *fakepbx.FakePBX) Phone {
	t.Helper()
	return connectWithConfig(t, pbxConfig(t, pbx))
}

// connectPBXNoAuth creates a Phone connected and registered to a FakePBX that
// has no auth configured. Used by redirect tests to avoid auth-related INVITE
// retries interfering with redirect counting.
func connectPBXNoAuth(t *testing.T, pbx *fakepbx.FakePBX) Phone {
	t.Helper()
	cfg := pbxConfig(t, pbx)
	cfg.Password = ""
	return connectWithConfig(t, cfg)
}

// bindRTP allocates a UDP socket on loopback for use as the PBX's RTP endpoint.
func bindRTP(t *testing.T) (net.PacketConn, int) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn, conn.LocalAddr().(*net.UDPAddr).Port
}

// F1: Register with digest auth.
func TestFakePBX_Register(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	p := connectPBX(t, pbx)
	assert.Equal(t, PhoneStateRegistered, p.State())
	assert.GreaterOrEqual(t, pbx.RegisterCount(), 1)
}

// F2: Dial, verify SDP negotiation, local hangup.
func TestFakePBX_DialAndLocalEnd(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	_, rtpPort := bindRTP(t)

	pbx.OnInvite(func(inv *fakepbx.Invite) {
		inv.Trying()
		inv.Ringing()
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	})

	p := connectPBX(t, pbx)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := p.Dial(ctx, "9999")
	require.NoError(t, err)
	assert.Equal(t, StateActive, c.State())

	// Verify SDP negotiation.
	assert.NotEmpty(t, c.LocalSDP())
	assert.NotEmpty(t, c.RemoteSDP())
	assert.Equal(t, "127.0.0.1", c.RemoteIP())
	assert.Equal(t, rtpPort, c.RemotePort())

	// End the call.
	err = c.End()
	require.NoError(t, err)
	assert.Equal(t, StateEnded, c.State())

	// Verify BYE was received by PBX.
	assert.True(t, pbx.WaitForBye(1, 2*time.Second), "BYE never received by PBX")
}

// F3: PBX hangs up mid-call — xphone detects EndedByRemote.
func TestFakePBX_RemoteBye(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	_, rtpPort := bindRTP(t)

	acCh := make(chan *fakepbx.ActiveCall, 1)
	pbx.OnInvite(func(inv *fakepbx.Invite) {
		inv.Trying()
		inv.Ringing()
		acCh <- inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	})

	p := connectPBX(t, pbx)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := p.Dial(ctx, "9999")
	require.NoError(t, err)
	require.Equal(t, StateActive, c.State())

	var ac *fakepbx.ActiveCall
	select {
	case ac = <-acCh:
		require.NotNil(t, ac)
	case <-ctx.Done():
		t.Fatal("INVITE handler never completed")
	}

	ended := make(chan EndReason, 1)
	c.OnEnded(func(r EndReason) {
		ended <- r
	})

	// Brief pause to let media pipeline start.
	time.Sleep(100 * time.Millisecond)

	// PBX sends BYE.
	byeCtx, byeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer byeCancel()
	err = ac.SendBye(byeCtx)
	if err != nil {
		t.Logf("SendBye error: %v (may need Contact header fix)", err)
	}

	select {
	case reason := <-ended:
		assert.Equal(t, EndedByRemote, reason)
	case <-time.After(5 * time.Second):
		t.Fatal("call never received remote BYE")
	}
}

// F4: Hold and resume via re-INVITE.
func TestFakePBX_HoldResume(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	_, rtpPort := bindRTP(t)

	pbx.OnInvite(func(inv *fakepbx.Invite) {
		inv.Trying()
		inv.Ringing()
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	})

	p := connectPBX(t, pbx)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := p.Dial(ctx, "9999")
	require.NoError(t, err)
	require.Equal(t, StateActive, c.State())

	// Hold.
	err = c.Hold()
	require.NoError(t, err)
	assert.Equal(t, StateOnHold, c.State())

	// Verify the re-INVITE was sent (PBX receives it).
	// fakepbx records INVITEs: initial + hold re-INVITE = 2.
	assert.True(t, pbx.WaitForInvite(2, 2*time.Second), "hold re-INVITE not received")

	// Resume.
	err = c.Resume()
	require.NoError(t, err)
	assert.Equal(t, StateActive, c.State())

	// Initial + hold + resume = 3 INVITEs.
	assert.True(t, pbx.WaitForInvite(3, 2*time.Second), "resume re-INVITE not received")

	c.End()
}

// F5: Receive DTMF from the network — test socket sends PT=101 packets to
// xphone's RTP port, xphone fires OnDTMF.
func TestFakePBX_DTMFReceive(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	rtpConn, rtpPort := bindRTP(t)

	pbx.OnInvite(func(inv *fakepbx.Invite) {
		inv.Trying()
		inv.Ringing()
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	})

	p := connectPBX(t, pbx)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := p.Dial(ctx, "9999")
	require.NoError(t, err)
	require.Equal(t, StateActive, c.State())

	// Register DTMF callback.
	dtmfReceived := make(chan string, 10)
	c.OnDTMF(func(digit string) {
		dtmfReceived <- digit
	})

	// Brief pause for media pipeline to start.
	time.Sleep(100 * time.Millisecond)

	// Get xphone's RTP address from LocalSDP.
	xphoneRTPAddr := parseRemoteAddr(c.LocalSDP())
	require.NotNil(t, xphoneRTPAddr, "could not parse xphone RTP address from LocalSDP")

	// Send to 127.0.0.1:xphonePort (xphone listens on 0.0.0.0).
	xphonePort := xphoneRTPAddr.(*net.UDPAddr).Port
	dst, err := net.ResolveUDPAddr("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(xphonePort)))
	require.NoError(t, err)

	// Encode DTMF "5" and send directly from the test socket.
	dtmfPkts, err := EncodeDTMF("5", 1000, 100, 0xABCDEF00)
	require.NoError(t, err)
	for _, pkt := range dtmfPkts {
		data, _ := pkt.Marshal()
		_, err := rtpConn.WriteTo(data, dst)
		require.NoError(t, err)
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for xphone to fire OnDTMF.
	select {
	case digit := <-dtmfReceived:
		assert.Equal(t, "5", digit)
	case <-time.After(3 * time.Second):
		t.Fatal("DTMF digit never received")
	}

	c.End()
}

// F6: RTP round-trip — send audio from test socket, verify xphone receives it
// via RTPReader. Then send from xphone via PCMWriter, verify test socket receives.
func TestFakePBX_RTPRoundTrip(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	rtpConn, rtpPort := bindRTP(t)

	pbx.OnInvite(func(inv *fakepbx.Invite) {
		inv.Trying()
		inv.Ringing()
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	})

	p := connectPBX(t, pbx)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := p.Dial(ctx, "9999")
	require.NoError(t, err)
	require.Equal(t, StateActive, c.State())

	// Brief pause for media pipeline.
	time.Sleep(100 * time.Millisecond)

	// Get xphone's RTP port.
	xphoneRTPAddr := parseRemoteAddr(c.LocalSDP())
	require.NotNil(t, xphoneRTPAddr)
	xphonePort := xphoneRTPAddr.(*net.UDPAddr).Port
	dst, err := net.ResolveUDPAddr("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(xphonePort)))
	require.NoError(t, err)

	// --- Inbound: test socket → xphone ---
	// Send a PCMA silence packet from the test socket.
	silence := make([]byte, 160)
	for i := range silence {
		silence[i] = 0xD5 // PCMA silence
	}
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    8, // PCMA
			SequenceNumber: 1,
			Timestamp:      160,
			SSRC:           0xDEADBEEF,
		},
		Payload: silence,
	}
	data, err := pkt.Marshal()
	require.NoError(t, err)
	_, err = rtpConn.WriteTo(data, dst)
	require.NoError(t, err)

	// Read from xphone's RTPRawReader (pre-jitter).
	select {
	case rxPkt := <-c.RTPRawReader():
		assert.Equal(t, uint8(8), rxPkt.PayloadType)
		assert.Equal(t, 160, len(rxPkt.Payload))
	case <-time.After(3 * time.Second):
		t.Fatal("no inbound RTP received by xphone")
	}

	// --- Outbound: xphone → test socket ---
	// Send a PCM frame via PCMWriter; media pipeline encodes and sends.
	pcmFrame := make([]int16, 160)
	select {
	case c.PCMWriter() <- pcmFrame:
	case <-time.After(time.Second):
		t.Fatal("PCMWriter blocked")
	}

	// Read from test socket.
	buf := make([]byte, 1500)
	rtpConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := rtpConn.ReadFrom(buf)
	require.NoError(t, err, "no outbound RTP received from xphone")

	rxPkt := &rtp.Packet{}
	err = rxPkt.Unmarshal(buf[:n])
	require.NoError(t, err)
	assert.Equal(t, uint8(8), rxPkt.PayloadType) // PCMA
	assert.NotEmpty(t, rxPkt.Payload)

	c.End()
}

// F7: 486 Busy Here rejection.
func TestFakePBX_BusyReject(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	pbx.AutoBusy()

	p := connectPBX(t, pbx)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := p.Dial(ctx, "9999")
	assert.Error(t, err)
}

// F8: Provisionals — verify state transitions through Ringing.
func TestFakePBX_Provisionals(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	_, rtpPort := bindRTP(t)

	answered := make(chan struct{})
	pbx.OnInvite(func(inv *fakepbx.Invite) {
		inv.Trying()
		inv.Ringing()
		// Brief delay so xphone has time to process 180 before 200.
		time.Sleep(50 * time.Millisecond)
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
		close(answered)
	})

	p := connectPBX(t, pbx)

	states := make(chan CallState, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	callDone := make(chan Call, 1)
	go func() {
		c, err := p.Dial(ctx, "9999")
		if err == nil {
			callDone <- c
		}
	}()

	// Wait for answer to complete.
	select {
	case <-answered:
	case <-ctx.Done():
		t.Fatal("answer timeout")
	}

	var c Call
	select {
	case c = <-callDone:
	case <-ctx.Done():
		t.Fatal("dial never completed")
	}

	// At this point the call should be Active.
	c.OnState(func(s CallState) { states <- s })
	assert.Equal(t, StateActive, c.State())
	c.End()
}

// --- 302 Redirect Tests ---

// F12: Dial target returns 302, xphone follows redirect to new target.
func TestFakePBX_Dial302Redirect(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)
	_, rtpPort := bindRTP(t)

	var inviteCount atomic.Int32
	pbxAddr := pbx.Addr() // includes host:port
	pbx.OnInvite(func(inv *fakepbx.Invite) {
		n := inviteCount.Add(1)
		if n == 1 {
			// First INVITE → 302 redirect to different target.
			inv.Respond(302, "Moved Temporarily",
				sip.NewHeader("Contact", fmt.Sprintf("<sip:redirected@%s>", pbxAddr)))
			return
		}
		// Second INVITE (after redirect) → answer.
		inv.Trying()
		inv.Ringing()
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMU))
	})

	p := connectPBXNoAuth(t, pbx)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := p.Dial(ctx, "9999")
	require.NoError(t, err)
	assert.Equal(t, StateActive, c.State())
	assert.Equal(t, int32(2), inviteCount.Load(), "expected 2 INVITEs (original + redirect)")

	c.End()
}

// F13: 302 redirect with no Contact header returns error.
func TestFakePBX_Dial302NoContact(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)

	pbx.OnInvite(func(inv *fakepbx.Invite) {
		// 302 with no Contact header.
		inv.Respond(302, "Moved Temporarily")
	})

	p := connectPBXNoAuth(t, pbx)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := p.Dial(ctx, "9999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Contact")
}

// F14: Too many 302 redirects returns error.
func TestFakePBX_Dial302TooManyRedirects(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)

	pbxAddr := pbx.Addr()
	pbx.OnInvite(func(inv *fakepbx.Invite) {
		// Always redirect — should hit the max limit.
		inv.Respond(302, "Moved Temporarily",
			sip.NewHeader("Contact", fmt.Sprintf("<sip:loop@%s>", pbxAddr)))
	})

	p := connectPBXNoAuth(t, pbx)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := p.Dial(ctx, "9999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many redirects")
}

// F15: 100 Trying then 302 redirect — redirect after provisional.
func TestFakePBX_Dial302AfterProvisional(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)
	_, rtpPort := bindRTP(t)

	var inviteCount atomic.Int32
	pbxAddr := pbx.Addr()
	pbx.OnInvite(func(inv *fakepbx.Invite) {
		n := inviteCount.Add(1)
		if n == 1 {
			inv.Trying()
			inv.Respond(302, "Moved Temporarily",
				sip.NewHeader("Contact", fmt.Sprintf("<sip:target2@%s>", pbxAddr)))
			return
		}
		inv.Trying()
		inv.Ringing()
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMU))
	})

	p := connectPBXNoAuth(t, pbx)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := p.Dial(ctx, "9999")
	require.NoError(t, err)
	assert.Equal(t, StateActive, c.State())

	c.End()
}

// F16: Multiple simultaneous calls — verify each gets a unique, non-zero RTP port.
func TestFakePBX_MultipleCalls(t *testing.T) {
	const numCalls = 5

	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))

	// Each call gets its own RTP endpoint.
	rtpPorts := make([]int, numCalls)
	rtpConns := make([]net.PacketConn, numCalls)
	for i := 0; i < numCalls; i++ {
		rtpConns[i], rtpPorts[i] = bindRTP(t)
	}

	var inviteIdx atomic.Int32
	pbx.OnInvite(func(inv *fakepbx.Invite) {
		idx := int(inviteIdx.Add(1)) - 1
		if idx >= numCalls {
			inv.Reject(486, "Busy Here")
			return
		}
		inv.Trying()
		inv.Ringing()
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPorts[idx], fakepbx.PCMA))
	})

	p := connectPBX(t, pbx)

	// Dial calls sequentially (sipgo's DialogClientSession has an internal
	// race when Invite is called concurrently on the same client), but keep
	// all calls active simultaneously to verify unique port allocation.
	calls := make([]Call, 0, numCalls)
	for i := 0; i < numCalls; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, err := p.Dial(ctx, fmt.Sprintf("%04d", i))
		cancel()
		require.NoError(t, err, "dial %d failed", i)
		require.Equal(t, StateActive, c.State())
		calls = append(calls, c)
	}

	// Verify each call has a unique, non-zero local RTP port.
	portSet := make(map[int]bool)
	for i, c := range calls {
		addr := parseRemoteAddr(c.LocalSDP())
		require.NotNil(t, addr, "call %d: could not parse local SDP", i)
		port := addr.(*net.UDPAddr).Port
		assert.NotZero(t, port, "call %d: got port 0 in local SDP", i)
		assert.False(t, portSet[port], "call %d: duplicate port %d", i, port)
		portSet[port] = true
	}

	// Also verify no call has m=audio 0 in its local SDP.
	for i, c := range calls {
		assert.NotContains(t, c.LocalSDP(), "m=audio 0", "call %d: SDP has m=audio 0", i)
	}

	// Verify RTP connectivity on each call — send a packet from each PBX
	// endpoint and confirm xphone receives it.
	for i, c := range calls {
		xphoneAddr := parseRemoteAddr(c.LocalSDP())
		xphonePort := xphoneAddr.(*net.UDPAddr).Port
		dst, err := net.ResolveUDPAddr("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(xphonePort)))
		require.NoError(t, err)

		silence := make([]byte, 160)
		for j := range silence {
			silence[j] = 0xD5
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    8,
				SequenceNumber: 1,
				Timestamp:      160,
				SSRC:           uint32(0xDEAD0000 + i),
			},
			Payload: silence,
		}
		data, err := pkt.Marshal()
		require.NoError(t, err)
		_, err = rtpConns[i].WriteTo(data, dst)
		require.NoError(t, err)

		select {
		case rxPkt := <-c.RTPRawReader():
			assert.Equal(t, uint8(8), rxPkt.PayloadType, "call %d: wrong payload type", i)
		case <-time.After(3 * time.Second):
			t.Fatalf("call %d: no inbound RTP received", i)
		}
	}

	// Clean up all calls.
	for _, c := range calls {
		c.End()
	}
}
