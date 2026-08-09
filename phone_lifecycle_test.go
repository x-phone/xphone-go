package xphone

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/fakepbx"
)

// Lifecycle split tests (#101): Prepare / Register / Unregister with a real
// transport against fakepbx, plus the Dial/SendMessage registrarless gating.

func preparedPhone(t *testing.T, cfg Config) *phone {
	t.Helper()
	applyDefaults(&cfg)
	p := newPhone(cfg)
	require.NoError(t, p.Prepare(context.Background()))
	t.Cleanup(func() { p.Disconnect() })
	return p
}

func TestPrepare_TransitionsToPrepared(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	p := preparedPhone(t, pbxConfig(t, pbx))

	assert.Equal(t, PhoneStatePrepared, p.State())
	// Transport is bound and dialFn wired; no REGISTER has been sent.
	p.mu.Lock()
	assert.NotNil(t, p.tr, "transport must be attached")
	assert.NotNil(t, p.reg, "registry must be created (not started)")
	p.mu.Unlock()
	assert.Equal(t, 0, pbx.RegisterCount(), "Prepare must not REGISTER")
}

func TestPrepare_Idempotent(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	p := preparedPhone(t, pbxConfig(t, pbx))

	p.mu.Lock()
	tr1 := p.tr
	p.mu.Unlock()

	require.NoError(t, p.Prepare(context.Background()))
	p.mu.Lock()
	tr2 := p.tr
	p.mu.Unlock()
	assert.Equal(t, PhoneStatePrepared, p.State())
	assert.Same(t, tr1.(*sipUA), tr2.(*sipUA), "second Prepare must not rebuild the transport")
}

func TestRegister_FromPrepared(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	p := preparedPhone(t, pbxConfig(t, pbx))

	registered := make(chan struct{}, 1)
	p.OnRegistered(func() {
		select {
		case registered <- struct{}{}:
		default:
		}
	})

	require.NoError(t, p.Register(context.Background()))
	assert.Equal(t, PhoneStateRegistered, p.State())
	assert.GreaterOrEqual(t, pbx.RegisterCount(), 1)
	select {
	case <-registered:
	case <-time.After(2 * time.Second):
		t.Fatal("OnRegistered callback never fired")
	}
}

func TestRegister_AutoPrepares(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	cfg := pbxConfig(t, pbx)
	applyDefaults(&cfg)
	p := newPhone(cfg)
	t.Cleanup(func() { p.Disconnect() })

	// Straight from Disconnected: Register must prepare the transport itself.
	require.NoError(t, p.Register(context.Background()))
	assert.Equal(t, PhoneStateRegistered, p.State())
}

func TestRegister_Idempotent(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	p := preparedPhone(t, pbxConfig(t, pbx))

	require.NoError(t, p.Register(context.Background()))
	count := pbx.RegisterCount()
	require.NoError(t, p.Register(context.Background()), "Register when Registered must be a no-op")
	assert.Equal(t, count, pbx.RegisterCount(), "no extra REGISTER on idempotent call")
}

func TestRegister_FailureKeepsTransport(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "correct-password"))
	cfg := pbxConfig(t, pbx)
	cfg.Password = "wrong-password"
	cfg.RegisterMaxRetry = 1
	p := preparedPhone(t, cfg)

	err := p.Register(context.Background())
	require.Error(t, err, "registration with wrong password must fail")
	assert.Equal(t, PhoneStateRegistrationFailed, p.State())
	// The split's point: transport survives a registration failure.
	p.mu.Lock()
	assert.NotNil(t, p.tr, "transport must survive registration failure")
	p.mu.Unlock()
	// And Disconnect still cleans up from this state.
	require.NoError(t, p.Disconnect())
	assert.Equal(t, PhoneStateDisconnected, p.State())
}

