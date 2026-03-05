package xphone

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
)

// TransportConfig holds SIP transport settings.
type TransportConfig struct {
	Protocol  string
	Host      string
	Port      int
	TLSConfig *tls.Config
}

// sipTransport is the internal interface for SIP transport.
// MockTransport in testutil satisfies this interface plus test helpers.
// Return types use primitives (int, string) to avoid circular imports with testutil.
type sipTransport interface {
	// SendRequest sends a SIP request and waits for a response.
	// Returns (response code, response header value, error).
	// The headers map is included in the outgoing message (e.g. Authorization).
	SendRequest(ctx context.Context, method string, headers map[string]string) (int, string, error)

	// ReadResponse reads the next response for the current dialog.
	// Used after SendRequest("INVITE") to consume provisional (1xx) and final (2xx) responses.
	// Returns (response code, response header/reason, error).
	ReadResponse(ctx context.Context) (int, string, error)

	// SendKeepalive sends a NAT keepalive packet.
	SendKeepalive() error

	// Respond sends a SIP response to an incoming request (e.g. 100 Trying, 180 Ringing).
	Respond(code int, reason string)

	// OnDrop registers a callback that fires when the transport connection drops.
	OnDrop(fn func())

	// OnIncoming registers a callback for incoming SIP requests (e.g. INVITE).
	OnIncoming(fn func(from, to string))

	Close() error
}

// transport is the concrete SIP transport implementation.
// Real SIP logic will be added in a later phase.
type transport struct{}

func (t *transport) SendRequest(_ context.Context, _ string, _ map[string]string) (int, string, error) {
	return 0, "", errors.New("not implemented")
}

func (t *transport) ReadResponse(_ context.Context) (int, string, error) {
	return 0, "", errors.New("not implemented")
}

func (t *transport) SendKeepalive() error              { return nil }
func (t *transport) Respond(_ int, _ string)           {}
func (t *transport) OnDrop(_ func())                   {}
func (t *transport) OnIncoming(_ func(string, string)) {}
func (t *transport) Close() error                      { return nil }

// newTransport creates a new SIP transport from the given config.
func newTransport(cfg TransportConfig) (sipTransport, error) {
	switch cfg.Protocol {
	case "udp", "tcp":
		return &transport{}, nil
	case "tls":
		if cfg.TLSConfig == nil {
			return nil, ErrTLSConfigRequired
		}
		return &transport{}, nil
	default:
		return nil, fmt.Errorf("xphone: unsupported protocol %q", cfg.Protocol)
	}
}
