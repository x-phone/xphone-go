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
	"github.com/x-phone/xphone-go/ice"
	"github.com/x-phone/xphone-go/internal/rtcp"
	"github.com/x-phone/xphone-go/internal/sdp"
	"github.com/x-phone/xphone-go/internal/srtp"
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
	return fmt.Sprintf("CA%x", b)
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
	// SendInfoDTMF sends a SIP INFO request with application/dtmf-relay body.
	SendInfoDTMF(digit string, duration int) error
	// OnNotify registers a callback for NOTIFY events (REFER progress).
	OnNotify(fn func(code int))
	// FireNotify fires the on_notify callback with the given status code.
	FireNotify(code int)
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
	CallID() string
	Direction() Direction
	From() string
	To() string
	FromName() string
	RemoteURI() string
	RemoteDID() string
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
	MediaSessionActive() bool
	Hold() error
	Resume() error
	Mute() error
	Unmute() error
	MuteAudio() error
	UnmuteAudio() error
	SendDTMF(digit string) error
	BlindTransfer(target string) error
	AttendedTransfer(other Call) error
	RTPRawReader() <-chan *rtp.Packet
	RTPReader() <-chan *rtp.Packet
	RTPWriter() chan<- *rtp.Packet
	PCMReader() <-chan []int16
	PCMWriter() chan<- []int16
	PacedPCMWriter() chan<- []int16
	ReplaceAudioWriter(newSrc <-chan []int16) error
	OnDTMF(func(digit string))
	OnHold(func())
	OnResume(func())
	OnMute(func())
	OnUnmute(func())
	HasVideo() bool
	VideoCodec() VideoCodec
	VideoReader() <-chan VideoFrame
	VideoWriter() chan<- VideoFrame
	VideoRTPReader() <-chan *rtp.Packet
	VideoRTPWriter() chan<- *rtp.Packet
	MuteVideo() error
	UnmuteVideo() error
	RequestKeyframe() error
	AddVideo(codecs ...VideoCodec) error
	OnVideoRequest(func(*VideoUpgradeRequest))
	OnVideo(func())
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

	onEndedFns          []func(EndReason)
	onEndedCleanup      func(EndReason) // internal: call tracking (untrackCall)
	onStatePhone        func(CallState) // internal: phone-level OnCallState
	onEndedPhone        func(EndReason) // internal: phone-level OnCallEnded
	onDTMFPhone         func(string)    // internal: phone-level OnCallDTMF
	onMediaFns          []func()
	onStateFns          []func(CallState)
	onDTMFFns           []func(string)
	onHoldFns           []func()
	onResumeFns         []func()
	onMuteFns           []func()
	onUnmuteFns         []func()
	onVideoRequestFns   []func(*VideoUpgradeRequest)
	onVideoFns          []func()
	pendingVideoUpgrade *VideoUpgradeRequest

	audioStream    *mediaStream
	videoStream    *mediaStream
	dtmfMode       DtmfMode
	hasVideo       bool
	videoCodecType VideoCodec

	codecPrefs  []int          // codec preference order (payload types)
	jitterDepth time.Duration  // jitter buffer depth (0 = default)
	pcmRate     int            // PCM sample rate (0 = default 8000)
	sipHost     string         // SIP server host (for local IP detection)
	localIP     string         // cached local IP (set by ensureRTPPort)
	rtpPort     int            // allocated RTP port for SDP
	rtpPortMin  int            // minimum RTP port (0 = OS-assigned)
	rtpPortMax  int            // maximum RTP port (0 = OS-assigned)
	rtpConn     net.PacketConn // bound UDP socket to keep port reserved
	rtcpConn    net.PacketConn // RTCP socket (RTP port + 1), nil if bind fails
	remoteAddr  net.Addr       // remote RTP endpoint (from remote SDP)
	remoteIP    string         // cached remote IP (from remote SDP)
	remotePort  int            // cached remote port (from remote SDP)

	localSDP  string
	remoteSDP string

	srtpLocalKey string        // base64 inline key for outbound encryption (non-empty = SRTP active)
	srtpIn       *srtp.Context // inbound SRTP decryption context (audio)
	srtpOut      *srtp.Context // outbound SRTP encryption context (audio)
	videoSrtpIn  *srtp.Context // inbound SRTP decryption context (video — separate to avoid concurrent state corruption)
	videoSrtpOut *srtp.Context // outbound SRTP encryption context (video)

	iceEnabled bool       // ICE-Lite enabled for this call
	iceAgent   *ice.Agent // ICE-Lite agent (nil if ICE disabled)
	hostIP     string     // real local interface IP (for ICE host candidate)

	codec           Codec // negotiated codec (default CodecPCMU)
	sessionTimer    *time.Timer
	mediaActive     bool
	mediaTimeout    time.Duration
	mediaDone       chan struct{}
	rtpInbound      chan *rtp.Packet
	rtpReader       chan *rtp.Packet
	rtpRawReader    chan *rtp.Packet
	rtpWriter       chan *rtp.Packet
	pcmReader       chan []int16
	pcmWriter       chan []int16
	pacedPCMWriter  chan []int16       // paced PCM: accepts arbitrary-length buffers, auto-framed at 20ms
	audioSrc        <-chan []int16     // swappable audio input (protected by mu); nil = paused
	audioSwap       chan struct{}      // signals media goroutine to re-read audioSrc
	sentRTP         chan *rtp.Packet   // test hook: outbound packets copied here
	mediaTimerReset chan time.Duration // signals the media goroutine to reset the timeout
	callbackCh      chan func()        // single dispatch goroutine for all callbacks
	callbackOnce    sync.Once          // ensures callbackCh is closed exactly once
	closeChOnce     sync.Once          // ensures audio output channels are closed exactly once

	// Video pipeline state.
	videoRTPInbound   chan *rtp.Packet
	videoRTPReader    chan *rtp.Packet
	videoRTPRawReader chan *rtp.Packet
	videoRTPWriter    chan *rtp.Packet
	videoReader       chan VideoFrame
	videoWriter       chan VideoFrame
	videoSentRTP      chan *rtp.Packet // test hook: outbound video packets
	videoRTPConn      net.PacketConn   // video RTP socket
	videoRTCPConn     net.PacketConn   // video RTCP socket
	videoRTPPort      int              // allocated video RTP port
	videoRemoteAddr   net.Addr         // remote video RTP endpoint
	videoDone         chan struct{}    // signals video goroutine to stop
	videoWg           sync.WaitGroup   // waits for video goroutine to exit
	closeVideoChOnce  sync.Once        // ensures video output channels are closed exactly once
}

