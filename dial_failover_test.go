package xphone

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/fakepbx"
	"github.com/x-phone/xphone-go/testutil"
)

// --- peerDialCandidates ---

func stubLookup(t *testing.T, table map[string][]string) {
	t.Helper()
	orig := lookupHostFn
	lookupHostFn = func(_ context.Context, host string) ([]string, error) {
		if addrs, ok := table[host]; ok {
			return addrs, nil
		}
		return nil, fmt.Errorf("stub: no such host %q", host)
	}
	t.Cleanup(func() { lookupHostFn = orig })
}

// drainCandidates collects every candidate the lazy iterator yields.
func drainCandidates(t *testing.T, s *server, p *PeerConfig) []dialCandidate {
	t.Helper()
	it := s.newDialCandidateIter(p)
	var out []dialCandidate
	for {
		c, ok := it.next(context.Background())
		if !ok {
			return out
		}
		out = append(out, c)
	}
}

func TestPeerDialCandidates_HostThenHostsDedupedCIDRSkipped(t *testing.T) {
	s := newServer(ServerConfig{})
	p := &PeerConfig{
		Host: "10.0.0.1",
		Port: 5060,
		Hosts: []string{
			"10.0.0.1",      // duplicate of Host
			"10.0.0.2",      // second endpoint
			"172.16.0.0/12", // CIDR — inbound auth only, never dialed
			"10.0.0.2",      // duplicate within Hosts
		},
	}
	got := drainCandidates(t, s, p)
	assert.Equal(t, []dialCandidate{{uriHost: "10.0.0.1:5060"}, {uriHost: "10.0.0.2:5060"}}, got)
}

func TestPeerDialCandidates_HostnameExpandsToAllIPv4s(t *testing.T) {
	stubLookup(t, map[string][]string{
		"sip.example.com": {"198.51.100.1", "2001:db8::1", "198.51.100.2"},
	})
	s := newServer(ServerConfig{})
	p := &PeerConfig{Host: "sip.example.com", Port: 5080}
	got := drainCandidates(t, s, p)
	// All IPv4 A records in DNS order; the IPv6 record is filtered out.
	// The Request-URI keeps the hostname; only the transport dest varies.
	assert.Equal(t, []dialCandidate{
		{uriHost: "sip.example.com:5080", dest: "198.51.100.1:5080"},
		{uriHost: "sip.example.com:5080", dest: "198.51.100.2:5080"},
	}, got)
}

func TestPeerDialCandidates_LookupFailureKeepsHostnameVerbatim(t *testing.T) {
	stubLookup(t, map[string][]string{}) // every lookup errors
	s := newServer(ServerConfig{})
	p := &PeerConfig{Host: "unresolvable.example.com", Port: 5060}
	got := drainCandidates(t, s, p)
	assert.Equal(t, []dialCandidate{{uriHost: "unresolvable.example.com:5060"}}, got)
}

func TestPeerDialCandidates_OnlyIPv6RecordsKeepsHostname(t *testing.T) {
	stubLookup(t, map[string][]string{
		"v6only.example.com": {"2001:db8::1"},
	})
	s := newServer(ServerConfig{})
	p := &PeerConfig{Host: "v6only.example.com", Port: 5060}
	got := drainCandidates(t, s, p)
	assert.Equal(t, []dialCandidate{{uriHost: "v6only.example.com:5060"}}, got)
}

func TestPeerDialCandidates_MixedLiteralAndHostnameDedupsAcross(t *testing.T) {
	stubLookup(t, map[string][]string{
		"trunk.example.com": {"10.0.0.1", "10.0.0.9"},
	})
	s := newServer(ServerConfig{})
	p := &PeerConfig{Host: "10.0.0.1", Port: 5060, Hosts: []string{"trunk.example.com"}}
	got := drainCandidates(t, s, p)
	// The literal 10.0.0.1 and the hostname's A record for the same IP are
	// BOTH kept: their Request-URIs differ (IP vs domain), and a domain URI
	// can route to a different virtual host on the same wire address.
	assert.Equal(t, []dialCandidate{
		{uriHost: "10.0.0.1:5060"},
		{uriHost: "trunk.example.com:5060", dest: "10.0.0.1:5060"},
		{uriHost: "trunk.example.com:5060", dest: "10.0.0.9:5060"},
	}, got)
}

