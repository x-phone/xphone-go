package xphone

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/x-phone/xphone-go/internal/sdp"
	"github.com/x-phone/xphone-go/internal/srtp"
)

const (
	// reaperInterval is how often the stale call reaper runs.
	reaperInterval = 10 * time.Second
	// setupTTL is the max time a call can stay in Ringing/Dialing before being reaped.
	setupTTL = 30 * time.Second
	// activeTTL is the max time a call can stay active without media before being reaped.
	activeTTL = 4 * time.Hour
)

// ServerState represents the state of a Server instance.
type ServerState int

const (
	ServerStateStopped   ServerState = iota
	ServerStateListening             // Listen() active, accepting SIP
)

// Server is the public interface for SIP trunk/server mode.
// It accepts and places calls directly with trusted SIP peers (PBXes, trunk
// providers) without SIP registration. Both Server and Phone produce the same
// Call interface — the downstream API is identical.
type Server interface {
	Listen(ctx context.Context) error
	Shutdown() error
	Dial(ctx context.Context, peer string, to string, from string, opts ...DialOption) (Call, error)
	DialURI(ctx context.Context, uri string, from string, opts ...DialOption) (Call, error)
	OnIncoming(func(call Call))
	OnCallState(func(call Call, state CallState))
	OnCallEnded(func(call Call, reason EndReason))
	OnCallDTMF(func(call Call, digit string))
	OnError(func(err error))
	OnOptions(func() int)
	FindCall(callID string) Call
	Calls() []Call
	State() ServerState
}

type server struct {
	mu sync.Mutex

	cfg        ServerConfig
	logger     *slog.Logger
	localIP    string // IP to advertise in SDP c= line
	codecPrefs []int
	state      ServerState

	ua     *sipgo.UserAgent
	client *sipgo.Client
	srv    *sipgo.Server
	dc     *sipgo.DialogClientCache
	ds     *sipgo.DialogServerCache

	peerMatchers  []peerMatcher // pre-parsed IP/CIDR data for auth
	cancelListen  context.CancelFunc
	incomingFns   []func(Call)
	calls         map[string]*call
	callCreatedAt map[string]time.Time // tracks when each call was registered

	onCallStateFns []func(Call, CallState)
	onCallEndedFns []func(Call, EndReason)
	onCallDTMFFns  []func(Call, string)
	onErrorFns     []func(error)
	onOptionsFn    func() int
}

// NewServer creates a new Server with the given configuration.
func NewServer(cfg ServerConfig) Server {
	applyServerDefaults(&cfg)
	return newServer(cfg)
}

func newServer(cfg ServerConfig) *server {
	return &server{
		cfg:           cfg,
		logger:        resolveLogger(cfg.Logger),
		codecPrefs:    codecPrefsToInts(cfg.CodecPrefs),
		peerMatchers:  buildPeerMatchers(cfg.Peers),
		state:         ServerStateStopped,
		calls:         make(map[string]*call),
		callCreatedAt: make(map[string]time.Time),
	}
}