// wireCallCallbacks hooks phone/server-level callbacks (OnCallState, OnCallEnded,
// OnCallDTMF) onto a call's internal callback fields. The Call interface is
// captured via closure so the callback receives the correct public type.
func wireCallCallbacks(c *call, onState func(Call, CallState), onEnded func(Call, EndReason), onDTMF func(Call, string)) {
	if onState != nil {
		fn := onState
		c.onStatePhone = func(state CallState) { fn(c, state) }
	}
	if onEnded != nil {
		fn := onEnded
		c.onEndedPhone = func(reason EndReason) { fn(c, reason) }
	}
	if onDTMF != nil {
		fn := onDTMF
		c.onDTMFPhone = func(digit string) { fn(c, digit) }
	}
}

// setupSRTP initializes SRTP contexts on a call.
// Audio and video get separate contexts because srtp.Context has mutable state
// (ROC, sequence tracking, HMAC) that would corrupt under concurrent access.
func (c *call) setupSRTP(logger *slog.Logger, localKey, remoteKey string) {
	outCtx, err := srtp.FromSDESInline(localKey)
	if err != nil {
		logger.Error("SRTP outbound context setup failed", "err", err)
		return
	}
	inCtx, err := srtp.FromSDESInline(remoteKey)
	if err != nil {
		logger.Error("SRTP inbound context setup failed", "err", err)
		return
	}
	c.mu.Lock()
	c.srtpLocalKey = localKey
	c.srtpOut = outCtx
	c.srtpIn = inCtx
	hasVideo := c.hasVideo
	c.mu.Unlock()

	if hasVideo {
		videoOutCtx, err := srtp.FromSDESInline(localKey)
		if err != nil {
			logger.Error("SRTP video outbound context setup failed", "err", err)
			return
		}
		videoInCtx, err := srtp.FromSDESInline(remoteKey)
		if err != nil {
			logger.Error("SRTP video inbound context setup failed", "err", err)
			return
		}
		c.mu.Lock()
		c.videoSrtpOut = videoOutCtx
		c.videoSrtpIn = videoInCtx
		c.mu.Unlock()
	}
	logger.Info("SRTP enabled for call", "id", c.id)
}

func newInboundCall(d dialog) *call {
	c := &call{
		id:             newCallID(),
		dlg:            d,
		state:          StateRinging,
		direction:      DirectionInbound,
		logger:         resolveLogger(nil),
		rtpInbound:     make(chan *rtp.Packet, 256),
		rtpReader:      make(chan *rtp.Packet, 256),
		rtpRawReader:   make(chan *rtp.Packet, 256),
		rtpWriter:      make(chan *rtp.Packet, 256),
		pcmReader:      make(chan []int16, 256),
		pcmWriter:      make(chan []int16, 256),
		pacedPCMWriter: make(chan []int16, 256),
		audioSwap:      make(chan struct{}, 1),
		callbackCh:     make(chan func(), 16),
	}
	c.audioStream = &mediaStream{call: c, outSSRC: randUint32()}
	go c.runCallbackDispatcher()
	return c
}

func newOutboundCall(d dialog, dialOpts ...DialOption) *call {
	opts := applyDialOptions(dialOpts)
	c := &call{
		id:             newCallID(),
		dlg:            d,
		state:          StateDialing,
		direction:      DirectionOutbound,
		opts:           opts,
		logger:         resolveLogger(nil),
		rtpInbound:     make(chan *rtp.Packet, 256),
		rtpReader:      make(chan *rtp.Packet, 256),
		rtpRawReader:   make(chan *rtp.Packet, 256),
		rtpWriter:      make(chan *rtp.Packet, 256),
		pcmReader:      make(chan []int16, 256),
		pcmWriter:      make(chan []int16, 256),
		pacedPCMWriter: make(chan []int16, 256),
		audioSwap:      make(chan struct{}, 1),
		callbackCh:     make(chan func(), 16),
	}
	c.audioStream = &mediaStream{call: c, outSSRC: randUint32()}
	go c.runCallbackDispatcher()
	return c
}

func (c *call) ID() string { return c.id }

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
	return sipHeaderURI(vals[0])
}

// RemoteDID returns the remote party's DID/extension.
// For inbound calls this is the From user; for outbound calls it's the To user.
func (c *call) RemoteDID() string {
	if c.Direction() == DirectionInbound {
		return c.From()
	}
	return c.To()
}

func (c *call) From() string {
	vals := c.dlg.Header("From")
	if len(vals) == 0 {
		return ""
	}
	return sipHeaderUser(vals[0])
}

func (c *call) To() string {
	vals := c.dlg.Header("To")
	if len(vals) == 0 {
		return ""
	}
	return sipHeaderUser(vals[0])
}

func (c *call) FromName() string {
	vals := c.dlg.Header("From")
	if len(vals) == 0 {
		return ""
	}
	return sipHeaderDisplayName(vals[0])
}

// sipHeaderURI extracts the SIP URI from a header value.
// e.g. `"Alice" <sip:1001@host>;tag=xyz` → `sip:1001@host`
func sipHeaderURI(val string) string {
	start := strings.Index(val, "<")
	end := strings.Index(val, ">")
	if start >= 0 && end > start {
		return val[start+1 : end]
	}
	return val
}

// sipHeaderUser extracts the user part from a SIP header value.
// e.g. `"Alice" <sip:+15551234567@host>;tag=xyz` → `+15551234567`
func sipHeaderUser(val string) string {
	uri := sipHeaderURI(val)
	// Strip scheme (sip:, sips:)
	if i := strings.Index(uri, ":"); i >= 0 {
		uri = uri[i+1:]
	}
	// Strip host (@host:port;params)
	if i := strings.Index(uri, "@"); i >= 0 {
		uri = uri[:i]
	}
	return uri
}

// sipHeaderDisplayName extracts the display name from a SIP header value.
// e.g. `"Alice" <sip:1001@host>` → `Alice`
// e.g. `Alice <sip:1001@host>` → `Alice`
func sipHeaderDisplayName(val string) string {
	lt := strings.Index(val, "<")
	if lt <= 0 {
		return ""
	}
	name := strings.TrimSpace(val[:lt])
	// Strip surrounding quotes
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		name = name[1 : len(name)-1]
	}
	return name
}

