package xphone

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/testutil"
)

// newTestLogger returns a logger that writes JSON to a buffer for inspection.
func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, buf
}

// ==========================================================================
// WithLogger option
// ==========================================================================

func TestWithLogger(t *testing.T) {
	logger, _ := newTestLogger()
	p := New(WithLogger(logger)).(*phone)
	assert.Equal(t, logger, p.cfg.Logger)
}

func TestDefaultLoggerIsNotNil(t *testing.T) {
	p := New().(*phone)
	// Even without WithLogger, resolveLogger should return a non-nil logger.
	assert.NotNil(t, resolveLogger(p.cfg.Logger))
}

// ==========================================================================
// Registry logging
// ==========================================================================

func TestLogger_RegistrationSuccess(t *testing.T) {
	logger, buf := newTestLogger()

	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	cfg := testConfig()
	cfg.Logger = logger
	p := newPhone(cfg)
	p.connectWithTransport(tr)

	assert.Contains(t, buf.String(), "registration successful")
}

func TestLogger_RegistrationFailed(t *testing.T) {
	logger, buf := newTestLogger()

	tr := testutil.NewMockTransport()
	tr.FailNext(99)

	cfg := testConfig()
	cfg.Logger = logger
	cfg.RegisterMaxRetry = 1
	p := newPhone(cfg)
	p.connectWithTransport(tr)

	assert.Contains(t, buf.String(), "registration failed")
}

// ==========================================================================
// Phone logging
// ==========================================================================

func TestLogger_ConnectDisconnect(t *testing.T) {
	logger, buf := newTestLogger()

	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	cfg := testConfig()
	cfg.Logger = logger
	p := newPhone(cfg)
	p.connectWithTransport(tr)
	p.Disconnect()

	output := buf.String()
	assert.Contains(t, output, "phone connected")
	assert.Contains(t, output, "phone disconnected")
}

func TestLogger_IncomingCall(t *testing.T) {
	logger, buf := newTestLogger()

	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	cfg := testConfig()
	cfg.Logger = logger
	p := newPhone(cfg)
	p.connectWithTransport(tr)

	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	assert.Contains(t, buf.String(), "incoming call")
}

func TestLogger_Dial(t *testing.T) {
	logger, buf := newTestLogger()

	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	cfg := testConfig()
	cfg.Logger = logger
	p := newPhone(cfg)
	p.connectWithTransport(tr)

	tr.OnInvite(func() {
		tr.RespondWith(200, "OK")
	})
	p.Dial(context.Background(), "sip:1002@pbx",
		WithDialTimeout(100*time.Millisecond))

	assert.Contains(t, buf.String(), "dialing")
}

// ==========================================================================
// Call state logging
// ==========================================================================

func TestLogger_CallAccept(t *testing.T) {
	logger, buf := newTestLogger()

	cfg := testConfig()
	cfg.Logger = logger
	c := newInboundCallWithLogger(cfg.Logger)
	c.Accept()

	assert.Contains(t, buf.String(), "call accepted")
}

func TestLogger_CallReject(t *testing.T) {
	logger, buf := newTestLogger()

	cfg := testConfig()
	cfg.Logger = logger
	c := newInboundCallWithLogger(cfg.Logger)
	c.Reject(486, "Busy Here")

	assert.Contains(t, buf.String(), "call rejected")
}

func TestLogger_CallEnd(t *testing.T) {
	logger, buf := newTestLogger()

	cfg := testConfig()
	cfg.Logger = logger
	c := newInboundCallWithLogger(cfg.Logger)
	c.Accept()
	c.End()

	assert.Contains(t, buf.String(), "call ended")
}

func TestLogger_CallHoldResume(t *testing.T) {
	logger, buf := newTestLogger()

	c := newInboundCallWithLogger(logger)
	c.Accept()
	c.Hold()
	c.Resume()

	output := buf.String()
	assert.Contains(t, output, "call hold")
	assert.Contains(t, output, "call resumed")
}

func TestLogger_MediaTimeout(t *testing.T) {
	logger, buf := newTestLogger()

	c := newInboundCallWithLogger(logger)
	c.mediaTimeout = 30 * time.Millisecond

	done := make(chan struct{})
	c.OnEnded(func(EndReason) {
		close(done)
	})

	c.Accept()
	c.startMedia()

	// Wait for media timeout to fire via OnEnded callback.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for media timeout")
	}

	assert.Contains(t, buf.String(), "media timeout")
}

// ==========================================================================
// Helpers
// ==========================================================================

func newInboundCallWithLogger(logger *slog.Logger) *call {
	dlg := testutil.NewMockDialog()
	c := newInboundCall(dlg)
	c.logger = resolveLogger(logger)
	return c
}

func TestResolveLogger_NilReturnsDefault(t *testing.T) {
	l := resolveLogger(nil)
	require.NotNil(t, l)
}

func TestResolveLogger_NonNilPassthrough(t *testing.T) {
	logger, _ := newTestLogger()
	l := resolveLogger(logger)
	assert.Equal(t, logger, l)
}
