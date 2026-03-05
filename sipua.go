package xphone

import (
	"context"
	"fmt"
	"sync"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/x-phone/xphone-go/internal/sdp"
)

// sipUA wraps sipgo's UserAgent and Client to implement sipTransport.
// It provides real SIP signaling for registration and call control.
type sipUA struct {
	mu     sync.Mutex // protects dropHandler, incomingHandler only; cfg is immutable after construction
	ua     *sipgo.UserAgent
	client *sipgo.Client
	dc     *sipgo.DialogClientCache // outbound dialog management
	cfg    Config                   // immutable after newSipUA returns

	dropHandler     func()
	incomingHandler func(from, to string)
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
		sipgo.WithUserAgent("xphone"),
	)
	if err != nil {
		return nil, fmt.Errorf("xphone: create UA: %w", err)
	}

	client, err := sipgo.NewClient(ua)
	if err != nil {
		ua.Close()
		return nil, fmt.Errorf("xphone: create client: %w", err)
	}

	contactHDR := sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: cfg.Username, Host: "0.0.0.0"},
	}
	dc := sipgo.NewDialogClientCache(client, contactHDR)

	return &sipUA{
		ua:     ua,
		client: client,
		dc:     dc,
		cfg:    cfg,
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

	// Build SDP offer.
	sdpOffer := sdp.BuildOffer("0.0.0.0", 0, defaultCodecPrefs(), sdp.DirSendRecv)

	// Create the dialog session and send INVITE.
	sess, err := s.dc.Invite(ctx, recipient, []byte(sdpOffer))
	if err != nil {
		return nil, fmt.Errorf("xphone: invite: %w", err)
	}

	// Ensure cleanup on any error after Invite succeeds.
	waitCtx, waitCancel := context.WithCancel(ctx)
	ok := false
	defer func() {
		if !ok {
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
		sess:     sess,
		invite:   sess.InviteRequest,
		response: sess.InviteResponse,
		cancelFn: waitCancel,
	}, nil
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

	// For 401, return WWW-Authenticate header value so caller can handle auth.
	if res.StatusCode == 401 {
		if h := res.GetHeader("WWW-Authenticate"); h != nil {
			return 401, h.Value(), nil
		}
		return 401, "", nil
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
	s.client.Close()
	return s.ua.Close()
}

// Ensure sipUA satisfies sipTransport at compile time.
var _ sipTransport = (*sipUA)(nil)
