package xphone

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/testutil"
)

func TestPhone_OnIncomingFiresOnInvite(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)

	incoming := make(chan Call, 1)
	phone.OnIncoming(func(c Call) { incoming <- c })

	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	select {
	case call := <-incoming:
		assert.Equal(t, StateRinging, call.State())
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnIncoming never fired")
	}
}

func TestPhone_Auto100SentOnInvite(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)
	phone.OnIncoming(func(c Call) {})

	saw100 := tr.WaitForResponse(100, 100*time.Millisecond)
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	assert.True(t, <-saw100, "100 Trying never sent")
}

func TestPhone_Auto180SentAfterOnIncoming(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)
	phone.OnIncoming(func(c Call) {})

	saw180 := tr.WaitForResponse(180, 100*time.Millisecond)
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	assert.True(t, <-saw180, "180 Ringing never sent")
}

func TestPhone_DialReturnsActiveCall(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")
	tr.OnInvite(func() {
		tr.RespondWith(180, "Ringing")
		tr.RespondWith(200, "OK")
	})

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)

	call, err := phone.Dial(context.Background(), "sip:1002@pbx")

	require.NoError(t, err)
	assert.Equal(t, StateActive, call.State())
}

func TestPhone_DialFailsWhenNotRegistered(t *testing.T) {
	phone := newPhone(testConfig())
	// not connected

	_, err := phone.Dial(context.Background(), "sip:1002@pbx")
	assert.ErrorIs(t, err, ErrNotRegistered)
}

func TestPhone_StateIsRegisteredAfterConnect(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)

	assert.Equal(t, PhoneStateRegistered, phone.State())
}

// --- New() constructor with functional options ---

func TestNew_WithCredentials(t *testing.T) {
	p := New(
		WithCredentials("1001", "secret", "pbx.example.com"),
	)
	ph := p.(*phone)
	assert.Equal(t, "1001", ph.cfg.Username)
	assert.Equal(t, "secret", ph.cfg.Password)
	assert.Equal(t, "pbx.example.com", ph.cfg.Host)
}

func TestNew_WithTransport(t *testing.T) {
	tlsCfg := &tls.Config{}
	p := New(
		WithTransport("tls", tlsCfg),
	)
	ph := p.(*phone)
	assert.Equal(t, "tls", ph.cfg.Transport)
	assert.Equal(t, tlsCfg, ph.cfg.TLSConfig)
}

func TestNew_WithRTPPorts(t *testing.T) {
	p := New(WithRTPPorts(10000, 20000))
	ph := p.(*phone)
	assert.Equal(t, 10000, ph.cfg.RTPPortMin)
	assert.Equal(t, 20000, ph.cfg.RTPPortMax)
}

func TestNew_WithCodecs(t *testing.T) {
	p := New(WithCodecs(CodecPCMU, CodecPCMA, CodecG722))
	ph := p.(*phone)
	assert.Equal(t, []Codec{CodecPCMU, CodecPCMA, CodecG722}, ph.cfg.CodecPrefs)
}

func TestNew_WithJitterBuffer(t *testing.T) {
	p := New(WithJitterBuffer(100 * time.Millisecond))
	ph := p.(*phone)
	assert.Equal(t, 100*time.Millisecond, ph.cfg.JitterBuffer)
}

func TestNew_WithMediaTimeout(t *testing.T) {
	p := New(WithMediaTimeout(60 * time.Second))
	ph := p.(*phone)
	assert.Equal(t, 60*time.Second, ph.cfg.MediaTimeout)
}

func TestNew_DefaultState(t *testing.T) {
	p := New()
	assert.Equal(t, PhoneStateDisconnected, p.State())
}

// --- Outbound proxy config ---

func TestWithOutboundProxy(t *testing.T) {
	p := New(
		WithCredentials("1001", "secret", "pbx.example.com"),
		WithOutboundProxy("sip:proxy.example.com:5060"),
	).(*phone)
	// WithOutboundProxy normalises the URI by appending ";lr" for loose routing.
	assert.Equal(t, "sip:proxy.example.com:5060;lr", p.cfg.OutboundProxy)
}

