// Package xphone provides an event-driven SIP user agent for embedding
// VoIP telephony into Go applications. It manages SIP registration, call
// state machines, RTP media pipelines, codec encode/decode, and DTMF —
// exposing a concurrency-safe API for handling multiple concurrent calls.
package xphone

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"

	"github.com/x-phone/xphone-go/ice"
	"github.com/x-phone/xphone-go/internal/sdp"
	"github.com/x-phone/xphone-go/internal/srtp"
	"github.com/x-phone/xphone-go/internal/stun"
)

// Phone is the public interface for the xphone library.
type Phone interface {
	// Connect prepares the transport and registers: Prepare + Register.
	Connect(ctx context.Context) error
	// Prepare binds the transport and resolves the advertised address
	// without registering; the phone lands in PhoneStatePrepared (#101).
	Prepare(ctx context.Context) error
	// Register drives the REGISTER flow, auto-preparing when needed. Unlike
	// Connect, a registration failure keeps the prepared transport.
	Register(ctx context.Context) error
	// Unregister removes the registration but keeps the transport bound,
	// returning to PhoneStatePrepared. Active calls are not touched.
	Unregister(ctx context.Context) error
	Disconnect() error
	Dial(ctx context.Context, target string, opts ...DialOption) (Call, error)
	OnIncoming(func(call Call))
	OnRegistered(func())
	OnUnregistered(func())
	OnError(func(err error))
	OnCallState(func(call Call, state CallState))
	OnCallEnded(func(call Call, reason EndReason))
	OnCallDTMF(func(call Call, digit string))
	OnVoicemail(func(status VoicemailStatus))
	OnMessage(func(msg SipMessage))
	SendMessage(ctx context.Context, target string, body string) error
	SendMessageWithType(ctx context.Context, target string, contentType string, body string) error
	Watch(ctx context.Context, extension string, fn func(ext string, state ExtensionState, prev ExtensionState)) (string, error)
	Unwatch(subscriptionID string) error
	SubscribeEvent(ctx context.Context, uri string, event string, expires int, fn func(NotifyEvent)) (string, error)
	UnsubscribeEvent(subscriptionID string) error
	AttendedTransfer(callA Call, callB Call) error
	FindCall(callID string) Call
	Calls() []Call
	State() PhoneState
}

// phone is the concrete implementation of Phone.
type phone struct {
	mu sync.Mutex

	cfg         Config
	logger      *slog.Logger
	tr          sipTransport
	reg         *registry
	localIP     string // advertised IP — SDP c=, default SIP Contact/Via. Override, STUN, or localIPFor(cfg.Host).
	hostIP      string // ICE host candidate IP (= override when set, else localIPFor(cfg.Host))
	interfaceIP string // real bind interface IP — fallback for SIP Contact when cfg.NAT is set with an RTPAddress override, so rport + REGISTER keepalive can drive NAT learning.
	codecPrefs  []int  // cached codecPrefsToInts(cfg.CodecPrefs), set at construction
	state       PhoneState
	preparing   bool               // standalone Prepare() in flight; Prepared is only published after transport attachment (#101)
	regCancel   context.CancelFunc // cancels an in-flight initial REGISTER so Disconnect() unblocks Register/Connect (#101)
	// lifecycleGen identifies the current lifecycle attempt. Every
	// Connect/Prepare/Register start and every Disconnect bumps it; in-flight
	// setup validates its captured value before mutating shared state, so a
	// stale attempt can never clobber its replacement (#101).
	lifecycleGen uint64
	incomingFns  []func(Call)
	calls        map[string]*call // active calls keyed by SIP Call-ID

	// dialFn establishes an outbound SIP dialog and returns a dialog interface.
	// Production: set by Connect() to use sipgo's dialog API.
	// Tests: set by connectWithTransport() to use the mock transport.
	// The onResponse callback is invoked for each provisional/final SIP response.
	dialFn func(ctx context.Context, target string, opts DialOptions, onResponse func(code int, reason string)) (dialog, error)

	// Buffered callbacks set before Connect.
	onRegisteredFns   []func()
	onUnregisteredFns []func()
	onErrorFns        []func(error)

	// Phone-level call callbacks — auto-wired to every new call.
	onCallStateFns []func(Call, CallState)
	onCallEndedFns []func(Call, EndReason)
	onCallDTMFFns  []func(Call, string)

	// SIP MESSAGE callback.
	onMessageFns []func(SipMessage)

	// MWI (voicemail) state.
	onVoicemailFns []func(VoicemailStatus)
	mwi            *mwiSubscriber

	// Subscription manager for Watch/SubscribeEvent.
	subMgr *subscriptionManager
}

// codecPrefsToInts converts []Codec to []int for SDP/negotiation.
func codecPrefsToInts(codecs []Codec) []int {
	if len(codecs) == 0 {
		return nil
	}
	pts := make([]int, len(codecs))
	for i, c := range codecs {
		pts[i] = int(c)
	}
	return pts
}

// resolveContactIP returns the IP to advertise in SIP Contact/Via. By default
// this is p.localIP (which already accounts for STUN discovery and RTPAddress
// override). Carve-out for Docker-bridge and similar setups: when cfg.NAT is
// set AND an RTPAddress override is active, Contact/Via fall back to the
// real bind interface IP so rport (RFC 3581) + REGISTER keepalive can drive
// the PBX's NAT learning. Rewriting Contact to a reachable-looking host
// while the SIP port is ephemeral (sipgo binds a random UDP port) would
// trick the PBX into targeting an unpublished port. See #109.
func (p *phone) resolveContactIP() string {
	if p.cfg.NAT && effectiveRTPAddress(p.cfg.RTPAddress) != "" {
		return p.interfaceIP
	}
	return p.localIP
}

// effectiveRTPAddress returns s if it parses as an IPv4 literal, else "".
// The stack's media path is IPv4-only (udp4 sockets, SDP "IN IP4"), so IPv6
// literals and non-IP strings (hostnames, whitespace, CRLF injection) are
// rejected rather than propagated into SIP Contact / SDP.
func effectiveRTPAddress(s string) string {
	if s == "" {
		return ""
	}
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		return ""
	}
	return s
}