// Listen starts the SIP listener and blocks until ctx is cancelled or
// Shutdown is called. The server begins accepting incoming SIP requests
// from authenticated peers.
func (s *server) Listen(ctx context.Context) error {
	s.mu.Lock()
	if s.state != ServerStateStopped {
		s.mu.Unlock()
		return ErrAlreadyListening
	}
	s.mu.Unlock()

	// Determine the IP for SDP.
	s.localIP = s.resolveLocalIP()

	// Create sipgo stack.
	if err := s.setupSipStack(); err != nil {
		return err
	}

	// Register SIP handlers with peer auth.
	s.registerHandlers()

	listenCtx, listenCancel := context.WithCancel(ctx)
	defer listenCancel() // Ensure context-watcher goroutine exits on any return path.

	s.mu.Lock()
	s.cancelListen = listenCancel
	s.state = ServerStateListening
	s.mu.Unlock()

	s.logger.Info("server listening", "addr", s.listenAddr(), "rtp_address", s.localIP)

	// Start stale call reaper.
	go s.runReaper(listenCtx)

	// Block until the listener exits.
	var err error
	if s.cfg.Listener != nil {
		// ServeUDP doesn't accept a context; closing the conn is the only
		// way to unblock it (same approach sipgo uses in ListenAndServe).
		go func() {
			<-listenCtx.Done()
			s.cfg.Listener.Close()
		}()
		err = s.srv.ServeUDP(s.cfg.Listener)
	} else {
		err = s.srv.ListenAndServe(listenCtx, "udp4", s.cfg.Listen)
	}

	// Clean up.
	s.mu.Lock()
	s.state = ServerStateStopped
	s.cancelListen = nil
	s.mu.Unlock()

	s.closeSipStack()

	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// Shutdown gracefully stops the server, ending all active calls.
func (s *server) Shutdown() error {
	s.mu.Lock()
	cancel := s.cancelListen
	activeCalls := make([]*call, 0, len(s.calls))
	for _, c := range s.calls {
		activeCalls = append(activeCalls, c)
	}
	s.calls = make(map[string]*call)
	s.callCreatedAt = make(map[string]time.Time)
	s.mu.Unlock()

	for _, c := range activeCalls {
		c.End()
	}

	if cancel != nil {
		cancel()
	}
	return nil
}

// Dial initiates an outbound call to a named peer.
// peer is the PeerConfig.Name, to is the destination number, from is the caller ID.
func (s *server) Dial(ctx context.Context, peer string, to string, from string, opts ...DialOption) (Call, error) {
	s.mu.Lock()
	if s.state != ServerStateListening {
		s.mu.Unlock()
		return nil, ErrNotListening
	}
	s.mu.Unlock()

	peerCfg := s.findPeer(peer)
	if peerCfg == nil {
		return nil, fmt.Errorf("%w: %q", ErrPeerNotFound, peer)
	}

	dialOpts := applyDialOptions(opts)
	if from != "" {
		dialOpts.CallerID = from
	}

	var dialCtx context.Context
	var dialCancel context.CancelFunc
	if dialOpts.Timeout > 0 {
		dialCtx, dialCancel = context.WithTimeout(ctx, dialOpts.Timeout)
	} else {
		dialCtx, dialCancel = context.WithCancel(ctx)
	}
	defer dialCancel()

	peerAddr := s.resolvePeerAddr(peerCfg)
	target := fmt.Sprintf("sip:%s@%s", to, peerAddr)

	// Resolve per-peer RTP address and codecs.
	rtpAddr := s.resolveRTPAddressForPeer(peerCfg)
	var peerCodecs []int
	if len(peerCfg.Codecs) > 0 {
		peerCodecs = codecPrefsToInts(peerCfg.Codecs)
	}

	s.logger.Info("server dialing", "peer", peer, "to", to, "from", from, "target", target)

	var responses []sipResponseEntry

	dlg, err := s.dialOnce(dialCtx, target, from, rtpAddr, peerCodecs, dialOpts, func(code int, reason string) {
		responses = append(responses, sipResponseEntry{code, reason})
	})
	if err != nil {
		return nil, err
	}

	return s.setupOutboundCall(dlg, responses, opts...)
}

// DialURI initiates an outbound call to an arbitrary SIP URI.
// Unlike Dial, it does not require a pre-configured peer. Server-level
// RTP address and codec preferences are used.
func (s *server) DialURI(ctx context.Context, uri string, from string, opts ...DialOption) (Call, error) {
	if !strings.HasPrefix(uri, "sip:") && !strings.HasPrefix(uri, "sips:") {
		return nil, fmt.Errorf("xphone: invalid SIP URI: %q", uri)
	}
	if !strings.Contains(uri, "@") {
		return nil, fmt.Errorf("xphone: SIP URI has no user part: %q", uri)
	}

	s.mu.Lock()
	if s.state != ServerStateListening {
		s.mu.Unlock()
		return nil, ErrNotListening
	}
	s.mu.Unlock()

	dialOpts := applyDialOptions(opts)
	if from != "" {
		dialOpts.CallerID = from
	}

	var dialCtx context.Context
	var dialCancel context.CancelFunc
	if dialOpts.Timeout > 0 {
		dialCtx, dialCancel = context.WithTimeout(ctx, dialOpts.Timeout)
	} else {
		dialCtx, dialCancel = context.WithCancel(ctx)
	}
	defer dialCancel()

	s.logger.Info("server dialing URI", "uri", uri, "from", from)

	var responses []sipResponseEntry

	dlg, err := s.dialOnce(dialCtx, uri, from, s.localIP, nil, dialOpts, func(code int, reason string) {
		responses = append(responses, sipResponseEntry{code, reason})
	})
	if err != nil {
		return nil, err
	}

	return s.setupOutboundCall(dlg, responses, opts...)
}

// sipResponseEntry collects provisional SIP responses during dial.
type sipResponseEntry struct {
	code   int
	reason string
}

// setupOutboundCall wires an outbound dialog into a Call after dialOnce succeeds.
func (s *server) setupOutboundCall(dlg dialog, responses []sipResponseEntry, opts ...DialOption) (Call, error) {
	c := newOutboundCall(dlg, opts...)
	c.logger = s.logger
	c.localIP = s.localIP
	s.applyCallConfig(c)

	if uac, ok := dlg.(*sipgoDialogUAC); ok {
		if uac.rtpConn != nil {
			c.rtpConn = uac.rtpConn
			c.rtpPort = uac.rtpConn.LocalAddr().(*net.UDPAddr).Port
			uac.rtpConn = nil
		}
		if uac.invite != nil {
			c.localSDP = string(uac.invite.Body())
		}
		if uac.response != nil {
			body := uac.response.Body()
			if len(body) > 0 {
				c.remoteSDP = string(body)
				if sess, parseErr := sdp.Parse(c.remoteSDP); parseErr == nil {
					c.negotiateCodec(sess)
					c.setRemoteEndpoint(sess)
					if uac.srtpLocalKey != "" && sess.IsSRTP() {
						if crypto := sess.FirstCrypto(); crypto != nil {
							s.setupSRTP(c, uac.srtpLocalKey, crypto.InlineKey())
						}
					}
				}
			}
		}
	}

	s.registerCall(c)
	for _, r := range responses {
		c.simulateResponse(r.code, r.reason)
	}

	if c.rtpConn != nil {
		c.startMedia()
		c.startRTPReader()
	}

	return c, nil
}

func (s *server) OnIncoming(fn func(Call)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.incomingFns = append(s.incomingFns, fn)
}

func (s *server) OnCallState(fn func(Call, CallState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onCallStateFns = append(s.onCallStateFns, fn)
}

func (s *server) OnCallEnded(fn func(Call, EndReason)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onCallEndedFns = append(s.onCallEndedFns, fn)
}

func (s *server) OnCallDTMF(fn func(Call, string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onCallDTMFFns = append(s.onCallDTMFFns, fn)
}

func (s *server) OnError(fn func(error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onErrorFns = append(s.onErrorFns, fn)
}

// OnOptions sets a callback invoked on each SIP OPTIONS request. The callback
// returns a SIP status code (e.g. 200, 503). It must return quickly — a slow
// callback delays the response and may cause the SIP proxy to mark the server
// as down. When no callback is set, the server responds 200 OK.
func (s *server) OnOptions(fn func() int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onOptionsFn = fn
}

func (s *server) FindCall(callID string) Call {
	c := s.findCall(callID)
	if c == nil {
		return nil
	}
	return c
}

func (s *server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Call, 0, len(s.calls))
	for _, c := range s.calls {
		result = append(result, c)
	}
	return result
}

func (s *server) State() ServerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// --- internal ---

// sipReasonPhrase returns the standard SIP reason phrase for a status code.
func sipReasonPhrase(code int) string {
	switch code {
	case 200:
		return "OK"
	case 400:
		return "Bad Request"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 405:
		return "Method Not Allowed"
	case 408:
		return "Request Timeout"
	case 480:
		return "Temporarily Unavailable"
	case 486:
		return "Busy Here"
	case 500:
		return "Internal Server Error"
	case 503:
		return "Service Unavailable"
	default:
		return "Unknown"
	}
}

// listenAddr returns the effective SIP listen address — from the
// caller-provided Listener if set, otherwise from the Listen config.
func (s *server) listenAddr() string {
	if s.cfg.Listener != nil {
		return s.cfg.Listener.LocalAddr().String()
	}
	return s.cfg.Listen
}

// resolveLocalIP determines the IP to advertise in SDP.
func (s *server) resolveLocalIP() string {
	if s.cfg.RTPAddress != "" {
		return s.cfg.RTPAddress
	}
	host, _, err := net.SplitHostPort(s.listenAddr())
	if err == nil && host != "" && host != "0.0.0.0" && host != "::" {
		return host
	}
	// Auto-detect outbound interface IP.
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// setupSipStack creates the sipgo UA, Client, Server, and dialog caches.
func (s *server) setupSipStack() error {
	ua, err := sipgo.NewUA(
		sipgo.WithUserAgentHostname(s.localIP),
	)
	if err != nil {
		return fmt.Errorf("xphone: server create UA: %w", err)
	}

	clientOpts := []sipgo.ClientOption{sipgo.WithClientHostname(s.localIP)}
	if s.cfg.NAT {
		clientOpts = append(clientOpts, sipgo.WithClientNAT())
	}
	client, err := sipgo.NewClient(ua, clientOpts...)
	if err != nil {
		ua.Close()
		return fmt.Errorf("xphone: server create client: %w", err)
	}

	srv, err := sipgo.NewServer(ua)
	if err != nil {
		client.Close()
		ua.Close()
		return fmt.Errorf("xphone: server create server: %w", err)
	}

	contactPort := 0
	if _, p, err := net.SplitHostPort(s.listenAddr()); err == nil {
		if pn, err := strconv.Atoi(p); err == nil && pn != sip.DefaultUdpPort {
			contactPort = pn
		}
	}
	contactHDR := sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", Host: s.localIP, Port: contactPort},
	}
	dc := sipgo.NewDialogClientCache(client, contactHDR)
	ds := sipgo.NewDialogServerCache(client, contactHDR)

	s.mu.Lock()
	s.ua = ua
	s.client = client
	s.srv = srv
	s.dc = dc
	s.ds = ds
	s.mu.Unlock()

	return nil
}

// closeSipStack tears down sipgo resources.
func (s *server) closeSipStack() {
	s.mu.Lock()
	srv := s.srv
	client := s.client
	ua := s.ua
	s.srv = nil
	s.client = nil
	s.ua = nil
	s.dc = nil
	s.ds = nil
	s.mu.Unlock()

	if srv != nil {
		srv.Close()
	}
	if client != nil {
		client.Close()
	}
	if ua != nil {
		ua.Close()
	}
}

// registerHandlers wires sipgo server handlers with peer authentication.
func (s *server) registerHandlers() {
	s.srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		s.handleInvite(req, tx)
	})

	s.srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		s.mu.Lock()
		ds := s.ds
		s.mu.Unlock()
		if ds == nil {
			return
		}
		ds.ReadAck(req, tx)
	})

	s.srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		s.mu.Lock()
		ds, dc := s.ds, s.dc
		s.mu.Unlock()
		if ds == nil {
			res := sip.NewResponseFromRequest(req, 200, "OK", nil)
			tx.Respond(res)
			return
		}
		err := ds.ReadBye(req, tx)
		if err != nil && dc != nil {
			err = dc.ReadBye(req, tx)
		}
		if err != nil {
			res := sip.NewResponseFromRequest(req, 200, "OK", nil)
			tx.Respond(res)
			return
		}
		callID := ""
		if h := req.CallID(); h != nil {
			callID = h.Value()
		}
		if callID != "" {
			if c := s.findCall(callID); c != nil {
				c.simulateBye()
			}
		}
	})

	s.srv.OnCancel(func(req *sip.Request, tx sip.ServerTransaction) {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		tx.Respond(res)

		callID := ""
		if h := req.CallID(); h != nil {
			callID = h.Value()
		}
		if callID != "" {
			if c := s.findCall(callID); c != nil {
				c.simulateCancel()
			}
		}
	})

	s.srv.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		code := 200
		s.mu.Lock()
		fn := s.onOptionsFn
		s.mu.Unlock()
		if fn != nil {
			code = fn()
		}
		reason := sipReasonPhrase(code)
		res := sip.NewResponseFromRequest(req, code, reason, nil)
		tx.Respond(res)
	})

	s.srv.OnInfo(func(req *sip.Request, tx sip.ServerTransaction) {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		tx.Respond(res)

		ct := ""
		if h := req.ContentType(); h != nil {
			ct = strings.ToLower(h.Value())
		}
		if !strings.Contains(ct, "application/dtmf-relay") {
			return
		}
		digit := ParseInfoDTMF(string(req.Body()))
		if digit == "" {
			return
		}
		callID := ""
		if h := req.CallID(); h != nil {
			callID = h.Value()
		}
		if callID != "" {
			if c := s.findCall(callID); c != nil {
				c.mu.Lock()
				mode := c.dtmfMode
				c.mu.Unlock()
				if mode == DtmfSipInfo || mode == DtmfBoth {
					c.fireOnDTMF(digit)
				}
			}
		}
	})

	s.srv.OnNotify(func(req *sip.Request, tx sip.ServerTransaction) {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		tx.Respond(res)

		// REFER progress (message/sipfrag).
		code := parseSipfragStatus(string(req.Body()))
		if code > 0 {
			callID := ""
			if h := req.CallID(); h != nil {
				callID = h.Value()
			}
			if callID != "" {
				if c := s.findCall(callID); c != nil {
					c.dlg.FireNotify(code)
				}
			}
		}
	})
}

