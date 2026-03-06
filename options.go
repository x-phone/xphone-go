package xphone

import (
	"crypto/tls"
	"log/slog"
	"time"
)

// Config holds all configuration for a Phone instance.
type Config struct {
	Username  string
	Password  string
	Host      string
	Port      int
	Transport string
	TLSConfig *tls.Config

	RegisterExpiry   time.Duration
	RegisterRetry    time.Duration
	RegisterMaxRetry int

	NATKeepaliveInterval time.Duration

	RTPPortMin   int
	RTPPortMax   int
	CodecPrefs   []Codec
	JitterBuffer time.Duration
	MediaTimeout time.Duration
	PCMFrameSize int
	PCMRate      int

	Logger *slog.Logger
}

// Codec represents an audio codec.
type Codec int

const (
	CodecPCMU Codec = 0
	CodecPCMA Codec = 8
	CodecG722 Codec = 9
	CodecOpus Codec = 111
)

// DialOption is a functional option for Dial().
type DialOption func(*DialOptions)

// DialOptions holds configuration for an outbound call.
type DialOptions struct {
	CallerID      string
	CustomHeaders map[string]string
	EarlyMedia    bool
	Timeout       time.Duration
	CodecOverride []Codec
}

func applyDialOptions(opts []DialOption) DialOptions {
	o := DialOptions{
		Timeout: 30 * time.Second,
	}
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// WithCallerID sets the caller ID for an outbound call.
func WithCallerID(id string) DialOption {
	return func(o *DialOptions) { o.CallerID = id }
}

// WithHeader adds a custom SIP header to an outbound call.
func WithHeader(name, value string) DialOption {
	return func(o *DialOptions) {
		if o.CustomHeaders == nil {
			o.CustomHeaders = make(map[string]string)
		}
		o.CustomHeaders[name] = value
	}
}

// WithEarlyMedia enables early media (183 Session Progress).
func WithEarlyMedia() DialOption {
	return func(o *DialOptions) { o.EarlyMedia = true }
}

// WithDialTimeout sets the dial timeout for an outbound call.
func WithDialTimeout(d time.Duration) DialOption {
	return func(o *DialOptions) { o.Timeout = d }
}

// WithCodecOverride overrides the codec preference for an outbound call.
func WithCodecOverride(codecs ...Codec) DialOption {
	return func(o *DialOptions) { o.CodecOverride = codecs }
}

// AcceptOption is a functional option for Accept().
type AcceptOption func(*acceptOptions)

type acceptOptions struct{}

// PhoneOption is a functional option for New().
type PhoneOption func(*Config)

// New creates a new Phone with the given options.
// Unset fields receive sensible defaults.
func New(opts ...PhoneOption) Phone {
	cfg := Config{}
	for _, fn := range opts {
		fn(&cfg)
	}
	applyDefaults(&cfg)
	return newPhone(cfg)
}

// applyDefaults fills zero-value Config fields with sensible defaults.
func applyDefaults(cfg *Config) {
	if cfg.Transport == "" {
		cfg.Transport = "udp"
	}
	if cfg.Port == 0 {
		cfg.Port = 5060
	}
	if cfg.RegisterExpiry == 0 {
		cfg.RegisterExpiry = 60 * time.Second
	}
	if cfg.RegisterRetry == 0 {
		cfg.RegisterRetry = 1 * time.Second
	}
	if cfg.RegisterMaxRetry == 0 {
		cfg.RegisterMaxRetry = 3
	}
	if cfg.MediaTimeout == 0 {
		cfg.MediaTimeout = 30 * time.Second
	}
	if cfg.JitterBuffer == 0 {
		cfg.JitterBuffer = 50 * time.Millisecond
	}
	if cfg.PCMRate == 0 {
		cfg.PCMRate = 8000
	}
}

// WithCredentials sets the SIP username, password, and host.
func WithCredentials(username, password, host string) PhoneOption {
	return func(c *Config) {
		c.Username = username
		c.Password = password
		c.Host = host
	}
}

// WithTransport sets the SIP transport protocol and optional TLS config.
func WithTransport(protocol string, tlsCfg *tls.Config) PhoneOption {
	return func(c *Config) {
		c.Transport = protocol
		c.TLSConfig = tlsCfg
	}
}

// WithRTPPorts sets the RTP port range.
func WithRTPPorts(min, max int) PhoneOption {
	return func(c *Config) {
		c.RTPPortMin = min
		c.RTPPortMax = max
	}
}

// WithCodecs sets the codec preference order.
func WithCodecs(codecs ...Codec) PhoneOption {
	return func(c *Config) {
		c.CodecPrefs = codecs
	}
}

// WithJitterBuffer sets the jitter buffer depth.
func WithJitterBuffer(d time.Duration) PhoneOption {
	return func(c *Config) {
		c.JitterBuffer = d
	}
}

// WithMediaTimeout sets the RTP media timeout.
func WithMediaTimeout(d time.Duration) PhoneOption {
	return func(c *Config) {
		c.MediaTimeout = d
	}
}

// WithNATKeepalive sets the NAT keepalive interval (CRLF ping over UDP).
func WithNATKeepalive(d time.Duration) PhoneOption {
	return func(c *Config) {
		c.NATKeepaliveInterval = d
	}
}

// WithPCMRate sets the output sample rate for PCMReader.
func WithPCMRate(rate int) PhoneOption {
	return func(c *Config) {
		c.PCMRate = rate
	}
}

// WithLogger sets the structured logger for the phone.
// If nil or not called, slog.Default() is used.
func WithLogger(l *slog.Logger) PhoneOption {
	return func(c *Config) {
		c.Logger = l
	}
}