func newPhone(cfg Config) *phone {
	interfaceIP := localIPFor(cfg.Host)
	localIP := interfaceIP
	hostIP := interfaceIP
	// When RTPAddress is a valid IPv4 literal, use it for BOTH the advertised
	// IP (SDP c=) AND the ICE host candidate — otherwise ICE would emit a
	// high-priority host candidate at the auto-detected IP and a lower-priority
	// srflx at the override, leading peers to probe the unroutable address
	// first. interfaceIP stays at the real bind IP so Contact can fall back
	// to it when cfg.NAT is set (see Connect).
	if override := effectiveRTPAddress(cfg.RTPAddress); override != "" {
		localIP = override
		hostIP = override
	}
	logger := resolveLogger(cfg.Logger)
	if cfg.RTPAddress != "" && effectiveRTPAddress(cfg.RTPAddress) == "" {
		logger.Warn("xphone: WithRTPAddress ignored, not an IPv4 literal", "value", cfg.RTPAddress)
	}
	return &phone{
		cfg:         cfg,
		logger:      logger,
		localIP:     localIP,
		hostIP:      hostIP,
		interfaceIP: interfaceIP,
		codecPrefs:  codecPrefsToInts(cfg.CodecPrefs),
		state:       PhoneStateDisconnected,
		calls:       make(map[string]*call),
	}
}

// wireCallCallbacks hooks phone-level call callbacks (OnCallState, OnCallEnded,
// OnCallDTMF) onto a call's internal callback fields so they coexist with
// user-set per-call callbacks. Must be called with p.mu held.
func (p *phone) wireCallCallbacks(c *call) {
	wireCallCallbacks(c, p.composedOnCallState(), p.composedOnCallEnded(), p.composedOnCallDTMF())
}

// composedOnCallState returns a single function that calls all registered
// OnCallState callbacks, or nil if none are registered. Must be called with p.mu held.
func (p *phone) composedOnCallState() func(Call, CallState) {
	if len(p.onCallStateFns) == 0 {
		return nil
	}
	fns := make([]func(Call, CallState), len(p.onCallStateFns))
	copy(fns, p.onCallStateFns)
	return func(call Call, state CallState) {
		for _, fn := range fns {
			fn(call, state)
		}
	}
}

// composedOnCallEnded returns a single function that calls all registered
// OnCallEnded callbacks, or nil if none are registered. Must be called with p.mu held.
func (p *phone) composedOnCallEnded() func(Call, EndReason) {
	if len(p.onCallEndedFns) == 0 {
		return nil
	}
	fns := make([]func(Call, EndReason), len(p.onCallEndedFns))
	copy(fns, p.onCallEndedFns)
	return func(call Call, reason EndReason) {
		for _, fn := range fns {
			fn(call, reason)
		}
	}
}

// composedOnCallDTMF returns a single function that calls all registered
// OnCallDTMF callbacks, or nil if none are registered. Must be called with p.mu held.
func (p *phone) composedOnCallDTMF() func(Call, string) {
	if len(p.onCallDTMFFns) == 0 {
		return nil
	}
	fns := make([]func(Call, string), len(p.onCallDTMFFns))
	copy(fns, p.onCallDTMFFns)
	return func(call Call, digit string) {
		for _, fn := range fns {
			fn(call, digit)
		}
	}
}

// setupSRTP is a convenience wrapper calling call.setupSRTP with the phone logger.
func (p *phone) setupSRTP(c *call, localKey, remoteKey string) {
	c.setupSRTP(p.logger, localKey, remoteKey)
}

// applyCallConfig threads phone-level config into a new call.
func (p *phone) applyCallConfig(c *call) {
	c.mediaTimeout = p.cfg.MediaTimeout
	c.jitterDepth = p.cfg.JitterBuffer
	c.pcmRate = p.cfg.PCMRate
	c.codecPrefs = p.codecPrefs
	c.dtmfMode = p.cfg.DtmfMode
	c.iceEnabled = p.cfg.ICE
	c.hostIP = p.hostIP
}

// transportDial implements dialFn using the sipTransport interface.
// Used by connectWithTransport() for test mocks.
func (p *phone) transportDial(ctx context.Context, target string, _ DialOptions, onResponse func(code int, reason string)) (dialog, error) {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()

	code, reason, err := tr.SendRequest(ctx, "INVITE", nil)
	if err != nil {
		return nil, err
	}
	if onResponse != nil {
		onResponse(code, reason)
	}

	for code >= 100 && code < 200 {
		code, reason, err = tr.ReadResponse(ctx)
		if err != nil {
			return nil, err
		}
		if onResponse != nil {
			onResponse(code, reason)
		}
	}

	return newPhoneDialog(), nil
}

// connectWithTransport is a test hook that connects with a mock transport.
// It performs registration (consuming the queued 200 OK) and wires up the
// incoming INVITE handler on the transport. Returns the registration error
// (if any) so Connect() can propagate it to the caller.
func (p *phone) connectWithTransport(tr sipTransport) error {
	p.mu.Lock()
	p.tr = tr
	if p.dialFn == nil {
		p.dialFn = p.transportDial
	}
	p.reg = p.newWiredRegistryLocked(tr)
	// runRegistration publishes its result only from the Registering marker
	// of the current lifecycle attempt; establish both like every caller.
	p.state = PhoneStateRegistering
	p.lifecycleGen++
	gen := p.lifecycleGen
	p.mu.Unlock()

	// Wire up incoming INVITE and MESSAGE handling on the transport.
	tr.OnIncoming(p.handleIncoming)
	tr.OnMessage(p.handleMessage)

	return p.runRegistration(context.Background(), gen)
}