// handleInvite processes an inbound INVITE with peer authentication.
func (s *server) handleInvite(req *sip.Request, tx sip.ServerTransaction) {
	// Extract source IP from Via received param or request source.
	sourceIP := s.extractSourceIP(req)

	// Authenticate the peer.
	authHdr := ""
	if h := req.GetHeader("Authorization"); h != nil {
		authHdr = h.Value()
	}
	result := authenticatePeer(s.peerMatchers, sourceIP, authHdr)

	if result.Challenge {
		res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
		res.AppendHeader(sip.NewHeader("WWW-Authenticate", buildWWWAuthenticate(result.Nonce)))
		tx.Respond(res)
		return
	}
	if !result.Authenticated {
		s.logger.Warn("peer rejected", "source_ip", sourceIP)
		res := sip.NewResponseFromRequest(req, 403, "Forbidden", nil)
		tx.Respond(res)
		return
	}

	s.mu.Lock()
	ds := s.ds
	s.mu.Unlock()
	if ds == nil {
		return
	}

	sess, err := ds.ReadInvite(req, tx)
	if err != nil {
		return
	}

	callID := ""
	if h := req.CallID(); h != nil {
		callID = h.Value()
	}
	sdpBody := string(req.Body())

	// Check for re-INVITE.
	if callID != "" {
		if c := s.findCall(callID); c != nil {
			responder := &sipgoDialogUAS{
				dialogBase: dialogBase{sess: sess, invite: req},
			}
			c.handleReInvite(responder, sdpBody)
			waitDialogConfirmed(sess, tx)
			return
		}
	}

	// New INVITE — send 100 Trying.
	sess.Respond(100, "Trying", nil)

	from := ""
	if f := req.From(); f != nil {
		from = f.Address.String()
	}
	to := ""
	if t := req.To(); t != nil {
		to = t.Address.String()
	}

	dlg := &sipgoDialogUAS{
		dialogBase: dialogBase{sess: sess, invite: req},
	}

	go func() {
		s.handleDialogInvite(dlg, from, to, sdpBody, result.Peer)
	}()
	waitDialogConfirmed(sess, tx)
}