func TestPeerDialCandidates_TwoDomainsSameIPBothKept(t *testing.T) {
	// codex-review regression: two SIP domains behind the same load-balancer
	// IP are different virtual hosts — after primary.example 503s, the
	// backup.example URI must still get its attempt.
	stubLookup(t, map[string][]string{
		"primary.example.com": {"198.51.100.5"},
		"backup.example.com":  {"198.51.100.5"},
	})
	s := newServer(ServerConfig{})
	p := &PeerConfig{Host: "primary.example.com", Port: 5060, Hosts: []string{"backup.example.com"}}
	got := drainCandidates(t, s, p)
	assert.Equal(t, []dialCandidate{
		{uriHost: "primary.example.com:5060", dest: "198.51.100.5:5060"},
		{uriHost: "backup.example.com:5060", dest: "198.51.100.5:5060"},
	}, got)
}

func TestPeerDialCandidates_HostsEntryWithEmbeddedPort(t *testing.T) {
	// agy-review regression: "10.0.0.2:5070" must keep its own port, not
	// become the un-dialable "10.0.0.2:5070:5060".
	stubLookup(t, map[string][]string{
		"alt.example.com": {"198.51.100.7"},
	})
	s := newServer(ServerConfig{})
	p := &PeerConfig{Host: "10.0.0.1", Port: 5060, Hosts: []string{"10.0.0.2:5070", "alt.example.com:5090"}}
	got := drainCandidates(t, s, p)
	assert.Equal(t, []dialCandidate{
		{uriHost: "10.0.0.1:5060"},
		{uriHost: "10.0.0.2:5070"},
		{uriHost: "alt.example.com:5090", dest: "198.51.100.7:5090"},
	}, got)
}

func TestPeerDialCandidates_BracketedIPv6NoDoubleBrackets(t *testing.T) {
	// agy-review: "[2001:db8::1]" without a port must not become
	// "[[2001:db8::1]]:5060".
	s := newServer(ServerConfig{})
	p := &PeerConfig{Host: "[2001:db8::1]", Port: 5060}
	got := drainCandidates(t, s, p)
	assert.Equal(t, []dialCandidate{{uriHost: "[2001:db8::1]:5060"}}, got)
}

// --- retryableDialError ---

func dialogResponseErr(code int) error {
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "x", Host: "h"})
	return &sipgo.ErrDialogResponse{Res: sip.NewResponseFromRequest(req, code, "x", nil)}
}

func TestRetryableDialError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"local setup", &dialSetupError{errors.New("no ports")}, false},
		{"503 service unavailable", dialogResponseErr(503), true},
		{"500", dialogResponseErr(500), true},
		{"408 request timeout", dialogResponseErr(408), true},
		{"486 busy", dialogResponseErr(486), false},
		{"404 not found", dialogResponseErr(404), false},
		{"401 auth", dialogResponseErr(401), false},
		{"603 decline (6xx global)", dialogResponseErr(603), false},
		{"transport error", errors.New("dial udp: connection refused"), true},
		{"context deadline (per-hop silence)", context.DeadlineExceeded, true},
		{"wrapped 502", fmt.Errorf("invite: %w", dialogResponseErr(502)), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, retryableDialError(tc.err), "err=%v", tc.err)
		})
	}
}

// --- Dial failover loop (via the dialOnceFn seam) ---

func failoverTestServer(t *testing.T, cfg ServerConfig) *server {
	t.Helper()
	if cfg.Peers == nil {
		cfg.Peers = []PeerConfig{{
			Name:  "trunk",
			Host:  "10.0.0.1",
			Hosts: []string{"10.0.0.2", "10.0.0.3"},
		}}
	}
	s := NewServer(cfg).(*server)
	s.mu.Lock()
	s.state = ServerStateListening
	s.mu.Unlock()
	t.Cleanup(func() {
		// Release any call registered by a successful Dial.
		s.mu.Lock()
		for _, c := range s.calls {
			c.onEndedCleanup = nil
		}
		s.calls = map[string]*call{}
		s.mu.Unlock()
	})
	return s
}