func TestWithOutboundProxy_AlreadyHasLR(t *testing.T) {
	p := New(
		WithCredentials("1001", "secret", "pbx.example.com"),
		WithOutboundProxy("sip:proxy.example.com:5060;lr"),
	).(*phone)
	// Should not double-append ";lr".
	assert.Equal(t, "sip:proxy.example.com:5060;lr", p.cfg.OutboundProxy)
}

func TestWithOutboundProxy_CaseInsensitiveLR(t *testing.T) {
	p := New(
		WithCredentials("1001", "secret", "pbx.example.com"),
		WithOutboundProxy("sip:proxy.example.com:5060;LR"),
	).(*phone)
	// RFC 3261 §19.1.1: URI parameters are case-insensitive.
	assert.Equal(t, "sip:proxy.example.com:5060;LR", p.cfg.OutboundProxy)
}

func TestWithOutboundProxy_LRNotConfusedByPrefix(t *testing.T) {
	p := New(
		WithCredentials("1001", "secret", "pbx.example.com"),
		WithOutboundProxy("sip:proxy.example.com:5060;lrfoo"),
	).(*phone)
	// ";lrfoo" is not ";lr" — should still append ";lr".
	assert.Equal(t, "sip:proxy.example.com:5060;lrfoo;lr", p.cfg.OutboundProxy)
}

func TestWithOutboundCredentials(t *testing.T) {
	p := New(
		WithCredentials("1001", "secret", "pbx.example.com"),
		WithOutboundCredentials("trunk-user", "trunk-pass"),
	).(*phone)
	assert.Equal(t, "trunk-user", p.cfg.OutboundUsername)
	assert.Equal(t, "trunk-pass", p.cfg.OutboundPassword)
}

func TestOutboundCredentials_FallbackToRegistration(t *testing.T) {
	p := New(
		WithCredentials("1001", "secret", "pbx.example.com"),
	).(*phone)
	// When OutboundUsername/Password are empty, dialOnce should use Username/Password.
	assert.Empty(t, p.cfg.OutboundUsername)
	assert.Empty(t, p.cfg.OutboundPassword)
}

// --- Host:Port parsing ---

func TestNormalizeHost_SplitsHostPort(t *testing.T) {
	cfg := Config{Host: "10.0.0.7:5061"}
	normalizeHost(&cfg)
	assert.Equal(t, "10.0.0.7", cfg.Host)
	assert.Equal(t, 5061, cfg.Port)
}

func TestNormalizeHost_HostOnly(t *testing.T) {
	cfg := Config{Host: "10.0.0.7"}
	normalizeHost(&cfg)
	assert.Equal(t, "10.0.0.7", cfg.Host)
	assert.Equal(t, 0, cfg.Port) // unchanged
}

func TestNormalizeHost_HostnameWithPort(t *testing.T) {
	cfg := Config{Host: "sip.example.com:5080"}
	normalizeHost(&cfg)
	assert.Equal(t, "sip.example.com", cfg.Host)
	assert.Equal(t, 5080, cfg.Port)
}

func TestNormalizeHost_IPv6BracketWithPort(t *testing.T) {
	cfg := Config{Host: "[::1]:5060"}
	normalizeHost(&cfg)
	assert.Equal(t, "::1", cfg.Host)
	assert.Equal(t, 5060, cfg.Port)
}

func TestNormalizeHost_BareIPv6NoSplit(t *testing.T) {
	cfg := Config{Host: "::1"}
	normalizeHost(&cfg)
	assert.Equal(t, "::1", cfg.Host)
	assert.Equal(t, 0, cfg.Port)
}

func TestNormalizeHost_ExplicitPortWins(t *testing.T) {
	cfg := Config{Host: "10.0.0.7:5080", Port: 9999}
	normalizeHost(&cfg)
	assert.Equal(t, "10.0.0.7", cfg.Host) // host still stripped
	assert.Equal(t, 9999, cfg.Port)       // explicit port preserved
}