// newWiredRegistryLocked creates a registry wired to the phone's buffered
// callbacks. Caller must hold p.mu.
func (p *phone) newWiredRegistryLocked(tr sipTransport) *registry {
	reg := newRegistry(tr, p.cfg)
	if len(p.onRegisteredFns) > 0 {
		fns := make([]func(), len(p.onRegisteredFns))
		copy(fns, p.onRegisteredFns)
		reg.OnRegistered(func() {
			for _, fn := range fns {
				fn()
			}
		})
	}
	if len(p.onUnregisteredFns) > 0 {
		fns := make([]func(), len(p.onUnregisteredFns))
		copy(fns, p.onUnregisteredFns)
		reg.OnUnregistered(func() {
			for _, fn := range fns {
				fn()
			}
		})
	}
	if len(p.onErrorFns) > 0 {
		fns := make([]func(error), len(p.onErrorFns))
		copy(fns, p.onErrorFns)
		reg.OnError(func(err error) {
			for _, fn := range fns {
				fn(err)
			}
		})
	}
	return reg
}

// runRegistration drives the REGISTER flow on the already-attached transport,
// sets Registered/RegistrationFailed, and on success starts MWI and the
// subscription manager (#101: shared by Connect, Register, and the
// connectWithTransport test hook). ctx bounds the initial registration
// attempt (the refresh loop runs on the registry's own lifecycle context).
func (p *phone) runRegistration(ctx context.Context, gen uint64) error {
	regCtx, cancelReg := context.WithCancel(ctx)
	defer cancelReg()
	p.mu.Lock()
	if p.lifecycleGen != gen {
		// Superseded before we even started.
		p.mu.Unlock()
		return ErrInvalidState
	}
	reg := p.reg
	tr := p.tr
	p.regCancel = cancelReg // lets Disconnect() unblock a parked REGISTER
	p.mu.Unlock()

	// Perform registration (consumes the pre-queued 200 OK in tests).
	err := reg.Start(regCtx)

	p.mu.Lock()
	if p.lifecycleGen != gen || p.state != PhoneStateRegistering {
		// A racing Disconnect() tore the phone down — or a newer attempt
		// owns the phone now. Either way this result is stale: don't touch
		// state or regCancel. Surface the registration error if there was one.
		p.mu.Unlock()
		if err != nil {
			return err
		}
		return ErrInvalidState
	}
	p.regCancel = nil
	if err == nil {
		p.state = PhoneStateRegistered
	} else {
		p.state = PhoneStateRegistrationFailed
	}
	voicemailURI := p.cfg.VoicemailURI
	voicemailFns := make([]func(VoicemailStatus), len(p.onVoicemailFns))
	copy(voicemailFns, p.onVoicemailFns)
	p.mu.Unlock()

	// Auto-start MWI subscription if configured and registration succeeded.
	// Assignments are generation+state guarded: if Disconnect/Unregister
	// raced in after Registered was published, storing a fresh service
	// against the torn-down transport would leak it — stop it instead.
	if err == nil && voicemailURI != "" {
		sub := newMWISubscriber(tr, voicemailURI, p.logger)
		if len(voicemailFns) > 0 {
			sub.setOnVoicemail(func(status VoicemailStatus) {
				for _, fn := range voicemailFns {
					fn(status)
				}
			})
		}
		sub.start()
		p.mu.Lock()
		if p.lifecycleGen == gen && p.state == PhoneStateRegistered {
			p.mwi = sub
			p.mu.Unlock()
		} else {
			p.mu.Unlock()
			sub.stop()
		}
	}

	// Start subscription manager for Watch/SubscribeEvent.
	if err == nil {
		sm := newSubscriptionManager(tr, p.cfg.Host, p.logger)
		sm.start()
		p.mu.Lock()
		if p.lifecycleGen == gen && p.state == PhoneStateRegistered {
			p.subMgr = sm
			p.mu.Unlock()
		} else {
			p.mu.Unlock()
			sm.stop()
		}
	}

	if err != nil {
		return err
	}
	p.logger.Info("phone connected")
	return nil
}

// stunMappedAddr resolves the NAT-mapped address via STUN. Indirected so
// tests can stub the network round-trip (#106).
var stunMappedAddr = stun.MappedAddr

// prepareTransport performs the network-side setup shared by Prepare(),
// Register(), and Connect(): STUN discovery (unlocked, #106), sipgo UA
// creation, dialog-handler wiring, server start, and transport attachment.
// stillValid is evaluated under p.mu after the STUN round-trip — if a racing
// Disconnect() invalidated the operation, setup aborts with ErrInvalidState.
func (p *phone) prepareTransport(ctx context.Context, stillValid func() bool) error {
	// invalidated returns the context error when cancellation is the cause,
	// so callers see context.Canceled/DeadlineExceeded rather than an opaque
	// ErrInvalidState.
	invalidated := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrInvalidState
	}
	p.mu.Lock()
	stunServer := p.cfg.StunServer
	rtpAddress := p.cfg.RTPAddress
	fallbackIP := p.localIP
	p.mu.Unlock()

	// STUN discovery: if configured, try to discover our NAT-mapped IP.
	// On success, override the localIP used for Contact headers and SDP.
	// On failure, log a warning and keep the existing localIPFor() result.
	// Skipped when a valid cfg.RTPAddress is set — explicit override wins.
	// Runs WITHOUT p.mu held: this is a ~1s network round-trip, and holding
	// the lock here blocked State()/Dial()/Disconnect() for the duration (#106).
	var stunIP string
	if stunServer != "" && effectiveRTPAddress(rtpAddress) == "" {
		ip, _, err := stunMappedAddr(stunServer, stun.DefaultTimeout)
		if err != nil {
			p.logger.Warn("STUN discovery failed, using local IP", "err", err, "fallback", fallbackIP)
		} else {
			p.logger.Info("STUN mapped address discovered", "ip", ip)
			stunIP = ip
		}
	}

	p.mu.Lock()
	if !stillValid() {
		// Disconnect() raced us, or ctx was canceled, while STUN was in flight.
		p.mu.Unlock()
		return invalidated()
	}
	if stunIP != "" {
		p.localIP = stunIP
	}

	contactIP := p.resolveContactIP()
	if contactIP != p.localIP {
		p.logger.Debug("NAT mode with RTPAddress: keeping Contact/Via at interface IP for rport NAT learning",
			"interface_ip", p.interfaceIP, "rtp_address", p.cfg.RTPAddress)
	}
	p.mu.Unlock()

	tr, err := newSipUA(p.cfg, contactIP)
	if err != nil {
		return err
	}

	// Wire inbound INVITE, BYE, and NOTIFY handlers for sipgo path.
	tr.mu.Lock()
	tr.onDialogInvite = p.handleDialogInvite
	tr.onDialogReInvite = p.handleDialogReInvite
	tr.onDialogBye = p.handleDialogBye
	tr.onDialogCancel = p.handleDialogCancel
	tr.onDialogNotify = p.handleDialogNotify
	tr.onDialogInfo = p.handleDialogInfo
	tr.mu.Unlock()
	tr.startServer()

	p.mu.Lock()
	if !stillValid() {
		// Superseded (or ctx canceled) while we were building the transport —
		// do not clobber whatever the current attempt has established.
		p.mu.Unlock()
		tr.Close()
		return invalidated()
	}
	p.tr = tr
	p.dialFn = tr.dialWithOpts // assigned only under the guard: a stale attempt must not point dialFn at its closed UA
	p.reg = p.newWiredRegistryLocked(tr)
	p.mu.Unlock()

	tr.OnIncoming(p.handleIncoming)
	tr.OnMessage(p.handleMessage)
	return nil
}

