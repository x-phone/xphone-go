package xphone

import (
	"context"
	"errors"
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
func (p *phone) connectWithTransport(tr sipTransport) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tr = tr
	p.reg = newRegistry(tr, p.cfg)
	p.state = PhoneStateRegistered
}

func (p *phone) Connect(ctx context.Context) error {
	return errors.New("not implemented")
}

func (p *phone) Disconnect() error {
	return errors.New("not implemented")
}

func (p *phone) Dial(ctx context.Context, target string, opts ...DialOption) (Call, error) {
	p.mu.Lock()
	if p.state != PhoneStateRegistered {
		p.mu.Unlock()
		return nil, ErrNotRegistered
	}
	p.mu.Unlock()
	return nil, errors.New("not implemented")
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
