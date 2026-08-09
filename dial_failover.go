package xphone

import (
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

// lookupHostFn is indirected so tests can stub DNS resolution.
var lookupHostFn = net.LookupHost

// dialSetupError marks a Dial failure local to this server — RTP port
// allocation, SRTP key generation — that no alternate peer address can fix.
// The failover loop stops on these instead of burning candidates.
type dialSetupError struct{ err error }

func (e *dialSetupError) Error() string { return e.err.Error() }
func (e *dialSetupError) Unwrap() error { return e.err }

// peerDialCandidates returns the ordered, deduplicated list of host:port dial
// targets for a peer: Host first, then each non-CIDR Hosts entry, with
// hostnames expanded to every IPv4 A record they resolve to (order preserved).
// A hostname that fails to resolve, or resolves to no IPv4, is kept verbatim
// so sipgo's own resolution can still try it — matching prior behavior.
func (s *server) peerDialCandidates(p *PeerConfig) []string {
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

	port := fmt.Sprintf("%d", p.Port)
	seenAddr := make(map[string]bool)
	var out []string
	addOut := func(hp string) {
		if !seenAddr[hp] {
			seenAddr[hp] = true
			out = append(out, hp)
		}
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			addOut(net.JoinHostPort(h, port))
			continue
		}
		addrs, err := lookupHostFn(h)
		if err != nil {
			addOut(net.JoinHostPort(h, port))
			continue
		}
		expanded := false
		for _, addr := range addrs {
			if ip := net.ParseIP(addr); ip != nil && ip.To4() != nil {
				addOut(net.JoinHostPort(addr, port))
				expanded = true
			}
		}
		if !expanded {
			addOut(net.JoinHostPort(h, port))
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