func TestServerDial_FailsOverOnTransportError(t *testing.T) {
	s := failoverTestServer(t, ServerConfig{})
	var targets []string
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		targets = append(targets, target)
		if len(targets) < 2 {
			return nil, errors.New("dial udp: no route to host")
		}
		onResponse(200, "OK")
		return testutil.NewMockDialog(), nil
	}

	c, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, []string{"sip:15551230100@10.0.0.1:5060", "sip:15551230100@10.0.0.2:5060"}, targets)
}

func TestServerDial_HostnamePeerKeepsDomainInRequestURI(t *testing.T) {
	// codex-review regression: for hostname peers the Request-URI must keep
	// the SIP domain on every attempt (tenant routing, TLS SNI, URI policy);
	// only the transport destination walks the A records.
	stubLookup(t, map[string][]string{
		"trunk.example.com": {"10.0.0.1", "10.0.0.2"},
	})
	s := failoverTestServer(t, ServerConfig{
		Peers: []PeerConfig{{Name: "trunk", Host: "trunk.example.com", Port: 5060}},
	})
	var targets, dests []string
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		targets = append(targets, target)
		dests = append(dests, dest)
		if len(targets) < 2 {
			return nil, errors.New("dial udp: no route to host")
		}
		return testutil.NewMockDialog(), nil
	}

	_, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"sip:15551230100@trunk.example.com:5060",
		"sip:15551230100@trunk.example.com:5060",
	}, targets, "Request-URI must keep the domain on every attempt")
	assert.Equal(t, []string{"10.0.0.1:5060", "10.0.0.2:5060"}, dests)
}

func TestServerDial_PrimaryDialedWithoutResolvingFallbackHostnames(t *testing.T) {
	// codex-review regression: candidates must resolve lazily. A hung or dead
	// DNS server for a FALLBACK hostname must never delay dialing the
	// already-known literal primary.
	orig := lookupHostFn
	lookupHostFn = func(context.Context, string) ([]string, error) {
		t.Fatal("fallback hostname resolved before the primary was dialed")
		return nil, nil
	}
	t.Cleanup(func() { lookupHostFn = orig })

	s := failoverTestServer(t, ServerConfig{
		Peers: []PeerConfig{{Name: "trunk", Host: "10.0.0.1", Port: 5060,
			Hosts: []string{"backup.example.com"}}},
	})
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		return testutil.NewMockDialog(), nil // primary succeeds immediately
	}

	_, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.NoError(t, err)
}

func TestServerDial_FailsOverOn503(t *testing.T) {
	s := failoverTestServer(t, ServerConfig{})
	var attempts int
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		attempts++
		if attempts == 1 {
			onResponse(503, "Service Unavailable")
			return nil, dialogResponseErr(503)
		}
		return testutil.NewMockDialog(), nil
	}

	_, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
}

func TestServerDial_StopsOnNonRetryableResponse(t *testing.T) {
	s := failoverTestServer(t, ServerConfig{})
	var attempts int
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		attempts++
		onResponse(486, "Busy Here")
		return nil, dialogResponseErr(486)
	}

	_, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.Error(t, err)
	assert.Equal(t, 1, attempts, "486 means the peer answered — no failover")
}

func TestServerDial_DisabledUsesSingleCandidate(t *testing.T) {
	s := failoverTestServer(t, ServerConfig{OutboundFailover: OutboundFailoverConfig{Disabled: true}})
	var attempts int
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		attempts++
		return nil, errors.New("transport down")
	}

	_, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.Error(t, err)
	assert.Equal(t, 1, attempts, "Disabled must preserve single-attempt behavior")
}

