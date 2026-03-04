package xphone

import (
	"context"
	"errors"
	"sync"
)

// registry manages SIP registration lifecycle.
type registry struct {
	mu sync.Mutex

	tr    sipTransport
	cfg   Config
	state PhoneState

	onRegistered   func()
	onUnregistered func()
	onError        func(error)

	cancel context.CancelFunc
}

func newRegistry(tr sipTransport, cfg Config) *registry {
	return &registry{
		tr:    tr,
		cfg:   cfg,
		state: PhoneStateDisconnected,
	}
}

// Start begins the registration loop.
func (r *registry) Start(ctx context.Context) error {
	return errors.New("not implemented")
}

// Stop halts the registration loop and unregisters.
func (r *registry) Stop() error {
	return errors.New("not implemented")
}

// OnRegistered sets the callback for successful registration.
func (r *registry) OnRegistered(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onRegistered = fn
}

// OnUnregistered sets the callback for loss of registration.
func (r *registry) OnUnregistered(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onUnregistered = fn
}

// OnError sets the callback for registration errors.
func (r *registry) OnError(fn func(error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onError = fn
}

// State returns the current phone state.
func (r *registry) State() PhoneState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}
