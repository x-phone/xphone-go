package xphone

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/x-phone/xphone-go/internal/sdp"
	"github.com/x-phone/xphone-go/internal/srtp"
)

// parseSIPTarget parses a dial target into a sip.Uri.
// Accepts either a full SIP URI ("sip:1002@pbx.example.com") or a user-only
// string ("1002"), in which case defaultHost and defaultPort are used.
func parseSIPTarget(target, defaultHost string, defaultPort int) sip.Uri {
	if strings.HasPrefix(target, "sip:") || strings.HasPrefix(target, "sips:") {
		scheme := "sip"
		rest := target[4:]
		if strings.HasPrefix(target, "sips:") {
			scheme = "sips"
			rest = target[5:]
		}
		user := rest
		host := defaultHost
		port := defaultPort
		if at := strings.Index(rest, "@"); at >= 0 {
			user = rest[:at]
			hostPart := rest[at+1:]
			// Strip URI parameters (;transport=udp, etc.)
			if semi := strings.Index(hostPart, ";"); semi >= 0 {
				hostPart = hostPart[:semi]
			}
			// Parse host:port if present.
			if h, p, err := net.SplitHostPort(hostPart); err == nil {
				host = h
				port, _ = strconv.Atoi(p)
			} else {
				host = hostPart
				port = 0 // let sipgo resolve via SRV/default
			}
		}
		return sip.Uri{Scheme: scheme, User: user, Host: host, Port: port}
	}
	// Plain user part — combine with configured host/port.
	return sip.Uri{Scheme: "sip", User: target, Host: defaultHost, Port: defaultPort}
}

// sipUA wraps sipgo's UserAgent and Client to implement sipTransport.
// It provides real SIP signaling for registration and call control.
type sipUA struct {
	mu      sync.Mutex // protects dropHandler, incomingHandler, onDialogInvite only; cfg is immutable after construction
	ua      *sipgo.UserAgent
	client  *sipgo.Client
	server  *sipgo.Server
	dc      *sipgo.DialogClientCache // outbound dialog management
	ds      *sipgo.DialogServerCache // inbound dialog management
	cfg     Config                   // immutable after newSipUA returns
	localIP string                   // cached localIPFor(cfg.Host), immutable after construction

	dropHandler     func()
	incomingHandler func(from, to string)

	// onDialogInvite is called for inbound INVITEs with a fully-constructed
	// sipgoDialogUAS. Set by phone.Connect() before startServer().
	onDialogInvite func(dlg dialog, from, to, sdpBody string)

	// onDialogBye is called when an inbound BYE is received for a server dialog.
	// The phone uses this to transition the call to StateEnded.
	onDialogBye func(callID string)

	// onDialogCancel is called when an inbound CANCEL is received for a ringing call.
	// The phone uses this to transition the call to StateEnded.
	onDialogCancel func(callID string)

	// onDialogNotify is called when an inbound NOTIFY is received (REFER progress).
	// The phone uses this to fire the dialog's OnNotify callback.
	onDialogNotify func(callID string, code int)

	// onDialogInfo is called when an inbound INFO with application/dtmf-relay is received.
	// The phone uses this to fire DTMF callbacks on the call.
	onDialogInfo func(callID string, digit string)

	// onDialogReInvite is called when an inbound re-INVITE is received for an
	// existing dialog. Returns true if the call was found and handled.
	onDialogReInvite func(callID string, responder reInviteResponder, sdpBody string) bool

	// onNotify is the general NOTIFY callback for SUBSCRIBE/NOTIFY (RFC 6665).
	// Receives event, from, contentType, body, subscriptionState.
	onNotify func(event, from, contentType, body, subscriptionState string)

	// onMWINotify is called when an inbound NOTIFY with application/simple-message-summary
	// Content-Type is received (MWI). The callback receives the raw body string.
	onMWINotify func(body string)

	// onMessage is called when an inbound SIP MESSAGE is received (RFC 3428).
	onMessage func(from, to, contentType, body string)
}