// handleDialogInvite creates an inbound call from an authenticated peer INVITE.
func (s *server) handleDialogInvite(dlg dialog, from, to, sdpBody, peerName string) {
	// Check handler BEFORE allocating resources.
	s.mu.Lock()
	incomingFns := make([]func(Call), len(s.incomingFns))
	copy(incomingFns, s.incomingFns)
	s.mu.Unlock()

	if len(incomingFns) == 0 {
		s.logger.Warn("no OnIncoming handler, rejecting call", "from", from)
		if err := dlg.Respond(480, "Temporarily Unavailable", nil); err != nil {
			s.logger.Error("failed to send 480", "err", err)
		}
		return
	}

	peerCfg := s.findPeer(peerName)

	c := newInboundCall(dlg)
	c.logger = s.logger
	c.localIP = s.resolveRTPAddressForPeer(peerCfg)
	c.rtpPortMin = s.cfg.RTPPortMin
	c.rtpPortMax = s.cfg.RTPPortMax
	s.applyCallConfig(c)
	// Apply per-peer codec filtering.
	if peerCfg != nil && len(peerCfg.Codecs) > 0 {
		c.codecPrefs = codecPrefsToInts(peerCfg.Codecs)
	}
	c.remoteSDP = sdpBody

	// Eagerly allocate RTP port. On failure, reject and clean up the callback
	// dispatcher goroutine spawned by newInboundCall.
	c.mu.Lock()
	if err := c.ensureRTPPort(); err != nil {
		c.mu.Unlock()
		s.logger.Error("RTP port allocation failed, rejecting INVITE", "from", from, "error", err)
		c.Reject(500, "Internal Server Error")
		return
	}
	c.mu.Unlock()

	s.registerCall(c)

	s.logger.Info("incoming call (server)", "peer", peerName, "from", from, "to", to)

	// Parse remote SDP for SRTP setup.
	if sdpBody != "" {
		if sess, err := sdp.Parse(sdpBody); err == nil {
			if s.cfg.SRTP && sess.IsSRTP() {
				if crypto := sess.FirstCrypto(); crypto != nil {
					localKey, err := srtp.GenerateKeyingMaterial()
					if err == nil {
						s.setupSRTP(c, localKey, crypto.InlineKey())
					}
				}
			}
			if vm := sess.VideoMedia(); vm != nil && len(vm.Codecs) > 0 {
				c.mu.Lock()
				if err := c.ensureVideoRTPPort(); err != nil {
					s.logger.Warn("video RTP port allocation failed, video disabled", "error", err)
				}
				c.mu.Unlock()
			}
		}
	}

	// Auto-send 180 Ringing.
	if err := dlg.Respond(180, "Ringing", nil); err != nil {
		s.logger.Error("failed to send 180 Ringing", "err", err)
	}

	for _, fn := range incomingFns {
		fn(c)
	}
}

