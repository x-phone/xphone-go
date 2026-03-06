package xphone

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/x-phone/xphone-go/internal/sdp"
)

// localIPFor discovers the local IP address used to reach the given host
// by making a connectionless UDP "dial" (no packets are sent).
// If the result is loopback (e.g. when host is 127.0.0.1 in Docker setups),
// it falls back to the outbound interface IP via a public DNS dial.
func localIPFor(host string) string {
	conn, err := net.Dial("udp", net.JoinHostPort(host, "5060"))
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	ip := conn.LocalAddr().(*net.UDPAddr).IP
	if !ip.IsLoopback() {
		return ip.String()
	}
	// Loopback detected — find a non-loopback IP via outbound interface.
	conn2, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return ip.String()
	}
	defer conn2.Close()
	return conn2.LocalAddr().(*net.UDPAddr).IP.String()
}

// resolveLogger returns l if non-nil, otherwise slog.Default().
func resolveLogger(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.Default()
}

func newCallID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// dialog is the internal interface for SIP dialog operations.
// Production: backed by sipgo DialogClientSession/DialogServerSession.
// Tests: satisfied by testutil.MockDialog.
//
// Methods that send SIP messages return error (network may fail).
// call.go logs errors but does not block state transitions on them.
type dialog interface {
	// Respond sends a SIP response (200 OK with SDP for accept, 4xx/6xx for reject).
	Respond(code int, reason string, body []byte) error
	// SendBye terminates the dialog.
	SendBye() error
	// SendCancel cancels a pending INVITE (pre-active calls).
	SendCancel() error
	// SendReInvite sends a re-INVITE with new SDP (hold/resume/refresh).
	SendReInvite(sdp []byte) error
	// SendRefer sends a REFER for blind transfer.
	SendRefer(target string) error
	// OnNotify registers a callback for NOTIFY events (REFER progress).
	OnNotify(fn func(code int))
	// CallID returns the SIP Call-ID.
	CallID() string
	// Header returns values for a SIP header (case-insensitive).
	Header(name string) []string
	// Headers returns a copy of all SIP headers.
	Headers() map[string][]string
}

// Call is the public interface for an active call.
type Call interface {
	ID() string
	DialogID() string
	CallID() string
	Direction() Direction
	RemoteURI() string
	RemoteIP() string
	RemotePort() int
	State() CallState
	Codec() Codec
	LocalSDP() string
	RemoteSDP() string
	StartTime() time.Time
	Duration() time.Duration
	Header(name string) []string
	Headers() map[string][]string
	Accept(opts ...AcceptOption) error
	Reject(code int, reason string) error
	End() error
	Hold() error
	Resume() error
	Mute() error
	Unmute() error
	SendDTMF(digit string) error
	BlindTransfer(target string) error
	RTPRawReader() <-chan *rtp.Packet
	RTPReader() <-chan *rtp.Packet
	RTPWriter() chan<- *rtp.Packet
	PCMReader() <-chan []int16
	PCMWriter() chan<- []int16
	OnDTMF(func(digit string))
	OnHold(func())
	OnResume(func())
	OnMute(func())
	OnUnmute(func())
	OnMedia(func())
	OnState(func(state CallState))
	OnEnded(func(reason EndReason))
}

// call is the concrete implementation of Call.
type call struct {
	mu sync.Mutex

	id        string
	dlg       dialog
	state     CallState
	direction Direction
	opts      DialOptions
	startTime time.Time
	logger    *slog.Logger

	onEndedFn       func(EndReason)
	onEndedInternal func(EndReason) // internal hook for call tracking; not overwritten by OnEnded
	onMediaFn       func()
	onStateFn       func(CallState)
	onDTMFFn    func(string)
	onHoldFn    func()
	onResumeFn  func()
	onMuteFn    func()
	onUnmuteFn  func()

	muted bool

	sipHost    string         // SIP server host (for local IP detection)
	localIP    string         // cached local IP (set by ensureRTPPort)
	rtpPort    int            // allocated RTP port for SDP
	rtpPortMin int            // minimum RTP port (0 = OS-assigned)
	rtpPortMax int            // maximum RTP port (0 = OS-assigned)
	rtpConn    net.PacketConn // bound UDP socket to keep port reserved
	remoteAddr net.Addr       // remote RTP endpoint (from remote SDP)

	localSDP  string
	remoteSDP string

	codec        Codec // negotiated codec (default CodecPCMU)
	sessionTimer *time.Timer
	mediaActive  bool
	mediaTimeout time.Duration
	mediaDone    chan struct{}
	rtpInbound   chan *rtp.Packet
	rtpReader    chan *rtp.Packet
	rtpRawReader chan *rtp.Packet
	rtpWriter    chan *rtp.Packet
	pcmReader    chan []int16
	pcmWriter    chan []int16
	sentRTP      chan *rtp.Packet // test hook: outbound packets copied here
}

