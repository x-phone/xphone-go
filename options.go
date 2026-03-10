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

	StunServer string
	SRTP       bool

	// TurnServer is the TURN server address (host:port) for relay allocation.
	// When set with TurnUsername/TurnPassword, the phone allocates a TURN relay
	// during call setup for symmetric NAT traversal.
	TurnServer   string
	TurnUsername string
	TurnPassword string

	// ICE enables ICE-Lite candidate gathering and STUN responder on the media socket.
	ICE bool

	RTPPortMin   int
	RTPPortMax   int
	CodecPrefs   []Codec
	JitterBuffer time.Duration
	MediaTimeout time.Duration
	PCMFrameSize int
	PCMRate      int
	DtmfMode     DtmfMode

	// VoicemailURI is the SIP URI to subscribe for MWI (RFC 3842).
	// Example: "sip:*97@pbx.local". When set, the phone auto-subscribes on connect.
	VoicemailURI string

	Logger *slog.Logger
}

// DtmfMode controls how DTMF digits are sent and received.
type DtmfMode int

const (
	// DtmfRfc4733 sends and receives DTMF via RFC 4733 RTP telephone-event packets (default).
	DtmfRfc4733 DtmfMode = iota
	// DtmfSipInfo sends and receives DTMF via SIP INFO with application/dtmf-relay body (RFC 2976).
	DtmfSipInfo
	// DtmfBoth sends DTMF via RFC 4733 RTP; also accepts incoming SIP INFO DTMF.
	DtmfBoth
)

// Codec represents an audio codec.
type Codec int

const (
	CodecPCMU Codec = 0
	CodecPCMA Codec = 8
	CodecG722 Codec = 9
	CodecG729 Codec = 18
	CodecOpus Codec = 111
)

// VideoCodec represents a video codec.
type VideoCodec int

const (
	VideoCodecH264 VideoCodec = 96
	VideoCodecVP8  VideoCodec = 97
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
	Video         bool         // enable video in SDP offer
	VideoCodecs   []VideoCodec // video codec preferences (default: [H264, VP8])
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

// WithVideo enables video in the SDP offer. If no codecs are specified,
// defaults to [H264, VP8] preference order.
func WithVideo(codecs ...VideoCodec) DialOption {
	return func(o *DialOptions) {
		o.Video = true
		if len(codecs) > 0 {
			o.VideoCodecs = codecs
		} else {
			o.VideoCodecs = []VideoCodec{VideoCodecH264, VideoCodecVP8}
		}
	}
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

// WithStunServer sets the STUN server for NAT-mapped address discovery.
// Format: "host:port" (e.g. "stun.l.google.com:19302").
// When set, the phone queries the STUN server during Connect() to discover
// its public IP, which is then used in SIP Contact headers and SDP.
func WithStunServer(server string) PhoneOption {
	return func(c *Config) {
		c.StunServer = server
	}
}

// WithTurnServer sets the TURN server for relay allocation (RFC 5766).
// Format: "host:port" (e.g. "turn.example.com:3478").
// Must be used with WithTurnCredentials.
func WithTurnServer(server string) PhoneOption {
	return func(c *Config) {
		c.TurnServer = server
	}
}

// WithTurnCredentials sets the TURN long-term credentials.
func WithTurnCredentials(username, password string) PhoneOption {
	return func(c *Config) {
		c.TurnUsername = username
		c.TurnPassword = password
	}
}

// WithICE enables ICE-Lite candidate gathering and STUN connectivity
// check responder on the media socket (RFC 8445 section 2.2).
func WithICE(enabled bool) PhoneOption {
	return func(c *Config) {
		c.ICE = enabled
	}
}

// WithSRTP enables SRTP (Secure RTP) for media encryption.
// When enabled, SDP offers use RTP/SAVP with AES_CM_128_HMAC_SHA1_80.
func WithSRTP() PhoneOption {
	return func(c *Config) {
		c.SRTP = true
	}
}

// WithPCMRate sets the output sample rate for PCMReader.
func WithPCMRate(rate int) PhoneOption {
	return func(c *Config) {
		c.PCMRate = rate
	}
}

// WithDtmfMode sets the DTMF signaling mode.
// Default is DtmfRfc4733 (RTP telephone-event packets).
func WithDtmfMode(mode DtmfMode) PhoneOption {
	return func(c *Config) {
		c.DtmfMode = mode
	}
}

// WithVoicemailURI sets the voicemail server URI for MWI SUBSCRIBE (RFC 3842).
// When set, the phone subscribes to voicemail notifications on Connect()
// and fires the OnVoicemail callback when the mailbox status changes.
// Example: "sip:*97@pbx.local"
func WithVoicemailURI(uri string) PhoneOption {
	return func(c *Config) {
		c.VoicemailURI = uri
	}
}

// WithLogger sets the structured logger for the phone.
// If nil or not called, slog.Default() is used.
func WithLogger(l *slog.Logger) PhoneOption {
	return func(c *Config) {
		c.Logger = l
	}
}