func TestNormalizeHost_InvalidPortIgnored(t *testing.T) {
	cfg := Config{Host: "10.0.0.7:notaport"}
	normalizeHost(&cfg)
	assert.Equal(t, "10.0.0.7:notaport", cfg.Host) // unchanged
	assert.Equal(t, 0, cfg.Port)
}

func TestNormalizeHost_EmptyHost(t *testing.T) {
	cfg := Config{}
	normalizeHost(&cfg)
	assert.Equal(t, "", cfg.Host)
}

func TestWithCredentials_HostPortSplit(t *testing.T) {
	p := New(
		WithCredentials("1002", "password123", "10.0.0.7:5060"),
	).(*phone)
	assert.Equal(t, "10.0.0.7", p.cfg.Host)
	assert.Equal(t, 5060, p.cfg.Port)
}

// --- Connect ---

func TestPhone_ConnectAlreadyConnectedReturnsError(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	// Second connect should fail.
	err := p.Connect(context.Background())
	assert.ErrorIs(t, err, ErrAlreadyConnected)
}

func TestPhone_ConnectWithInvalidProtocol(t *testing.T) {
	p := New(
		WithCredentials("1001", "secret", "pbx.example.com"),
		WithTransport("sctp", nil),
	).(*phone)

	err := p.Connect(context.Background())
	assert.Error(t, err)
}

func TestPhone_ConnectTLSRequiresTLSConfig(t *testing.T) {
	p := New(
		WithCredentials("1001", "secret", "pbx.example.com"),
		WithTransport("tls", nil),
	).(*phone)

	err := p.Connect(context.Background())
	assert.ErrorIs(t, err, ErrTLSConfigRequired)
}

func TestPhone_ConnectWithEmptyConfig(t *testing.T) {
	p := New().(*phone)
	err := p.Connect(context.Background())
	assert.Error(t, err, "Connect with zero-value config should fail")
}

// --- Disconnect ---

func TestPhone_DisconnectTransitionsToDisconnected(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	err := p.Disconnect()
	require.NoError(t, err)
	assert.Equal(t, PhoneStateDisconnected, p.State())
}

func TestPhone_DisconnectStopsRefreshLoop(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	cfg := testConfig()
	cfg.RegisterExpiry = 50 * time.Millisecond

	p := newPhone(cfg)
	p.connectWithTransport(tr)

	p.Disconnect()

	countAfter := tr.CountSent("REGISTER")
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, countAfter, tr.CountSent("REGISTER"),
		"no more REGISTER refreshes should be sent after Disconnect")
}

func TestPhone_DisconnectClosesTransport(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	p.Disconnect()
	assert.True(t, tr.Closed(), "transport must be closed after Disconnect")
}

func TestPhone_DisconnectWhenDisconnectedReturnsError(t *testing.T) {
	p := newPhone(testConfig())
	err := p.Disconnect()
	assert.ErrorIs(t, err, ErrNotConnected)
}

func TestPhone_DisconnectTwiceReturnsError(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	require.NoError(t, p.Disconnect())
	assert.ErrorIs(t, p.Disconnect(), ErrNotConnected)
}

func TestPhone_CannotDialAfterDisconnect(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)
	p.Disconnect()

	_, err := p.Dial(context.Background(), "sip:1002@pbx",
		WithDialTimeout(50*time.Millisecond))
	assert.ErrorIs(t, err, ErrNotRegistered)
}

func TestPhone_DisconnectFiresOnUnregistered(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	fired := make(chan struct{}, 1)
	p.OnUnregistered(func() { fired <- struct{}{} })

	p.Disconnect()

	select {
	case <-fired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnUnregistered never fired after Disconnect")
	}
}

// --- Callback buffering (set before Connect, apply after) ---

func TestPhone_OnRegisteredBeforeConnect(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())

	fired := make(chan struct{}, 1)
	p.OnRegistered(func() { fired <- struct{}{} })

	// Connect after setting callback — should still fire.
	p.connectWithTransport(tr)

	select {
	case <-fired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnRegistered set before Connect never fired")
	}
}

