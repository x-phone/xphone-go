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

// dialCandidateIter walks a peer's failover candidates LAZILY: Host first,
// then each non-CIDR Hosts entry, with hostnames expanded to every IPv4 A
// record they resolve to (order preserved). Laziness matters: a hostname is
// only resolved when its turn comes, so a hung DNS lookup for a fallback
// entry can never delay dialing an already-known primary address. The
// Request-URI keeps the configured hostname; only the transport destination
// varies per A record. A hostname that fails to resolve, or resolves to no
// IPv4, yields one candidate with no destination override so sipgo's own
// resolution can still try it — matching prior behavior. Candidates are
// deduplicated on the (Request-URI, wire address) pair: identical pairs are
// pointless repeats, but the same IP reached under two different SIP domains
// is two virtual hosts and both are tried.
type dialCandidateIter struct {
	entries  []hostPortEntry // config entries not yet resolved/emitted
	queue    []dialCandidate // expanded candidates awaiting emission
	seenPair map[string]bool
}

type hostPortEntry struct{ host, port string }

func (s *server) newDialCandidateIter(p *PeerConfig) *dialCandidateIter {
	defaultPort := fmt.Sprintf("%d", p.Port)
	seenRaw := make(map[string]bool)
	it := &dialCandidateIter{seenPair: make(map[string]bool)}
	add := func(h string) {
		if h == "" || seenRaw[h] {
			return
		}
		seenRaw[h] = true
		// An entry may carry its own port ("10.0.0.2:5070"); split it off so
		// we never produce host:port:port. Plain hosts and bare IPv6 literals
		// make SplitHostPort error, which leaves the defaults.
		host, port := h, defaultPort
		if sh, sp, err := net.SplitHostPort(h); err == nil {
			host, port = sh, sp
		}
		it.entries = append(it.entries, hostPortEntry{host, port})
	}
	add(p.Host)
	for _, h := range p.Hosts {
		if !strings.Contains(h, "/") { // skip CIDR ranges — inbound-auth only
			add(h)
		}
	}
	return it
}

// push queues a candidate unless the same (Request-URI, wire address) pair
// was already produced. The URI is part of the key deliberately: two SIP
// domains resolving to the same load-balancer IP are different virtual
// hosts, and each deserves its own attempt.
func (it *dialCandidateIter) push(c dialCandidate) {
	key := c.uriHost + "|" + c.dest
	if !it.seenPair[key] {
		it.seenPair[key] = true
		it.queue = append(it.queue, c)
	}
}

// next returns the following candidate, resolving hostnames on demand.
// ctx bounds the DNS lookup (the dial context in production).
func (it *dialCandidateIter) next(ctx context.Context) (dialCandidate, bool) {
	for {
		if len(it.queue) > 0 {
			c := it.queue[0]
			it.queue = it.queue[1:]
			return c, true
		}
		if len(it.entries) == 0 {
			return dialCandidate{}, false
		}
		e := it.entries[0]
		it.entries = it.entries[1:]
		if ip := net.ParseIP(e.host); ip != nil {
			// IP-literal config: the URI host is the IP, as it always was.
			it.push(dialCandidate{uriHost: net.JoinHostPort(e.host, e.port)})
			continue
		}
		uriHost := net.JoinHostPort(e.host, e.port)
		addrs, err := lookupHostFn(ctx, e.host)
		if err != nil {
			it.push(dialCandidate{uriHost: uriHost})
			continue
		}
		expanded := false
		for _, addr := range addrs {
			if ip := net.ParseIP(addr); ip != nil && ip.To4() != nil {
				it.push(dialCandidate{uriHost: uriHost, dest: net.JoinHostPort(addr, e.port)})
				expanded = true
			}
		}
		if !expanded {
			it.push(dialCandidate{uriHost: uriHost})
		}
	}
}

// hasMore reports whether another candidate may follow. It can overestimate —
// a pending hostname might resolve entirely to already-produced (URI, dest)
// pairs, in which case a hop abandoned by the silence guard turns out to have
// been the last one. That corner trades against never letting a fallback's
// dead DNS block the primary; the guard's error is still surfaced to the
// caller.
func (it *dialCandidateIter) hasMore() bool {
	return len(it.queue) > 0 || len(it.entries) > 0
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
