package xphone

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/x-phone/xphone-go/internal/sdp"
)

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
}

// newSipUA creates a sipgo-backed SIP transport.
func newSipUA(cfg Config) (*sipUA, error) {
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

	contactIP := localIPFor(cfg.Host)
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

// dial establishes an outbound SIP dialog using sipgo's dialog API.
// It sends INVITE with SDP, waits for the answer (handling provisionals via
// onResponse), sends ACK, and returns a sipgoDialogUAC.
func (s *sipUA) dial(ctx context.Context, target string, onResponse func(code int, reason string)) (dialog, error) {
	recipient := sip.Uri{
		Scheme: "sip",
		User:   target,
		Host:   s.cfg.Host,
		Port:   s.cfg.Port,
	}

	// Build SDP offer with cached local IP and an allocated RTP port.
	ip := s.localIP
	rtpConn, err := listenRTPPort(s.cfg.RTPPortMin, s.cfg.RTPPortMax)
	if err != nil {
		return nil, fmt.Errorf("xphone: allocate RTP port: %w", err)
	}
	rtpPort := rtpConn.LocalAddr().(*net.UDPAddr).Port
	sdpOffer := sdp.BuildOffer(ip, rtpPort, defaultCodecPrefs(), sdp.DirSendRecv)

	// Create the dialog session and send INVITE.
	// Content-Type must be set explicitly — sipgo doesn't add it automatically,
	// and without it Asterisk treats the body as non-SDP (late offer).
	contentType := sip.NewHeader("Content-Type", contentTypeSDP)
	sess, err := s.dc.Invite(ctx, recipient, []byte(sdpOffer), contentType)
	if err != nil {
		rtpConn.Close()
		return nil, fmt.Errorf("xphone: invite: %w", err)
	}

	// Ensure cleanup on any error after Invite succeeds.
	waitCtx, waitCancel := context.WithCancel(ctx)
	ok := false
	defer func() {
		if !ok {
			rtpConn.Close()
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
		return nil, err
	}

	// ACK the 200 OK.
	if err := sess.Ack(ctx); err != nil {
		return nil, fmt.Errorf("xphone: ack: %w", err)
	}

	ok = true
	return &sipgoDialogUAC{
		dialogBase: dialogBase{
			sess:     sess,
			invite:   sess.InviteRequest,
			response: sess.InviteResponse,
		},
		cancelFn: waitCancel,
		rtpConn:  rtpConn,
	}, nil
}

// startServer registers sipgo server handlers for inbound SIP requests.
// Must be called after onDialogInvite is set.
func (s *sipUA) startServer() {
	s.server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		sess, err := s.ds.ReadInvite(req, tx)
		if err != nil {
			return
		}

		// Send 100 Trying immediately to stop INVITE retransmissions.
		sess.Respond(100, "Trying", nil)

		// Extract From/To.
		from := ""
		if f := req.From(); f != nil {
			from = f.Address.String()
		}
		to := ""
		if t := req.To(); t != nil {
			to = t.Address.String()
		}

		dlg := &sipgoDialogUAS{
			dialogBase: dialogBase{
				sess:   sess,
				invite: req,
			},
		}

		// Extract SDP body from INVITE.
		sdpBody := string(req.Body())

		s.mu.Lock()
		fn := s.onDialogInvite
		s.mu.Unlock()
		if fn != nil {
			// Dispatch call handling in a goroutine so the user's OnIncoming
			// callback isn't blocked by the SIP server handler.
			go fn(dlg, from, to, sdpBody)

			// Wait until the dialog reaches Confirmed (ACK received after 200 OK)
			// or Ended. sipgo's Server calls tx.TerminateGracefully() after this
			// handler returns. For UDP, that blocks for timer_l (32s). By waiting
			// for Confirmed and then calling tx.Terminate(), we avoid the delay.
			stateCh := sess.StateRead()
			for state := range stateCh {
				if state >= sip.DialogStateConfirmed {
					break
				}
			}
			tx.Terminate()
		}
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
	})

	s.server.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		tx.Respond(res)
	})
}

// --- sipTransport interface ---

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
			Address: sip.Uri{Scheme: "sip", User: s.cfg.Username, Host: "0.0.0.0"},
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

	// Send and wait for final response (skips provisional 1xx).
	res, err := s.client.Do(ctx, req, opts...)
	if err != nil {
		return 0, "", err
	}

	// Handle 401/407 with digest authentication automatically.
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

func (s *sipUA) ReadResponse(_ context.Context) (int, string, error) {
	return 0, "", fmt.Errorf("xphone: ReadResponse not yet implemented")
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

func (s *sipUA) Close() error {
	s.server.Close()
	s.client.Close()
	return s.ua.Close()
}

// Ensure sipUA satisfies sipTransport at compile time.
var _ sipTransport = (*sipUA)(nil)