// dialOnce sends a single INVITE to a peer target.
// rtpAddr is the IP to advertise in SDP (per-peer or server-level).
// peerCodecs overrides server-level codec prefs (nil means use server defaults).
func (s *server) dialOnce(ctx context.Context, target string, from string, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(code int, reason string)) (dialog, error) {
	recipient := parseSIPTarget(target, rtpAddr, 5060)

	// Build SDP offer.
	rtpConn, err := listenRTPPort(s.cfg.RTPPortMin, s.cfg.RTPPortMax)
	if err != nil {
		return nil, fmt.Errorf("xphone: server allocate RTP port: %w", err)
	}
	rtpPort := rtpConn.LocalAddr().(*net.UDPAddr).Port
	codecPT := peerCodecs
	if len(codecPT) == 0 {
		codecPT = s.codecPrefs
	}
	if len(codecPT) == 0 {
		codecPT = defaultCodecPrefs
	}

	var sdpOffer string
	var srtpLocalKey string
	if s.cfg.SRTP {
		key, err := srtp.GenerateKeyingMaterial()
		if err != nil {
			rtpConn.Close()
			return nil, fmt.Errorf("xphone: server generate SRTP key: %w", err)
		}
		srtpLocalKey = key
		sdpOffer = sdp.BuildOfferSRTP(rtpAddr, rtpPort, codecPT, sdp.DirSendRecv, key)
	} else {
		sdpOffer = sdp.BuildOffer(rtpAddr, rtpPort, codecPT, sdp.DirSendRecv)
	}

	// Set From header with caller ID.
	contentType := sip.NewHeader("Content-Type", contentTypeSDP)
	var extraHeaders []sip.Header
	extraHeaders = append(extraHeaders, contentType)
	if from != "" {
		fromHeader := &sip.FromHeader{
			Address: sip.Uri{Scheme: "sip", User: from, Host: s.localIP},
		}
		fromHeader.Params.Add("tag", sip.GenerateTagN(16))
		extraHeaders = append(extraHeaders, fromHeader)
	}

	sess, err := s.dc.Invite(ctx, recipient, []byte(sdpOffer), extraHeaders...)
	if err != nil {
		rtpConn.Close()
		return nil, fmt.Errorf("xphone: server invite: %w", err)
	}

	waitCtx, waitCancel := context.WithCancel(ctx)
	ok := false
	defer func() {
		if !ok {
			rtpConn.Close()
			waitCancel()
			sess.Close()
		}
	}()

	// Resolve per-call auth credentials (Server has no config-level fallback).
	authUser, authPass := resolveAuthCredentials(opts, Config{})

	err = sess.WaitAnswer(waitCtx, sipgo.AnswerOptions{
		OnResponse: func(res *sip.Response) error {
			if onResponse != nil {
				onResponse(res.StatusCode, res.Reason)
			}
			return nil
		},
		Username: authUser,
		Password: authPass,
	})
	if err != nil {
		return nil, err
	}

	if err := sess.Ack(ctx); err != nil {
		return nil, fmt.Errorf("xphone: server ack: %w", err)
	}

	ok = true
	return &sipgoDialogUAC{
		dialogBase: dialogBase{
			sess:     sess,
			invite:   sess.InviteRequest,
			response: sess.InviteResponse,
		},
		cancelFn:     waitCancel,
		rtpConn:      rtpConn,
		srtpLocalKey: srtpLocalKey,
	}, nil
}

