package xphone

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/x-phone/xphone-go/internal/sdp"
)

func newCallID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// dialog is the internal interface for SIP dialog operations.
// Satisfied by testutil.MockDialog in tests.
type dialog interface {
	SDPAnswerSent() bool
	SendSDPAnswer()
	LastResponseCode() int
	LastResponseReason() string
	Respond(code int, reason string)
	CancelSent() bool
	SendCancel()
	ByeSent() bool
	SendBye()
	LastReInviteSDP() string
	SendReInvite(sdp string)
	ReferSent() bool
	LastReferTarget() string
	SendRefer(target string)
	OnNotify(fn func(code int))
	SimulateNotify(code int)
	CallID() string
	Header(name string) []string
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

	// internal test hooks
	simulateResponse(code int, reason string)
	simulateBye()
	simulateReInvite(sdp string)
	mediaSessionActive() bool
	injectRTP(pkt *rtp.Packet)
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

	onEndedFn  func(EndReason)
	onMediaFn  func()
	onStateFn  func(CallState)
	onDTMFFn   func(string)
	onHoldFn   func()
	onResumeFn func()

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

func (c *call) RemoteURI() string { return "" }
func (c *call) RemoteIP() string  { return "" }
func (c *call) RemotePort() int   { return 0 }

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
	headers := c.dlg.Headers()
	lower := strings.ToLower(name)
	for k, v := range headers {
		if strings.ToLower(k) == lower {
			return v
		}
	}
	return nil
}

func (c *call) Headers() map[string][]string {
	return c.dlg.Headers()
}

func defaultCodecPrefs() []int {
	return []int{8, 0, 9, 111}
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
		c.mu.Unlock()
		refreshSDP := sdp.BuildOffer("0.0.0.0", 0, defaultCodecPrefs(), "sendrecv")
		c.dlg.SendReInvite(refreshSDP)
	})
}

func (c *call) Accept(opts ...AcceptOption) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateRinging {
		return ErrInvalidState
	}
	c.localSDP = sdp.BuildOffer("0.0.0.0", 0, defaultCodecPrefs(), "sendrecv")
	if c.remoteSDP != "" {
		if sess, err := sdp.Parse(c.remoteSDP); err == nil {
			c.negotiateCodec(sess)
		}
	}
	c.dlg.SendSDPAnswer()
	c.state = StateActive
	c.startTime = time.Now()
	c.startSessionTimer()
	if c.onMediaFn != nil {
		fn := c.onMediaFn
		go fn()
	}
	return nil
}

func (c *call) Reject(code int, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateRinging {
		return ErrInvalidState
	}
	c.dlg.Respond(code, reason)
	c.state = StateEnded
	if c.onEndedFn != nil {
		fn := c.onEndedFn
		go fn(EndedByRejected)
	}
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
		if c.onEndedFn != nil {
			fn := c.onEndedFn
			go fn(EndedByCancelled)
		}
		return nil
	case StateActive, StateOnHold:
		c.dlg.SendBye()
		c.state = StateEnded
		if c.onEndedFn != nil {
			fn := c.onEndedFn
			go fn(EndedByLocal)
		}
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
	c.localSDP = sdp.BuildOffer("0.0.0.0", 0, defaultCodecPrefs(), "sendonly")
	c.dlg.SendReInvite(c.localSDP)
	c.state = StateOnHold
	return nil
}

func (c *call) Resume() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateOnHold {
		return ErrInvalidState
	}
	c.localSDP = sdp.BuildOffer("0.0.0.0", 0, defaultCodecPrefs(), "sendrecv")
	c.dlg.SendReInvite(c.localSDP)
	c.state = StateActive
	return nil
}

func (c *call) Mute() error                 { return nil }
func (c *call) Unmute() error               { return nil }
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
	c.mu.Unlock()

	pkts, err := EncodeDTMF(digit, 0, 0, 0)
	if err != nil {
		return err
	}
	if sentRTP != nil {
		for _, pkt := range pkts {
			sendDropOldest(sentRTP, pkt)
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
			fn := c.onEndedFn
			c.mu.Unlock()
			if fn != nil {
				fn(EndedByTransfer)
			}
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
func (c *call) OnMute(fn func())       {}
func (c *call) OnUnmute(fn func())     {}

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
		}
	case code == 183:
		if c.opts.EarlyMedia {
			if c.state == StateDialing || c.state == StateRemoteRinging {
				c.state = StateEarlyMedia
				c.mediaActive = true
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
	if c.onEndedFn != nil {
		fn := c.onEndedFn
		go fn(EndedByRemote)
	}
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
	var holdFn, resumeFn func()

	switch {
	case dir == "sendonly" && c.state == StateActive:
		c.state = StateOnHold
		holdFn = c.onHoldFn
	case dir == "sendrecv" && c.state == StateOnHold:
		c.state = StateActive
		resumeFn = c.onResumeFn
	}

	c.negotiateCodec(sess)

	c.mu.Unlock()

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