// sipHeaderTag extracts the tag parameter from a SIP header value.
// e.g. `<sip:1001@host>;tag=abc123` → `abc123`
func sipHeaderTag(val string) string {
	const needle = ";tag="
	idx := -1
	for i := 0; i <= len(val)-len(needle); i++ {
		if strings.EqualFold(val[i:i+len(needle)], needle) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ""
	}
	rest := val[idx+len(needle):]
	if end := strings.IndexAny(rest, ";>,"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// uriEncode percent-encodes URI-reserved characters for SIP Replaces headers (RFC 3891).
func uriEncode(val string) string {
	var b strings.Builder
	b.Grow(len(val) * 2)
	for i := 0; i < len(val); i++ {
		switch val[i] {
		case '%':
			b.WriteString("%25")
		case '@':
			b.WriteString("%40")
		case ' ':
			b.WriteString("%20")
		case ';':
			b.WriteString("%3B")
		case '?':
			b.WriteString("%3F")
		case '&':
			b.WriteString("%26")
		case '=':
			b.WriteString("%3D")
		case '+':
			b.WriteString("%2B")
		case ':':
			b.WriteString("%3A")
		default:
			b.WriteByte(val[i])
		}
	}
	return b.String()
}

func (c *call) RemoteIP() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remoteIP != "" {
		return c.remoteIP
	}
	// Fallback: parse remoteSDP for callers that set it directly.
	if c.remoteSDP == "" {
		return ""
	}
	sess, err := sdp.Parse(c.remoteSDP)
	if err != nil {
		return ""
	}
	return sess.Connection
}

func (c *call) RemotePort() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remotePort != 0 {
		return c.remotePort
	}
	// Fallback: parse remoteSDP for callers that set it directly.
	if c.remoteSDP == "" {
		return 0
	}
	sess, err := sdp.Parse(c.remoteSDP)
	if err != nil {
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

// defaultCodecPrefs is the default codec preference order (payload types).
// Shared slice — callers must not modify.
var defaultCodecPrefs = []int{8, 0, 9, 101, 111}

// resolveCodecPrefs returns the call's codec prefs if set, otherwise the defaults.
func (c *call) resolveCodecPrefs() []int {
	if len(c.codecPrefs) > 0 {
		return c.codecPrefs
	}
	return defaultCodecPrefs
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

// setRemoteEndpoint updates remoteAddr, remoteIP, and remotePort from a parsed SDP session.
// Must be called with c.mu held.
func (c *call) setRemoteEndpoint(sess *sdp.Session) {
	c.remoteAddr = remoteAddrFromSession(sess)
	if len(sess.Media) > 0 {
		c.remoteIP = sess.Connection
		c.remotePort = sess.Media[0].Port
	}
	c.logger.Debug("remote endpoint set", "id", c.id, "remote_ip", c.remoteIP, "remote_port", c.remotePort, "remote_addr", c.remoteAddr)
}

// listenRTPPort allocates a UDP socket for RTP. If min/max are both > 0,
// it tries even ports in that range; otherwise it uses an OS-assigned port.
func listenRTPPort(min, max int) (net.PacketConn, error) {
	if min > 0 && max > 0 {
		for p := min; p <= max; p++ {
			if p%2 != 0 {
				continue // RTP uses even ports
			}
			conn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", p))
			if err == nil {
				return conn, nil
			}
		}
		return nil, fmt.Errorf("xphone: no available RTP port in range %d-%d", min, max)
	}
	return net.ListenPacket("udp4", ":0")
}

// ensureRTPPort lazily allocates a UDP socket and caches the local IP and
// RTP port. Must be called with c.mu held.
func (c *call) ensureRTPPort() error {
	if c.localIP == "" {
		c.localIP = localIPFor(c.sipHost)
	}
	if c.rtpPort == 0 {
		conn, err := listenRTPPort(c.rtpPortMin, c.rtpPortMax)
		if err != nil {
			return fmt.Errorf("allocate RTP port: %w", err)
		}
		c.rtpPort = conn.LocalAddr().(*net.UDPAddr).Port
		c.rtpConn = conn
	}
	return nil
}

// buildLocalSDP creates an SDP offer with the call's allocated RTP address/port.
// Must be called with c.mu held.
func (c *call) buildLocalSDP(direction string) string {
	if err := c.ensureRTPPort(); err != nil {
		c.logger.Error("RTP port allocation failed for SDP offer", "id", c.id, "error", err)
	}
	iceParams := c.buildICEParams()
	codecs := c.resolveCodecPrefs()

	// Determine video codecs and port for the offer.
	videoCodecs := videoCodecPrefsToInts(c.opts.VideoCodecs)
	videoPort := c.videoRTPPort

	var offer string
	if len(videoCodecs) > 0 && videoPort > 0 {
		if c.srtpLocalKey != "" && iceParams != nil {
			offer = sdp.BuildOfferVideoSRTPICE(c.localIP, c.rtpPort, codecs, videoPort, videoCodecs, direction, c.srtpLocalKey, iceParams)
		} else if c.srtpLocalKey != "" {
			offer = sdp.BuildOfferVideoSRTP(c.localIP, c.rtpPort, codecs, videoPort, videoCodecs, direction, c.srtpLocalKey)
		} else if iceParams != nil {
			offer = sdp.BuildOfferVideoICE(c.localIP, c.rtpPort, codecs, videoPort, videoCodecs, direction, iceParams)
		} else {
			offer = sdp.BuildOfferVideo(c.localIP, c.rtpPort, codecs, videoPort, videoCodecs, direction)
		}
	} else if c.srtpLocalKey != "" && iceParams != nil {
		offer = sdp.BuildOfferSRTPICE(c.localIP, c.rtpPort, codecs, direction, c.srtpLocalKey, iceParams)
	} else if c.srtpLocalKey != "" {
		offer = sdp.BuildOfferSRTP(c.localIP, c.rtpPort, codecs, direction, c.srtpLocalKey)
	} else if iceParams != nil {
		offer = sdp.BuildOfferICE(c.localIP, c.rtpPort, codecs, direction, iceParams)
	} else {
		offer = sdp.BuildOffer(c.localIP, c.rtpPort, codecs, direction)
	}
	c.logger.Debug("SDP offer built", "id", c.id, "local_ip", c.localIP, "rtp_port", c.rtpPort, "direction", direction, "srtp", c.srtpLocalKey != "", "ice", iceParams != nil, "sdp", offer)
	return offer
}

// buildAnswerSDP creates an SDP answer that only includes codecs from the
// remote offer (RFC 3264 compliance). Must be called with c.mu held.
func (c *call) buildAnswerSDP(remote *sdp.Session, direction string) string {
	if err := c.ensureRTPPort(); err != nil {
		c.logger.Error("RTP port allocation failed for SDP answer", "id", c.id, "error", err)
	}
	iceParams := c.buildICEParams()
	// Set remote ICE credentials on agent if available.
	if c.iceAgent != nil && remote.IceUfrag != "" && remote.IcePwd != "" {
		c.iceAgent.SetRemoteCredentials(ice.Credentials{Ufrag: remote.IceUfrag, Pwd: remote.IcePwd})
	}
	var remoteAudioCodecs []int
	if am := remote.AudioMedia(); am != nil {
		remoteAudioCodecs = am.Codecs
	}
	codecs := c.resolveCodecPrefs()

	// Check for video m= line in the remote offer.
	videoCodecs := videoCodecPrefsToInts(c.opts.VideoCodecs)
	videoPort := c.videoRTPPort
	var remoteVideoCodecs []int
	if vm := remote.VideoMedia(); vm != nil {
		remoteVideoCodecs = vm.Codecs
	}

	var answer string
	if len(videoCodecs) > 0 && videoPort > 0 && len(remoteVideoCodecs) > 0 {
		if c.srtpLocalKey != "" && iceParams != nil {
			answer = sdp.BuildAnswerVideoSRTPICE(c.localIP, c.rtpPort, codecs, remoteAudioCodecs, videoPort, videoCodecs, remoteVideoCodecs, direction, c.srtpLocalKey, iceParams)
		} else if c.srtpLocalKey != "" {
			answer = sdp.BuildAnswerVideoSRTP(c.localIP, c.rtpPort, codecs, remoteAudioCodecs, videoPort, videoCodecs, remoteVideoCodecs, direction, c.srtpLocalKey)
		} else if iceParams != nil {
			answer = sdp.BuildAnswerVideoICE(c.localIP, c.rtpPort, codecs, remoteAudioCodecs, videoPort, videoCodecs, remoteVideoCodecs, direction, iceParams)
		} else {
			answer = sdp.BuildAnswerVideo(c.localIP, c.rtpPort, codecs, remoteAudioCodecs, videoPort, videoCodecs, remoteVideoCodecs, direction)
		}
	} else if c.srtpLocalKey != "" && iceParams != nil {
		answer = sdp.BuildAnswerSRTPICE(c.localIP, c.rtpPort, codecs, remoteAudioCodecs, direction, c.srtpLocalKey, iceParams)
	} else if c.srtpLocalKey != "" {
		answer = sdp.BuildAnswerSRTP(c.localIP, c.rtpPort, codecs, remoteAudioCodecs, direction, c.srtpLocalKey)
	} else if iceParams != nil {
		answer = sdp.BuildAnswerICE(c.localIP, c.rtpPort, codecs, remoteAudioCodecs, direction, iceParams)
	} else {
		answer = sdp.BuildAnswer(c.localIP, c.rtpPort, codecs, remoteAudioCodecs, direction)
	}
	c.logger.Debug("SDP answer built", "id", c.id, "local_ip", c.localIP, "rtp_port", c.rtpPort, "direction", direction, "srtp", c.srtpLocalKey != "", "ice", iceParams != nil, "sdp", answer)
	return answer
}

// negotiateCodec updates c.codec from a parsed remote SDP session.
// Also negotiates video if a video m= line is present.
// Must be called with c.mu held.
func (c *call) negotiateCodec(sess *sdp.Session) {
	c.negotiateAudioCodec(sess)
	c.negotiateVideoCodec(sess)
}

// negotiateAudioCodec updates c.codec from the audio m= line only.
// Must be called with c.mu held.
func (c *call) negotiateAudioCodec(sess *sdp.Session) {
	var remoteCodecs []int
	if am := sess.AudioMedia(); am != nil {
		remoteCodecs = am.Codecs
	}
	if pt := sdp.NegotiateCodec(c.resolveCodecPrefs(), remoteCodecs); pt >= 0 {
		c.codec = Codec(pt)
	}
	c.logger.Debug("codec negotiated", "id", c.id, "codec", int(c.codec), "local_prefs", c.resolveCodecPrefs(), "remote_codecs", remoteCodecs)
}

// buildICEParams creates ICE SDP parameters and initializes the ICE agent.
// Returns nil if ICE is disabled. Must be called with c.mu held.
func (c *call) buildICEParams() *sdp.ICEParams {
	if !c.iceEnabled {
		return nil
	}
	// Only create agent once per call (first SDP build).
	if c.iceAgent != nil {
		// Reuse existing credentials/candidates for re-INVITEs.
		return &sdp.ICEParams{
			Ufrag:      c.iceAgent.LocalCreds().Ufrag,
			Pwd:        c.iceAgent.LocalCreds().Pwd,
			Candidates: candidateStrings(c.iceAgent.Candidates()),
			Lite:       true,
		}
	}

	creds := ice.GenerateCredentials()

	hostIP := c.hostIP
	if hostIP == "" {
		hostIP = c.localIP
	}
	localAddr := &net.UDPAddr{IP: net.ParseIP(hostIP), Port: c.rtpPort}

	// Server-reflexive: only if STUN mapped IP differs from host IP.
	var srflxAddr *net.UDPAddr
	if c.localIP != hostIP {
		srflxAddr = &net.UDPAddr{IP: net.ParseIP(c.localIP), Port: c.rtpPort}
	}

	candidates := ice.GatherCandidates(localAddr, srflxAddr, nil, 1)
	c.iceAgent = ice.NewAgent(creds, candidates)

	return &sdp.ICEParams{
		Ufrag:      creds.Ufrag,
		Pwd:        creds.Pwd,
		Candidates: candidateStrings(candidates),
		Lite:       true,
	}
}

// candidateStrings converts ICE candidates to their SDP string representations.
func candidateStrings(cands []ice.Candidate) []string {
	s := make([]string, len(cands))
	for i, c := range cands {
		s[i] = c.SDPValue()
	}
	return s
}

// initVideoChannels allocates all video pipeline channels and the video mediaStream.
// Must be called with c.mu held.
func (c *call) initVideoChannels() {
	c.videoRTPInbound = make(chan *rtp.Packet, 256)
	c.videoRTPReader = make(chan *rtp.Packet, 256)
	c.videoRTPRawReader = make(chan *rtp.Packet, 256)
	c.videoRTPWriter = make(chan *rtp.Packet, 256)
	c.videoReader = make(chan VideoFrame, 256)
	c.videoWriter = make(chan VideoFrame, 256)
	c.videoStream = &mediaStream{call: c, outSSRC: randUint32()}
	c.closeVideoChOnce = sync.Once{} // reset for re-upgrade after downgrade
}

// ensureVideoRTPPort lazily allocates a UDP socket for video RTP.
// Must be called with c.mu held.
func (c *call) ensureVideoRTPPort() error {
	if c.localIP == "" {
		c.localIP = localIPFor(c.sipHost)
	}
	if c.videoRTPPort == 0 {
		conn, err := listenRTPPort(c.rtpPortMin, c.rtpPortMax)
		if err != nil {
			return fmt.Errorf("allocate video RTP port: %w", err)
		}
		c.videoRTPPort = conn.LocalAddr().(*net.UDPAddr).Port
		c.videoRTPConn = conn
	}
	return nil
}

// videoCodecPrefsToInts converts []VideoCodec to []int.
func videoCodecPrefsToInts(codecs []VideoCodec) []int {
	pts := make([]int, len(codecs))
	for i, c := range codecs {
		pts[i] = int(c)
	}
	return pts
}

// negotiateVideoCodec updates hasVideo and videoCodecType from a parsed SDP.
// Must be called with c.mu held.
func (c *call) negotiateVideoCodec(sess *sdp.Session) {
	vm := sess.VideoMedia()
	if vm == nil || len(vm.Codecs) == 0 {
		return
	}
	// Accept the first video codec that we support.
	for _, pt := range vm.Codecs {
		switch VideoCodec(pt) {
		case VideoCodecH264, VideoCodecVP8:
			c.hasVideo = true
			c.videoCodecType = VideoCodec(pt)
			c.logger.Debug("video codec negotiated", "id", c.id, "codec", pt)
			return
		}
	}
}

// setVideoRemoteEndpoint extracts the video remote address from a parsed SDP.
// Must be called with c.mu held.
func (c *call) setVideoRemoteEndpoint(sess *sdp.Session) {
	vm := sess.VideoMedia()
	if vm == nil {
		return
	}
	ip := sess.Connection
	if ip == "" || vm.Port <= 0 {
		return
	}
	addr, _ := net.ResolveUDPAddr("udp", net.JoinHostPort(ip, strconv.Itoa(vm.Port)))
	c.videoRemoteAddr = addr
}

// closeVideoOutputChannels closes the consumer-facing video channels exactly once.
// Safe to call after channels have been nil'd (e.g. by stopVideoPipeline).
func (c *call) closeVideoOutputChannels() {
	c.closeVideoChOnce.Do(func() {
		if c.videoRTPReader != nil {
			close(c.videoRTPReader)
		}
		if c.videoRTPRawReader != nil {
			close(c.videoRTPRawReader)
		}
		if c.videoReader != nil {
			close(c.videoReader)
		}
	})
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

// cleanup closes the callback dispatcher if it hasn't been closed yet.
// Used in tests to prevent goroutine leaks from calls that never reach fireOnEnded.
func (c *call) cleanup() {
	c.callbackOnce.Do(func() { close(c.callbackCh) })
}

// runCallbackDispatcher is the single goroutine that executes all user-facing
// callbacks for this call. It exits when callbackCh is closed.
func (c *call) runCallbackDispatcher() {
	for fn := range c.callbackCh {
		fn()
	}
}

// dispatch enqueues fn on the callback dispatcher goroutine.
// Falls back to a new goroutine if the channel is full or already closed
// (race between late callbacks and fireOnEnded).
func (c *call) dispatch(fn func()) {
	defer func() {
		if recover() != nil {
			go fn()
		}
	}()
	select {
	case c.callbackCh <- fn:
	default:
		go fn()
	}
}

// fireOnDTMF dispatches both the phone-level and public OnDTMF callbacks.
// Acquires c.mu internally to snapshot function pointers.
func (c *call) fireOnDTMF(digit string) {
	c.mu.Lock()
	fns := make([]func(string), len(c.onDTMFFns))
	copy(fns, c.onDTMFFns)
	fnPhone := c.onDTMFPhone
	c.mu.Unlock()
	if fnPhone != nil {
		c.dispatch(func() { fnPhone(digit) })
	}
	for _, fn := range fns {
		fn := fn
		c.dispatch(func() { fn(digit) })
	}
}

// fireOnState dispatches both the phone-level and public OnState callbacks.
// Must be called with c.mu held. Copies function pointers and dispatches via
// the callback goroutine.
func (c *call) fireOnState(state CallState) {
	if c.onStatePhone != nil {
		fn := c.onStatePhone
		c.dispatch(func() { fn(state) })
	}
	for _, fn := range c.onStateFns {
		fn := fn
		c.dispatch(func() { fn(state) })
	}
}

// fireOnEnded dispatches the cleanup, phone-level, and public OnEnded callbacks.
// Must be called with c.mu held.
func (c *call) fireOnEnded(reason EndReason) {
	// Stop session refresh timer.
	if c.sessionTimer != nil {
		c.sessionTimer.Stop()
		c.sessionTimer = nil
	}
	// Stop media pipeline goroutine.
	if c.mediaDone != nil {
		select {
		case <-c.mediaDone:
		default:
			close(c.mediaDone)
		}
		c.mediaActive = false
	}
	// Stop video pipeline goroutine.
	if c.videoDone != nil {
		select {
		case <-c.videoDone:
		default:
			close(c.videoDone)
		}
	}
	// Close video RTCP and RTP sockets.
	if c.videoRTCPConn != nil {
		c.videoRTCPConn.Close()
		c.videoRTCPConn = nil
	}
	if c.videoRTPConn != nil {
		c.videoRTPConn.Close()
		c.videoRTPConn = nil
	}
	// Close RTP and RTCP sockets (also stops the RTP reader goroutine).
	if c.rtcpConn != nil {
		c.rtcpConn.Close()
		c.rtcpConn = nil
	}
	if c.rtpConn != nil {
		c.rtpConn.Close()
		c.rtpConn = nil
	}
	// Zeroize SRTP key material.
	if c.srtpIn != nil {
		c.srtpIn.Zeroize()
	}
	if c.srtpOut != nil {
		c.srtpOut.Zeroize()
	}
	if c.videoSrtpIn != nil {
		c.videoSrtpIn.Zeroize()
	}
	if c.videoSrtpOut != nil {
		c.videoSrtpOut.Zeroize()
	}
	// Close output channels only if the media goroutine was never started
	// (otherwise the goroutine's defer handles it to avoid send-on-closed panic).
	if c.mediaDone == nil {
		c.closeOutputChannels()
	}
	if c.videoDone == nil && c.videoRTPReader != nil {
		c.closeVideoOutputChannels()
	}
	// Reject any pending video upgrade request.
	if c.pendingVideoUpgrade != nil {
		req := c.pendingVideoUpgrade
		c.pendingVideoUpgrade = nil
		go req.Reject()
	}
	// Clear OnNotify to break closure reference cycles.
	c.dlg.OnNotify(nil)
	if c.onEndedCleanup != nil {
		fn := c.onEndedCleanup
		c.dispatch(func() { fn(reason) })
	}
	if c.onEndedPhone != nil {
		fn := c.onEndedPhone
		c.dispatch(func() { fn(reason) })
	}
	for _, fn := range c.onEndedFns {
		fn := fn
		c.dispatch(func() { fn(reason) })
	}
	// Close the dispatch channel — the dispatcher goroutine exits after
	// draining the remaining callbacks. Once-guard prevents double-close if
	// fireOnEnded is reached from multiple paths.
	c.callbackOnce.Do(func() { close(c.callbackCh) })
}

// closeOutputChannels closes the consumer-facing channels exactly once.
func (c *call) closeOutputChannels() {
	c.closeChOnce.Do(func() {
		close(c.rtpReader)
		close(c.rtpRawReader)
		close(c.pcmReader)
	})
}

func (c *call) Accept(opts ...AcceptOption) error {
	c.mu.Lock()
	if c.state != StateRinging {
		c.mu.Unlock()
		return ErrInvalidState
	}
	// Build SDP answer: if we have the remote offer, restrict to offered codecs.
	c.logger.Debug("accepting call", "id", c.id, "remote_sdp", c.remoteSDP)
	var sess *sdp.Session
	if c.remoteSDP != "" {
		var err error
		sess, err = sdp.Parse(c.remoteSDP)
		if err == nil {
			c.negotiateCodec(sess)
			c.localSDP = c.buildAnswerSDP(sess, sdp.DirSendRecv)
			c.setRemoteEndpoint(sess)
		} else {
			c.localSDP = c.buildLocalSDP(sdp.DirSendRecv)
		}
	} else {
		c.localSDP = c.buildLocalSDP(sdp.DirSendRecv)
	}
	if err := c.dlg.Respond(200, "OK", []byte(c.localSDP)); err != nil {
		c.logger.Error("failed to send 200 OK", "err", err)
	}

	// Set up video if negotiated.
	if c.hasVideo && c.videoRTPConn != nil {
		if sess != nil {
			c.setVideoRemoteEndpoint(sess)
		}
		if c.videoRTPReader == nil {
			c.initVideoChannels()
		}
	}

	c.state = StateActive
	c.startTime = time.Now()
	c.startSessionTimer()
	c.fireOnState(StateActive)
	c.logger.Info("call accepted", "id", c.id)
	hasRTP := c.rtpConn != nil
	hasVideoRTP := c.hasVideo && c.videoRTPConn != nil
	onMediaFns := make([]func(), len(c.onMediaFns))
	copy(onMediaFns, c.onMediaFns)
	c.mu.Unlock()

	// Start media pipeline and RTP socket I/O for production calls.
	if hasRTP {
		c.startMedia()
		c.startRTPReader()
	}
	if hasVideoRTP {
		c.startVideoMedia()
		c.startVideoRTPReader()
	}
	for _, fn := range onMediaFns {
		c.dispatch(fn)
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
	switch c.state {
	case StateRinging:
		// Inbound call not yet accepted — reject with 487.
		if err := c.dlg.Respond(487, "Request Terminated", nil); err != nil {
			c.logger.Error("failed to send 487", "id", c.id, "err", err)
		}
		c.state = StateEnded
		c.fireOnState(StateEnded)
		c.logger.Info("call ended", "id", c.id, "reason", "cancelled")
		c.fireOnEnded(EndedByCancelled)
		return nil
	case StateDialing, StateRemoteRinging, StateEarlyMedia:
		if err := c.dlg.SendCancel(); err != nil {
			c.logger.Error("failed to send CANCEL", "id", c.id, "err", err)
		} else {
			c.logger.Debug("CANCEL sent", "id", c.id)
		}
		c.state = StateEnded
		c.fireOnState(StateEnded)
		c.logger.Info("call ended", "id", c.id, "reason", "cancelled")
		c.fireOnEnded(EndedByCancelled)
		return nil
	case StateActive, StateOnHold:
		if err := c.dlg.SendBye(); err != nil {
			c.logger.Error("failed to send BYE", "id", c.id, "err", err)
		} else {
			c.logger.Debug("BYE sent", "id", c.id)
		}
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
	prevSDP := c.localSDP
	c.localSDP = c.buildLocalSDP(sdp.DirSendOnly)
	if err := c.dlg.SendReInvite([]byte(c.localSDP)); err != nil {
		c.localSDP = prevSDP
		c.logger.Warn("hold re-INVITE failed", "id", c.id, "error", err)
		return err
	}
	c.state = StateOnHold
	c.fireOnState(StateOnHold)
	c.signalMediaTimerReset(defaultHoldMediaTimeout)
	c.logger.Info("call hold", "id", c.id)
	return nil
}

func (c *call) Resume() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateOnHold {
		return ErrInvalidState
	}
	prevSDP := c.localSDP
	c.localSDP = c.buildLocalSDP(sdp.DirSendRecv)
	if err := c.dlg.SendReInvite([]byte(c.localSDP)); err != nil {
		c.localSDP = prevSDP
		c.logger.Warn("resume re-INVITE failed", "id", c.id, "error", err)
		return err
	}
	c.state = StateActive
	c.fireOnState(StateActive)
	c.signalMediaTimerReset(c.effectiveMediaTimeout())
	c.logger.Info("call resumed", "id", c.id)
	return nil
}

func (c *call) Mute() error {
	c.mu.Lock()
	if c.state != StateActive {
		c.mu.Unlock()
		return ErrInvalidState
	}
	if c.audioStream.muted {
		c.mu.Unlock()
		return ErrAlreadyMuted
	}
	c.audioStream.muted = true
	if c.videoStream != nil {
		c.videoStream.muted = true
	}
	fns := make([]func(), len(c.onMuteFns))
	copy(fns, c.onMuteFns)
	c.mu.Unlock()
	for _, fn := range fns {
		c.dispatch(fn)
	}
	return nil
}

func (c *call) Unmute() error {
	c.mu.Lock()
	if c.state != StateActive {
		c.mu.Unlock()
		return ErrInvalidState
	}
	if !c.audioStream.muted {
		c.mu.Unlock()
		return ErrNotMuted
	}
	c.audioStream.muted = false
	if c.videoStream != nil {
		c.videoStream.muted = false
	}
	fns := make([]func(), len(c.onUnmuteFns))
	copy(fns, c.onUnmuteFns)
	c.mu.Unlock()
	for _, fn := range fns {
		c.dispatch(fn)
	}
	return nil
}

func (c *call) MuteAudio() error {
	return c.Mute()
}

func (c *call) UnmuteAudio() error {
	return c.Unmute()
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
	mode := c.dtmfMode
	sentRTP := c.sentRTP
	conn := c.rtpConn
	addr := c.remoteAddr
	srtpOut := c.srtpOut
	c.mu.Unlock()

	if mode == DtmfSipInfo {
		return c.dlg.SendInfoDTMF(digit, defaultInfoDTMFDuration)
	}

	// DtmfRfc4733 and DtmfBoth both send via RTP.
	pkts, err := EncodeDTMF(digit, 0, 0, 0)
	if err != nil {
		return err
	}
	var sendErr error
	for _, pkt := range pkts {
		if sentRTP != nil {
			sendDropOldest(sentRTP, pkt)
		}
		if conn != nil && addr != nil {
			if data, marshalErr := pkt.Marshal(); marshalErr == nil {
				if srtpOut != nil {
					data, marshalErr = srtpOut.Protect(data)
					if marshalErr != nil {
						continue
					}
				}
				if _, err := conn.WriteTo(data, addr); err != nil && sendErr == nil {
					sendErr = err
				}
			}
		}
	}
	if sendErr != nil {
		c.logger.Warn("DTMF WriteTo failed", "id", c.id, "digit", digit, "dst", addr, "error", sendErr)
	}
	return nil
}

// defaultInfoDTMFDuration is the duration in milliseconds sent in SIP INFO DTMF bodies.
const defaultInfoDTMFDuration = 160

func (c *call) BlindTransfer(target string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateActive && c.state != StateOnHold {
		return ErrInvalidState
	}
	c.dlg.SendRefer(target)
	c.dlg.OnNotify(func(code int) {
		if code == 200 {
			c.endWithReason(EndedByTransfer)
		} else if code >= 300 {
			c.endWithReason(EndedByTransferFailed)
		}
	})
	return nil
}

// AttendedTransfer performs an attended (consultative) transfer.
// This call's dialog sends a REFER with a Replaces header built from the
// other call's dialog identifiers. On NOTIFY 200, both calls end with
// EndedByTransfer; on failure (NOTIFY >= 300), both end with EndedByTransferFailed.
// Both calls must be Active or OnHold.
func (c *call) AttendedTransfer(other Call) error {
	if other == nil {
		return fmt.Errorf("xphone: other call is nil")
	}
	b, ok := other.(*call)
	if !ok {
		return fmt.Errorf("xphone: unsupported Call implementation")
	}
	if b == c {
		return fmt.Errorf("xphone: cannot transfer a call to itself")
	}
	// Validate both call states before touching dialog internals.
	if s := other.State(); s != StateActive && s != StateOnHold {
		return ErrInvalidState
	}
	c.mu.Lock()
	if c.state != StateActive && c.state != StateOnHold {
		c.mu.Unlock()
		return ErrInvalidState
	}
	c.mu.Unlock()

	// Extract other call's dialog identifiers for the Replaces header.
	// Read from b without holding c.mu to avoid lock ordering issues.
	bCallID, bLocalTag, bRemoteTag := b.dialogID()
	if bCallID == "" || bLocalTag == "" || bRemoteTag == "" {
		return fmt.Errorf("xphone: attended transfer: other call dialog missing call-id or tags")
	}

	// Build remote party URI from other call's headers.
	var remoteURI string
	if other.Direction() == DirectionOutbound {
		if vals := b.dlg.Header("To"); len(vals) > 0 {
			remoteURI = sipHeaderURI(vals[0])
		}
	} else {
		if vals := b.dlg.Header("From"); len(vals) > 0 {
			remoteURI = sipHeaderURI(vals[0])
		}
	}
	if remoteURI == "" {
		return fmt.Errorf("xphone: attended transfer: cannot determine other call remote URI")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateActive && c.state != StateOnHold {
		return ErrInvalidState
	}

	// Build Refer-To with Replaces parameter (RFC 3891).
	referTo := remoteURI + "?Replaces=" +
		uriEncode(bCallID) + "%3Bto-tag%3D" +
		uriEncode(bRemoteTag) + "%3Bfrom-tag%3D" +
		uriEncode(bLocalTag)

	// Send REFER, then wire NOTIFY handler on success.
	if err := c.dlg.SendRefer(referTo); err != nil {
		return err
	}

	// Wire NOTIFY handler: end both calls on success (200) or failure (>=300).
	c.dlg.OnNotify(func(code int) {
		if code == 200 {
			c.endWithReason(EndedByTransfer)
			b.endWithReason(EndedByTransfer)
		} else if code >= 300 {
			c.endWithReason(EndedByTransferFailed)
			b.endWithReason(EndedByTransferFailed)
		}
	})

	return nil
}

// dialogID returns the Call-ID, local tag, and remote tag for this call's dialog.
// For outbound calls: local=From tag, remote=To tag.
// For inbound calls: local=To tag, remote=From tag.
func (c *call) dialogID() (callID, localTag, remoteTag string) {
	callID = c.dlg.CallID()
	var fromTag, toTag string
	if vals := c.dlg.Header("From"); len(vals) > 0 {
		fromTag = sipHeaderTag(vals[0])
	}
	if vals := c.dlg.Header("To"); len(vals) > 0 {
		toTag = sipHeaderTag(vals[0])
	}
	c.mu.Lock()
	dir := c.direction
	c.mu.Unlock()
	if dir == DirectionOutbound {
		return callID, fromTag, toTag
	}
	return callID, toTag, fromTag
}

// endWithReason transitions the call to Ended with the given reason.
// Safe to call from outside the call's goroutine (e.g., from a NOTIFY handler).
func (c *call) endWithReason(reason EndReason) {
	c.mu.Lock()
	if c.state == StateEnded {
		c.mu.Unlock()
		return
	}
	c.state = StateEnded
	c.fireOnState(StateEnded)
	c.fireOnEnded(reason)
	c.mu.Unlock()
}

func (c *call) RTPRawReader() <-chan *rtp.Packet { return c.rtpRawReader }
func (c *call) RTPReader() <-chan *rtp.Packet    { return c.rtpReader }
func (c *call) RTPWriter() chan<- *rtp.Packet    { return c.rtpWriter }
func (c *call) PCMReader() <-chan []int16        { return c.pcmReader }
func (c *call) PCMWriter() chan<- []int16        { return c.pcmWriter }
func (c *call) PacedPCMWriter() chan<- []int16   { return c.pacedPCMWriter }

func (c *call) ReplaceAudioWriter(newSrc <-chan []int16) error {
	c.mu.Lock()
	if c.state == StateEnded {
		c.mu.Unlock()
		return ErrInvalidState
	}
	c.audioSrc = newSrc
	c.mu.Unlock()
	// Non-blocking signal to media goroutine to re-read audioSrc.
	select {
	case c.audioSwap <- struct{}{}:
	default:
	}
	return nil
}

func (c *call) HasVideo() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hasVideo
}

func (c *call) VideoCodec() VideoCodec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.videoCodecType
}

func (c *call) VideoReader() <-chan VideoFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.videoReader
}

func (c *call) VideoWriter() chan<- VideoFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.videoWriter
}

func (c *call) VideoRTPReader() <-chan *rtp.Packet {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.videoRTPReader
}

func (c *call) VideoRTPWriter() chan<- *rtp.Packet {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.videoRTPWriter
}

func (c *call) MuteVideo() error {
	c.mu.Lock()
	if c.state != StateActive {
		c.mu.Unlock()
		return ErrInvalidState
	}
	if !c.hasVideo || c.videoStream == nil {
		c.mu.Unlock()
		return ErrNoVideo
	}
	if c.videoStream.muted {
		c.mu.Unlock()
		return ErrAlreadyMuted
	}
	c.videoStream.muted = true
	c.mu.Unlock()
	return nil
}

func (c *call) UnmuteVideo() error {
	c.mu.Lock()
	if c.state != StateActive {
		c.mu.Unlock()
		return ErrInvalidState
	}
	if !c.hasVideo || c.videoStream == nil {
		c.mu.Unlock()
		return ErrNoVideo
	}
	if !c.videoStream.muted {
		c.mu.Unlock()
		return ErrNotMuted
	}
	c.videoStream.muted = false
	c.mu.Unlock()
	return nil
}

func (c *call) RequestKeyframe() error {
	c.mu.Lock()
	if c.state != StateActive {
		c.mu.Unlock()
		return ErrInvalidState
	}
	if !c.hasVideo || c.videoStream == nil {
		c.mu.Unlock()
		return ErrNoVideo
	}
	rtcpConn := c.videoRTCPConn
	remoteAddr := c.videoRemoteAddr
	srtpOut := c.videoSrtpOut
	ssrc := c.videoStream.outSSRC
	c.mu.Unlock()

	if rtcpConn == nil || remoteAddr == nil {
		return nil
	}

	// Build PLI with media SSRC = 0 (we don't know remote video SSRC yet).
	pli := rtcp.BuildPLI(ssrc, 0)
	if srtpOut != nil {
		var err error
		pli, err = srtpOut.ProtectRTCP(pli)
		if err != nil {
			return err
		}
	}
	_, err := rtcpConn.WriteTo(pli, remoteAddr)
	return err
}

func (c *call) OnDTMF(fn func(string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onDTMFFns = append(c.onDTMFFns, fn)
}

func (c *call) OnHold(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onHoldFns = append(c.onHoldFns, fn)
}

func (c *call) OnResume(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onResumeFns = append(c.onResumeFns, fn)
}

func (c *call) OnMute(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMuteFns = append(c.onMuteFns, fn)
}

func (c *call) OnUnmute(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onUnmuteFns = append(c.onUnmuteFns, fn)
}

func (c *call) OnMedia(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMediaFns = append(c.onMediaFns, fn)
}

func (c *call) OnState(fn func(CallState)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onStateFns = append(c.onStateFns, fn)
}

func (c *call) OnEnded(fn func(EndReason)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onEndedFns = append(c.onEndedFns, fn)
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
				for _, fn := range c.onMediaFns {
					c.dispatch(fn)
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
			for _, fn := range c.onMediaFns {
				c.dispatch(fn)
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

func (c *call) simulateCancel() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateRinging {
		return
	}
	c.state = StateEnded
	c.fireOnState(StateEnded)
	c.logger.Info("call ended", "id", c.id, "reason", "cancelled")
	c.fireOnEnded(EndedByCancelled)
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
	c.setRemoteEndpoint(sess)

	// Update remote ICE credentials if present.
	if c.iceAgent != nil && sess.IceUfrag != "" && sess.IcePwd != "" {
		c.iceAgent.SetRemoteCredentials(ice.Credentials{Ufrag: sess.IceUfrag, Pwd: sess.IcePwd})
	}

	dir := sess.Dir()
	var holdFns, resumeFns []func()

	switch {
	case dir == sdp.DirSendOnly && c.state == StateActive:
		c.state = StateOnHold
		holdFns = make([]func(), len(c.onHoldFns))
		copy(holdFns, c.onHoldFns)
		c.fireOnState(StateOnHold)
		c.signalMediaTimerReset(defaultHoldMediaTimeout)
	case dir == sdp.DirSendRecv && c.state == StateOnHold:
		c.state = StateActive
		resumeFns = make([]func(), len(c.onResumeFns))
		copy(resumeFns, c.onResumeFns)
		c.fireOnState(StateActive)
		c.signalMediaTimerReset(c.effectiveMediaTimeout())
	}

	c.negotiateCodec(sess)

	c.mu.Unlock()

	for _, fn := range holdFns {
		c.dispatch(fn)
	}
	for _, fn := range resumeFns {
		c.dispatch(fn)
	}
}

func (c *call) MediaSessionActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mediaActive
}

// injectRTP is a test hook that feeds an RTP packet into the call's media
// pipeline as if it arrived from the network.
func (c *call) injectRTP(pkt *rtp.Packet) {
	c.rtpInbound <- pkt
}