func TestPhone_OnUnregisteredBeforeConnect(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())

	fired := make(chan struct{}, 1)
	p.OnUnregistered(func() { fired <- struct{}{} })

	p.connectWithTransport(tr)

	// Simulate transport drop — OnUnregistered should fire.
	tr.SimulateDrop()

	select {
	case <-fired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnUnregistered set before Connect never fired on drop")
	}
}

func TestPhone_OnErrorBeforeConnect(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.FailNext(99)

	cfg := testConfig()
	cfg.RegisterMaxRetry = 1

	p := newPhone(cfg)

	fired := make(chan error, 1)
	p.OnError(func(err error) { fired <- err })

	p.connectWithTransport(tr)

	select {
	case err := <-fired:
		assert.ErrorIs(t, err, ErrRegistrationFailed)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnError set before Connect never fired")
	}
}

// --- Phone-level call callbacks ---

func TestPhone_OnCallStateFiresOnIncoming(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())

	type stateEvent struct {
		callID string
		state  CallState
	}
	ch := make(chan stateEvent, 8)
	p.OnCallState(func(c Call, s CallState) { ch <- stateEvent{c.ID(), s} })
	p.connectWithTransport(tr)

	incoming := make(chan Call, 1)
	p.OnIncoming(func(c Call) { incoming <- c })
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	var c Call
	select {
	case c = <-incoming:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnIncoming never fired")
	}

	// Accept the call — should trigger OnCallState with StateActive.
	require.NoError(t, c.Accept())

	select {
	case ev := <-ch:
		assert.Equal(t, c.ID(), ev.callID)
		assert.Equal(t, StateActive, ev.state)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnCallState never fired")
	}
}

func TestPhone_OnCallEndedFiresOnHangup(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())

	type endEvent struct {
		callID string
		reason EndReason
	}
	ch := make(chan endEvent, 1)
	p.OnCallEnded(func(c Call, r EndReason) { ch <- endEvent{c.ID(), r} })
	p.connectWithTransport(tr)

	incoming := make(chan Call, 1)
	p.OnIncoming(func(c Call) { incoming <- c })
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	c := <-incoming
	require.NoError(t, c.Accept())
	require.NoError(t, c.End())

	select {
	case ev := <-ch:
		assert.Equal(t, c.ID(), ev.callID)
		assert.Equal(t, EndedByLocal, ev.reason)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnCallEnded never fired")
	}
}

func TestPhone_OnCallStateFiresOnDial(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")
	tr.OnInvite(func() {
		tr.RespondWith(180, "Ringing")
		tr.RespondWith(200, "OK")
	})

	p := newPhone(testConfig())

	type stateEvent struct {
		callID string
		state  CallState
	}
	ch := make(chan stateEvent, 8)
	p.OnCallState(func(c Call, s CallState) { ch <- stateEvent{c.ID(), s} })
	p.connectWithTransport(tr)

	c, err := p.Dial(context.Background(), "sip:1002@pbx")
	require.NoError(t, err)

	// Collect state events — we should see RemoteRinging and Active.
	// Callbacks fire via goroutines so arrival order is not guaranteed.
	var states []CallState
	timeout := time.After(200 * time.Millisecond)
	for len(states) < 2 {
		select {
		case ev := <-ch:
			assert.Equal(t, c.ID(), ev.callID)
			states = append(states, ev.state)
		case <-timeout:
			t.Fatalf("expected 2 state events, got %d: %v", len(states), states)
		}
	}
	assert.Contains(t, states, StateRemoteRinging)
	assert.Contains(t, states, StateActive)
}