// newSipUA creates a sipgo-backed SIP transport.
// contactIP is the IP to advertise in SIP Contact headers and Via.
// Typically this is the result of STUN discovery or localIPFor().
func newSipUA(cfg Config, contactIP string) (*sipUA, error) {
	if cfg.Host == "" {
		return nil, ErrHostRequired
	}
	switch cfg.Transport {
	case "udp", "tcp":
		// OK
	case "tls":
		if cfg.TLSConfig == nil {
			return nil, ErrTLSConfigRequired
		}
	default:
		return nil, fmt.Errorf("xphone: unsupported protocol %q", cfg.Transport)
	}

	ua, err := sipgo.NewUA(
		sipgo.WithUserAgent(cfg.Username),
		sipgo.WithUserAgentHostname(cfg.Host),
	)
	if err != nil {
		return nil, fmt.Errorf("xphone: create UA: %w", err)
	}

	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(contactIP))
	if err != nil {
		ua.Close()
		return nil, fmt.Errorf("xphone: create client: %w", err)
	}

	server, err := sipgo.NewServer(ua)
	if err != nil {
		client.Close()
		ua.Close()
		return nil, fmt.Errorf("xphone: create server: %w", err)
	}

	contactHDR := sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: cfg.Username, Host: contactIP},
	}
	dc := sipgo.NewDialogClientCache(client, contactHDR)
	ds := sipgo.NewDialogServerCache(client, contactHDR)

	return &sipUA{
		ua:      ua,
		client:  client,
		server:  server,
		dc:      dc,
		ds:      ds,
		cfg:     cfg,
		localIP: contactIP,
	}, nil
}

// maxRedirects is the maximum number of 3xx redirect hops before giving up.
const maxRedirects = 3

// dialWithOpts establishes an outbound SIP dialog with DialOptions.
// It delegates to dial, passing through video options for SDP construction.
func (s *sipUA) dialWithOpts(ctx context.Context, target string, opts DialOptions, onResponse func(code int, reason string)) (dialog, error) {
	currentTarget := target
	for redirects := 0; ; redirects++ {
		dlg, redirectURI, err := s.dialOnce(ctx, currentTarget, opts, onResponse)
		if err != nil {
			return nil, err
		}
		if redirectURI == "" {
			return dlg, nil
		}
		// 3xx redirect — retry with the new target.
		if redirects+1 > maxRedirects {
			return nil, fmt.Errorf("xphone: too many redirects (max %d)", maxRedirects)
		}
		currentTarget = redirectURI
	}
}

// dialOnce sends a single INVITE attempt. Returns (dialog, "", nil) on success,
// or (nil, redirectURI, nil) on 3xx redirect.
func (s *sipUA) dialOnce(ctx context.Context, target string, opts DialOptions, onResponse func(code int, reason string)) (dialog, string, error) {
	recipient := parseSIPTarget(target, s.cfg.Host, s.cfg.Port)

	// Build SDP offer with cached local IP and an allocated RTP port.
	ip := s.localIP
	rtpConn, err := listenRTPPort(s.cfg.RTPPortMin, s.cfg.RTPPortMax)
	if err != nil {
		return nil, "", fmt.Errorf("xphone: allocate RTP port: %w", err)
	}
	rtpPort := rtpConn.LocalAddr().(*net.UDPAddr).Port
	codecPT := codecPrefsToInts(s.cfg.CodecPrefs)
	if len(codecPT) == 0 {
		codecPT = defaultCodecPrefs
	}

	// Allocate video RTP port if video is requested.
	var videoRtpConn net.PacketConn
	var videoRtpPort int
	if opts.Video && len(opts.VideoCodecs) > 0 {
		videoRtpConn, err = listenRTPPort(s.cfg.RTPPortMin, s.cfg.RTPPortMax)
		if err != nil {
			rtpConn.Close()
			return nil, "", fmt.Errorf("xphone: allocate video RTP port: %w", err)
		}
		videoRtpPort = videoRtpConn.LocalAddr().(*net.UDPAddr).Port
	}
	videoCodecs := videoCodecPrefsToInts(opts.VideoCodecs)

	var sdpOffer string
	var srtpLocalKey string
	if s.cfg.SRTP {
		key, err := srtp.GenerateKeyingMaterial()
		if err != nil {
			rtpConn.Close()
			if videoRtpConn != nil {
				videoRtpConn.Close()
			}
			return nil, "", fmt.Errorf("xphone: generate SRTP key: %w", err)
		}
		srtpLocalKey = key
		if len(videoCodecs) > 0 && videoRtpPort > 0 {
			sdpOffer = sdp.BuildOfferVideoSRTP(ip, rtpPort, codecPT, videoRtpPort, videoCodecs, sdp.DirSendRecv, key)
		} else {
			sdpOffer = sdp.BuildOfferSRTP(ip, rtpPort, codecPT, sdp.DirSendRecv, key)
		}
	} else {
		if len(videoCodecs) > 0 && videoRtpPort > 0 {
			sdpOffer = sdp.BuildOfferVideo(ip, rtpPort, codecPT, videoRtpPort, videoCodecs, sdp.DirSendRecv)
		} else {
			sdpOffer = sdp.BuildOffer(ip, rtpPort, codecPT, sdp.DirSendRecv)
		}
	}

	// Create the dialog session and send INVITE.
	// Content-Type must be set explicitly — sipgo doesn't add it automatically,
	// and without it Asterisk treats the body as non-SDP (late offer).
	contentType := sip.NewHeader("Content-Type", contentTypeSDP)
	sess, err := s.dc.Invite(ctx, recipient, []byte(sdpOffer), contentType)
	if err != nil {
		rtpConn.Close()
		if videoRtpConn != nil {
			videoRtpConn.Close()
		}
		return nil, "", fmt.Errorf("xphone: invite: %w", err)
	}

	// Ensure cleanup on any error after Invite succeeds.
	waitCtx, waitCancel := context.WithCancel(ctx)
	ok := false
	defer func() {
		if !ok {
			rtpConn.Close()
			if videoRtpConn != nil {
				videoRtpConn.Close()
			}
			waitCancel()
			sess.Close()
		}
	}()

	// WaitAnswer blocks until 200 OK (or failure/cancel).
	// The OnResponse callback fires for each provisional and final response.
	err = sess.WaitAnswer(waitCtx, sipgo.AnswerOptions{
		OnResponse: func(res *sip.Response) error {
			if onResponse != nil {
				onResponse(res.StatusCode, res.Reason)
			}
			return nil
		},
		Username: s.cfg.Username,
		Password: s.cfg.Password,
	})
	if err != nil {
		// Check for 3xx redirect.
		var errResp *sipgo.ErrDialogResponse
		if errors.As(err, &errResp) && errResp.Res.StatusCode >= 300 && errResp.Res.StatusCode < 400 {
			contact := errResp.Res.Contact()
			if contact == nil {
				return nil, "", fmt.Errorf("xphone: %d redirect with no Contact header", errResp.Res.StatusCode)
			}
			newTarget := contact.Address.String()
			if newTarget == "" {
				return nil, "", fmt.Errorf("xphone: %d redirect with empty Contact URI", errResp.Res.StatusCode)
			}
			return nil, newTarget, nil
		}
		return nil, "", err
	}

	// ACK the 200 OK.
	if err := sess.Ack(ctx); err != nil {
		return nil, "", fmt.Errorf("xphone: ack: %w", err)
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
		videoRtpConn: videoRtpConn,
		srtpLocalKey: srtpLocalKey,
	}, "", nil
}

