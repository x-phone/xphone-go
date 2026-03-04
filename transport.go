package xphone

import (
	"crypto/tls"
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
type sipTransport interface {
	Close() error
}

// transport is the concrete SIP transport implementation.
type transport struct{}

func (t *transport) Close() error {
	return nil
}

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
