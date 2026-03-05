package xphone

import (
	"context"
	"errors"
	"strings"
	"sync"
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
	State() PhoneState
}

// phone is the concrete implementation of Phone.
type phone struct {
	mu sync.Mutex

	cfg      Config
	tr       sipTransport
	reg      *registry
	state    PhoneState
	incoming func(Call)
}

func newPhone(cfg Config) *phone {
	return &phone{
		cfg:   cfg,
		state: PhoneStateDisconnected,
	}
}

// connectWithTransport is a test hook that connects with a mock transport.
// It performs registration (consuming the queued 200 OK) and wires up the
// incoming INVITE handler on the transport.
func (p *phone) connectWithTransport(tr sipTransport) {
	p.mu.Lock()
	p.tr = tr
	p.reg = newRegistry(tr, p.cfg)
	// Apply any callbacks that were set before connectWithTransport was called.
	incomingFn := p.incoming
	p.mu.Unlock()

	// Perform registration (consumes the pre-queued 200 OK in tests).
	err := p.reg.Start(context.Background())

	// Wire up incoming INVITE handling on the transport.
	tr.OnIncoming(p.handleIncoming)

	p.mu.Lock()
	if err == nil {
		p.state = PhoneStateRegistered
	} else {
		p.state = PhoneStateRegistrationFailed
	}
	_ = incomingFn // stored for future use by handleIncoming
	p.mu.Unlock()
}

func (p *phone) Connect(ctx context.Context) error {
	return errors.New("not implemented")
}

func (p *phone) Disconnect() error {
	return errors.New("not implemented")
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
	tr := p.tr
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

	// Create the outbound call with a minimal dialog.
	dlg := newPhoneDialog()
	c := newOutboundCall(dlg, opts...)

	// Send the INVITE and get the first response.
	code, reason, err := tr.SendRequest(dialCtx, "INVITE", nil)
	if err != nil {
		return nil, p.classifyDialError(ctx, dialCtx, err)
	}

	// Process responses until we get a final one (>= 200).
	for code >= 100 && code < 200 {
		c.simulateResponse(code, reason)
		code, reason, err = tr.ReadResponse(dialCtx)
		if err != nil {
			return nil, p.classifyDialError(ctx, dialCtx, err)
		}
	}

	// Process the final response.
	c.simulateResponse(code, reason)

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

// handleIncoming is called by the transport when an incoming INVITE arrives.
// It auto-sends 100 Trying, creates an inbound call, fires OnIncoming, and
// auto-sends 180 Ringing.
func (p *phone) handleIncoming(from, to string) {
	// Auto-send 100 Trying immediately.
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	tr.Respond(100, "Trying")

	// Create an inbound call with a phone dialog.
	dlg := newPhoneDialog()
	c := newInboundCall(dlg)

	// Fire the OnIncoming callback if set.
	p.mu.Lock()
	fn := p.incoming
	p.mu.Unlock()
	if fn != nil {
		fn(c)
	}

	// Auto-send 180 Ringing after presenting the call.
	tr.Respond(180, "Ringing")
}

func (p *phone) OnIncoming(fn func(Call)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.incoming = fn
}

func (p *phone) OnRegistered(fn func()) {
	if p.reg != nil {
		p.reg.OnRegistered(fn)
	}
}

func (p *phone) OnUnregistered(fn func()) {
	if p.reg != nil {
		p.reg.OnUnregistered(fn)
	}
}

func (p *phone) OnError(fn func(error)) {
	if p.reg != nil {
		p.reg.OnError(fn)
	}
}

func (p *phone) State() PhoneState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// phoneDialog is a minimal dialog implementation for phone-created calls.
// It satisfies the dialog interface with basic state tracking.
// Real SIP dialog operations will be implemented in a later phase.
type phoneDialog struct {
	mu              sync.Mutex
	sdpAnswerSent   bool
	cancelSent      bool
	byeSent         bool
	referSent       bool
	lastReInviteSDP string
	lastReferTarget string
	lastRespCode    int
	lastRespReason  string
	callID          string
	headers         map[string][]string
	onNotify        func(int)
}

func newPhoneDialog() *phoneDialog {
	return &phoneDialog{
		callID:  newCallID(),
		headers: make(map[string][]string),
	}
}

func (d *phoneDialog) SDPAnswerSent() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sdpAnswerSent
}

func (d *phoneDialog) SendSDPAnswer() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sdpAnswerSent = true
}

func (d *phoneDialog) LastResponseCode() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastRespCode
}

func (d *phoneDialog) LastResponseReason() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastRespReason
}

func (d *phoneDialog) Respond(code int, reason string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastRespCode = code
	d.lastRespReason = reason
}

func (d *phoneDialog) CancelSent() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancelSent
}

func (d *phoneDialog) SendCancel() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelSent = true
}

func (d *phoneDialog) ByeSent() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.byeSent
}

func (d *phoneDialog) SendBye() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.byeSent = true
}

func (d *phoneDialog) LastReInviteSDP() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastReInviteSDP
}

func (d *phoneDialog) SendReInvite(sdp string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastReInviteSDP = sdp
}

func (d *phoneDialog) ReferSent() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.referSent
}

func (d *phoneDialog) LastReferTarget() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastReferTarget
}

func (d *phoneDialog) SendRefer(target string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.referSent = true
	d.lastReferTarget = target
}

func (d *phoneDialog) OnNotify(fn func(code int)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onNotify = fn
}

func (d *phoneDialog) SimulateNotify(code int) {
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