func TestUnregister_KeepsTransportAndReRegisters(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	p := preparedPhone(t, pbxConfig(t, pbx))
	require.NoError(t, p.Register(context.Background()))

	unregistered := make(chan struct{}, 1)
	p.OnUnregistered(func() {
		select {
		case unregistered <- struct{}{}:
		default:
		}
	})

	require.NoError(t, p.Unregister(context.Background()))
	assert.Equal(t, PhoneStatePrepared, p.State())
	p.mu.Lock()
	assert.NotNil(t, p.tr, "Unregister must keep the transport")
	p.mu.Unlock()

	// Round-trip: Register again on the SAME transport.
	require.NoError(t, p.Register(context.Background()))
	assert.Equal(t, PhoneStateRegistered, p.State())
}

func TestUnregister_InvalidFromDisconnectedAndPrepared(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	cfg := pbxConfig(t, pbx)
	applyDefaults(&cfg)
	p := newPhone(cfg)
	assert.ErrorIs(t, p.Unregister(context.Background()), ErrInvalidState)

	require.NoError(t, p.Prepare(context.Background()))
	t.Cleanup(func() { p.Disconnect() })
	assert.ErrorIs(t, p.Unregister(context.Background()), ErrInvalidState,
		"Unregister from Prepared (never registered) must be invalid")
}

func TestDial_FromPreparedRequiresRegistrarless(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t) // no auth
	p := preparedPhone(t, pbxConfig(t, pbx))

	_, err := p.Dial(context.Background(), "9999")
	assert.ErrorIs(t, err, ErrNotRegistered,
		"Dial from Prepared without WithRegistrarless must be rejected")
}

func TestDial_RegistrarlessFromPrepared(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t) // no auth — IP-trust style peer
	_, rtpPort := bindRTP(t)
	pbx.OnInvite(func(inv *fakepbx.Invite) {
		inv.Trying()
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	})

	cfg := pbxConfig(t, pbx)
	cfg.Password = ""
	cfg.Registrarless = true
	p := preparedPhone(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := p.Dial(ctx, "9999")
	require.NoError(t, err, "registrarless Dial from Prepared must work")
	assert.Equal(t, StateActive, c.State())
	assert.Equal(t, 0, pbx.RegisterCount(), "no REGISTER must ever have been sent")
	c.End()
}

func TestSendMessage_FromPreparedRequiresRegistrarless(t *testing.T) {
	pbx := fakepbx.NewFakePBX(t)
	p := preparedPhone(t, pbxConfig(t, pbx))

	err := p.SendMessage(context.Background(), "9999", "hello")
	assert.ErrorIs(t, err, ErrNotRegistered)
}

func TestConnect_StillEqualsPrepareAndRegister(t *testing.T) {
	// Connect must behave exactly as before the split (the fakepbx suite
	// covers the full path; this pins the state result and teardown-on-fail).
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "correct"))
	cfg := pbxConfig(t, pbx)
	cfg.Password = "wrong"
	cfg.RegisterMaxRetry = 1
	applyDefaults(&cfg)
	p := newPhone(cfg)

	err := p.Connect(context.Background())
	require.Error(t, err)
	// Historical semantics: Connect failure tears everything down.
	assert.Equal(t, PhoneStateDisconnected, p.State())
	p.mu.Lock()
	assert.Nil(t, p.tr, "Connect failure must tear the transport down")
	p.mu.Unlock()
}

// --- MockPhone lifecycle parity ---

func TestMockPhone_LifecycleSplit(t *testing.T) {
	m := NewMockPhone()
	assert.Equal(t, PhoneStateDisconnected, m.State())

	require.NoError(t, m.Prepare(context.Background()))
	assert.Equal(t, PhoneStatePrepared, m.State())

	registered := false
	m.OnRegistered(func() { registered = true })
	require.NoError(t, m.Register(context.Background()))
	assert.Equal(t, PhoneStateRegistered, m.State())
	assert.True(t, registered)

	require.NoError(t, m.Unregister(context.Background()))
	assert.Equal(t, PhoneStatePrepared, m.State())

	require.NoError(t, m.Register(context.Background()))
	assert.Equal(t, PhoneStateRegistered, m.State())

	require.NoError(t, m.Disconnect())
	assert.Equal(t, PhoneStateDisconnected, m.State())
}
