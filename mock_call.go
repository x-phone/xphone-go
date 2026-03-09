package xphone

import (
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// MockCall is a mock implementation of the Call interface for testing.
// It provides setters for all state and simulation methods for callbacks.
type MockCall struct {
	mu sync.Mutex

	id          string
	callID      string
	state       CallState
	direction   Direction
	from        string
	to          string
	fromName    string
	remoteURI   string
	remoteIP    string
	remotePort  int
	codec       Codec
	localSDP    string
	remoteSDP   string
	startTime   time.Time
	muted       bool
	mediaActive bool
	sentDTMF    []string
	transferTo  string
	headers     map[string][]string

	onDTMFFn       func(string)
	onHoldFn       func()
	onResumeFn     func()
	onMuteFn       func()
	onUnmuteFn     func()
	onMediaFn      func()
	onStateFn      func(CallState)
	onEndedFn      func(EndReason)
	onEndedCleanup func(EndReason) // internal: call tracking cleanup
	onStatePhone   func(CallState) // internal: phone-level OnCallState
	onEndedPhone   func(EndReason) // internal: phone-level OnCallEnded
	onDTMFPhone    func(string)    // internal: phone-level OnCallDTMF

	rtpRawReader chan *rtp.Packet
	rtpReader    chan *rtp.Packet
	rtpWriter    chan *rtp.Packet
	pcmReader    chan []int16
	pcmWriter    chan []int16
}

// NewMockCall creates a new MockCall with default state (Ringing, Inbound).
func NewMockCall() *MockCall {
	return &MockCall{
		id:           newCallID(),
		callID:       newCallID(),
		state:        StateRinging,
		direction:    DirectionInbound,
		headers:      make(map[string][]string),
		rtpRawReader: make(chan *rtp.Packet, 256),
		rtpReader:    make(chan *rtp.Packet, 256),
		rtpWriter:    make(chan *rtp.Packet, 256),
		pcmReader:    make(chan []int16, 256),
		pcmWriter:    make(chan []int16, 256),
	}
}

// --- Call interface implementation ---

func (c *MockCall) ID() string     { return c.id }
func (c *MockCall) CallID() string { return c.callID }

func (c *MockCall) Direction() Direction {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.direction
}

func (c *MockCall) From() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.from
}

func (c *MockCall) To() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.to
}

func (c *MockCall) FromName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fromName
}

func (c *MockCall) RemoteURI() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remoteURI
}

func (c *MockCall) RemoteDID() string {
	c.mu.Lock()
	dir := c.direction
	c.mu.Unlock()
	if dir == DirectionInbound {
		return c.From()
	}
	return c.To()
}

func (c *MockCall) RemoteIP() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remoteIP
}

func (c *MockCall) RemotePort() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remotePort
}

func (c *MockCall) State() CallState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *MockCall) MediaSessionActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mediaActive
}

func (c *MockCall) Codec() Codec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.codec
}

func (c *MockCall) LocalSDP() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.localSDP
}

func (c *MockCall) RemoteSDP() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remoteSDP
}

func (c *MockCall) StartTime() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startTime
}

func (c *MockCall) Duration() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.startTime.IsZero() {
		return 0
	}
	return time.Since(c.startTime)
}

func (c *MockCall) Header(name string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	lower := strings.ToLower(name)
	for k, v := range c.headers {
		if strings.ToLower(k) == lower {
			cp := make([]string, len(v))
			copy(cp, v)
			return cp
		}
	}
	return nil
}

func (c *MockCall) Headers() map[string][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make(map[string][]string, len(c.headers))
	for k, v := range c.headers {
		vals := make([]string, len(v))
		copy(vals, v)
		cp[k] = vals
	}
	return cp
}

func (c *MockCall) Accept(opts ...AcceptOption) error {
	c.mu.Lock()
	if c.state != StateRinging {
		c.mu.Unlock()
		return ErrInvalidState
	}
	c.state = StateActive
	c.startTime = time.Now()
	phoneFn := c.onStatePhone
	fn := c.onStateFn
	c.mu.Unlock()
	if phoneFn != nil {
		phoneFn(StateActive)
	}
	if fn != nil {
		fn(StateActive)
	}
	return nil
}

func (c *MockCall) Reject(code int, reason string) error {
	c.mu.Lock()
	if c.state != StateRinging {
		c.mu.Unlock()
		return ErrInvalidState
	}
	c.state = StateEnded
	cleanupFn := c.onEndedCleanup
	statePhoneFn := c.onStatePhone
	stateFn := c.onStateFn
	endPhoneFn := c.onEndedPhone
	endFn := c.onEndedFn
	c.mu.Unlock()
	if cleanupFn != nil {
		cleanupFn(EndedByRejected)
	}
	if statePhoneFn != nil {
		statePhoneFn(StateEnded)
	}
	if stateFn != nil {
		stateFn(StateEnded)
	}
	if endPhoneFn != nil {
		endPhoneFn(EndedByRejected)
	}
	if endFn != nil {
		endFn(EndedByRejected)
	}
	return nil
}

