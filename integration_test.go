//go:build integration

package xphone

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests require a running Asterisk instance.
// Start with: cd testutil/docker && docker compose up -d
// Run with:   go test -tags=integration -v -count=1 ./...
// Stop with:  cd testutil/docker && docker compose down

func asteriskHost() string {
	if h := os.Getenv("ASTERISK_HOST"); h != "" {
		return h
	}
	return "127.0.0.1"
}

func integrationConfig(ext, password string) Config {
	return Config{
		Username:         ext,
		Password:         password,
		Host:             asteriskHost(),
		Port:             5060,
		Transport:        "udp",
		RegisterExpiry:   60 * time.Second,
		RegisterRetry:    time.Second,
		RegisterMaxRetry: 3,
		MediaTimeout:     10 * time.Second,
		RTPPortMin:       20000,
		RTPPortMax:       20099,
	}
}

func connectPhone(t *testing.T, ext, password string) Phone {
	t.Helper()
	p := newPhone(integrationConfig(ext, password))

	registered := make(chan struct{}, 1)
	p.OnRegistered(func() {
		select {
		case registered <- struct{}{}:
		default:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := p.Connect(ctx)
	require.NoError(t, err, "Connect failed for extension %s", ext)

	select {
	case <-registered:
	case <-ctx.Done():
		t.Fatalf("registration timeout for extension %s", ext)
	}

	t.Cleanup(func() { p.Disconnect() })
	return p
}

// E1: Register extension 1001 with Asterisk.
func TestIntegration_Register(t *testing.T) {
	p := connectPhone(t, "1001", "test")
	assert.Equal(t, PhoneStateRegistered, p.State())
}

// E2: Dial 1001 → 1002 (two xphone instances).
func TestIntegration_DialBetweenExtensions(t *testing.T) {
	p1 := connectPhone(t, "1001", "test")
	p2 := connectPhone(t, "1002", "test")

	// p2 accepts incoming calls.
	incoming := make(chan Call, 1)
	p2.OnIncoming(func(c Call) {
		incoming <- c
	})

	// p1 dials p2.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	callDone := make(chan Call, 1)
	go func() {
		c, err := p1.Dial(ctx, "1002")
		if err == nil {
			callDone <- c
		}
	}()

	// Wait for p2 to receive the incoming call and accept it.
	var inCall Call
	select {
	case inCall = <-incoming:
		require.Equal(t, StateRinging, inCall.State())
	case <-ctx.Done():
		t.Fatal("incoming call never received by 1002")
	}

	err := inCall.Accept()
	require.NoError(t, err)

	// Wait for p1's dial to complete.
	var outCall Call
	select {
	case outCall = <-callDone:
		assert.Equal(t, StateActive, outCall.State())
	case <-ctx.Done():
		t.Fatal("outbound call never completed")
	}

	assert.Equal(t, StateActive, inCall.State())

	// End the call from p1.
	err = outCall.End()
	require.NoError(t, err)
	assert.Equal(t, StateEnded, outCall.State())
}

// E3: Inbound call accept + BYE from remote.
func TestIntegration_InboundAcceptAndRemoteBye(t *testing.T) {
	p1 := connectPhone(t, "1001", "test")
	p2 := connectPhone(t, "1002", "test")

	incoming := make(chan Call, 1)
	p2.OnIncoming(func(c Call) {
		incoming <- c
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// p1 dials p2.
	callDone := make(chan Call, 1)
	go func() {
		c, err := p1.Dial(ctx, "1002")
		if err == nil {
			callDone <- c
		}
	}()

	// p2 accepts.
	var inCall Call
	select {
	case inCall = <-incoming:
	case <-ctx.Done():
		t.Fatal("incoming call never received")
	}

	err := inCall.Accept()
	require.NoError(t, err)

	// Wait for p1's outbound call.
	var outCall Call
	select {
	case outCall = <-callDone:
	case <-ctx.Done():
		t.Fatal("outbound call never completed")
	}

	// p1 sends BYE — p2 should see the call end.
	ended := make(chan EndReason, 1)
	inCall.OnEnded(func(r EndReason) {
		ended <- r
	})

	err = outCall.End()
	require.NoError(t, err)

	select {
	case reason := <-ended:
		assert.Equal(t, EndedByRemote, reason)
	case <-time.After(5 * time.Second):
		t.Fatal("inbound call never received BYE")
	}
}

// establishCall connects two phones and dials from p1 to p2, returning
// the outbound and inbound calls after accept. Both calls are Active.
func establishCall(t *testing.T, p1, p2 Phone) (outCall, inCall Call) {
	t.Helper()

	incoming := make(chan Call, 1)
	p2.OnIncoming(func(c Call) { incoming <- c })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	callDone := make(chan Call, 1)
	go func() {
		c, err := p1.Dial(ctx, "1002")
		if err == nil {
			callDone <- c
		}
	}()

	select {
	case inCall = <-incoming:
	case <-ctx.Done():
		t.Fatal("incoming call never received")
	}

	err := inCall.Accept()
	require.NoError(t, err)

	select {
	case outCall = <-callDone:
	case <-ctx.Done():
		t.Fatal("outbound call never completed")
	}

	require.Equal(t, StateActive, outCall.State())
	require.Equal(t, StateActive, inCall.State())
	return outCall, inCall
}

// E4: Hold/resume via re-INVITE.
func TestIntegration_HoldResume(t *testing.T) {
	p1 := connectPhone(t, "1001", "test")
	p2 := connectPhone(t, "1002", "test")
	outCall, _ := establishCall(t, p1, p2)

	// Let media paths stabilize.
	time.Sleep(500 * time.Millisecond)

	// p1 puts the call on hold.
	err := outCall.Hold()
	require.NoError(t, err)
	assert.Equal(t, StateOnHold, outCall.State())

	time.Sleep(500 * time.Millisecond)

	// p1 resumes the call.
	err = outCall.Resume()
	require.NoError(t, err)
	assert.Equal(t, StateActive, outCall.State())

	outCall.End()
}

// E5: DTMF send/receive.
// p1 dials p2, p2 sends DTMF digits, p1 receives them via OnDTMF.
func TestIntegration_DTMF(t *testing.T) {
	p1 := connectPhone(t, "1001", "test")
	p2 := connectPhone(t, "1002", "test")
	outCall, inCall := establishCall(t, p1, p2)

	// Register DTMF callback on p1's outbound call.
	dtmfReceived := make(chan string, 10)
	outCall.OnDTMF(func(digit string) {
		dtmfReceived <- digit
	})

	// Brief pause to let RTP media paths establish through Asterisk.
	time.Sleep(500 * time.Millisecond)

	// p2 sends DTMF digit "5".
	err := inCall.SendDTMF("5")
	require.NoError(t, err)

	// Wait for p1 to receive the DTMF.
	select {
	case digit := <-dtmfReceived:
		assert.Equal(t, "5", digit)
	case <-time.After(5 * time.Second):
		t.Fatal("DTMF digit never received by p1")
	}

	outCall.End()
}

// E6: Echo test — dial 9999, send audio, verify we receive RTP back.
func TestIntegration_EchoTest(t *testing.T) {
	p := connectPhone(t, "1001", "test")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := p.Dial(ctx, "9999")
	require.NoError(t, err)
	require.Equal(t, StateActive, c.State())

	// Send silence via RTPWriter so Asterisk Echo() has something to reflect.
	silence := make([]byte, 160)
	for i := range silence {
		silence[i] = 0xFF // PCMU silence
	}
	for i := 0; i < 50; i++ {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    0,
				SequenceNumber: uint16(i),
				Timestamp:      uint32(i) * 160,
				SSRC:           0xDEADBEEF,
			},
			Payload: silence,
		}
		c.RTPWriter() <- pkt
		time.Sleep(20 * time.Millisecond)
	}

	// Verify we receive echoed RTP back.
	select {
	case pkt := <-c.RTPReader():
		assert.NotNil(t, pkt)
		assert.NotEmpty(t, pkt.Payload)
	case <-time.After(5 * time.Second):
		t.Fatal("no echo response received")
	}

	c.End()
}