// teardownAfterPrepareError resets state after a failed prepareTransport,
// but only if a racing Disconnect() hasn't already moved the state on.
func (p *phone) teardownAfterPrepareError(gen uint64, inProgress PhoneState) {
	p.mu.Lock()
	if p.lifecycleGen == gen && p.state == inProgress {
		p.state = PhoneStateDisconnected
		p.tr = nil
		p.reg = nil
	}
	p.mu.Unlock()
}

func (p *phone) Connect(ctx context.Context) error {
	p.mu.Lock()
	if p.state != PhoneStateDisconnected {
		p.mu.Unlock()
		return ErrAlreadyConnected
	}
	p.state = PhoneStateRegistering
	p.lifecycleGen++
	gen := p.lifecycleGen
	p.mu.Unlock()

	stillValid := func() bool {
		return ctx.Err() == nil && p.lifecycleGen == gen && p.state == PhoneStateRegistering
	}
	if err := p.prepareTransport(ctx, stillValid); err != nil {
		p.teardownAfterPrepareError(gen, PhoneStateRegistering)
		return err
	}

	if err := p.runRegistration(ctx, gen); err != nil {
		// Historical Connect semantics: registration failure tears the
		// transport down. (Register() keeps it — that is its point.)
		// Generation-guarded: if we were superseded, the phone belongs to
		// the newer attempt (or Disconnect already cleaned up) — touch
		// nothing.
		p.mu.Lock()
		if p.lifecycleGen != gen {
			p.mu.Unlock()
			return err
		}
		tr := p.tr
		p.tr = nil
		p.reg = nil
		p.state = PhoneStateDisconnected
		p.mu.Unlock()
		if tr != nil {
			tr.Close()
		}
		return err
	}
	return nil
}

// Prepare binds the SIP transport, runs STUN discovery, and resolves the
// advertised address WITHOUT performing any REGISTER (#101). The phone lands
// in PhoneStatePrepared: incoming handlers are wired, and with
// WithRegistrarless() outbound Dial/SendMessage work immediately. Idempotent:
// calling it when the transport is already up is a no-op.
func (p *phone) Prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	if p.preparing {
		// Another Prepare is in flight. Returning nil here would let the
		// caller believe the phone is ready while the first attempt may
		// still fail — be explicit instead.
		p.mu.Unlock()
		return ErrInvalidState
	}
	if p.state != PhoneStateDisconnected {
		// Idempotent no-op only when a transport is actually attached.
		// During another caller's in-flight setup (Connect/Register mid-
		// prepareTransport: state Registering, tr still nil) be explicit
		// rather than reporting readiness that doesn't exist yet.
		attached := p.tr != nil
		p.mu.Unlock()
		if attached {
			return nil
		}
		return ErrInvalidState
	}
	// The unexported preparing flag is the in-progress marker: the phone
	// keeps reporting Disconnected until the transport is fully attached, so
	// no gate (registrarless Dial, SendMessage) can ever observe Prepared
	// with a half-initialized phone. The generation ties this attempt's
	// mutations to itself: once superseded, it touches nothing.
	p.preparing = true
	p.lifecycleGen++
	gen := p.lifecycleGen
	p.mu.Unlock()

	// ctx participates in validity: a canceled caller aborts at the next
	// checkpoint (post-STUN, pre-attach) instead of committing a transport.
	err := p.prepareTransport(ctx, func() bool {
		return ctx.Err() == nil && p.lifecycleGen == gen && p.preparing
	})

	p.mu.Lock()
	if p.lifecycleGen != gen {
		// Superseded by Disconnect() (which tore down anything we attached)
		// or by a newer attempt that owns the phone now — touch nothing.
		p.mu.Unlock()
		return ErrInvalidState
	}
	p.preparing = false
	if err != nil {
		p.tr = nil
		p.reg = nil
		p.mu.Unlock()
		return err
	}
	p.state = PhoneStatePrepared // published only now, transport attached
	p.mu.Unlock()
	p.logger.Info("phone prepared")
	return nil
}

// Register drives the REGISTER flow (#101). Auto-prepares the transport when
// needed, so Connect() remains exactly Prepare + Register. Unlike Connect, a
// registration failure KEEPS the prepared transport — the caller learns
// "transport OK, registrar said no" and can simply call Register() again.
// Returns nil immediately when already registered.
func (p *phone) Register(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	switch p.state {
	case PhoneStateRegistered:
		p.mu.Unlock()
		return nil
	case PhoneStateRegistering, PhoneStateUnregistering:
		p.mu.Unlock()
		return ErrInvalidState
	case PhoneStateDisconnected:
		p.state = PhoneStateRegistering
		p.lifecycleGen++
		gen := p.lifecycleGen
		p.mu.Unlock()
		stillValid := func() bool {
			return ctx.Err() == nil && p.lifecycleGen == gen && p.state == PhoneStateRegistering
		}
		if err := p.prepareTransport(ctx, stillValid); err != nil {
			p.teardownAfterPrepareError(gen, PhoneStateRegistering)
			return err
		}
		return p.runRegistration(ctx, gen)
	default: // PhoneStatePrepared, PhoneStateRegistrationFailed
		if p.reg == nil {
			p.reg = p.newWiredRegistryLocked(p.tr)
		}
		p.state = PhoneStateRegistering
		p.lifecycleGen++
		gen := p.lifecycleGen
		p.mu.Unlock()
		return p.runRegistration(ctx, gen)
	}
}

