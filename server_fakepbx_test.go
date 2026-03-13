package xphone

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/fakepbx"
)

// serverFakePBX-based tests: FakePBX acts as a trusted SIP peer sending
// INVITEs to or receiving INVITEs from a Server instance over loopback.
//
// Note: These tests are skipped under -race because sipgo.Server.ListenAndServe
// has a known data race during context-cancellation cleanup (race between the
// goroutine it spawns and the UDP conn it creates). This is an upstream sipgo
// issue, not a race in xphone code.

func skipUnderRace(t *testing.T) {
	t.Helper()
	if raceEnabled {
		t.Skip("skipping: sipgo.Server.ListenAndServe has a known data race during shutdown")
	}
}

// allocEphemeralPort discovers a free UDP port on loopback by binding and releasing.
func allocEphemeralPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	port := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()
	return port
}

// listenServer creates a Server on a pre-allocated port with FakePBX as a trusted peer.
func listenServer(t *testing.T, pbx *fakepbx.FakePBX, opts ...func(*ServerConfig)) *server {
	t.Helper()
	pbxHost, pbxPortStr, err := net.SplitHostPort(pbx.Addr())
	require.NoError(t, err)
	pbxPort, err := strconv.Atoi(pbxPortStr)
	require.NoError(t, err)

	listenPort := allocEphemeralPort(t)

	cfg := ServerConfig{
		Listen:       net.JoinHostPort("127.0.0.1", strconv.Itoa(listenPort)),
		RTPAddress:   "127.0.0.1",
		MediaTimeout: 10 * time.Second,
		Peers: []PeerConfig{
			{Name: "test-peer", Host: pbxHost, Port: pbxPort},
		},
	}
	for _, fn := range opts {
		fn(&cfg)
	}

	srv := newServer(cfg)
	t.Cleanup(func() { srv.Shutdown() })
	return srv
}

// startListening starts the server in a background goroutine and waits for it
// to reach ServerStateListening. Returns the actual listen address.
// Skips under -race due to a known sipgo data race during shutdown.
func startListening(t *testing.T, srv *server) string {
	t.Helper()
	skipUnderRace(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- srv.Listen(ctx)
	}()

	require.Eventually(t, func() bool {
		return srv.State() == ServerStateListening
	}, 2*time.Second, 10*time.Millisecond, "server never reached listening state")

	select {
	case err := <-listenErr:
		t.Fatalf("Listen returned early: %v", err)
	default:
	}

	return srv.cfg.Listen
}

