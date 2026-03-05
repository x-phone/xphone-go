package xphone

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/testutil"
)

// ==========================================================================
// New() constructor with functional options
// ==========================================================================

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

// ==========================================================================
// Connect
// ==========================================================================

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

// ==========================================================================
// Disconnect
// ==========================================================================

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

// ==========================================================================
// Callback buffering (set before Connect, apply after)
// ==========================================================================

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
