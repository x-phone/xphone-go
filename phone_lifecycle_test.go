package xphone

import (
	"context"
	"net"
	"strconv"
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

func TestPrepare_InFlightIsInvisibleToGates(t *testing.T) {
	// codex-review regression: Prepared must only be published AFTER the
	// transport is attached. While Prepare is parked inside STUN, the phone
	// reports Disconnected and registrarless Dial is rejected — never a
	// nil-dialFn panic.
	orig := stunMappedAddr
	defer func() { stunMappedAddr = orig }()
	stunEntered := make(chan struct{})
	releaseStun := make(chan struct{})
	stunMappedAddr = func(server string, timeout time.Duration) (string, int, error) {
		close(stunEntered)
		<-releaseStun
		return "", 0, context.DeadlineExceeded // fall back to local IP
	}

	pbx := fakepbx.NewFakePBX(t)
	cfg := pbxConfig(t, pbx)
	cfg.Password = ""
	cfg.Registrarless = true
	cfg.StunServer = "stun.example.com:3478"
	applyDefaults(&cfg)
	p := newPhone(cfg)
	t.Cleanup(func() { p.Disconnect() })

	prepDone := make(chan error, 1)
	go func() { prepDone <- p.Prepare(context.Background()) }()
	<-stunEntered // Prepare is parked inside STUN

	assert.Equal(t, PhoneStateDisconnected, p.State(),
		"in-flight Prepare must not publish Prepared")
	_, err := p.Dial(context.Background(), "9999")
	assert.ErrorIs(t, err, ErrNotRegistered,
		"registrarless Dial mid-Prepare must be rejected, not panic")
	// Concurrent Prepare is rejected explicitly, not silently "successful".
	assert.ErrorIs(t, p.Prepare(context.Background()), ErrInvalidState)

	close(releaseStun)
	require.NoError(t, <-prepDone)
	assert.Equal(t, PhoneStatePrepared, p.State())
}

func TestSendMessage_RejectedDuringTransitionalStates(t *testing.T) {
	// codex-review regression: with the transport attached but the state in
	// Registering/Unregistering, MESSAGE must be rejected without the
	// registrarless opt-in.
	pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
	p := preparedPhone(t, pbxConfig(t, pbx))

	for _, transitional := range []PhoneState{PhoneStateRegistering, PhoneStateUnregistering} {
		p.mu.Lock()
		p.state = transitional
		p.mu.Unlock()
		err := p.SendMessage(context.Background(), "9999", "hello")
		assert.ErrorIs(t, err, ErrNotRegistered, "state=%v", transitional)
	}
	p.mu.Lock()
	p.state = PhoneStatePrepared
	p.mu.Unlock()
}

func TestRegister_HonorsContext(t *testing.T) {
	// codex-review regression: Register(ctx) must be cancelable. A silent
	// registrar (socket that never answers) must not hold Register hostage
	// to the full retry schedule when the caller's deadline expires.
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { silent.Close() })
	host, portStr, _ := net.SplitHostPort(silent.LocalAddr().String())
	port, _ := strconv.Atoi(portStr)

	cfg := Config{
		Username: "1001", Password: "test",
		Host: host, Port: port, Transport: "udp",
		RegisterExpiry: 60 * time.Second, RegisterRetry: 10 * time.Second, RegisterMaxRetry: 5,
	}
	applyDefaults(&cfg)
	p := newPhone(cfg)
	t.Cleanup(func() { p.Disconnect() })
	require.NoError(t, p.Prepare(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = p.Register(ctx)
	require.Error(t, err, "silent registrar must fail registration")
	assert.Less(t, time.Since(start), 5*time.Second,
		"Register must return at the caller's deadline, not after the retry schedule")
	assert.Equal(t, PhoneStateRegistrationFailed, p.State())
}

func TestDisconnect_CancelsInFlightPrepare(t *testing.T) {
	// codex-review regression: Disconnect() during an in-flight Prepare must
	// cancel it — the phone must not end up Prepared with a live transport
	// after shutdown was requested.
	orig := stunMappedAddr
	defer func() { stunMappedAddr = orig }()
	stunEntered := make(chan struct{})
	releaseStun := make(chan struct{})
	stunMappedAddr = func(server string, timeout time.Duration) (string, int, error) {
		close(stunEntered)
		<-releaseStun
		return "", 0, context.DeadlineExceeded
	}

	pbx := fakepbx.NewFakePBX(t)
	cfg := pbxConfig(t, pbx)
	cfg.StunServer = "stun.example.com:3478"
	applyDefaults(&cfg)
	p := newPhone(cfg)

	prepDone := make(chan error, 1)
	go func() { prepDone <- p.Prepare(context.Background()) }()
	<-stunEntered

	require.NoError(t, p.Disconnect(), "Disconnect must cancel the in-flight Prepare")
	close(releaseStun)

	err := <-prepDone
	assert.ErrorIs(t, err, ErrInvalidState, "canceled Prepare must not succeed")
	assert.Equal(t, PhoneStateDisconnected, p.State())
	p.mu.Lock()
	assert.Nil(t, p.tr, "no transport may survive a canceled Prepare")
	p.mu.Unlock()
}

func TestDisconnect_DuringRegisterKeepsDisconnected(t *testing.T) {
	// codex-review regression: Disconnect while REGISTER is in flight must
	// win — runRegistration must not resurrect Registered/RegistrationFailed
	// over the torn-down phone.
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { silent.Close() })
	host, portStr, _ := net.SplitHostPort(silent.LocalAddr().String())
	port, _ := strconv.Atoi(portStr)

	cfg := Config{
		Username: "1001", Password: "test",
		Host: host, Port: port, Transport: "udp",
		RegisterExpiry: 60 * time.Second, RegisterRetry: 200 * time.Millisecond, RegisterMaxRetry: 2,
	}
	applyDefaults(&cfg)
	p := newPhone(cfg)
	require.NoError(t, p.Prepare(context.Background()))

	regDone := make(chan error, 1)
	go func() { regDone <- p.Register(context.Background()) }()

	// Let Register park against the silent registrar, then tear down.
	time.Sleep(150 * time.Millisecond)
	require.NoError(t, p.Disconnect())

	err = <-regDone
	require.Error(t, err, "canceled registration must not report success")
	assert.Equal(t, PhoneStateDisconnected, p.State(),
		"Disconnect must win over the in-flight registration result")
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