// Unregister sends the un-REGISTER (Expires: 0) and stops the registration
// refresh loop, MWI, and subscriptions — but KEEPS the transport bound and
// returns the phone to PhoneStatePrepared (#101). Active calls are not
// touched: de-registration does not end established dialogs. Register() or
// Disconnect() are both valid next steps.
func (p *phone) Unregister(ctx context.Context) error {
	p.mu.Lock()
	if p.state != PhoneStateRegistered && p.state != PhoneStateRegistrationFailed {
		p.mu.Unlock()
		return ErrInvalidState
	}
	p.state = PhoneStateUnregistering
	reg := p.reg
	mwi := p.mwi
	sm := p.subMgr
	p.reg = nil
	p.mwi = nil
	p.subMgr = nil
	unregFns := make([]func(), len(p.onUnregisteredFns))
	copy(unregFns, p.onUnregisteredFns)
	p.mu.Unlock()

	if sm != nil {
		sm.stop()
	}
	if mwi != nil {
		mwi.stop()
	}
	if reg != nil {
		reg.UnregisterCtx(ctx)
		reg.Stop()
	}

	p.mu.Lock()
	completed := p.state == PhoneStateUnregistering
	if completed {
		// Only publish Prepared if a racing Disconnect() hasn't torn the
		// transport down in the meantime — otherwise we would advertise a
		// prepared phone with no transport.
		p.state = PhoneStatePrepared
	}
	p.mu.Unlock()

	if !completed {
		// A racing Disconnect() owns the teardown — it already fired the
		// OnUnregistered callbacks; firing them again would double-notify.
		return nil
	}
	p.logger.Info("phone unregistered, transport kept")
	for _, fn := range unregFns {
		go fn()
	}
	return nil
}

func (p *phone) Disconnect() error {
	p.mu.Lock()
	if p.state == PhoneStateDisconnected {
		if p.preparing {
			// Cancel an in-flight standalone Prepare(): the generation bump
			// invalidates it (it will touch nothing on its way out), and we
			// tear down anything it already attached.
			p.preparing = false
			p.lifecycleGen++
			tr := p.tr
			p.tr = nil
			p.reg = nil
			p.mu.Unlock()
			if tr != nil {
				tr.Close()
			}
			return nil
		}
		p.mu.Unlock()
		return ErrNotConnected
	}
	p.lifecycleGen++ // invalidate any in-flight lifecycle attempt
	if p.regCancel != nil {
		// Unblock an in-flight Register/Connect parked on a slow or silent
		// registrar; the generation bump keeps this Disconnected state
		// authoritative over the stale result.
		p.regCancel()
		p.regCancel = nil
	}
	reg := p.reg
	tr := p.tr
	mwi := p.mwi
	sm := p.subMgr
	p.state = PhoneStateDisconnected
	p.reg = nil
	p.tr = nil
	p.mwi = nil
	p.subMgr = nil
	unregFns := make([]func(), len(p.onUnregisteredFns))
	copy(unregFns, p.onUnregisteredFns)

	// Snapshot and clear active calls so we can end them outside the lock.
	activeCalls := make([]*call, 0, len(p.calls))
	for _, c := range p.calls {
		activeCalls = append(activeCalls, c)
	}
	p.calls = make(map[string]*call)
	p.mu.Unlock()

	// End all active calls.
	for _, c := range activeCalls {
		c.End()
	}

	// Stop subscription manager (sends Expires: 0 unsubscribes).
	if sm != nil {
		sm.stop()
	}

	// Stop MWI subscription (sends Expires: 0 unsubscribe).
	if mwi != nil {
		mwi.stop()
	}

	// Send un-REGISTER (Expires: 0) to remove our contact from the registrar
	// per RFC 3261 §10.2.2. This must happen before Stop() cancels the
	// lifecycle context and before Close() tears down the transport.
	if reg != nil {
		reg.Unregister()
	}

	// Stop the registry (cancels refresh loop and re-registration goroutines).
	if reg != nil {
		reg.Stop()
	}

	// Close the transport.
	if tr != nil {
		tr.Close()
	}

	p.logger.Info("phone disconnected")

	// Fire OnUnregistered callbacks.
	for _, fn := range unregFns {
		go fn()
	}

	return nil
}

