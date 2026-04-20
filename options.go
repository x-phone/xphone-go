package xphone

import (
	"crypto/tls"
	"log/slog"
	"net"
	"strconv"
	"strings"
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

	// AuthUsername is the default digest Authorization username. Applies to
	// REGISTER, SUBSCRIBE, MESSAGE, and outbound INVITE when no more specific
	// override is set (OutboundUsername for INVITE, per-call WithAuth for Dial).
	// When empty, Username is used. Set this when the PBX's auth identity
	// differs from the SIP AOR — for example, 3CX trunks where the
	// Authentication ID is not the extension. The SIP From/To/Contact headers
	// always use Username regardless.
	AuthUsername string

	RegisterExpiry   time.Duration
	RegisterRetry    time.Duration
	RegisterMaxRetry int

	// OutboundProxy is the SIP URI to route outbound requests through
	// (e.g. "sip:proxy.example.com:5060"). When set, REGISTER, SUBSCRIBE,
	// MESSAGE, and the initial INVITE are all sent to this address; the
	// Request-URI keeps pointing at Host so registration still targets the
	// registrar logically. In-dialog requests use the route set from
	// Record-Route. This matches how Kamailio / OpenSIPS / Asterisk
	// outbound-proxy deployments expect a single next-hop for all signaling.
	OutboundProxy string
	// OutboundUsername is the SIP digest username for outbound INVITE auth (401/407).
	// Falls back to Username if empty.
	OutboundUsername string
	// OutboundPassword is the SIP digest password for outbound INVITE auth (401/407).
	// Falls back to Password if empty.
	OutboundPassword string

	NATKeepaliveInterval time.Duration
	// NAT enables RFC 3581 rport on outgoing SIP requests. rport (response
	// port) instructs the server to send SIP responses back to the source
	// IP:port the request arrived on rather than the sent-by address in
	// the Via header. Required when the phone sits behind NAT (most dev
	// environments, containerized deployments) and the PBX would otherwise
	// reply to an unreachable port. Applies only to UDP transports.
	NAT bool

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

	// DisplayName is included in SIP Contact headers.
	// Helps PBXes identify the device (e.g., "VoiceApp/1.0").
	DisplayName string

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
	AuthUsername  string       // per-call SIP digest username (overrides config)
	AuthPassword  string       // per-call SIP digest password (overrides config)
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

// WithAuth sets per-call SIP digest credentials for 401/407 proxy
// authentication challenges. Overrides phone-level WithOutboundCredentials
// and WithCredentials for this single dial attempt.
func WithAuth(username, password string) DialOption {
	return func(o *DialOptions) {
		o.AuthUsername = username
		o.AuthPassword = password
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

// hasURIParam checks if a SIP URI contains a specific parameter (case-insensitive
// per RFC 3261 §19.1.1). Matches ";param" at end of string or followed by ";", ">", or "?".
func hasURIParam(uri, param string) bool {
	lower := strings.ToLower(uri)
	target := ";" + strings.ToLower(param)
	idx := strings.Index(lower, target)
	if idx < 0 {
		return false
	}
	end := idx + len(target)
	if end == len(lower) {
		return true
	}
	next := lower[end]
	return next == ';' || next == '>' || next == '?'
}

// extractHostPort splits an embedded "host:port" string into separate host and
// port values. If the port is valid and *port is still zero, it is set.
// The host string is always updated if splitting succeeds.
func extractHostPort(host *string, port *int) {
	if *host == "" {
		return
	}
	h, portStr, err := net.SplitHostPort(*host)
	if err != nil {
		return
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return
	}
	*host = h
	if *port == 0 {
		*port = p
	}
}

// normalizeHost splits an embedded port from cfg.Host (e.g. "10.0.0.7:5060")
// into the separate Host and Port fields. Only applies when Port is still at
// the zero value — an explicit Port setting takes precedence.
func normalizeHost(cfg *Config) { extractHostPort(&cfg.Host, &cfg.Port) }

// applyDefaults fills zero-value Config fields with sensible defaults.
func applyDefaults(cfg *Config) {
	normalizeHost(cfg)
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

// WithOutboundProxy sets the SIP URI to route all outbound requests
// through (REGISTER, SUBSCRIBE, MESSAGE, initial INVITE). In-dialog
// requests (re-INVITE, BYE) use the route set established via
// Record-Route. See Config.OutboundProxy for details.
// Example: "sip:proxy.example.com:5060"
func WithOutboundProxy(uri string) PhoneOption {
	return func(c *Config) {
		// Ensure loose-routing parameter is present (RFC 3261 §16.6).
		// Check for ";lr" at end of string or followed by ";" / ">" / "?"
		// to avoid false-matching parameters like ";lrfoo".
		if uri != "" && !hasURIParam(uri, "lr") {
			uri += ";lr"
		}
		c.OutboundProxy = uri
	}
}

// WithOutboundCredentials sets separate SIP digest credentials for outbound
// INVITE authentication (401/407 challenges). When unset, the registration
// credentials (Username/Password) are used for all requests.
func WithOutboundCredentials(username, password string) PhoneOption {
	return func(c *Config) {
		c.OutboundUsername = username
		c.OutboundPassword = password
	}
}

// WithAuthUsername sets Config.AuthUsername — the default digest Authorization
// username applied to REGISTER, SUBSCRIBE, MESSAGE, and outbound INVITE when
// no more specific override is set. See the field doc for precedence details.
func WithAuthUsername(username string) PhoneOption {
	return func(c *Config) {
		c.AuthUsername = username
	}
}

// WithNAT enables RFC 3581 rport on outgoing SIP requests. See Config.NAT.
// Use this when the phone sits behind NAT and the PBX must reply to the
// source IP:port of the request (the NAT-mapped address) rather than the
// sent-by address in the Via header.
func WithNAT() PhoneOption {
	return func(c *Config) {
		c.NAT = true
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

// WithDisplayName sets the display name for SIP Contact headers.
// Helps PBXes identify the device (e.g., "VoiceApp/1.0").
func WithDisplayName(name string) PhoneOption {
	return func(c *Config) {
		c.DisplayName = name
	}
}

// WithLogger sets the structured logger for the phone.
// If nil or not called, slog.Default() is used.
func WithLogger(l *slog.Logger) PhoneOption {
	return func(c *Config) {
		c.Logger = l
	}
}

// ServerConfig holds all configuration for a Server instance.
type ServerConfig struct {
	// Listen is the address to bind the SIP listener on (e.g. "0.0.0.0:5080").
	Listen string
	// Listener is an optional pre-created UDP socket for the SIP listener.
	// When set, the server uses this connection instead of creating its own,
	// giving the caller full control over socket creation (e.g. SO_REUSEPORT,
	// buffer sizes, fd passing for zero-downtime deploys). The server takes
	// ownership: it will close the connection when Listen returns. Listen is
	// ignored when Listener is set.
	Listener net.PacketConn
	// RTPPortMin is the minimum RTP port to allocate. 0 means OS-assigned.
	RTPPortMin int
	// RTPPortMax is the maximum RTP port to allocate. 0 means OS-assigned.
	RTPPortMax int
	// RTPAddress is the public IP to advertise in SDP when listening on 0.0.0.0.
	// If empty, the listen address or auto-detected local IP is used.
	RTPAddress string
	// SRTP enables SRTP media encryption (RTP/SAVP with AES_CM_128_HMAC_SHA1_80).
	SRTP bool
	// NAT enables RFC 3581 rport on outgoing SIP requests. See Config.NAT.
	NAT bool
	// CodecPrefs sets the codec preference order. Default: [PCMU].
	CodecPrefs []Codec
	// JitterBuffer sets the jitter buffer depth. Default: 50ms.
	JitterBuffer time.Duration
	// MediaTimeout sets the RTP inactivity timeout. Default: 30s.
	MediaTimeout time.Duration
	// PCMRate sets the output sample rate for PCMReader. Default: 8000.
	PCMRate int
	// DtmfMode controls DTMF signaling mode. Default: DtmfRfc4733.
	DtmfMode DtmfMode
	// Peers is the list of trusted SIP peers that can send/receive calls.
	Peers []PeerConfig
	// Logger sets the structured logger. Default: slog.Default().
	Logger *slog.Logger
}

// PeerConfig defines a trusted SIP peer for Server mode.
type PeerConfig struct {
	// Name is a human-readable identifier for this peer (e.g. "office-pbx", "twilio").
	Name string
	// Host is a single IP address for this peer. Checked during IP-based auth.
	Host string
	// Hosts is a list of IP addresses and/or CIDR ranges (e.g. "54.172.60.0/30").
	Hosts []string
	// Port is the SIP port for this peer. Default: 5060.
	Port int
	// Auth enables SIP digest authentication for this peer.
	Auth *PeerAuthConfig
	// RTPAddress overrides the server-level RTPAddress for this peer.
	// Use when this peer needs to see a different IP in SDP (e.g. different
	// network interface). Empty means use the server-level setting.
	RTPAddress string
	// Codecs restricts the codecs offered/accepted for this peer.
	// Empty means accept any codec from the server-level CodecPrefs.
	Codecs []Codec
}

// PeerAuthConfig holds SIP digest authentication credentials for a peer.
type PeerAuthConfig struct {
	Username string
	Password string
}

// normalizePeerHost splits an embedded port from PeerConfig.Host.
func normalizePeerHost(p *PeerConfig) { extractHostPort(&p.Host, &p.Port) }

// applyServerDefaults fills zero-value ServerConfig fields with sensible defaults.
func applyServerDefaults(cfg *ServerConfig) {
	if cfg.Listen == "" {
		cfg.Listen = "0.0.0.0:5060"
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
	for i := range cfg.Peers {
		normalizePeerHost(&cfg.Peers[i])
		if cfg.Peers[i].Port == 0 {
			cfg.Peers[i].Port = 5060
		}
	}
}
