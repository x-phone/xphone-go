package xphone

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/emiago/sipgo"
)

// Outbound multi-target failover for Server.Dial (#113, levels 1 and 2):
// walk PeerConfig.Host + the non-CIDR entries of PeerConfig.Hosts, expanding
// hostnames to their full IPv4 A-record sets instead of first-match-wins.
// Full RFC 3263 NAPTR/SRV resolution is deliberately out of scope here.

// lookupHostFn is indirected so tests can stub DNS resolution. It is
// context-aware so a slow or dead DNS server cannot stall Dial past the
// caller's cancellation or WithDialTimeout deadline.
var lookupHostFn = func(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// dialSetupError marks a Dial failure local to this server — RTP port
// allocation, SRTP key generation — that no alternate peer address can fix.
// The failover loop stops on these instead of burning candidates.
type dialSetupError struct{ err error }

func (e *dialSetupError) Error() string { return e.err.Error() }
func (e *dialSetupError) Unwrap() error { return e.err }

// dialCandidate is one failover target: uriHost goes into the SIP Request-URI
// (a hostname peer keeps its domain there — tenant routing, TLS SNI, and
// URI-based policy stay intact on every attempt), while dest, when non-empty,
// steers the transport to a specific resolved IP via Request.SetDestination.
type dialCandidate struct {
	uriHost string // host:port for the Request-URI
	dest    string // transport next-hop (ip:port); "" lets sipgo resolve the URI
}

// peerDialCandidates returns the ordered, deduplicated failover candidates
// for a peer: Host first, then each non-CIDR Hosts entry, with hostnames
// expanded to every IPv4 A record they resolve to (order preserved). The
// Request-URI keeps the configured hostname; only the transport destination
// varies per A record. A hostname that fails to resolve, or resolves to no
// IPv4, yields one candidate with no destination override so sipgo's own
// resolution can still try it — matching prior behavior.
// ctx bounds the DNS lookups (the dial context in production).
func (s *server) peerDialCandidates(ctx context.Context, p *PeerConfig) []dialCandidate {
	seenHost := make(map[string]bool)
	var hosts []string
	addHost := func(h string) {
		if h != "" && !seenHost[h] {
			seenHost[h] = true
			hosts = append(hosts, h)
		}
	}
	addHost(p.Host)
	for _, h := range p.Hosts {
		if !strings.Contains(h, "/") { // skip CIDR ranges — inbound-auth only
			addHost(h)
		}
	}

	defaultPort := fmt.Sprintf("%d", p.Port)
	// Dedup on the effective transport address (dest when set, else the URI
	// host): an IP listed literally and reached again via a hostname's A
	// record is the same wire target — dialing it twice buys nothing.
	seenWire := make(map[string]bool)
	var out []dialCandidate
	addOut := func(c dialCandidate) {
		wire := c.dest
		if wire == "" {
			wire = c.uriHost
		}
		if !seenWire[wire] {
			seenWire[wire] = true
			out = append(out, c)
		}
	}
	for _, h := range hosts {
		// A Hosts entry may carry its own port ("10.0.0.2:5070"); split it off
		// so we never produce host:port:port. Plain hosts and bare IPv6
		// literals make SplitHostPort error, which leaves the defaults.
		host, port := h, defaultPort
		if sh, sp, err := net.SplitHostPort(h); err == nil {
			host, port = sh, sp
		}
		if ip := net.ParseIP(host); ip != nil {
			// IP-literal config: the URI host is the IP, as it always was.
			addOut(dialCandidate{uriHost: net.JoinHostPort(host, port)})
			continue
		}
		uriHost := net.JoinHostPort(host, port)
		addrs, err := lookupHostFn(ctx, host)
		if err != nil {
			addOut(dialCandidate{uriHost: uriHost})
			continue
		}
		expanded := false
		for _, addr := range addrs {
			if ip := net.ParseIP(addr); ip != nil && ip.To4() != nil {
				addOut(dialCandidate{uriHost: uriHost, dest: net.JoinHostPort(addr, port)})
				expanded = true
			}
		}
		if !expanded {
			addOut(dialCandidate{uriHost: uriHost})
		}
	}
	return out
}

// retryableDialError reports whether a failed dial attempt justifies trying
// the next candidate. Local setup failures never do. A final SIP response
// justifies failover only for 408 (request timeout) and 5xx (server failure)
// — any other final response means the peer is alive and has made a decision
// (busy, decline, auth, 6xx global failure). Everything else — transport
// errors, per-hop silence timeouts, transaction timeouts — is retryable.
func retryableDialError(err error) bool {
	if err == nil {
		return false
	}
	var setup *dialSetupError
	if errors.As(err, &setup) {
		return false
	}
	// sipgo returns ErrDialogResponse both by value and by pointer.
	var resPtr *sipgo.ErrDialogResponse
	if errors.As(err, &resPtr) && resPtr.Res != nil {
		return retryableStatusCode(resPtr.Res.StatusCode)
	}
	var resVal sipgo.ErrDialogResponse
	if errors.As(err, &resVal) && resVal.Res != nil {
		return retryableStatusCode(resVal.Res.StatusCode)
	}
	return true
}

func retryableStatusCode(code int) bool {
	return code == 408 || (code >= 500 && code < 600)
}

// sawSuccessResponse reports whether a hop observed any 2xx final response.
// Once a callee has answered, failover must stop no matter what error follows
// (e.g. an ACK send failure) — re-INVITEing another address would ring a
// second endpoint for a call the first one already accepted.
func sawSuccessResponse(responses []sipResponseEntry) bool {
	for _, r := range responses {
		if r.code >= 200 && r.code < 300 {
			return true
		}
	}
	return false
}