func newInboundCall(d dialog) *call {
	return &call{
		id:           newCallID(),
		dlg:          d,
		state:        StateRinging,
		direction:    DirectionInbound,
		logger:       resolveLogger(nil),
		rtpInbound:   make(chan *rtp.Packet, 256),
		rtpReader:    make(chan *rtp.Packet, 256),
		rtpRawReader: make(chan *rtp.Packet, 256),
		rtpWriter:    make(chan *rtp.Packet, 256),
		pcmReader:    make(chan []int16, 256),
		pcmWriter:    make(chan []int16, 256),
	}
}

func newOutboundCall(d dialog, dialOpts ...DialOption) *call {
	opts := applyDialOptions(dialOpts)
	return &call{
		id:           newCallID(),
		dlg:          d,
		state:        StateDialing,
		direction:    DirectionOutbound,
		opts:         opts,
		logger:       resolveLogger(nil),
		rtpInbound:   make(chan *rtp.Packet, 256),
		rtpReader:    make(chan *rtp.Packet, 256),
		rtpRawReader: make(chan *rtp.Packet, 256),
		rtpWriter:    make(chan *rtp.Packet, 256),
		pcmReader:    make(chan []int16, 256),
		pcmWriter:    make(chan []int16, 256),
	}
}

func (c *call) ID() string { return c.id }

func (c *call) DialogID() string { return c.dlg.CallID() }

func (c *call) CallID() string { return c.dlg.CallID() }

func (c *call) Direction() Direction {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.direction
}

func (c *call) RemoteURI() string {
	vals := c.dlg.Header("From")
	if len(vals) == 0 {
		return ""
	}
	v := vals[0]
	start := strings.Index(v, "<")
	end := strings.Index(v, ">")
	if start >= 0 && end > start {
		return v[start+1 : end]
	}
	return v
}

// remoteSession parses the remote SDP under lock and returns the session.
// Returns nil if no remote SDP is set or parsing fails.
func (c *call) remoteSession() *sdp.Session {
	c.mu.Lock()
	raw := c.remoteSDP
	c.mu.Unlock()
	if raw == "" {
		return nil
	}
	sess, err := sdp.Parse(raw)
	if err != nil {
		return nil
	}
	return sess
}

func (c *call) RemoteIP() string {
	sess := c.remoteSession()
	if sess == nil {
		return ""
	}
	return sess.Connection
}

func (c *call) RemotePort() int {
	sess := c.remoteSession()
	if sess == nil {
		return 0
	}
	if len(sess.Media) > 0 {
		return sess.Media[0].Port
	}
	return 0
}

func (c *call) State() CallState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *call) Codec() Codec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.codec
}

func (c *call) setCodec(codec Codec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.codec = codec
}
func (c *call) LocalSDP() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.localSDP
}

func (c *call) RemoteSDP() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remoteSDP
}

func (c *call) StartTime() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startTime
}

func (c *call) Duration() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.startTime.IsZero() {
		return 0
	}
	return time.Since(c.startTime)
}

func (c *call) Header(name string) []string {
	return c.dlg.Header(name)
}

func (c *call) Headers() map[string][]string {
	return c.dlg.Headers()
}

func defaultCodecPrefs() []int {
	return []int{8, 0, 9, 101, 111}
}

// remoteAddrFromSession extracts the remote RTP endpoint (IP:port) from a parsed SDP session.
func remoteAddrFromSession(sess *sdp.Session) net.Addr {
	if len(sess.Media) == 0 {
		return nil
	}
	ip := sess.Connection
	port := sess.Media[0].Port
	if ip == "" || port <= 0 {
		return nil
	}
	addr, _ := net.ResolveUDPAddr("udp", net.JoinHostPort(ip, strconv.Itoa(port)))
	return addr
}

// parseRemoteAddr extracts the remote RTP endpoint (IP:port) from a raw SDP string.
func parseRemoteAddr(rawSDP string) net.Addr {
	sess, err := sdp.Parse(rawSDP)
	if err != nil {
		return nil
	}
	return remoteAddrFromSession(sess)
}

