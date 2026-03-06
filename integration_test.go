//go:build integration

package xphone

import (
	"context"
	"os"
	"testing"
	"time"

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

// E4: Hold/resume via re-INVITE.
// Requires working RTP port allocation (not yet wired).
func TestIntegration_HoldResume(t *testing.T) {
	t.Skip("requires RTP port allocation (Phase 5+)")
}

// E5: DTMF send/receive.
// Requires working media pipeline (not yet wired).
func TestIntegration_DTMF(t *testing.T) {
	t.Skip("requires media pipeline (Phase 5+)")
}

// E6: Echo test — dial 9999, verify media.
// Requires working media pipeline (not yet wired).
func TestIntegration_EchoTest(t *testing.T) {
	t.Skip("requires media pipeline (Phase 5+)")
}