func TestServerDial_DisabledKeepsHostnameVerbatim(t *testing.T) {
	// codex-review regression: Disabled must dial the legacy target — the
	// hostname itself, unexpanded — so TLS SNI and domain routing behave
	// exactly as before the failover feature.
	lookupCalled := false
	orig := lookupHostFn
	lookupHostFn = func(context.Context, string) ([]string, error) {
		lookupCalled = true
		return []string{"198.51.100.1"}, nil
	}
	t.Cleanup(func() { lookupHostFn = orig })

	s := failoverTestServer(t, ServerConfig{
		OutboundFailover: OutboundFailoverConfig{Disabled: true},
		Peers:            []PeerConfig{{Name: "trunk", Host: "sip.example.com", Port: 5060}},
	})
	var target string
	s.dialOnceFn = func(ctx context.Context, tgt, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		target = tgt
		return testutil.NewMockDialog(), nil
	}

	_, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.NoError(t, err)
	assert.Equal(t, "sip:15551230100@sip.example.com:5060", target)
	assert.False(t, lookupCalled, "Disabled must not pay for DNS expansion")
}

func TestServerDial_MaxAttemptsCapsCandidates(t *testing.T) {
	s := failoverTestServer(t, ServerConfig{OutboundFailover: OutboundFailoverConfig{MaxAttempts: 2}})
	var attempts int
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		attempts++
		return nil, errors.New("transport down")
	}

	_, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.Error(t, err)
	assert.Equal(t, 2, attempts, "third candidate must not be tried")
}

func TestServerDial_StopsOnLocalSetupError(t *testing.T) {
	s := failoverTestServer(t, ServerConfig{})
	var attempts int
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		attempts++
		return nil, &dialSetupError{errors.New("no free RTP ports")}
	}

	_, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.Error(t, err)
	assert.Equal(t, 1, attempts, "local failures must not burn candidates")
}

func TestServerDial_SilentHopTimesOutAndFailsOver(t *testing.T) {
	s := failoverTestServer(t, ServerConfig{OutboundFailover: OutboundFailoverConfig{PerHopTimeout: 50 * time.Millisecond}})
	var attempts int
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		attempts++
		if attempts == 1 {
			<-ctx.Done() // completely silent hop: park until the per-hop guard fires
			return nil, ctx.Err()
		}
		return testutil.NewMockDialog(), nil
	}

	start := time.Now()
	_, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
	assert.Less(t, time.Since(start), 2*time.Second, "silent hop must be abandoned at PerHopTimeout, not Timer B")
}

func TestServerDial_ResponsiveHopDisarmsSilenceTimer(t *testing.T) {
	s := failoverTestServer(t, ServerConfig{OutboundFailover: OutboundFailoverConfig{PerHopTimeout: 40 * time.Millisecond}})
	var attempts int
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		attempts++
		onResponse(180, "Ringing") // disarms the silence guard
		select {
		case <-time.After(120 * time.Millisecond): // 3× PerHopTimeout of "ringing"
			return testutil.NewMockDialog(), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	_, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.NoError(t, err, "a hop that answered 180 must never be cut off by the silence timer")
	assert.Equal(t, 1, attempts)
}

func TestServerDial_LastCandidateNotCutOffBySilenceTimer(t *testing.T) {
	// codex-review regression: the silence guard exists to move on to a
	// fallback. The final candidate has none — it must be governed by the
	// caller's timeout, not abandoned at PerHopTimeout.
	s := failoverTestServer(t, ServerConfig{OutboundFailover: OutboundFailoverConfig{PerHopTimeout: 30 * time.Millisecond}})
	var attempts int
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		attempts++
		if attempts < 3 {
			<-ctx.Done() // hops 1 and 2: silent, abandoned by the guard
			return nil, ctx.Err()
		}
		// Hop 3 (last): silent well past PerHopTimeout, then answers.
		select {
		case <-time.After(90 * time.Millisecond):
			return testutil.NewMockDialog(), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	_, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.NoError(t, err, "last candidate must not be killed by the per-hop silence guard")
	assert.Equal(t, 3, attempts)
}

func TestServerDial_OverallTimeoutStopsFailover(t *testing.T) {
	s := failoverTestServer(t, ServerConfig{OutboundFailover: OutboundFailoverConfig{PerHopTimeout: time.Minute}})
	var attempts int
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		attempts++
		<-ctx.Done()
		return nil, ctx.Err()
	}

	_, err := s.Dial(context.Background(), "trunk", "15551230100", "", WithDialTimeout(60*time.Millisecond))
	require.Error(t, err)
	assert.Equal(t, 1, attempts, "overall dial timeout must not be spent walking more candidates")
}

func TestServerDial_NoFailoverAfterCalleeAnswered(t *testing.T) {
	// codex-review regression: a hop that got a 200 OK and then failed
	// (e.g. the ACK send errored) must NOT fail over — the callee already
	// accepted; a second INVITE would ring another endpoint for the same call.
	s := failoverTestServer(t, ServerConfig{})
	var attempts int
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		attempts++
		onResponse(100, "Trying")
		onResponse(200, "OK")
		return nil, errors.New("xphone: server ack: write udp: connection refused")
	}

	_, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.Error(t, err)
	assert.Equal(t, 1, attempts, "post-2xx failure must stop failover, not duplicate the call")
}