// listenRTPPort allocates a UDP socket for RTP. If min/max are both > 0,
// it tries even ports in that range; otherwise it uses an OS-assigned port.
func listenRTPPort(min, max int) (net.PacketConn, error) {
	if min > 0 && max > 0 {
		for p := min; p <= max; p++ {
			if p%2 != 0 {
				continue // RTP uses even ports
			}
			conn, err := net.ListenPacket("udp", fmt.Sprintf(":%d", p))
			if err == nil {
				return conn, nil
			}
		}
		return nil, fmt.Errorf("xphone: no available RTP port in range %d-%d", min, max)
	}
	return net.ListenPacket("udp", ":0")
}

// ensureRTPPort lazily allocates a UDP socket and caches the local IP and
// RTP port. Must be called with c.mu held.
func (c *call) ensureRTPPort() {
	if c.localIP == "" {
		c.localIP = localIPFor(c.sipHost)
	}
	if c.rtpPort == 0 {
		conn, err := listenRTPPort(c.rtpPortMin, c.rtpPortMax)
		if err == nil {
			c.rtpPort = conn.LocalAddr().(*net.UDPAddr).Port
			c.rtpConn = conn
		}
	}
}

// buildLocalSDP creates an SDP offer with the call's allocated RTP address/port.
// Must be called with c.mu held.
func (c *call) buildLocalSDP(direction string) string {
	c.ensureRTPPort()
	return sdp.BuildOffer(c.localIP, c.rtpPort, defaultCodecPrefs(), direction)
}

// buildAnswerSDP creates an SDP answer that only includes codecs from the
// remote offer (RFC 3264 compliance). Must be called with c.mu held.
func (c *call) buildAnswerSDP(remote *sdp.Session, direction string) string {
	c.ensureRTPPort()
	var remoteCodecs []int
	if len(remote.Media) > 0 {
		remoteCodecs = remote.Media[0].Codecs
	}
	return sdp.BuildAnswer(c.localIP, c.rtpPort, defaultCodecPrefs(), remoteCodecs, direction)
}

// negotiateCodec updates c.codec from a parsed remote SDP session.
// Must be called with c.mu held.
func (c *call) negotiateCodec(sess *sdp.Session) {
	var remoteCodecs []int
	if len(sess.Media) > 0 {
		remoteCodecs = sess.Media[0].Codecs
	}
	if pt := sdp.NegotiateCodec(defaultCodecPrefs(), remoteCodecs); pt >= 0 {
		c.codec = Codec(pt)
	}
}

func (c *call) startSessionTimer() {
	vals := c.dlg.Header("Session-Expires")
	if len(vals) == 0 {
		return
	}
	seconds, err := strconv.Atoi(vals[0])
	if err != nil || seconds <= 0 {
		return
	}
	interval := time.Duration(seconds) * time.Second / 2
	c.sessionTimer = time.AfterFunc(interval, func() {
		c.mu.Lock()
		if c.state == StateEnded {
			c.mu.Unlock()
			return
		}
		refreshSDP := c.buildLocalSDP(sdp.DirSendRecv)
		c.mu.Unlock()
		c.dlg.SendReInvite([]byte(refreshSDP))
	})
}

// fireOnState dispatches the OnState callback outside the lock.
// Must be called with c.mu held. Copies the function pointer and fires via goroutine.
func (c *call) fireOnState(state CallState) {
	if c.onStateFn != nil {
		fn := c.onStateFn
		go fn(state)
	}
}

// fireOnEnded dispatches both the public OnEnded and internal onEndedInternal callbacks.
// Must be called with c.mu held.
func (c *call) fireOnEnded(reason EndReason) {
	// Stop media pipeline goroutine.
	if c.mediaDone != nil {
		select {
		case <-c.mediaDone:
		default:
			close(c.mediaDone)
		}
		c.mediaActive = false
	}
	// Close RTP socket (also stops the RTP reader goroutine).
	if c.rtpConn != nil {
		c.rtpConn.Close()
		c.rtpConn = nil
	}
	if c.onEndedInternal != nil {
		fn := c.onEndedInternal
		go fn(reason)
	}
	if c.onEndedFn != nil {
		fn := c.onEndedFn
		go fn(reason)
	}
}