func TestPhone_OnCallStateCoexistsWithPerCallOnState(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())

	phoneCh := make(chan CallState, 4)
	p.OnCallState(func(_ Call, s CallState) { phoneCh <- s })
	p.connectWithTransport(tr)

	incoming := make(chan Call, 1)
	p.OnIncoming(func(c Call) { incoming <- c })
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	c := <-incoming

	// User also sets a per-call callback — both should fire.
	perCallCh := make(chan CallState, 4)
	c.OnState(func(s CallState) { perCallCh <- s })

	require.NoError(t, c.Accept())

	select {
	case s := <-phoneCh:
		assert.Equal(t, StateActive, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("phone-level OnCallState never fired")
	}
	select {
	case s := <-perCallCh:
		assert.Equal(t, StateActive, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("per-call OnState never fired")
	}
}

// --- FindCall ---

func TestPhone_FindCallReturnsActiveCall(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	incoming := make(chan Call, 1)
	p.OnIncoming(func(c Call) { incoming <- c })
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	c := <-incoming
	found := p.FindCall(c.CallID())
	require.NotNil(t, found)
	assert.Equal(t, c.ID(), found.ID())
}

func TestPhone_FindCallReturnsNilForUnknown(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	assert.Nil(t, p.FindCall("nonexistent"))
}

// --- DtmfMode ---

func TestPhone_DtmfModePropagatedToCalls(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	cfg := testConfig()
	cfg.DtmfMode = DtmfSipInfo
	p := newPhone(cfg)
	p.connectWithTransport(tr)

	incoming := make(chan Call, 1)
	p.OnIncoming(func(c Call) { incoming <- c })
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	c := <-incoming
	// Access the concrete call to check dtmfMode.
	cc := c.(*call)
	cc.mu.Lock()
	mode := cc.dtmfMode
	cc.mu.Unlock()
	assert.Equal(t, DtmfSipInfo, mode)
}

func TestPhone_InfoDTMFFiresCallDTMFCallback(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	cfg := testConfig()
	cfg.DtmfMode = DtmfSipInfo
	p := newPhone(cfg)
	p.connectWithTransport(tr)

	got := make(chan string, 1)
	incoming := make(chan Call, 1)
	p.OnIncoming(func(c Call) { incoming <- c })
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	c := <-incoming
	c.OnDTMF(func(digit string) { got <- digit })
	c.Accept()

	// Simulate SIP INFO DTMF via the phone handler.
	p.handleDialogInfo(c.CallID(), "5")

	select {
	case digit := <-got:
		assert.Equal(t, "5", digit)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnDTMF callback never fired for INFO DTMF")
	}
}

func TestPhone_InfoDTMFIgnoredInRfc4733Mode(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	cfg := testConfig()
	cfg.DtmfMode = DtmfRfc4733
	p := newPhone(cfg)
	p.connectWithTransport(tr)

	got := make(chan string, 1)
	incoming := make(chan Call, 1)
	p.OnIncoming(func(c Call) { incoming <- c })
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	c := <-incoming
	c.OnDTMF(func(digit string) { got <- digit })
	c.Accept()

	p.handleDialogInfo(c.CallID(), "5")

	select {
	case <-got:
		t.Fatal("INFO DTMF should be ignored in Rfc4733 mode")
	case <-time.After(100 * time.Millisecond):
		// Expected — no callback fired.
	}
}

func TestPhone_InfoDTMFAcceptedInBothMode(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	cfg := testConfig()
	cfg.DtmfMode = DtmfBoth
	p := newPhone(cfg)
	p.connectWithTransport(tr)

	got := make(chan string, 1)
	incoming := make(chan Call, 1)
	p.OnIncoming(func(c Call) { incoming <- c })
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	c := <-incoming
	c.OnDTMF(func(digit string) { got <- digit })
	c.Accept()

	p.handleDialogInfo(c.CallID(), "9")

	select {
	case digit := <-got:
		assert.Equal(t, "9", digit)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnDTMF callback never fired for INFO DTMF in Both mode")
	}
}

func TestWithDtmfMode(t *testing.T) {
	p := New(WithDtmfMode(DtmfSipInfo)).(*phone)
	assert.Equal(t, DtmfSipInfo, p.cfg.DtmfMode)
}

func TestDtmfModeDefaultIsRfc4733(t *testing.T) {
	p := New().(*phone)
	assert.Equal(t, DtmfRfc4733, p.cfg.DtmfMode)
}

// --- Calls() ---

func TestPhone_CallsReturnsEmpty(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	assert.Empty(t, p.Calls())
}

func TestPhone_CallsReturnsSingleCall(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	incoming := make(chan Call, 1)
	p.OnIncoming(func(c Call) { incoming <- c })
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	c := <-incoming
	calls := p.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, c.ID(), calls[0].ID())
}