// Dial initiates an outbound call to the given SIP target.
// It sends an INVITE, waits for provisional (1xx) and final (2xx) responses,
// and returns the active Call. Honors both context cancellation and DialTimeout.
func (p *phone) Dial(ctx context.Context, target string, opts ...DialOption) (Call, error) {
	p.mu.Lock()
	switch {
	case p.state == PhoneStateRegistered:
		// Normal path.
	case p.cfg.Registrarless && (p.state == PhoneStatePrepared || p.state == PhoneStateRegistrationFailed):
		// Registrarless (#101): transport is up, no registration required.
	default:
		p.mu.Unlock()
		return nil, ErrNotRegistered
	}
	dialFn := p.dialFn
	p.mu.Unlock()

	dialOpts := applyDialOptions(opts)

	// Create a context with the dial timeout. Both ctx and dialTimeout can
	// cancel the operation — whichever fires first wins.
	var dialCtx context.Context
	var dialCancel context.CancelFunc
	if dialOpts.Timeout > 0 {
		dialCtx, dialCancel = context.WithTimeout(ctx, dialOpts.Timeout)
	} else {
		dialCtx, dialCancel = context.WithCancel(ctx)
	}
	defer dialCancel()

	p.logger.Info("dialing", "target", target)

	// Collect provisional responses during dial; replay them on the call
	// after construction to avoid a race between the response callback
	// and the dialog field being swapped.
	type sipResponse struct {
		code   int
		reason string
	}
	var responses []sipResponse

	// Establish the SIP dialog via dialFn.
	dlg, err := dialFn(dialCtx, target, dialOpts, func(code int, reason string) {
		responses = append(responses, sipResponse{code, reason})
	})
	if err != nil {
		return nil, p.classifyDialError(ctx, dialCtx, err)
	}

	// Create the call with the real dialog and replay responses.
	c := newOutboundCall(dlg, opts...)
	c.logger = p.logger
	c.sipHost = p.cfg.Host
	p.applyCallConfig(c)
	// Transfer RTP socket ownership and capture SDP from the dialog.
	p.logger.Debug("dial completed, setting up call", "target", target)
	if uac, ok := dlg.(*sipgoDialogUAC); ok {
		if uac.rtpConn != nil {
			c.rtpConn = uac.rtpConn
			c.rtpPort = uac.rtpConn.LocalAddr().(*net.UDPAddr).Port
			c.localIP = p.localIP
			uac.rtpConn = nil // transfer ownership
		}
		if uac.videoRtpConn != nil {
			c.videoRTPConn = uac.videoRtpConn
			c.videoRTPPort = uac.videoRtpConn.LocalAddr().(*net.UDPAddr).Port
			uac.videoRtpConn = nil // transfer ownership
		}
		if uac.invite != nil {
			c.localSDP = string(uac.invite.Body())
		}
		if uac.response != nil {
			body := uac.response.Body()
			if len(body) > 0 {
				c.remoteSDP = string(body)
				p.logger.Debug("remote SDP received", "sdp", c.remoteSDP)
				if sess, parseErr := sdp.Parse(c.remoteSDP); parseErr == nil {
					c.negotiateCodec(sess)
					c.setRemoteEndpoint(sess)

					// Set remote ICE credentials from answer SDP.
					if c.iceAgent != nil && sess.IceUfrag != "" && sess.IcePwd != "" {
						c.iceAgent.SetRemoteCredentials(ice.Credentials{Ufrag: sess.IceUfrag, Pwd: sess.IcePwd})
					}

					// Set up SRTP if remote accepted with crypto.
					if uac.srtpLocalKey != "" && sess.IsSRTP() {
						if crypto := sess.FirstCrypto(); crypto != nil {
							p.setupSRTP(c, uac.srtpLocalKey, crypto.InlineKey())
						}
					}

					// Set up video if negotiated.
					if c.hasVideo && c.videoRTPConn != nil {
						c.setVideoRemoteEndpoint(sess)
						c.initVideoChannels()
					}
				}
			}
		}
	}
	p.registerCall(c)
	for _, r := range responses {
		c.simulateResponse(r.code, r.reason)
	}

	// Start media pipeline for production calls with an RTP socket.
	if c.rtpConn != nil {
		c.startMedia()
		c.startRTPReader()
	}
	if c.hasVideo && c.videoRTPConn != nil {
		c.startVideoMedia()
		c.startVideoRTPReader()
	}

	return c, nil
}

// classifyDialError determines whether a dial failure was due to the parent
// context being cancelled or the dial timeout expiring.
// If the parent ctx is done, return its error (e.g. context.DeadlineExceeded).
// Otherwise the dial timeout fired — return ErrDialTimeout.
func (p *phone) classifyDialError(ctx, dialCtx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if dialCtx.Err() != nil {
		return ErrDialTimeout
	}
	return err
}

// registerCall adds a call to the active calls map, wires phone-level
// callbacks, and sets up automatic cleanup on call end.
func (p *phone) registerCall(c *call) {
	c.onEndedCleanup = func(_ EndReason) {
		p.untrackCall(c.CallID())
	}
	p.mu.Lock()
	p.calls[c.CallID()] = c
	p.wireCallCallbacks(c)
	p.mu.Unlock()
}

// untrackCall removes a call from the active calls map under lock.
func (p *phone) untrackCall(dialogID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.calls, dialogID)
}

// findCall looks up an active call by Call-ID under lock (internal use).
func (p *phone) findCall(callID string) *call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[callID]
}

// Calls returns a snapshot of all active calls.
func (p *phone) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]Call, 0, len(p.calls))
	for _, c := range p.calls {
		result = append(result, c)
	}
	return result
}

// AttendedTransfer performs an attended (consultative) transfer.
// Delegates to callA.AttendedTransfer(callB). See Call.AttendedTransfer.
func (p *phone) AttendedTransfer(callA Call, callB Call) error {
	return callA.AttendedTransfer(callB)
}

// FindCall returns an active call by its SIP Call-ID, or nil if not found.
func (p *phone) FindCall(callID string) Call {
	c := p.findCall(callID)
	if c == nil {
		return nil
	}
	return c
}