func (c *MockCall) End() error {
	c.mu.Lock()
	var reason EndReason
	switch c.state {
	case StateDialing, StateRemoteRinging, StateEarlyMedia:
		reason = EndedByCancelled
	case StateActive, StateOnHold:
		reason = EndedByLocal
	default:
		c.mu.Unlock()
		return ErrInvalidState
	}
	c.state = StateEnded
	cleanupFn := c.onEndedCleanup
	statePhoneFn := c.onStatePhone
	stateFn := c.onStateFn
	endPhoneFn := c.onEndedPhone
	endFn := c.onEndedFn
	c.mu.Unlock()
	if cleanupFn != nil {
		cleanupFn(reason)
	}
	if statePhoneFn != nil {
		statePhoneFn(StateEnded)
	}
	if stateFn != nil {
		stateFn(StateEnded)
	}
	if endPhoneFn != nil {
		endPhoneFn(reason)
	}
	if endFn != nil {
		endFn(reason)
	}
	return nil
}

func (c *MockCall) Hold() error {
	c.mu.Lock()
	if c.state != StateActive {
		c.mu.Unlock()
		return ErrInvalidState
	}
	c.state = StateOnHold
	statePhoneFn := c.onStatePhone
	stateFn := c.onStateFn
	holdFn := c.onHoldFn
	c.mu.Unlock()
	if statePhoneFn != nil {
		statePhoneFn(StateOnHold)
	}
	if stateFn != nil {
		stateFn(StateOnHold)
	}
	if holdFn != nil {
		holdFn()
	}
	return nil
}

func (c *MockCall) Resume() error {
	c.mu.Lock()
	if c.state != StateOnHold {
		c.mu.Unlock()
		return ErrInvalidState
	}
	c.state = StateActive
	statePhoneFn := c.onStatePhone
	stateFn := c.onStateFn
	resumeFn := c.onResumeFn
	c.mu.Unlock()
	if statePhoneFn != nil {
		statePhoneFn(StateActive)
	}
	if stateFn != nil {
		stateFn(StateActive)
	}
	if resumeFn != nil {
		resumeFn()
	}
	return nil
}

func (c *MockCall) Mute() error {
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
		fn()
	}
	return nil
}

func (c *MockCall) Unmute() error {
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
		fn()
	}
	return nil
}

func (c *MockCall) SendDTMF(digit string) error {
	c.mu.Lock()
	if c.state != StateActive {
		c.mu.Unlock()
		return ErrInvalidState
	}
	c.sentDTMF = append(c.sentDTMF, digit)
	c.mu.Unlock()
	return nil
}

func (c *MockCall) BlindTransfer(target string) error {
	c.mu.Lock()
	if c.state != StateActive && c.state != StateOnHold {
		c.mu.Unlock()
		return ErrInvalidState
	}
	c.transferTo = target
	c.mu.Unlock()
	return nil
}

func (c *MockCall) RTPRawReader() <-chan *rtp.Packet { return c.rtpRawReader }
func (c *MockCall) RTPReader() <-chan *rtp.Packet    { return c.rtpReader }
func (c *MockCall) RTPWriter() chan<- *rtp.Packet    { return c.rtpWriter }
func (c *MockCall) PCMReader() <-chan []int16        { return c.pcmReader }
func (c *MockCall) PCMWriter() chan<- []int16        { return c.pcmWriter }

func (c *MockCall) OnDTMF(fn func(string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onDTMFFn = fn
}

func (c *MockCall) OnHold(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onHoldFn = fn
}

func (c *MockCall) OnResume(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onResumeFn = fn
}

func (c *MockCall) OnMute(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMuteFn = fn
}

func (c *MockCall) OnUnmute(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onUnmuteFn = fn
}

func (c *MockCall) OnMedia(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMediaFn = fn
}

func (c *MockCall) OnState(fn func(CallState)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onStateFn = fn
}

func (c *MockCall) OnEnded(fn func(EndReason)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onEndedFn = fn
}

// --- Test setters and inspection ---

func (c *MockCall) SetState(s CallState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = s
}

func (c *MockCall) SetDirection(d Direction) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.direction = d
}

func (c *MockCall) SetRemoteURI(uri string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remoteURI = uri
}

func (c *MockCall) SetRemoteIP(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remoteIP = ip
}

func (c *MockCall) SetRemotePort(port int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remotePort = port
}

func (c *MockCall) SetCodec(codec Codec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.codec = codec
}

func (c *MockCall) SetLocalSDP(sdp string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.localSDP = sdp
}

func (c *MockCall) SetRemoteSDP(sdp string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remoteSDP = sdp
}

func (c *MockCall) SetStartTime(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.startTime = t
}

func (c *MockCall) SetMediaSessionActive(active bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mediaActive = active
}

func (c *MockCall) SetHeader(name, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.headers[name] = []string{value}
}

func (c *MockCall) Muted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.muted
}

func (c *MockCall) SentDTMF() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]string, len(c.sentDTMF))
	copy(cp, c.sentDTMF)
	return cp
}

func (c *MockCall) LastTransferTarget() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transferTo
}

// SimulateDTMF fires both the phone-level and per-call OnDTMF callbacks.
func (c *MockCall) SimulateDTMF(digit string) {
	c.mu.Lock()
	phoneFn := c.onDTMFPhone
	fn := c.onDTMFFn
	c.mu.Unlock()
	if phoneFn != nil {
		phoneFn(digit)
	}
	if fn != nil {
		fn(digit)
	}
}

// InjectRTP pushes an RTP packet onto the RTPReader channel.
func (c *MockCall) InjectRTP(pkt *rtp.Packet) {
	c.rtpReader <- pkt
}

// Ensure MockCall satisfies Call at compile time.
var _ Call = (*MockCall)(nil)
