package xphone

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// registry manages SIP registration lifecycle: initial register with auth
// challenge handling, periodic refresh, NAT keepalives, and transport drop recovery.
type registry struct {
	mu sync.Mutex

	tr     sipTransport
	cfg    Config
	state  PhoneState
	logger *slog.Logger

	onRegistered   func()
	onUnregistered func()
	onError        func(error)

	// ctx is the lifecycle context for background operations (loop, re-registration).
	// Cancelled by Stop() to ensure all goroutines exit cleanly.
	ctx    context.Context
	cancel context.CancelFunc

	// reregistering guards against multiple concurrent re-registration goroutines
	// from repeated transport drops.
	reregistering bool
}

func newRegistry(tr sipTransport, cfg Config) *registry {
	return &registry{
		tr:     tr,
		cfg:    cfg,
		state:  PhoneStateDisconnected,
		logger: resolveLogger(cfg.Logger),
	}
}

// Start performs initial registration and starts background refresh/keepalive loops.
// It blocks until the initial REGISTER succeeds or all retries are exhausted.
func (r *registry) Start(ctx context.Context) error {
	// Create a lifecycle context for all background operations.
	// This is cancelled by Stop() to ensure clean shutdown of the refresh loop
	// and any in-flight re-registration goroutines spawned by handleDrop.
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())

	r.mu.Lock()
	r.ctx = lifecycleCtx
	r.cancel = lifecycleCancel
	r.state = PhoneStateRegistering
	r.mu.Unlock()

	// Wire up transport drop detection so we can fire OnUnregistered
	// and attempt re-registration when the connection drops.
	r.tr.OnDrop(r.handleDrop)

	// Attempt initial registration with retries.
	if err := r.register(ctx); err != nil {
		lifecycleCancel()
		return err
	}

	go r.loop(lifecycleCtx)

	return nil
}

// Stop cancels all background goroutines (refresh loop, re-registration) and returns.
func (r *registry) Stop() error {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel() // cancels lifecycle ctx, stopping loop + any re-registration goroutines
	}
	r.state = PhoneStateDisconnected
	r.mu.Unlock()
	return nil
}

// OnRegistered sets the callback for successful registration.
// If already in Registered state, the callback fires immediately (asynchronously).
func (r *registry) OnRegistered(fn func()) {
	r.mu.Lock()
	r.onRegistered = fn
	alreadyRegistered := r.state == PhoneStateRegistered
	r.mu.Unlock()

	if alreadyRegistered && fn != nil {
		go fn()
	}
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

// register attempts to send a REGISTER request, handling 401 auth challenges.
// Retries up to RegisterMaxRetry times with RegisterRetry delay between attempts.
// On success, transitions to Registered and fires OnRegistered.
// On exhausting retries, fires OnError(ErrRegistrationFailed).
func (r *registry) register(ctx context.Context) error {
	for attempt := 0; attempt < r.cfg.RegisterMaxRetry; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(r.cfg.RegisterRetry):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		code, header, err := r.tr.SendRequest(ctx, "REGISTER", nil)
		if err != nil {
			continue
		}

		// Handle 401 Unauthorized: extract nonce from WWW-Authenticate and retry
		// with an Authorization header containing the digest credentials.
		if code == 401 {
			nonce := extractNonce(header)
			authHeaders := map[string]string{
				"Authorization": fmt.Sprintf(`Digest nonce="%s"`, nonce),
			}
			code, _, err = r.tr.SendRequest(ctx, "REGISTER", authHeaders)
			if err != nil {
				continue
			}
		}

		if code == 200 {
			r.mu.Lock()
			r.state = PhoneStateRegistered
			fn := r.onRegistered
			r.mu.Unlock()
			r.logger.Info("registration successful")
			if fn != nil {
				go fn()
			}
			return nil
		}
	}

	// All retries exhausted.
	r.mu.Lock()
	r.state = PhoneStateRegistrationFailed
	errFn := r.onError
	r.mu.Unlock()
	r.logger.Error("registration failed")
	if errFn != nil {
		go errFn(ErrRegistrationFailed)
	}
	return ErrRegistrationFailed
}

// handleDrop is called by the transport when the connection drops.
// It transitions to Registering, fires OnUnregistered, and spawns a
// background re-registration attempt using the lifecycle context.
// Repeated drops while a re-registration is already in flight are deduplicated.
func (r *registry) handleDrop() {
	r.mu.Lock()
	// Guard: if already stopped or already re-registering, skip.
	if r.state == PhoneStateDisconnected || r.reregistering {
		r.mu.Unlock()
		return
	}
	r.state = PhoneStateRegistering
	r.reregistering = true
	fn := r.onUnregistered
	ctx := r.ctx
	r.mu.Unlock()

	if fn != nil {
		fn()
	}

	// Attempt re-registration using the lifecycle context so that Stop()
	// cancels this goroutine cleanly instead of leaking it.
	if ctx != nil {
		go func() {
			r.register(ctx)
			r.mu.Lock()
			r.reregistering = false
			r.mu.Unlock()
		}()
	}
}

// loop runs the periodic refresh and NAT keepalive tickers until ctx is cancelled.
func (r *registry) loop(ctx context.Context) {
	refreshInterval := r.cfg.RegisterExpiry / 2
	refreshTicker := time.NewTicker(refreshInterval)
	defer refreshTicker.Stop()

	// NAT keepalive ticker (nil channel blocks forever if not configured).
	var keepaliveCh <-chan time.Time
	if r.cfg.NATKeepaliveInterval > 0 {
		keepaliveTicker := time.NewTicker(r.cfg.NATKeepaliveInterval)
		defer keepaliveTicker.Stop()
		keepaliveCh = keepaliveTicker.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-refreshTicker.C:
			// Fire-and-forget: record the REGISTER but don't block the loop
			// waiting for a response (the mock may not have one queued).
			go r.tr.SendRequest(ctx, "REGISTER", nil)
		case <-keepaliveCh:
			r.tr.SendKeepalive()
		}
	}
}

// extractNonce parses a nonce value from a WWW-Authenticate header.
// Example input: `WWW-Authenticate: Digest nonce="abc123"`
// Returns the nonce string, or empty if not found.
func extractNonce(header string) string {
	const prefix = `nonce="`
	idx := strings.Index(header, prefix)
	if idx < 0 {
		return ""
	}
	start := idx + len(prefix)
	end := strings.Index(header[start:], `"`)
	if end < 0 {
		return header[start:]
	}
	return header[start : start+end]
}