// handleDialogInvite is called by sipUA's OnInvite handler when an inbound
// INVITE arrives via sipgo. It creates an inbound call with the real dialog,
// sends 100 Trying + 180 Ringing, and fires OnIncoming.
func (p *phone) handleDialogInvite(dlg dialog, from, to, sdpBody string) {
	c := newInboundCall(dlg)
	c.logger = p.logger
	c.sipHost = p.cfg.Host
	c.localIP = p.localIP
	c.rtpPortMin = p.cfg.RTPPortMin
	c.rtpPortMax = p.cfg.RTPPortMax
	p.applyCallConfig(c)
	c.remoteSDP = sdpBody

	// Eagerly allocate RTP port before presenting the call.
	// If allocation fails, reject with 500 instead of sending port 0 SDP.
	c.mu.Lock()
	if err := c.ensureRTPPort(); err != nil {
		c.mu.Unlock()
		p.logger.Error("RTP port allocation failed, rejecting INVITE", "from", from, "error", err)
		if err := dlg.Respond(500, "Internal Server Error", nil); err != nil {
			p.logger.Error("failed to send 500", "err", err)
		}
		return
	}
	c.mu.Unlock()

	p.registerCall(c)

	p.logger.Info("incoming call", "from", from, "to", to)
	if sdpBody != "" {
		p.logger.Debug("incoming remote SDP", "sdp", sdpBody)
	}

	// Parse remote SDP once for SRTP and video setup.
	if sdpBody != "" {
		if sess, err := sdp.Parse(sdpBody); err == nil {
			// Set up SRTP if remote offers it and we have SRTP enabled.
			if p.cfg.SRTP && sess.IsSRTP() {
				if crypto := sess.FirstCrypto(); crypto != nil {
					localKey, err := srtp.GenerateKeyingMaterial()
					if err == nil {
						p.setupSRTP(c, localKey, crypto.InlineKey())
					}
				}
			}
			// Set up video if remote offers it.
			if vm := sess.VideoMedia(); vm != nil && len(vm.Codecs) > 0 {
				c.mu.Lock()
				if err := c.ensureVideoRTPPort(); err != nil {
					p.logger.Warn("video RTP port allocation failed, video disabled", "error", err)
				}
				c.mu.Unlock()
			}
		}
	}

	// Auto-send 180 Ringing before presenting the call.
	if err := dlg.Respond(180, "Ringing", nil); err != nil {
		p.logger.Error("failed to send 180 Ringing", "err", err)
	}

	p.mu.Lock()
	fns := make([]func(Call), len(p.incomingFns))
	copy(fns, p.incomingFns)
	p.mu.Unlock()
	for _, fn := range fns {
		fn(c)
	}
}

// handleDialogReInvite is called by sipUA's OnInvite handler when an INVITE
// arrives with a Call-ID that may match an existing call. Returns true if the
// re-INVITE was handled (existing call found).
func (p *phone) handleDialogReInvite(callID string, responder reInviteResponder, sdpBody string) bool {
	c := p.findCall(callID)
	if c == nil {
		return false // not a re-INVITE — fall through to new call handling
	}
	c.handleReInvite(responder, sdpBody)
	return true
}

// handleDialogBye is called by sipUA's OnBye handler when a BYE is received
// for a server-side dialog. It looks up the call by Call-ID and fires simulateBye.
func (p *phone) handleDialogBye(callID string) {
	c := p.findCall(callID)
	if c != nil {
		c.simulateBye()
	}
}

// handleDialogCancel is called by sipUA's OnCancel handler when a CANCEL is received
// for a ringing inbound call. It ends the call with EndedByCancelled.
func (p *phone) handleDialogCancel(callID string) {
	c := p.findCall(callID)
	if c != nil {
		c.simulateCancel()
	}
}

// handleDialogNotify is called by sipUA's OnNotify handler when a NOTIFY is received.
// It looks up the call by Call-ID and fires the dialog's OnNotify callback.
func (p *phone) handleDialogNotify(callID string, code int) {
	c := p.findCall(callID)
	if c != nil {
		c.dlg.FireNotify(code)
	}
}

// handleDialogInfo is called by sipUA's OnInfo handler when a SIP INFO with
// application/dtmf-relay is received. It fires the call's DTMF callbacks.
func (p *phone) handleDialogInfo(callID string, digit string) {
	c := p.findCall(callID)
	if c == nil {
		return
	}
	c.mu.Lock()
	mode := c.dtmfMode
	c.mu.Unlock()
	if mode != DtmfSipInfo && mode != DtmfBoth {
		return
	}
	c.fireOnDTMF(digit)
}

// handleIncoming is called by the mock transport when an incoming INVITE arrives.
// It auto-sends 100 Trying via the transport, creates an inbound call, fires
// OnIncoming, and auto-sends 180 Ringing via the transport.
func (p *phone) handleIncoming(from, to string) {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	tr.Respond(100, "Trying")

	dlg := newPhoneDialog()
	c := newInboundCall(dlg)
	c.logger = p.logger
	c.sipHost = p.cfg.Host
	c.localIP = p.localIP
	c.rtpPortMin = p.cfg.RTPPortMin
	c.rtpPortMax = p.cfg.RTPPortMax
	p.applyCallConfig(c)
	p.registerCall(c)

	p.mu.Lock()
	incomingFns := make([]func(Call), len(p.incomingFns))
	copy(incomingFns, p.incomingFns)
	p.mu.Unlock()

	p.logger.Info("incoming call", "from", from, "to", to)
	for _, fn := range incomingFns {
		fn(c)
	}

	tr.Respond(180, "Ringing")
}

func (p *phone) OnIncoming(fn func(Call)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.incomingFns = append(p.incomingFns, fn)
}

func (p *phone) OnRegistered(fn func()) {
	p.mu.Lock()
	p.onRegisteredFns = append(p.onRegisteredFns, fn)
	reg := p.reg
	fns := make([]func(), len(p.onRegisteredFns))
	copy(fns, p.onRegisteredFns)
	p.mu.Unlock()
	if reg != nil {
		reg.OnRegistered(func() {
			for _, f := range fns {
				f()
			}
		})
	}
}

func (p *phone) OnUnregistered(fn func()) {
	p.mu.Lock()
	p.onUnregisteredFns = append(p.onUnregisteredFns, fn)
	reg := p.reg
	fns := make([]func(), len(p.onUnregisteredFns))
	copy(fns, p.onUnregisteredFns)
	p.mu.Unlock()
	if reg != nil {
		reg.OnUnregistered(func() {
			for _, f := range fns {
				f()
			}
		})
	}
}

func (p *phone) OnError(fn func(error)) {
	p.mu.Lock()
	p.onErrorFns = append(p.onErrorFns, fn)
	reg := p.reg
	fns := make([]func(error), len(p.onErrorFns))
	copy(fns, p.onErrorFns)
	p.mu.Unlock()
	if reg != nil {
		reg.OnError(func(err error) {
			for _, f := range fns {
				f(err)
			}
		})
	}
}

func (p *phone) OnCallState(fn func(Call, CallState)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCallStateFns = append(p.onCallStateFns, fn)
}

func (p *phone) OnCallEnded(fn func(Call, EndReason)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCallEndedFns = append(p.onCallEndedFns, fn)
}