// waitDialogConfirmed waits until a sipgo dialog reaches Confirmed state,
// then terminates the transaction to avoid sipgo's 32-second timer_l delay.
func waitDialogConfirmed(sess *sipgo.DialogServerSession, tx sip.ServerTransaction) {
	stateCh := sess.StateRead()
	for state := range stateCh {
		if state >= sip.DialogStateConfirmed {
			break
		}
	}
	tx.Terminate()
}

// handleInvite processes an inbound INVITE, dispatching re-INVITEs to existing
// calls or creating new inbound call dialogs.
func (s *sipUA) handleInvite(req *sip.Request, tx sip.ServerTransaction) {
	sess, err := s.ds.ReadInvite(req, tx)
	if err != nil {
		return
	}

	callID := ""
	if h := req.CallID(); h != nil {
		callID = h.Value()
	}
	sdpBody := string(req.Body())

	// Check if this is a re-INVITE for an existing call.
	s.mu.Lock()
	reInvFn := s.onDialogReInvite
	s.mu.Unlock()
	if reInvFn != nil && callID != "" {
		responder := &sipgoDialogUAS{
			dialogBase: dialogBase{sess: sess, invite: req},
		}
		if reInvFn(callID, responder, sdpBody) {
			waitDialogConfirmed(sess, tx)
			return
		}
	}

	// Not a re-INVITE — handle as new INVITE.
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

	s.mu.Lock()
	fn := s.onDialogInvite
	s.mu.Unlock()
	if fn != nil {
		go fn(dlg, from, to, sdpBody)
		waitDialogConfirmed(sess, tx)
	}
}

