package xphone

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/testutil"
)

func testConfig() Config {
	return Config{
		Username:         "1001",
		Password:         "secret",
		Host:             "127.0.0.1",
		Port:             5060,
		Transport:        "udp",
		RegisterExpiry:   60 * time.Second,
		RegisterRetry:    10 * time.Millisecond,
		RegisterMaxRetry: 5,
		MediaTimeout:     30 * time.Second,
	}
}

func TestRegistry_SendsInitialRegister(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	r := newRegistry(tr, testConfig())
	err := r.Start(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, tr.CountSent("REGISTER"))
}

func TestRegistry_SucceedsWhenTransportHandles401(t *testing.T) {
	// Auth challenges are handled by the transport layer (sipUA.DoDigestAuth).
	// The registry only sees the final response code (200 OK).
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	r := newRegistry(tr, testConfig())
	err := r.Start(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, tr.CountSent("REGISTER"))
	assert.Equal(t, PhoneStateRegistered, r.State())
}

func TestRegistry_FiresOnRegistered(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	r := newRegistry(tr, testConfig())
	fired := make(chan struct{}, 1)
	r.OnRegistered(func() { fired <- struct{}{} })
	r.Start(context.Background())

	select {
	case <-fired:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnRegistered never fired")
	}
}

func TestRegistry_RefreshesBeforeExpiry(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	cfg := testConfig()
	cfg.RegisterExpiry = 50 * time.Millisecond // refresh at 25ms

	r := newRegistry(tr, cfg)
	r.Start(context.Background())
	time.Sleep(80 * time.Millisecond)

	assert.GreaterOrEqual(t, tr.CountSent("REGISTER"), 2)
}

func TestRegistry_RetriesOnTransportDrop(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.FailNext(3)
	tr.RespondWith(200, "OK")

	r := newRegistry(tr, testConfig())
	fired := make(chan struct{}, 1)
	r.OnRegistered(func() { fired <- struct{}{} })
	r.Start(context.Background())

	select {
	case <-fired:
		// RegisterMaxRetry=5, 3 failures + 1 success = 4 attempts total
		assert.Equal(t, 4, tr.CountSent("REGISTER"))
	case <-time.After(500 * time.Millisecond):
		t.Fatal("never registered after retries")
	}
}

func TestRegistry_FiresOnUnregisteredAndTransitionsToRegistering(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	r := newRegistry(tr, testConfig())
	r.Start(context.Background())

	// Wait for initial registration
	registered := make(chan struct{}, 1)
	r.OnRegistered(func() { registered <- struct{}{} })
	select {
	case <-registered:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("initial registration never fired")
	}

	unregistered := make(chan struct{}, 1)
	r.OnUnregistered(func() { unregistered <- struct{}{} })

	tr.SimulateDrop()

	select {
	case <-unregistered:
		assert.Equal(t, PhoneStateRegistering, r.State())
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnUnregistered never fired after transport drop")
	}
}

func TestRegistry_ResumesRegisterAttemptsAfterDrop(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // initial

	r := newRegistry(tr, testConfig())
	r.Start(context.Background())

	// Simulate drop then recover
	tr.SimulateDrop()
	tr.RespondWith(200, "OK") // recovery

	reregistered := make(chan struct{}, 1)
	r.OnRegistered(func() { reregistered <- struct{}{} })

	select {
	case <-reregistered:
		assert.GreaterOrEqual(t, tr.CountSent("REGISTER"), 2)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("did not re-register after drop")
	}
}

func TestRegistry_StopsAfterMaxRetryExceeded(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.FailNext(99)

	cfg := testConfig()
	cfg.RegisterMaxRetry = 3 // total 3 attempts, then fail

	r := newRegistry(tr, cfg)
	errFired := make(chan error, 1)
	r.OnError(func(err error) { errFired <- err })
	r.Start(context.Background())

	select {
	case err := <-errFired:
		assert.ErrorIs(t, err, ErrRegistrationFailed)
		assert.Equal(t, 3, tr.CountSent("REGISTER"))
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnError never fired")
	}
}

func TestRegistry_StopsCleanlyOnDisconnect(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	r := newRegistry(tr, testConfig())
	r.Start(context.Background())
	time.Sleep(20 * time.Millisecond)

	err := r.Stop()
	require.NoError(t, err)

	countAfterStop := tr.CountSent("REGISTER")
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, countAfterStop, tr.CountSent("REGISTER"))
}

func TestRegistry_NATKeepalivesSentOnInterval(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	cfg := testConfig()
	cfg.NATKeepaliveInterval = 20 * time.Millisecond

	r := newRegistry(tr, cfg)
	r.Start(context.Background())
	time.Sleep(70 * time.Millisecond)

	assert.GreaterOrEqual(t, tr.CountSentKeepalives(), 2)
}