func (p *phone) OnCallDTMF(fn func(Call, string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCallDTMFFns = append(p.onCallDTMFFns, fn)
}

func (p *phone) OnVoicemail(fn func(VoicemailStatus)) {
	p.mu.Lock()
	p.onVoicemailFns = append(p.onVoicemailFns, fn)
	mwi := p.mwi
	fns := make([]func(VoicemailStatus), len(p.onVoicemailFns))
	copy(fns, p.onVoicemailFns)
	p.mu.Unlock()
	// If already subscribed, apply the composed callback immediately.
	if mwi != nil {
		mwi.setOnVoicemail(func(status VoicemailStatus) {
			for _, f := range fns {
				f(status)
			}
		})
	}
}

func (p *phone) OnMessage(fn func(SipMessage)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onMessageFns = append(p.onMessageFns, fn)
}

func (p *phone) SendMessage(ctx context.Context, target string, body string) error {
	return p.SendMessageWithType(ctx, target, contentTypeTextPlain, body)
}

func (p *phone) SendMessageWithType(ctx context.Context, target string, contentType string, body string) error {
	p.mu.Lock()
	tr := p.tr
	state := p.state
	registrarless := p.cfg.Registrarless
	p.mu.Unlock()
	if tr == nil {
		return ErrNotConnected
	}
	// Positive gate, mirroring Dial (#101): Registered, or — with the
	// explicit registrarless opt-in — a prepared-but-unregistered transport.
	// Transitional states (Registering/Unregistering) are rejected.
	switch {
	case state == PhoneStateRegistered:
	case registrarless && (state == PhoneStatePrepared || state == PhoneStateRegistrationFailed):
	default:
		return ErrNotRegistered
	}
	code, reason, err := tr.SendMessage(ctx, target, contentType, body, nil)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return fmt.Errorf("MESSAGE %d %s", code, reason)
	}
	return nil
}

func (p *phone) Watch(ctx context.Context, extension string, fn func(string, ExtensionState, ExtensionState)) (string, error) {
	p.mu.Lock()
	sm := p.subMgr
	p.mu.Unlock()
	if sm == nil {
		return "", ErrNotConnected
	}
	return sm.watch(ctx, extension, fn)
}

func (p *phone) Unwatch(subscriptionID string) error {
	p.mu.Lock()
	sm := p.subMgr
	p.mu.Unlock()
	if sm == nil {
		return ErrNotConnected
	}
	return sm.unwatch(subscriptionID)
}

func (p *phone) SubscribeEvent(ctx context.Context, uri string, event string, expires int, fn func(NotifyEvent)) (string, error) {
	p.mu.Lock()
	sm := p.subMgr
	p.mu.Unlock()
	if sm == nil {
		return "", ErrNotConnected
	}
	return sm.subscribeEvent(ctx, uri, event, expires, fn)
}

func (p *phone) UnsubscribeEvent(subscriptionID string) error {
	p.mu.Lock()
	sm := p.subMgr
	p.mu.Unlock()
	if sm == nil {
		return ErrNotConnected
	}
	return sm.unsubscribeEvent(subscriptionID)
}

func (p *phone) handleMessage(from, to, contentType, body string) {
	p.mu.Lock()
	fns := make([]func(SipMessage), len(p.onMessageFns))
	copy(fns, p.onMessageFns)
	p.mu.Unlock()
	msg := SipMessage{From: from, To: to, ContentType: contentType, Body: body}
	for _, fn := range fns {
		fn(msg)
	}
}

func (p *phone) State() PhoneState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// phoneDialog is a stub dialog implementation for phone-created calls.
// It satisfies the dialog interface with basic state tracking.
// Real SIP dialog operations (backed by sipgo) will replace this in Phase B/C/D.
type phoneDialog struct {
	mu       sync.Mutex
	callID   string
	headers  map[string][]string
	onNotify func(int)

	// State tracking (for test inspection via concrete type, not interface).
	byeSent          bool
	cancelSent       bool
	referSent        bool
	infoSent         bool
	lastInfoDigit    string
	lastInfoDuration int
	lastRespCode     int
	lastRespReason   string
	lastRespBody     []byte
	lastReInviteSDP  []byte
	lastReferTarget  string
}

func newPhoneDialog() *phoneDialog {
	return &phoneDialog{
		callID:  newCallID(),
		headers: make(map[string][]string),
	}
}

func (d *phoneDialog) Respond(code int, reason string, body []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastRespCode = code
	d.lastRespReason = reason
	d.lastRespBody = body
	return nil
}

func (d *phoneDialog) SendBye() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.byeSent = true
	return nil
}

func (d *phoneDialog) SendCancel() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelSent = true
	return nil
}

func (d *phoneDialog) SendReInvite(sdp []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastReInviteSDP = sdp
	return nil
}

func (d *phoneDialog) SendRefer(target string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.referSent = true
	d.lastReferTarget = target
	return nil
}

func (d *phoneDialog) SendInfoDTMF(digit string, duration int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.infoSent = true
	d.lastInfoDigit = digit
	d.lastInfoDuration = duration
	return nil
}

func (d *phoneDialog) OnNotify(fn func(code int)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onNotify = fn
}

func (d *phoneDialog) FireNotify(code int) {
	d.mu.Lock()
	fn := d.onNotify
	d.mu.Unlock()
	if fn != nil {
		fn(code)
	}
}

func (d *phoneDialog) CallID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.callID
}

func (d *phoneDialog) Header(name string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	lower := strings.ToLower(name)
	for k, v := range d.headers {
		if strings.ToLower(k) == lower {
			cp := make([]string, len(v))
			copy(cp, v)
			return cp
		}
	}
	return nil
}

func (d *phoneDialog) Headers() map[string][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := make(map[string][]string, len(d.headers))
	for k, v := range d.headers {
		vals := make([]string, len(v))
		copy(vals, v)
		cp[k] = vals
	}
	return cp
}

// Ensure phoneDialog satisfies the dialog interface at compile time.
var _ dialog = (*phoneDialog)(nil)