// extractSourceIP extracts the source IP from a SIP request.
// Prefers the Via "received" parameter, then the network source address,
// and falls back to the Via host.
func (s *server) extractSourceIP(req *sip.Request) string {
	via := req.Via()
	// Check Via "received" parameter (set by transport layer per RFC 3261).
	if via != nil {
		if received, ok := via.Params.Get("received"); ok && received != "" {
			return received
		}
	}
	// Use the actual network source address from the request.
	if src := req.Source(); src != "" {
		host, _, err := net.SplitHostPort(src)
		if err == nil && host != "" {
			return host
		}
		return src
	}
	// Final fallback: Via host.
	if via != nil {
		return via.Host
	}
	return ""
}

// findPeer looks up a PeerConfig by name.
func (s *server) findPeer(name string) *PeerConfig {
	for i := range s.cfg.Peers {
		if s.cfg.Peers[i].Name == name {
			return &s.cfg.Peers[i]
		}
	}
	return nil
}

// resolveRTPAddressForPeer returns the IP to use in SDP for a given peer.
// Prefers peer-level RTPAddress, falls back to server-level.
func (s *server) resolveRTPAddressForPeer(p *PeerConfig) string {
	if p != nil && p.RTPAddress != "" {
		return p.RTPAddress
	}
	return s.localIP
}