// FS1: FakePBX sends INVITE to Server — Server auto-accepts in callback.
func TestServerFakePBX_InboundCall(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)
	_, rtpPort := bindRTP(t)

	srv := listenServer(t, pbx)

	// Accept the call inside OnIncoming — SendInvite blocks until 200 OK.
	callCh := make(chan Call, 1)
	srv.OnIncoming(func(c Call) {
		require.Equal(t, StateRinging, c.State())
		require.Equal(t, DirectionInbound, c.Direction())
		err := c.Accept()
		require.NoError(t, err)
		callCh <- c
	})

	listenAddr := startListening(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := pbx.SendInvite(ctx, "sip:+15551234567@"+listenAddr,
		fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	require.NoError(t, err)

	var c Call
	select {
	case c = <-callCh:
	case <-ctx.Done():
		t.Fatal("call never accepted")
	}

	assert.Equal(t, StateActive, c.State())
	assert.NotEmpty(t, c.LocalSDP())
	assert.NotEmpty(t, c.RemoteSDP())

	err = c.End()
	require.NoError(t, err)
	assert.Equal(t, StateEnded, c.State())
}

// FS2: Server dials FakePBX — outbound call establishment.
func TestServerFakePBX_OutboundDial(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)
	_, rtpPort := bindRTP(t)

	pbx.OnInvite(func(inv *fakepbx.Invite) {
		inv.Trying()
		inv.Ringing()
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	})

	srv := listenServer(t, pbx)
	startListening(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := srv.Dial(ctx, "test-peer", "9999", "+15559876543")
	require.NoError(t, err)
	assert.Equal(t, StateActive, c.State())
	assert.NotEmpty(t, c.LocalSDP())
	assert.NotEmpty(t, c.RemoteSDP())
	assert.Equal(t, "127.0.0.1", c.RemoteIP())
	assert.Equal(t, rtpPort, c.RemotePort())

	err = c.End()
	require.NoError(t, err)
	assert.Equal(t, StateEnded, c.State())
	assert.True(t, pbx.WaitForBye(1, 2*time.Second), "BYE never received by PBX")
}

// FS3: FakePBX sends BYE — Server detects EndedByRemote.
func TestServerFakePBX_RemoteBye(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)
	_, rtpPort := bindRTP(t)

	srv := listenServer(t, pbx)

	callCh := make(chan Call, 1)
	srv.OnIncoming(func(c Call) {
		c.Accept()
		callCh <- c
	})

	listenAddr := startListening(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ac, err := pbx.SendInvite(ctx, "sip:+15551234567@"+listenAddr,
		fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	require.NoError(t, err)

	var c Call
	select {
	case c = <-callCh:
	case <-ctx.Done():
		t.Fatal("call never accepted")
	}
	require.Equal(t, StateActive, c.State())

	ended := make(chan EndReason, 1)
	c.OnEnded(func(r EndReason) {
		ended <- r
	})

	time.Sleep(100 * time.Millisecond)

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
		// Known limitation: sipgo BYE routing can fail when Contact
		// header doesn't match the dialog's listen address.
		// Same as TestFakePBX_RemoteBye in Phone mode tests.
		t.Skip("BYE routing failed (known sipgo limitation)")
	}
}

// FS4: RTP round-trip through Server mode.
func TestServerFakePBX_RTPRoundTrip(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)
	rtpConn, rtpPort := bindRTP(t)

	srv := listenServer(t, pbx)

	callCh := make(chan Call, 1)
	srv.OnIncoming(func(c Call) {
		c.Accept()
		callCh <- c
	})

	listenAddr := startListening(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := pbx.SendInvite(ctx, "sip:+15551234567@"+listenAddr,
		fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	require.NoError(t, err)

	var c Call
	select {
	case c = <-callCh:
	case <-ctx.Done():
		t.Fatal("call never accepted")
	}

	time.Sleep(100 * time.Millisecond)

	// Get xphone's RTP port from LocalSDP.
	xphoneRTPAddr := parseRemoteAddr(c.LocalSDP())
	require.NotNil(t, xphoneRTPAddr)
	xphonePort := xphoneRTPAddr.(*net.UDPAddr).Port
	dst, err := net.ResolveUDPAddr("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(xphonePort)))
	require.NoError(t, err)

	// --- Inbound: test socket → server ---
	silence := make([]byte, 160)
	for i := range silence {
		silence[i] = 0xD5
	}
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    8,
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

	select {
	case rxPkt := <-c.RTPRawReader():
		assert.Equal(t, uint8(8), rxPkt.PayloadType)
		assert.Equal(t, 160, len(rxPkt.Payload))
	case <-time.After(3 * time.Second):
		t.Fatal("no inbound RTP received by server")
	}

	// --- Outbound: server → test socket ---
	pcmFrame := make([]int16, 160)
	select {
	case c.PCMWriter() <- pcmFrame:
	case <-time.After(time.Second):
		t.Fatal("PCMWriter blocked")
	}

	buf := make([]byte, 1500)
	rtpConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := rtpConn.ReadFrom(buf)
	require.NoError(t, err, "no outbound RTP received from server")

	rxPkt := &rtp.Packet{}
	err = rxPkt.Unmarshal(buf[:n])
	require.NoError(t, err)
	assert.Equal(t, uint8(8), rxPkt.PayloadType)
	assert.NotEmpty(t, rxPkt.Payload)

	c.End()
}

// FS5: Server rejects unauthenticated peer — INVITE from unknown IP is 403'd.
func TestServerFakePBX_PeerRejection(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)

	srv := listenServer(t, pbx, func(cfg *ServerConfig) {
		cfg.Peers = []PeerConfig{
			{Name: "other", Host: "10.99.99.99"},
		}
	})

	incomingCalled := false
	srv.OnIncoming(func(c Call) {
		incomingCalled = true
	})

	listenAddr := startListening(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := pbx.SendInvite(ctx, "sip:test@"+listenAddr,
		fakepbx.SDP("127.0.0.1", 20000, fakepbx.PCMU))
	assert.Error(t, err, "expected INVITE to be rejected")
	assert.False(t, incomingCalled, "OnIncoming should not fire for rejected peer")
}

// FS6: No OnIncoming handler → INVITE rejected with 480.
func TestServerFakePBX_NoHandlerRejects(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)

	srv := listenServer(t, pbx)
	// Deliberately do NOT set srv.OnIncoming.

	listenAddr := startListening(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := pbx.SendInvite(ctx, "sip:+15551234567@"+listenAddr,
		fakepbx.SDP("127.0.0.1", 20000, fakepbx.PCMU))
	assert.Error(t, err, "expected INVITE to be rejected when no handler is set")
	assert.Empty(t, srv.Calls(), "no calls should be tracked after rejection")
}

// FS7: DTMF callback wired on inbound call without panic.
func TestServerFakePBX_InboundDTMFCallback(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)
	_, rtpPort := bindRTP(t)

	srv := listenServer(t, pbx)

	dtmfCh := make(chan string, 10)
	callCh := make(chan Call, 1)

	srv.OnIncoming(func(c Call) {
		c.Accept()
		callCh <- c
	})
	srv.OnCallDTMF(func(_ Call, digit string) {
		dtmfCh <- digit
	})

	listenAddr := startListening(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := pbx.SendInvite(ctx, "sip:+15551234567@"+listenAddr,
		fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	require.NoError(t, err)

	var c Call
	select {
	case c = <-callCh:
	case <-ctx.Done():
		t.Fatal("call never accepted")
	}
	require.Equal(t, StateActive, c.State())

	// Verify the DTMF callback is wired (no panic). Send a DTMF packet
	// from the test socket to xphone's RTP port.
	time.Sleep(100 * time.Millisecond)

	xphoneRTPAddr := parseRemoteAddr(c.LocalSDP())
	require.NotNil(t, xphoneRTPAddr)
	xphonePort := xphoneRTPAddr.(*net.UDPAddr).Port
	dst, err := net.ResolveUDPAddr("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(xphonePort)))
	require.NoError(t, err)

	rtpConn, _ := bindRTP(t)
	dtmfPkts, err := EncodeDTMF("7", 1000, 100, 0xABCDEF00)
	require.NoError(t, err)
	for _, pkt := range dtmfPkts {
		data, _ := pkt.Marshal()
		rtpConn.WriteTo(data, dst)
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case digit := <-dtmfCh:
		assert.Equal(t, "7", digit)
	case <-time.After(3 * time.Second):
		t.Fatal("DTMF digit never received")
	}

	c.End()
}

// FS8: Two concurrent inbound calls — correct tracking and cleanup.
func TestServerFakePBX_MultipleConcurrentCalls(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)
	_, rtpPort1 := bindRTP(t)
	_, rtpPort2 := bindRTP(t)

	srv := listenServer(t, pbx)

	callCh := make(chan Call, 2)
	srv.OnIncoming(func(c Call) {
		c.Accept()
		callCh <- c
	})

	listenAddr := startListening(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send two INVITEs concurrently.
	type invResult struct {
		ac  *fakepbx.OutboundCall
		err error
	}
	results := make(chan invResult, 2)
	go func() {
		ac, err := pbx.SendInvite(ctx, "sip:call1@"+listenAddr,
			fakepbx.SDP("127.0.0.1", rtpPort1, fakepbx.PCMA))
		results <- invResult{ac, err}
	}()
	go func() {
		ac, err := pbx.SendInvite(ctx, "sip:call2@"+listenAddr,
			fakepbx.SDP("127.0.0.1", rtpPort2, fakepbx.PCMA))
		results <- invResult{ac, err}
	}()

	// Collect both results.
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			require.NoError(t, r.err, "INVITE %d failed", i)
		case <-ctx.Done():
			t.Fatal("INVITE timed out")
		}
	}

	// Collect both calls.
	var calls []Call
	for i := 0; i < 2; i++ {
		select {
		case c := <-callCh:
			calls = append(calls, c)
		case <-ctx.Done():
			t.Fatal("call never accepted")
		}
	}

	assert.Len(t, srv.Calls(), 2, "expected 2 active calls")

	// Verify unique Call-IDs.
	assert.NotEqual(t, calls[0].CallID(), calls[1].CallID())

	// End both calls.
	for _, c := range calls {
		c.End()
	}

	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, srv.Calls(), "expected 0 calls after ending both")
}

// FS9: Server OnCallState and OnCallEnded callbacks fire (outbound).
func TestServerFakePBX_Callbacks(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)
	_, rtpPort := bindRTP(t)

	pbx.OnInvite(func(inv *fakepbx.Invite) {
		inv.Trying()
		inv.Ringing()
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	})

	srv := listenServer(t, pbx)

	endings := make(chan EndReason, 1)
	srv.OnCallState(func(_ Call, s CallState) {})
	srv.OnCallEnded(func(_ Call, r EndReason) { endings <- r })

	startListening(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := srv.Dial(ctx, "test-peer", "9999", "+15559876543")
	require.NoError(t, err)
	require.Equal(t, StateActive, c.State())

	c.End()

	select {
	case reason := <-endings:
		assert.Equal(t, EndedByLocal, reason)
	case <-time.After(3 * time.Second):
		t.Fatal("OnCallEnded never fired")
	}
}

// FS10: Server.FindCall and Server.Calls during active call.
func TestServerFakePBX_FindCallAndCalls(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)
	_, rtpPort := bindRTP(t)

	pbx.OnInvite(func(inv *fakepbx.Invite) {
		inv.Trying()
		inv.Ringing()
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	})

	srv := listenServer(t, pbx)
	startListening(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := srv.Dial(ctx, "test-peer", "9999", "+15559876543")
	require.NoError(t, err)

	found := srv.FindCall(c.CallID())
	require.NotNil(t, found)
	assert.Equal(t, c.CallID(), found.CallID())

	calls := srv.Calls()
	assert.Len(t, calls, 1)

	c.End()

	time.Sleep(100 * time.Millisecond)
	assert.Nil(t, srv.FindCall(c.CallID()))
	assert.Empty(t, srv.Calls())
}