func (c *call) Accept(opts ...AcceptOption) error {
	c.mu.Lock()
	if c.state != StateRinging {
		c.mu.Unlock()
		return ErrInvalidState
	}
	// Build SDP answer: if we have the remote offer, restrict to offered codecs.
	if c.remoteSDP != "" {
		if sess, err := sdp.Parse(c.remoteSDP); err == nil {
			c.negotiateCodec(sess)
			c.localSDP = c.buildAnswerSDP(sess, sdp.DirSendRecv)
			c.remoteAddr = remoteAddrFromSession(sess)
		} else {
			c.localSDP = c.buildLocalSDP(sdp.DirSendRecv)
		}
	} else {
		c.localSDP = c.buildLocalSDP(sdp.DirSendRecv)
	}
	if err := c.dlg.Respond(200, "OK", []byte(c.localSDP)); err != nil {
		c.logger.Error("failed to send 200 OK", "err", err)
	}
	c.state = StateActive
	c.startTime = time.Now()
	c.startSessionTimer()
	c.fireOnState(StateActive)
	c.logger.Info("call accepted", "id", c.id)
	hasRTP := c.rtpConn != nil
	onMediaFn := c.onMediaFn
	c.mu.Unlock()

	// Start media pipeline and RTP socket I/O for production calls.
	if hasRTP {
		c.startMedia()
		c.startRTPReader()
	}
	if onMediaFn != nil {
		go onMediaFn()
	}
	return nil
}

func (c *call) Reject(code int, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateRinging {
		return ErrInvalidState
	}
	c.dlg.Respond(code, reason, nil)
	c.state = StateEnded
	c.fireOnState(StateEnded)
	c.logger.Info("call rejected", "id", c.id, "code", code, "reason", reason)
	c.fireOnEnded(EndedByRejected)
	return nil
}

func (c *call) End() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionTimer != nil {
		c.sessionTimer.Stop()
	}
	switch c.state {
	case StateDialing, StateRemoteRinging, StateEarlyMedia:
		c.dlg.SendCancel()
		c.state = StateEnded
		c.fireOnState(StateEnded)
		c.logger.Info("call ended", "id", c.id, "reason", "cancelled")
		c.fireOnEnded(EndedByCancelled)
		return nil
	case StateActive, StateOnHold:
		c.dlg.SendBye()
		c.state = StateEnded
		c.fireOnState(StateEnded)
		c.logger.Info("call ended", "id", c.id, "reason", "local")
		c.fireOnEnded(EndedByLocal)
		return nil
	case StateEnded:
		return ErrInvalidState
	default:
		return ErrInvalidState
	}
}

func (c *call) Hold() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateActive {
		return ErrInvalidState
	}
	c.localSDP = c.buildLocalSDP(sdp.DirSendOnly)
	c.dlg.SendReInvite([]byte(c.localSDP))
	c.state = StateOnHold
	c.fireOnState(StateOnHold)
	c.logger.Info("call hold", "id", c.id)
	return nil
}

func (c *call) Resume() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateOnHold {
		return ErrInvalidState
	}
	c.localSDP = c.buildLocalSDP(sdp.DirSendRecv)
	c.dlg.SendReInvite([]byte(c.localSDP))
	c.state = StateActive
	c.fireOnState(StateActive)
	c.logger.Info("call resumed", "id", c.id)
	return nil
}

func (c *call) Mute() error {
	c.mu.Lock()
	if c.state != StateActive {
		c.mu.Unlock()
		return ErrInvalidState
	}
	if c.muted {
		c.mu.Unlock()
		return ErrAlreadyMuted
	}
	c.muted = true
	fn := c.onMuteFn
	c.mu.Unlock()
	if fn != nil {
		go fn()
	}
	return nil
}

func (c *call) Unmute() error {
	c.mu.Lock()
	if c.state != StateActive {
		c.mu.Unlock()
		return ErrInvalidState
	}
	if !c.muted {
		c.mu.Unlock()
		return ErrNotMuted
	}
	c.muted = false
	fn := c.onUnmuteFn
	c.mu.Unlock()
	if fn != nil {
		go fn()
	}
	return nil
}
func (c *call) SendDTMF(digit string) error {
	c.mu.Lock()
	if c.state != StateActive {
		c.mu.Unlock()
		return ErrInvalidState
	}
	if DTMFDigitCode(digit) < 0 {
		c.mu.Unlock()
		return ErrInvalidDTMFDigit
	}
	sentRTP := c.sentRTP
	conn := c.rtpConn
	addr := c.remoteAddr
	c.mu.Unlock()

	pkts, err := EncodeDTMF(digit, 0, 0, 0)
	if err != nil {
		return err
	}
	for _, pkt := range pkts {
		if sentRTP != nil {
			sendDropOldest(sentRTP, pkt)
		}
		if conn != nil && addr != nil {
			if data, marshalErr := pkt.Marshal(); marshalErr == nil {
				conn.WriteTo(data, addr)
			}
		}
	}
	return nil
}

