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
	Connect(ctx context.Context) error
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

	cfg        Config
	logger     *slog.Logger
	tr         sipTransport
	reg        *registry
	localIP    string // cached localIPFor(cfg.Host), or STUN-mapped IP after Connect
	hostIP     string // real local interface IP (pre-STUN, for ICE host candidate)
	codecPrefs []int  // cached codecPrefsToInts(cfg.CodecPrefs), set at construction
	state      PhoneState
	incoming   func(Call)
	calls      map[string]*call // active calls keyed by SIP Call-ID

	// dialFn establishes an outbound SIP dialog and returns a dialog interface.
	// Production: set by Connect() to use sipgo's dialog API.
	// Tests: set by connectWithTransport() to use the mock transport.
	// The onResponse callback is invoked for each provisional/final SIP response.
	dialFn func(ctx context.Context, target string, onResponse func(code int, reason string)) (dialog, error)

	// Buffered callbacks set before Connect.
	onRegisteredFn   func()
	onUnregisteredFn func()
	onErrorFn        func(error)

	// Phone-level call callbacks — auto-wired to every new call.
	onCallStateFn func(Call, CallState)
	onCallEndedFn func(Call, EndReason)
	onCallDTMFFn  func(Call, string)

	// SIP MESSAGE callback.
	onMessageFn func(SipMessage)

	// MWI (voicemail) state.
	onVoicemailFn func(VoicemailStatus)
	mwi           *mwiSubscriber

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

func newPhone(cfg Config) *phone {
	hostIP := localIPFor(cfg.Host)
	return &phone{
		cfg:        cfg,
		logger:     resolveLogger(cfg.Logger),
		localIP:    hostIP,
		hostIP:     hostIP,
		codecPrefs: codecPrefsToInts(cfg.CodecPrefs),
		state:      PhoneStateDisconnected,
		calls:      make(map[string]*call),
	}
}

// wireCallCallbacks hooks phone-level call callbacks (OnCallState, OnCallEnded,
// OnCallDTMF) onto a call's internal callback fields so they coexist with
// user-set per-call callbacks. Must be called with p.mu held.
func (p *phone) wireCallCallbacks(c *call) {
	if p.onCallStateFn != nil {
		fn := p.onCallStateFn
		c.onStatePhone = func(state CallState) { fn(c, state) }
	}
	if p.onCallEndedFn != nil {
		fn := p.onCallEndedFn
		c.onEndedPhone = func(reason EndReason) { fn(c, reason) }
	}
	if p.onCallDTMFFn != nil {
		fn := p.onCallDTMFFn
		c.onDTMFPhone = func(digit string) { fn(c, digit) }
	}
}