func TestPhone_ConcurrentIncomingCalls(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	var received []Call
	p.OnIncoming(func(c Call) { received = append(received, c) })

	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")
	tr.SimulateInvite("sip:1003@pbx", "sip:1002@pbx")

	require.Len(t, received, 2)
	calls := p.Calls()
	assert.Len(t, calls, 2)

	// Both calls should have unique Call-IDs.
	assert.NotEqual(t, received[0].CallID(), received[1].CallID())
}

func TestPhone_ByeOneCallLeavesOtherActive(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())

	// Set OnCallEnded before calls are created so it gets wired via registerCall.
	ended := make(chan struct{}, 2)
	p.OnCallEnded(func(_ Call, _ EndReason) { ended <- struct{}{} })

	p.connectWithTransport(tr)

	var received []Call
	p.OnIncoming(func(c Call) { received = append(received, c) })

	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")
	tr.SimulateInvite("sip:1003@pbx", "sip:1002@pbx")

	require.Len(t, received, 2)
	require.NoError(t, received[0].Accept())
	require.NoError(t, received[1].Accept())

	// End call 1.
	require.NoError(t, received[0].End())

	// Wait for call end cleanup to propagate (dispatched via callback goroutine).
	select {
	case <-ended:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnCallEnded never fired")
	}

	// Call 2 should still be active, call 1 removed from tracking.
	calls := p.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, received[1].ID(), calls[0].ID())
	assert.Equal(t, StateActive, received[1].State())
	assert.Equal(t, StateEnded, received[0].State())
}

func TestPhone_DisconnectEndsAllCalls(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	var received []Call
	p.OnIncoming(func(c Call) { received = append(received, c) })

	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")
	tr.SimulateInvite("sip:1003@pbx", "sip:1002@pbx")

	require.Len(t, received, 2)
	require.NoError(t, received[0].Accept())
	require.NoError(t, received[1].Accept())

	require.NoError(t, p.Disconnect())

	assert.Equal(t, StateEnded, received[0].State())
	assert.Equal(t, StateEnded, received[1].State())
}

func TestPhone_CallsUpdatedAfterDial(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")
	tr.OnInvite(func() {
		tr.RespondWith(180, "Ringing")
		tr.RespondWith(200, "OK")
	})

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	c, err := p.Dial(context.Background(), "sip:1002@pbx")
	require.NoError(t, err)

	calls := p.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, c.ID(), calls[0].ID())
}

// --- Attended Transfer ---

// mockDlgWithTags creates a MockDialog with From/To headers containing tags.
func mockDlgWithTags(callID, from, to string) *testutil.MockDialog {
	return testutil.NewMockDialogWithCallIDAndHeaders(callID, map[string][]string{
		"From": {from},
		"To":   {to},
	})
}

func TestPhone_AttendedTransferSendsReferWithReplaces(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")
	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	// Call A (outbound to Bob).
	dlgA := mockDlgWithTags("call-a-id", "<sip:1001@pbx>;tag=alice-a", "<sip:bob@pbx>;tag=bob-a")
	callA := testOutboundCall(t, dlgA)
	callA.simulateResponse(200, "OK")

	// Call B (outbound to Charlie) — Call-ID contains @ to test encoding.
	dlgB := mockDlgWithTags("call-b-id@pbx.local", "<sip:1001@pbx>;tag=alice-b", "<sip:charlie@pbx>;tag=charlie-b")
	callB := testOutboundCall(t, dlgB)
	callB.simulateResponse(200, "OK")
	callB.Hold()

	err := p.AttendedTransfer(callA, callB)
	require.NoError(t, err)

	// Verify REFER sent on Call A's dialog.
	assert.True(t, dlgA.ReferSent())
	target := dlgA.LastReferTarget()

	// Refer-To should point to Charlie's URI with Replaces.
	assert.True(t, strings.HasPrefix(target, "sip:charlie@pbx?Replaces="), "target: %s", target)
	assert.Contains(t, target, "call-b-id%40pbx.local", "Call-ID @ should be encoded")
	assert.Contains(t, target, "to-tag%3Dcharlie-b", "remote tag should be in to-tag")
	assert.Contains(t, target, "from-tag%3Dalice-b", "local tag should be in from-tag")
}