// handleNotify processes an inbound NOTIFY, dispatching to MWI, REFER, or
// general SUBSCRIBE/NOTIFY handlers.
func (s *sipUA) handleNotify(req *sip.Request, tx sip.ServerTransaction) {
	res := sip.NewResponseFromRequest(req, 200, "OK", nil)
	tx.Respond(res)

	ct := ""
	if h := req.ContentType(); h != nil {
		ct = h.Value()
	}
	mediaType := ct
	if semi := strings.IndexByte(ct, ';'); semi >= 0 {
		mediaType = ct[:semi]
	}
	mediaType = strings.TrimSpace(mediaType)

	// MWI dispatch.
	if strings.EqualFold(mediaType, contentTypeMWI) {
		s.mu.Lock()
		fn := s.onMWINotify
		s.mu.Unlock()
		if fn != nil {
			go fn(string(req.Body()))
		}
		return
	}

	// REFER progress (message/sipfrag).
	code := parseSipfragStatus(string(req.Body()))
	if code > 0 {
		callID := ""
		if h := req.CallID(); h != nil {
			callID = h.Value()
		}
		s.mu.Lock()
		fn := s.onDialogNotify
		s.mu.Unlock()
		if fn != nil && callID != "" {
			go fn(callID, code)
		}
		return
	}

	// General NOTIFY dispatch (for SubscriptionManager).
	s.mu.Lock()
	generalFn := s.onNotify
	s.mu.Unlock()
	if generalFn != nil {
		from := ""
		if f := req.From(); f != nil {
			from = f.Address.String()
		}
		event := ""
		if h := req.GetHeader("Event"); h != nil {
			event = h.Value()
		}
		subState := ""
		if h := req.GetHeader("Subscription-State"); h != nil {
			subState = h.Value()
		}
		go generalFn(event, from, ct, string(req.Body()), subState)
	}
}

// startServer registers sipgo server handlers for inbound SIP requests.
// Must be called after onDialogInvite is set.
func (s *sipUA) startServer() {
	s.server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		s.handleInvite(req, tx)
	})

	s.server.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		s.ds.ReadAck(req, tx)
	})

	s.server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		// Try server dialogs first, then client dialogs.
		err := s.ds.ReadBye(req, tx)
		if err != nil {
			err = s.dc.ReadBye(req, tx)
		}
		if err != nil {
			// Respond 200 OK even if dialog not found, to stop retransmissions.
			res := sip.NewResponseFromRequest(req, 200, "OK", nil)
			tx.Respond(res)
			return
		}
		// Notify the call that a BYE was received.
		callID := ""
		if h := req.CallID(); h != nil {
			callID = h.Value()
		}
		s.mu.Lock()
		fn := s.onDialogBye
		s.mu.Unlock()
		if fn != nil && callID != "" {
			go fn(callID)
		}
	})

	s.server.OnCancel(func(req *sip.Request, tx sip.ServerTransaction) {
		// Respond 200 OK to the CANCEL request.
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		tx.Respond(res)

		var callID string
		if h := req.CallID(); h != nil {
			callID = h.Value()
		}
		s.mu.Lock()
		fn := s.onDialogCancel
		s.mu.Unlock()
		if fn != nil && callID != "" {
			go fn(callID)
		}
	})

	s.server.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		tx.Respond(res)
	})

	s.server.OnInfo(func(req *sip.Request, tx sip.ServerTransaction) {
		// Always respond 200 OK to INFO.
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		tx.Respond(res)

		// Only process application/dtmf-relay bodies.
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

		s.mu.Lock()
		fn := s.onDialogInfo
		s.mu.Unlock()
		if fn != nil && callID != "" {
			go fn(callID, digit)
		}
	})

	s.server.OnNotify(func(req *sip.Request, tx sip.ServerTransaction) {
		s.handleNotify(req, tx)
	})

	s.server.OnMessage(func(req *sip.Request, tx sip.ServerTransaction) {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		tx.Respond(res)

		from := ""
		if f := req.From(); f != nil {
			from = f.Address.String()
		}
		to := ""
		if t := req.To(); t != nil {
			to = t.Address.String()
		}
		ct := contentTypeTextPlain
		if h := req.ContentType(); h != nil {
			ct = h.Value()
		}

		s.mu.Lock()
		fn := s.onMessage
		s.mu.Unlock()
		if fn != nil {
			go fn(from, to, ct, string(req.Body()))
		}
	})
}

// --- sipTransport interface ---

// doWithAuth sends a SIP request and handles 401/407 digest auth challenges.
func (s *sipUA) doWithAuth(ctx context.Context, req *sip.Request, opts ...sipgo.ClientRequestOption) (int, string, error) {
	res, err := s.client.Do(ctx, req, opts...)
	if err != nil {
		return 0, "", err
	}
	if res.StatusCode == 401 || res.StatusCode == 407 {
		authRes, err := s.client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{
			Username: s.cfg.Username,
			Password: s.cfg.Password,
		})
		if err != nil {
			return 0, "", err
		}
		return authRes.StatusCode, authRes.Reason, nil
	}
	return res.StatusCode, res.Reason, nil
}