// setupSRTP initializes SRTP contexts on a call.
// localKey is the base64 inline key for outbound encryption.
// remoteKey is the base64 inline key from the remote SDP for inbound decryption.
func (p *phone) setupSRTP(c *call, localKey, remoteKey string) {
	outCtx, err := srtp.FromSDESInline(localKey)
	if err != nil {
		p.logger.Error("SRTP outbound context setup failed", "err", err)
		return
	}
	inCtx, err := srtp.FromSDESInline(remoteKey)
	if err != nil {
		p.logger.Error("SRTP inbound context setup failed", "err", err)
		return
	}
	c.mu.Lock()
	c.srtpLocalKey = localKey
	c.srtpOut = outCtx
	c.srtpIn = inCtx
	c.mu.Unlock()
	p.logger.Info("SRTP enabled for call", "id", c.id)
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
func (p *phone) transportDial(ctx context.Context, target string, onResponse func(code int, reason string)) (dialog, error) {
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
// incoming INVITE handler on the transport.
func (p *phone) connectWithTransport(tr sipTransport) {
	p.mu.Lock()
	p.tr = tr
	if p.dialFn == nil {
		p.dialFn = p.transportDial
	}
	p.reg = newRegistry(tr, p.cfg)
	// Apply buffered callbacks to the registry.
	if p.onRegisteredFn != nil {
		p.reg.OnRegistered(p.onRegisteredFn)
	}
	if p.onUnregisteredFn != nil {
		p.reg.OnUnregistered(p.onUnregisteredFn)
	}
	if p.onErrorFn != nil {
		p.reg.OnError(p.onErrorFn)
	}
	p.mu.Unlock()

	// Perform registration (consumes the pre-queued 200 OK in tests).
	err := p.reg.Start(context.Background())

	// Wire up incoming INVITE and MESSAGE handling on the transport.
	tr.OnIncoming(p.handleIncoming)
	tr.OnMessage(p.handleMessage)

	p.mu.Lock()
	if err == nil {
		p.state = PhoneStateRegistered
	} else {
		p.state = PhoneStateRegistrationFailed
	}
	voicemailURI := p.cfg.VoicemailURI
	voicemailFn := p.onVoicemailFn
	p.mu.Unlock()

	// Auto-start MWI subscription if configured and registration succeeded.
	if err == nil && voicemailURI != "" {
		sub := newMWISubscriber(tr, voicemailURI, p.logger)
		if voicemailFn != nil {
			sub.setOnVoicemail(voicemailFn)
		}
		sub.start()
		p.mu.Lock()
		p.mwi = sub
		p.mu.Unlock()
	}

	// Start subscription manager for Watch/SubscribeEvent.
	if err == nil {
		sm := newSubscriptionManager(tr, p.cfg.Host, p.logger)
		sm.start()
		p.mu.Lock()
		p.subMgr = sm
		p.mu.Unlock()
	}

	p.logger.Info("phone connected")
}

func (p *phone) Connect(ctx context.Context) error {
	p.mu.Lock()
	if p.state != PhoneStateDisconnected {
		p.mu.Unlock()
		return ErrAlreadyConnected
	}
	p.state = PhoneStateRegistering

	// STUN discovery: if configured, try to discover our NAT-mapped IP.
	// On success, override the localIP used for Contact headers and SDP.
	// On failure, log a warning and keep the existing localIPFor() result.
	if p.cfg.StunServer != "" {
		ip, _, err := stun.MappedAddr(p.cfg.StunServer, stun.DefaultTimeout)
		if err != nil {
			p.logger.Warn("STUN discovery failed, using local IP", "err", err, "fallback", p.localIP)
		} else {
			p.logger.Info("STUN mapped address discovered", "ip", ip)
			p.localIP = ip
		}
	}

	contactIP := p.localIP
	p.mu.Unlock()

	tr, err := newSipUA(p.cfg, contactIP)
	if err != nil {
		p.mu.Lock()
		p.state = PhoneStateDisconnected
		p.mu.Unlock()
		return err
	}

	// Set dialFn to use sipgo's dialog API before connecting.
	p.mu.Lock()
	p.dialFn = tr.dial
	p.mu.Unlock()

	// Wire inbound INVITE, BYE, and NOTIFY handlers for sipgo path.
	tr.mu.Lock()
	tr.onDialogInvite = p.handleDialogInvite
	tr.onDialogBye = p.handleDialogBye
	tr.onDialogCancel = p.handleDialogCancel
	tr.onDialogNotify = p.handleDialogNotify
	tr.onDialogInfo = p.handleDialogInfo
	tr.mu.Unlock()
	tr.startServer()

	p.connectWithTransport(tr)
	return nil
}

func (p *phone) Disconnect() error {
	p.mu.Lock()
	if p.state == PhoneStateDisconnected {
		p.mu.Unlock()
		return ErrNotConnected
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
	fn := p.onUnregisteredFn

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

	// Stop the registry (cancels refresh loop and re-registration goroutines).
	if reg != nil {
		reg.Stop()
	}

	// Close the transport.
	if tr != nil {
		tr.Close()
	}

	p.logger.Info("phone disconnected")

	// Fire OnUnregistered callback.
	if fn != nil {
		go fn()
	}

	return nil
}

// Dial initiates an outbound call to the given SIP target.
// It sends an INVITE, waits for provisional (1xx) and final (2xx) responses,
// and returns the active Call. Honors both context cancellation and DialTimeout.
func (p *phone) Dial(ctx context.Context, target string, opts ...DialOption) (Call, error) {
	p.mu.Lock()
	if p.state != PhoneStateRegistered {
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
	dlg, err := dialFn(dialCtx, target, func(code int, reason string) {
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
// Call A's dialog sends a REFER with a Replaces header built from Call B's
// dialog identifiers. On NOTIFY 200, both calls end with EndedByTransfer.
// Both calls must be Active or OnHold.
func (p *phone) AttendedTransfer(callA Call, callB Call) error {
	// Validate states.
	if s := callA.State(); s != StateActive && s != StateOnHold {
		return ErrInvalidState
	}
	if s := callB.State(); s != StateActive && s != StateOnHold {
		return ErrInvalidState
	}

	a, ok := callA.(*call)
	if !ok {
		return fmt.Errorf("xphone: callA is not an *call")
	}
	b, ok := callB.(*call)
	if !ok {
		return fmt.Errorf("xphone: callB is not an *call")
	}

	// Extract Call B's dialog identifiers for the Replaces header.
	bCallID, bLocalTag, bRemoteTag := b.dialogID()
	if bCallID == "" || bLocalTag == "" || bRemoteTag == "" {
		return fmt.Errorf("xphone: attended transfer: call B dialog missing call-id or tags")
	}

	// Build remote party URI from Call B's headers.
	var remoteURI string
	if callB.Direction() == DirectionOutbound {
		if vals := b.dlg.Header("To"); len(vals) > 0 {
			remoteURI = sipHeaderURI(vals[0])
		}
	} else {
		if vals := b.dlg.Header("From"); len(vals) > 0 {
			remoteURI = sipHeaderURI(vals[0])
		}
	}
	if remoteURI == "" {
		return fmt.Errorf("xphone: attended transfer: cannot determine call B remote URI")
	}

	// Build Refer-To with Replaces parameter (RFC 3891).
	referTo := remoteURI + "?Replaces=" +
		uriEncode(bCallID) + "%3Bto-tag%3D" +
		uriEncode(bRemoteTag) + "%3Bfrom-tag%3D" +
		uriEncode(bLocalTag)

	// Send REFER, then wire NOTIFY handler on success.
	if err := a.dlg.SendRefer(referTo); err != nil {
		return err
	}

	// Wire NOTIFY handler: on 200, end both calls with Transfer reason.
	a.dlg.OnNotify(func(code int) {
		if code == 200 {
			a.endWithReason(EndedByTransfer)
			b.endWithReason(EndedByTransfer)
		}
	})

	return nil
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
	p.registerCall(c)

	p.logger.Info("incoming call", "from", from, "to", to)
	if sdpBody != "" {
		p.logger.Debug("incoming remote SDP", "sdp", sdpBody)
	}

	// Set up SRTP if remote offers it and we have SRTP enabled.
	if p.cfg.SRTP && sdpBody != "" {
		if sess, err := sdp.Parse(sdpBody); err == nil && sess.IsSRTP() {
			if crypto := sess.FirstCrypto(); crypto != nil {
				localKey, err := srtp.GenerateKeyingMaterial()
				if err == nil {
					p.setupSRTP(c, localKey, crypto.InlineKey())
				}
			}
		}
	}

	// Auto-send 180 Ringing before presenting the call.
	if err := dlg.Respond(180, "Ringing", nil); err != nil {
		p.logger.Error("failed to send 180 Ringing", "err", err)
	}

	p.mu.Lock()
	fn := p.incoming
	p.mu.Unlock()
	if fn != nil {
		fn(c)
	}
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
	p.applyCallConfig(c)
	p.registerCall(c)

	p.mu.Lock()
	fn := p.incoming
	p.mu.Unlock()

	p.logger.Info("incoming call", "from", from, "to", to)
	if fn != nil {
		fn(c)
	}

	tr.Respond(180, "Ringing")
}

func (p *phone) OnIncoming(fn func(Call)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.incoming = fn
}

func (p *phone) OnRegistered(fn func()) {
	p.mu.Lock()
	p.onRegisteredFn = fn
	reg := p.reg
	p.mu.Unlock()
	if reg != nil {
		reg.OnRegistered(fn)
	}
}

func (p *phone) OnUnregistered(fn func()) {
	p.mu.Lock()
	p.onUnregisteredFn = fn
	reg := p.reg
	p.mu.Unlock()
	if reg != nil {
		reg.OnUnregistered(fn)
	}
}

func (p *phone) OnError(fn func(error)) {
	p.mu.Lock()
	p.onErrorFn = fn
	reg := p.reg
	p.mu.Unlock()
	if reg != nil {
		reg.OnError(fn)
	}
}

func (p *phone) OnCallState(fn func(Call, CallState)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCallStateFn = fn
}

func (p *phone) OnCallEnded(fn func(Call, EndReason)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCallEndedFn = fn
}

func (p *phone) OnCallDTMF(fn func(Call, string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCallDTMFFn = fn
}

func (p *phone) OnVoicemail(fn func(VoicemailStatus)) {
	p.mu.Lock()
	p.onVoicemailFn = fn
	mwi := p.mwi
	p.mu.Unlock()
	// If already subscribed, apply the callback immediately.
	if mwi != nil {
		mwi.setOnVoicemail(fn)
	}
}

func (p *phone) OnMessage(fn func(SipMessage)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onMessageFn = fn
}

func (p *phone) SendMessage(ctx context.Context, target string, body string) error {
	return p.SendMessageWithType(ctx, target, contentTypeTextPlain, body)
}

func (p *phone) SendMessageWithType(ctx context.Context, target string, contentType string, body string) error {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	if tr == nil {
		return ErrNotConnected
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
	fn := p.onMessageFn
	p.mu.Unlock()
	if fn != nil {
		fn(SipMessage{From: from, To: to, ContentType: contentType, Body: body})
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
