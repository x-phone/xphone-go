package xphone

import (
	"context"
	"log/slog"
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
	logger   *slog.Logger
	tr       sipTransport
	reg      *registry
	state    PhoneState
	incoming func(Call)

	// Buffered callbacks set before Connect.
	onRegisteredFn   func()
	onUnregisteredFn func()
	onErrorFn        func(error)
}

func newPhone(cfg Config) *phone {
	return &phone{
		cfg:    cfg,
		logger: resolveLogger(cfg.Logger),
		state:  PhoneStateDisconnected,
	}
}

// connectWithTransport is a test hook that connects with a mock transport.
// It performs registration (consuming the queued 200 OK) and wires up the
// incoming INVITE handler on the transport.
func (p *phone) connectWithTransport(tr sipTransport) {
	p.mu.Lock()
	p.tr = tr
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

	// Wire up incoming INVITE handling on the transport.
	tr.OnIncoming(p.handleIncoming)

	p.mu.Lock()
	if err == nil {
		p.state = PhoneStateRegistered
	} else {
		p.state = PhoneStateRegistrationFailed
	}
	p.mu.Unlock()

	p.logger.Info("phone connected")
}

func (p *phone) Connect(ctx context.Context) error {
	p.mu.Lock()
	if p.state != PhoneStateDisconnected {
		p.mu.Unlock()
		return ErrAlreadyConnected
	}
	p.state = PhoneStateRegistering
	p.mu.Unlock()

	tr, err := newSipUA(p.cfg)
	if err != nil {
		p.mu.Lock()
		p.state = PhoneStateDisconnected
		p.mu.Unlock()
		return err
	}

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
	p.state = PhoneStateDisconnected
	p.reg = nil
	p.tr = nil
	fn := p.onUnregisteredFn
	p.mu.Unlock()

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

	p.logger.Info("dialing", "target", target)

	// Create the outbound call with a minimal dialog.
	dlg := newPhoneDialog()
	c := newOutboundCall(dlg, opts...)
	c.logger = p.logger

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
	c.logger = p.logger

	p.logger.Info("incoming call", "from", from, "to", to)

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
	byeSent         bool
	cancelSent      bool
	referSent       bool
	lastRespCode    int
	lastRespReason  string
	lastRespBody    []byte
	lastReInviteSDP []byte
	lastReferTarget string
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

func (d *phoneDialog) OnNotify(fn func(code int)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onNotify = fn
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