func TestPhone_AttendedTransferEndsBothOnNotify200(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")
	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	dlgA := mockDlgWithTags("call-a", "<sip:1001@pbx>;tag=a1", "<sip:bob@pbx>;tag=b1")
	callA := testOutboundCall(t, dlgA)
	callA.simulateResponse(200, "OK")

	dlgB := mockDlgWithTags("call-b", "<sip:1001@pbx>;tag=a2", "<sip:charlie@pbx>;tag=c2")
	callB := testOutboundCall(t, dlgB)
	callB.simulateResponse(200, "OK")

	endedA := make(chan EndReason, 1)
	endedB := make(chan EndReason, 1)
	callA.OnEnded(func(r EndReason) { endedA <- r })
	callB.OnEnded(func(r EndReason) { endedB <- r })

	require.NoError(t, p.AttendedTransfer(callA, callB))

	// Simulate NOTIFY 200 on Call A's dialog.
	dlgA.SimulateNotify(200)

	select {
	case r := <-endedA:
		assert.Equal(t, EndedByTransfer, r)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("callA OnEnded never fired")
	}
	select {
	case r := <-endedB:
		assert.Equal(t, EndedByTransfer, r)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("callB OnEnded never fired")
	}
	assert.Equal(t, StateEnded, callA.State())
	assert.Equal(t, StateEnded, callB.State())
}

func TestPhone_AttendedTransferRejectsInactiveCallA(t *testing.T) {
	p := newPhone(testConfig())
	callA := testInboundCall(t) // Ringing, not Active
	dlgB := testutil.NewMockDialog()
	callB := testOutboundCall(t, dlgB)
	callB.simulateResponse(200, "OK")

	assert.ErrorIs(t, p.AttendedTransfer(callA, callB), ErrInvalidState)
}

func TestPhone_AttendedTransferRejectsInactiveCallB(t *testing.T) {
	p := newPhone(testConfig())
	callA := testInboundCall(t)
	callA.Accept()
	callB := testInboundCall(t) // Ringing, not Active

	assert.ErrorIs(t, p.AttendedTransfer(callA, callB), ErrInvalidState)
}

func TestPhone_ConnectReturnsErrorOnRegistrationFailure(t *testing.T) {
	cfg := testConfig()
	cfg.RegisterMaxRetry = 1
	cfg.RegisterRetry = 10 * time.Millisecond

	tr := testutil.NewMockTransport()
	tr.FailNext(2) // fail both initial REGISTER and the retry

	p := newPhone(cfg)
	err := p.connectWithTransport(tr)
	require.Error(t, err, "connectWithTransport should return error on registration failure")
	assert.Equal(t, PhoneStateRegistrationFailed, p.State())
}

func TestPhone_ConnectReturnsNilOnSuccess(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	p := newPhone(testConfig())
	err := p.connectWithTransport(tr)
	require.NoError(t, err)
	assert.Equal(t, PhoneStateRegistered, p.State())
}

func TestPhone_AttendedTransferNotifyNon200KeepsCallsAlive(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")
	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	dlgA := mockDlgWithTags("call-a", "<sip:1001@pbx>;tag=a1", "<sip:bob@pbx>;tag=b1")
	callA := testOutboundCall(t, dlgA)
	callA.simulateResponse(200, "OK")

	dlgB := mockDlgWithTags("call-b", "<sip:1001@pbx>;tag=a2", "<sip:charlie@pbx>;tag=c2")
	callB := testOutboundCall(t, dlgB)
	callB.simulateResponse(200, "OK")

	require.NoError(t, p.AttendedTransfer(callA, callB))

	// NOTIFY 100 (Trying) should NOT end the calls.
	dlgA.SimulateNotify(100)
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, StateActive, callA.State())
	assert.Equal(t, StateActive, callB.State())
}