// resolvePeerAddr returns the peer's SIP address as "host:port".
func (s *server) resolvePeerAddr(p *PeerConfig) string {
	host := p.Host
	if host == "" && len(p.Hosts) > 0 {
		// Use first non-CIDR host.
		for _, h := range p.Hosts {
			if !strings.Contains(h, "/") {
				host = h
				break
			}
		}
	}
	if host == "" {
		return ""
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", p.Port))
}

// registerCall adds a call to the active calls map and wires callbacks.
func (s *server) registerCall(c *call) {
	c.onEndedCleanup = func(_ EndReason) {
		s.untrackCall(c.CallID())
	}
	s.mu.Lock()
	callID := c.CallID()
	s.calls[callID] = c
	s.callCreatedAt[callID] = time.Now()
	s.wireCallCallbacks(c)
	s.mu.Unlock()
}

// untrackCall removes a call from the active calls map.
func (s *server) untrackCall(dialogID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.calls, dialogID)
	delete(s.callCreatedAt, dialogID)
}

// findCall looks up an active call by Call-ID.
func (s *server) findCall(callID string) *call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[callID]
}

// wireCallCallbacks hooks server-level callbacks onto a call. Must be called with s.mu held.
func (s *server) wireCallCallbacks(c *call) {
	wireCallCallbacks(c, s.composedOnCallState(), s.composedOnCallEnded(), s.composedOnCallDTMF())
}