func (c *call) BlindTransfer(target string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateActive && c.state != StateOnHold {
		return ErrInvalidState
	}
	c.dlg.SendRefer(target)
	c.dlg.OnNotify(func(code int) {
		if code == 200 {
			c.mu.Lock()
			c.state = StateEnded
			c.fireOnState(StateEnded)
			c.fireOnEnded(EndedByTransfer)
			c.mu.Unlock()
		}
	})
	return nil
}

func (c *call) RTPRawReader() <-chan *rtp.Packet { return c.rtpRawReader }
func (c *call) RTPReader() <-chan *rtp.Packet    { return c.rtpReader }
func (c *call) RTPWriter() chan<- *rtp.Packet    { return c.rtpWriter }
func (c *call) PCMReader() <-chan []int16        { return c.pcmReader }
func (c *call) PCMWriter() chan<- []int16        { return c.pcmWriter }

func (c *call) OnDTMF(fn func(string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onDTMFFn = fn
}

func (c *call) OnHold(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onHoldFn = fn
}

func (c *call) OnResume(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onResumeFn = fn
}
func (c *call) OnMute(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMuteFn = fn
}

func (c *call) OnUnmute(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onUnmuteFn = fn
}

func (c *call) OnMedia(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMediaFn = fn
}

func (c *call) OnState(fn func(CallState)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onStateFn = fn
}

func (c *call) OnEnded(fn func(EndReason)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onEndedFn = fn
}

func (c *call) simulateResponse(code int, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case code == 180:
		if c.state == StateDialing {
			c.state = StateRemoteRinging
			c.fireOnState(StateRemoteRinging)
		}
	case code == 183:
		if c.opts.EarlyMedia {
			if c.state == StateDialing || c.state == StateRemoteRinging {
				c.state = StateEarlyMedia
				c.mediaActive = true
				c.fireOnState(StateEarlyMedia)
				if c.onMediaFn != nil {
					fn := c.onMediaFn
					go fn()
				}
			}
		}
		// Without EarlyMedia option, 183 is ignored for state transition
	case code == 200:
		if c.state == StateDialing || c.state == StateRemoteRinging || c.state == StateEarlyMedia {
			c.state = StateActive
			c.startTime = time.Now()
			c.mediaActive = true
			c.fireOnState(StateActive)
			if c.onMediaFn != nil {
				fn := c.onMediaFn
				go fn()
			}
		}
	}
}

func (c *call) simulateBye() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = StateEnded
	c.fireOnState(StateEnded)
	c.fireOnEnded(EndedByRemote)
}

func (c *call) simulateReInvite(rawSDP string) {
	c.mu.Lock()
	if c.state == StateEnded {
		c.mu.Unlock()
		return
	}

	sess, err := sdp.Parse(rawSDP)
	if err != nil {
		c.mu.Unlock()
		return
	}
	c.remoteSDP = rawSDP

	dir := sess.Dir()
	var holdFn, resumeFn, stateFn func()

	switch {
	case dir == sdp.DirSendOnly && c.state == StateActive:
		c.state = StateOnHold
		holdFn = c.onHoldFn
		if c.onStateFn != nil {
			onState := c.onStateFn
			stateFn = func() { onState(StateOnHold) }
		}
	case dir == sdp.DirSendRecv && c.state == StateOnHold:
		c.state = StateActive
		resumeFn = c.onResumeFn
		if c.onStateFn != nil {
			onState := c.onStateFn
			stateFn = func() { onState(StateActive) }
		}
	}

	c.negotiateCodec(sess)

	c.mu.Unlock()

	if stateFn != nil {
		go stateFn()
	}
	if holdFn != nil {
		go holdFn()
	}
	if resumeFn != nil {
		go resumeFn()
	}
}

func (c *call) mediaSessionActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mediaActive
}

// injectRTP is a test hook that feeds an RTP packet into the call's media
// pipeline as if it arrived from the network.
func (c *call) injectRTP(pkt *rtp.Packet) {
	c.rtpInbound <- pkt
}
