package xphone

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// authResult represents the outcome of peer authentication.
type authResult struct {
	// Peer is the authenticated peer name. Set only when authenticated is true.
	Peer string
	// Authenticated is true when the source was matched to a peer.
	Authenticated bool
	// Challenge is true when the server should respond with 401 + WWW-Authenticate.
	Challenge bool
	// Nonce is the challenge nonce (set when Challenge is true).
	Nonce string
}

// digestAuthRealm is the SIP digest authentication realm used in challenges.
const digestAuthRealm = "xphone"

// peerMatcher holds pre-parsed IP and CIDR data for a single peer,
// avoiding repeated net.ParseIP/net.ParseCIDR on the hot path.
type peerMatcher struct {
	name    string
	ips     []net.IP     // pre-parsed single IPs (from Host + non-CIDR Hosts)
	cidrs   []*net.IPNet // pre-parsed CIDR ranges (from Hosts)
	auth    *PeerAuthConfig
	hasAuth bool
}

// buildPeerMatchers pre-parses all peer IP/CIDR data once at construction time.
func buildPeerMatchers(peers []PeerConfig) []peerMatcher {
	matchers := make([]peerMatcher, len(peers))
	for i, p := range peers {
		m := peerMatcher{
			name:    p.Name,
			auth:    p.Auth,
			hasAuth: p.Auth != nil,
		}
		if p.Host != "" {
			if ip := net.ParseIP(p.Host); ip != nil {
				m.ips = append(m.ips, ip)
			}
		}
		for _, h := range p.Hosts {
			if strings.Contains(h, "/") {
				if _, cidr, err := net.ParseCIDR(h); err == nil {
					m.cidrs = append(m.cidrs, cidr)
				}
			} else {
				if ip := net.ParseIP(h); ip != nil {
					m.ips = append(m.ips, ip)
				}
			}
		}
		matchers[i] = m
	}
	return matchers
}

// authenticatePeer attempts to authenticate an incoming SIP request against
// the configured peer list. It first tries IP-based matching, then falls
// back to SIP digest auth if any peers have Auth configured.
func authenticatePeer(matchers []peerMatcher, sourceIP string, authHeader string) authResult {
	ip := net.ParseIP(sourceIP)

	// Step 1: try IP-based matching.
	if ip != nil {
		for i := range matchers {
			if matchPeerIPFast(&matchers[i], ip) {
				return authResult{Peer: matchers[i].name, Authenticated: true}
			}
		}
	}

	// Step 2: try digest auth if Authorization header is present.
	if authHeader != "" {
		for i := range matchers {
			if matchers[i].auth == nil {
				continue
			}
			if verifyDigestAuth(matchers[i].auth, authHeader) {
				return authResult{Peer: matchers[i].name, Authenticated: true}
			}
		}
		// Credentials were presented but didn't match any peer — reject
		// without re-challenging (RFC 2617: bad credentials = reject).
		return authResult{}
	}

	// Step 3: no credentials presented — if any peers have digest auth, challenge.
	for i := range matchers {
		if matchers[i].hasAuth {
			nonce := generateNonce()
			return authResult{Challenge: true, Nonce: nonce}
		}
	}

	return authResult{}
}

// matchPeerIPFast checks if ip matches a peer's pre-parsed IPs or CIDRs.
func matchPeerIPFast(m *peerMatcher, ip net.IP) bool {
	for _, peerIP := range m.ips {
		if peerIP.Equal(ip) {
			return true
		}
	}
	for _, cidr := range m.cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// matchPeerIP checks if sourceIP matches a peer's Host or any of its Hosts entries.
// Used by tests that work with PeerConfig directly.
func matchPeerIP(p PeerConfig, sourceIP string) bool {
	ip := net.ParseIP(sourceIP)
	if ip == nil {
		return false
	}
	m := buildPeerMatchers([]PeerConfig{p})
	return matchPeerIPFast(&m[0], ip)
}

// verifyDigestAuth verifies a SIP Authorization header against a peer's credentials.
// Implements RFC 2617 digest authentication with MD5.
func verifyDigestAuth(auth *PeerAuthConfig, authHeader string) bool {
	if auth == nil {
		return false
	}

	params := parseDigestParams(authHeader)
	username := params["username"]
	nonce := params["nonce"]
	uri := params["uri"]
	response := params["response"]
	realm := params["realm"]

	if username == "" || nonce == "" || response == "" {
		return false
	}
	if username != auth.Username {
		return false
	}
	// Reject if the realm doesn't match our challenge realm.
	if realm != digestAuthRealm {
		return false
	}

	// HA1 = MD5(username:realm:password)
	ha1 := md5Hex(fmt.Sprintf("%s:%s:%s", username, realm, auth.Password))
	// HA2 = MD5(method:uri) — for INVITE, method is always INVITE
	ha2 := md5Hex(fmt.Sprintf("INVITE:%s", uri))
	// Expected response = MD5(HA1:nonce:HA2)
	expected := md5Hex(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))

	return response == expected
}

// parseDigestParams extracts key-value pairs from a Digest Authorization header.
// Example: Digest username="alice", realm="xphone", nonce="abc123", ...
func parseDigestParams(header string) map[string]string {
	params := make(map[string]string)
	header = strings.TrimSpace(header)
	if strings.HasPrefix(strings.ToLower(header), "digest ") {
		header = header[7:]
	}

	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(part[:eq])
		val := strings.TrimSpace(part[eq+1:])
		// Remove surrounding quotes.
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		params[strings.ToLower(key)] = val
	}
	return params
}

// md5Hex returns the hex-encoded MD5 hash of s.
func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

// generateNonce generates a random hex-encoded nonce for digest auth challenges.
func generateNonce() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// buildWWWAuthenticate builds a WWW-Authenticate header value for a 401 challenge.
func buildWWWAuthenticate(nonce string) string {
	return fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=MD5`, digestAuthRealm, nonce)
}