// composedOnCallState returns a single function that calls all registered
// OnCallState callbacks, or nil if none are registered. Must be called with s.mu held.
func (s *server) composedOnCallState() func(Call, CallState) {
	if len(s.onCallStateFns) == 0 {
		return nil
	}
	fns := make([]func(Call, CallState), len(s.onCallStateFns))
	copy(fns, s.onCallStateFns)
	return func(call Call, state CallState) {
		for _, fn := range fns {
			fn(call, state)
		}
	}
}

// composedOnCallEnded returns a single function that calls all registered
// OnCallEnded callbacks, or nil if none are registered. Must be called with s.mu held.
func (s *server) composedOnCallEnded() func(Call, EndReason) {
	if len(s.onCallEndedFns) == 0 {
		return nil
	}
	fns := make([]func(Call, EndReason), len(s.onCallEndedFns))
	copy(fns, s.onCallEndedFns)
	return func(call Call, reason EndReason) {
		for _, fn := range fns {
			fn(call, reason)
		}
	}
}

// composedOnCallDTMF returns a single function that calls all registered
// OnCallDTMF callbacks, or nil if none are registered. Must be called with s.mu held.
func (s *server) composedOnCallDTMF() func(Call, string) {
	if len(s.onCallDTMFFns) == 0 {
		return nil
	}
	fns := make([]func(Call, string), len(s.onCallDTMFFns))
	copy(fns, s.onCallDTMFFns)
	return func(call Call, digit string) {
		for _, fn := range fns {
			fn(call, digit)
		}
	}
}

// applyCallConfig threads server-level config into a new call.
func (s *server) applyCallConfig(c *call) {
	c.mediaTimeout = s.cfg.MediaTimeout
	c.jitterDepth = s.cfg.JitterBuffer
	c.pcmRate = s.cfg.PCMRate
	c.codecPrefs = s.codecPrefs
	c.dtmfMode = s.cfg.DtmfMode
}

// setupSRTP is a convenience wrapper calling call.setupSRTP with the server logger.
func (s *server) setupSRTP(c *call, localKey, remoteKey string) {
	c.setupSRTP(s.logger, localKey, remoteKey)
}

// runReaper periodically cleans up stale calls that never completed setup
// or have been active beyond the TTL threshold.
func (s *server) runReaper(ctx context.Context) {
	ticker := time.NewTicker(reaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapStaleCalls()
		}
	}
}

// reapStaleCalls ends calls that exceeded their TTL.
func (s *server) reapStaleCalls() {
	now := time.Now()

	// Snapshot call refs and creation times under s.mu only — do NOT
	// acquire c.mu while holding s.mu (would risk deadlock if a call's
	// onEndedCleanup acquires s.mu while holding c.mu via dispatch).
	type callSnapshot struct {
		c         *call
		createdAt time.Time
	}
	s.mu.Lock()
	snaps := make([]callSnapshot, 0, len(s.calls))
	for callID, c := range s.calls {
		snaps = append(snaps, callSnapshot{c: c, createdAt: s.callCreatedAt[callID]})
	}
	s.mu.Unlock()

	// Now inspect each call's state outside s.mu.
	var stale []*call
	for _, snap := range snaps {
		snap.c.mu.Lock()
		state := snap.c.state
		start := snap.c.startTime
		snap.c.mu.Unlock()

		switch state {
		case StateRinging, StateDialing, StateRemoteRinging, StateEarlyMedia:
			if !snap.createdAt.IsZero() && now.Sub(snap.createdAt) > setupTTL {
				stale = append(stale, snap.c)
			}
		case StateActive, StateOnHold:
			if !start.IsZero() && now.Sub(start) > activeTTL {
				stale = append(stale, snap.c)
			}
		}
	}

	for _, c := range stale {
		s.logger.Warn("reaping stale call", "id", c.id, "state", c.State())
		c.End()
	}
}

// Ensure server satisfies Server at compile time.
var _ Server = (*server)(nil)
