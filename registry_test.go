package xphone

import (
	"context"
	"errors"
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

func TestRegistry_UnregisterSendsExpiresZero(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // initial REGISTER
	tr.RespondWith(200, "OK") // un-REGISTER

	r := newRegistry(tr, testConfig())
	r.Start(context.Background())

	r.Unregister()

	// Should have sent 2 REGISTERs: initial + un-REGISTER.
	assert.Equal(t, 2, tr.CountSent("REGISTER"))

	last := tr.LastSent("REGISTER")
	require.NotNil(t, last)
	assert.Equal(t, "0", last.Header("Expires"), "un-REGISTER must have Expires: 0")
}

func TestRegistry_UnregisterSkippedWhenNotRegistered(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.FailNext(99)

	cfg := testConfig()
	cfg.RegisterMaxRetry = 1
	r := newRegistry(tr, cfg)
	r.Start(context.Background())

	// Registration failed — Unregister should be a no-op.
	r.Unregister()

	// Only the failed initial REGISTER should have been sent.
	assert.Equal(t, 1, tr.CountSent("REGISTER"))
}

func TestRegistry_UnregisterLogsWarningOnFailure(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")           // initial REGISTER
	tr.RespondWith(500, "Server Error") // un-REGISTER rejected

	r := newRegistry(tr, testConfig())
	r.Start(context.Background())

	// Should not panic or hang — logs warning internally.
	r.Unregister()

	assert.Equal(t, 2, tr.CountSent("REGISTER"))
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

// Regression: #96 — REGISTER failure must surface last_code + last_reason so
// operators can diagnose "registration failed" without tcpdump. The typed
// error also unwraps to ErrRegistrationFailed for backward compatibility.
func TestRegistry_FailureErrorCarriesLastResponseCodeAndReason(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondSequence(
		testutil.Response{Code: 401, Header: "Unauthorized"},
		testutil.Response{Code: 403, Header: "Forbidden"},
	)

	cfg := testConfig()
	cfg.RegisterMaxRetry = 2

	r := newRegistry(tr, cfg)
	errFired := make(chan error, 1)
	r.OnError(func(err error) { errFired <- err })
	r.Start(context.Background())

	select {
	case err := <-errFired:
		assert.ErrorIs(t, err, ErrRegistrationFailed)
		var regErr *RegistrationFailedError
		require.True(t, errors.As(err, &regErr), "error must be *RegistrationFailedError")
		assert.Equal(t, 403, regErr.Code, "Code should be the last response code")
		assert.Equal(t, "Forbidden", regErr.Reason)
		assert.Nil(t, regErr.TransportErr)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnError never fired")
	}
}

// When the last retry fails at the transport layer, Code is 0 and
// TransportErr carries the underlying error.
func TestRegistry_FailureErrorCarriesTransportError(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.FailNext(99)

	cfg := testConfig()
	cfg.RegisterMaxRetry = 2

	r := newRegistry(tr, cfg)
	errFired := make(chan error, 1)
	r.OnError(func(err error) { errFired <- err })
	r.Start(context.Background())

	select {
	case err := <-errFired:
		assert.ErrorIs(t, err, ErrRegistrationFailed)
		var regErr *RegistrationFailedError
		require.True(t, errors.As(err, &regErr))
		assert.Equal(t, 0, regErr.Code)
		assert.Empty(t, regErr.Reason)
		require.NotNil(t, regErr.TransportErr)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnError never fired")
	}
}

func TestRegistrationFailedError_ErrorString(t *testing.T) {
	e1 := &RegistrationFailedError{Code: 403, Reason: "Forbidden"}
	assert.Contains(t, e1.Error(), "403")
	assert.Contains(t, e1.Error(), "Forbidden")
	assert.Contains(t, e1.Error(), "registration failed")

	e2 := &RegistrationFailedError{TransportErr: errors.New("conn refused")}
	assert.Contains(t, e2.Error(), "conn refused")
	assert.Contains(t, e2.Error(), "registration failed")

	// Empty reason shouldn't produce a trailing space.
	e3 := &RegistrationFailedError{Code: 500}
	assert.Equal(t, "xphone: registration failed: last response 500", e3.Error())
}

// errors.Is must match the underlying TransportErr through the multi-error
// Unwrap chain — not just ErrRegistrationFailed. This lets callers
// distinguish transient network failures (net.OpError, context.DeadlineExceeded)
// from permanent rejections.
func TestRegistrationFailedError_ErrorsIsMatchesTransportErr(t *testing.T) {
	netErr := errors.New("dial tcp: connection refused")
	regErr := &RegistrationFailedError{TransportErr: netErr}

	assert.ErrorIs(t, regErr, ErrRegistrationFailed)
	assert.ErrorIs(t, regErr, netErr)
}