func (s *sipUA) SendRequest(ctx context.Context, method string, headers map[string]string) (int, string, error) {
	recipientUri := sip.Uri{
		Scheme: "sip",
		Host:   s.cfg.Host,
		Port:   s.cfg.Port,
	}
	req := sip.NewRequest(sip.RequestMethod(method), recipientUri)

	// Set From with our username.
	from := sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: s.cfg.Username, Host: s.cfg.Host},
	}
	from.Params.Add("tag", sip.GenerateTagN(16))
	req.AppendHeader(&from)

	// Set To.
	to := sip.ToHeader{
		Address: sip.Uri{Scheme: "sip", User: s.cfg.Username, Host: s.cfg.Host},
	}
	req.AppendHeader(&to)

	// Add Contact for REGISTER.
	if method == "REGISTER" {
		contact := sip.ContactHeader{
			Address: sip.Uri{Scheme: "sip", User: s.cfg.Username, Host: s.localIP},
		}
		req.AppendHeader(&contact)
	}

	// Add caller-provided headers (e.g., Authorization for auth retry).
	for k, v := range headers {
		req.AppendHeader(sip.NewHeader(k, v))
	}

	// Determine request build options.
	var opts []sipgo.ClientRequestOption
	if method == "REGISTER" {
		opts = append(opts, sipgo.ClientRequestRegisterBuild)
	}

	return s.doWithAuth(ctx, req, opts...)
}

// ReadResponse is unused in the production sipgo path (dialogs handle responses
// internally via WaitAnswer/OnResponse). It exists to satisfy sipTransport for
// the mock-based transportDial path used in tests.
func (s *sipUA) ReadResponse(_ context.Context) (int, string, error) {
	return 0, "", fmt.Errorf("xphone: ReadResponse not used in sipgo transport")
}

func (s *sipUA) SendKeepalive() error {
	// CRLF keepalive requires raw UDP access.
	// Will be wired via sipgo's transport layer in a later phase.
	return nil
}

func (s *sipUA) Respond(_ int, _ string) {
	// Stub — inbound call responses will be implemented in Phase D.
}

func (s *sipUA) OnDrop(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropHandler = fn
}

func (s *sipUA) OnIncoming(fn func(from, to string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.incomingHandler = fn
}

// buildOutboundRequest constructs a SIP request with From (tagged), To, and
// caller-provided headers. Shared by SendSubscribe and SendMessage.
func (s *sipUA) buildOutboundRequest(method sip.RequestMethod, uri string, headers map[string]string) *sip.Request {
	recipient := parseSIPTarget(uri, s.cfg.Host, s.cfg.Port)
	req := sip.NewRequest(method, recipient)

	from := sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: s.cfg.Username, Host: s.cfg.Host},
	}
	from.Params.Add("tag", sip.GenerateTagN(16))
	req.AppendHeader(&from)

	to := sip.ToHeader{Address: recipient}
	req.AppendHeader(&to)

	for k, v := range headers {
		req.AppendHeader(sip.NewHeader(k, v))
	}
	return req
}

func (s *sipUA) SendSubscribe(ctx context.Context, uri string, headers map[string]string) (int, string, error) {
	req := s.buildOutboundRequest(sip.SUBSCRIBE, uri, headers)

	contact := sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: s.cfg.Username, Host: s.localIP},
	}
	req.AppendHeader(&contact)

	return s.doWithAuth(ctx, req)
}

func (s *sipUA) OnNotify(fn func(event, from, contentType, body, subscriptionState string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onNotify = fn
}

func (s *sipUA) OnMWINotify(fn func(body string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onMWINotify = fn
}

func (s *sipUA) SendMessage(ctx context.Context, uri string, contentType string, body string, headers map[string]string) (int, string, error) {
	req := s.buildOutboundRequest(sip.MESSAGE, uri, headers)
	req.AppendHeader(sip.NewHeader("Content-Type", contentType))
	req.SetBody([]byte(body))
	return s.doWithAuth(ctx, req)
}

func (s *sipUA) OnMessage(fn func(from, to, contentType, body string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onMessage = fn
}

func (s *sipUA) Close() error {
	s.server.Close()
	s.client.Close()
	return s.ua.Close()
}

// parseSipfragStatus extracts the status code from a message/sipfrag body.
// Example: "SIP/2.0 200 OK" → 200
func parseSipfragStatus(body string) int {
	line := body
	if i := strings.Index(body, "\n"); i >= 0 {
		line = body[:i]
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "SIP/") {
		return 0
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 {
		return 0
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	return code
}

// Ensure sipUA satisfies sipTransport at compile time.
var _ sipTransport = (*sipUA)(nil)