// TestServerDial_RealTransportSilentHopFailover exercises the failover path
// through sipgo's REAL WaitAnswer, not the dialOnceFn stub. codex-review
// regression: for a completely silent hop, sipgo honors RFC 3261 §9.1 and
// blocks until a provisional or Timer B (~32s) on a plain context cancel —
// the per-hop guard must use sipgo's WaitAnswerForceCancelErr cancel cause
// to abandon the transaction promptly. A stub cannot catch that.
func TestServerDial_RealTransportSilentHopFailover(t *testing.T) {
	skipUnderRace(t)
	// A UDP socket that swallows every packet: the silent first hop.
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { silent.Close() })
	silentAddr := silent.LocalAddr().String()

	// A fakepbx that answers: the healthy second hop.
	pbx := fakepbx.NewFakePBX(t)
	_, rtpPort := bindRTP(t)
	pbx.OnInvite(func(inv *fakepbx.Invite) {
		inv.Trying()
		inv.Answer(fakepbx.SDP("127.0.0.1", rtpPort, fakepbx.PCMA))
	})

	srv := listenServer(t, pbx, func(cfg *ServerConfig) {
		cfg.Peers[0].Hosts = []string{silentAddr, pbx.Addr()}
		cfg.Peers[0].Host = ""
		cfg.OutboundFailover.PerHopTimeout = 500 * time.Millisecond
	})
	startListening(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	c, err := srv.Dial(ctx, "test-peer", "9999", "+15559876543")
	elapsed := time.Since(start)
	require.NoError(t, err, "failover through the real transport failed")
	assert.Equal(t, StateActive, c.State())
	assert.Less(t, elapsed, 5*time.Second,
		"silent hop held Dial for %v — force-cancel cause not honored (Timer B leak)", elapsed)
	c.End()
}

func TestServerDial_OnlySuccessfulHopResponsesReachTheCall(t *testing.T) {
	s := failoverTestServer(t, ServerConfig{})
	var attempts int
	s.dialOnceFn = func(ctx context.Context, target, dest, from, rtpAddr string, peerCodecs []int, opts DialOptions, onResponse func(int, string)) (dialog, error) {
		attempts++
		if attempts == 1 {
			onResponse(503, "Service Unavailable")
			return nil, dialogResponseErr(503)
		}
		onResponse(183, "Session Progress")
		return testutil.NewMockDialog(), nil
	}

	c, err := s.Dial(context.Background(), "trunk", "15551230100", "")
	require.NoError(t, err)
	// The failed hop's 503 must not have been replayed into the call:
	// a 503 simulateResponse would have ended it.
	assert.NotEqual(t, StateEnded, c.State(), "dead hop's 503 leaked into the successful call")
}
