package xphone

import (
	"fmt"
	"testing"
)

func TestMatchPeerIP_SingleHost(t *testing.T) {
	p := PeerConfig{Name: "test", Host: "10.0.0.1"}
	if !matchPeerIP(p, "10.0.0.1") {
		t.Fatal("expected match for exact IP")
	}
	if matchPeerIP(p, "10.0.0.2") {
		t.Fatal("expected no match for different IP")
	}
}

func TestMatchPeerIP_HostsList(t *testing.T) {
	p := PeerConfig{Name: "test", Hosts: []string{"10.0.0.1", "10.0.0.2"}}
	if !matchPeerIP(p, "10.0.0.1") {
		t.Fatal("expected match for first host")
	}
	if !matchPeerIP(p, "10.0.0.2") {
		t.Fatal("expected match for second host")
	}
	if matchPeerIP(p, "10.0.0.3") {
		t.Fatal("expected no match for unlisted IP")
	}
}

func TestMatchPeerIP_CIDR(t *testing.T) {
	p := PeerConfig{Name: "twilio", Hosts: []string{"54.172.60.0/30"}}
	// /30 covers 54.172.60.0 - 54.172.60.3
	if !matchPeerIP(p, "54.172.60.0") {
		t.Fatal("expected match for network address")
	}
	if !matchPeerIP(p, "54.172.60.1") {
		t.Fatal("expected match for first usable")
	}
	if !matchPeerIP(p, "54.172.60.3") {
		t.Fatal("expected match for broadcast")
	}
	if matchPeerIP(p, "54.172.60.4") {
		t.Fatal("expected no match outside CIDR")
	}
}

func TestMatchPeerIP_MixedHostsAndCIDR(t *testing.T) {
	p := PeerConfig{
		Name:  "mixed",
		Host:  "192.168.1.1",
		Hosts: []string{"10.0.0.0/24", "172.16.0.5"},
	}
	if !matchPeerIP(p, "192.168.1.1") {
		t.Fatal("expected match for Host")
	}
	if !matchPeerIP(p, "10.0.0.42") {
		t.Fatal("expected match for CIDR in Hosts")
	}
	if !matchPeerIP(p, "172.16.0.5") {
		t.Fatal("expected match for plain IP in Hosts")
	}
	if matchPeerIP(p, "172.16.0.6") {
		t.Fatal("expected no match for unlisted")
	}
}

func TestMatchPeerIP_InvalidSourceIP(t *testing.T) {
	p := PeerConfig{Name: "test", Host: "10.0.0.1"}
	if matchPeerIP(p, "not-an-ip") {
		t.Fatal("expected no match for invalid source IP")
	}
	if matchPeerIP(p, "") {
		t.Fatal("expected no match for empty source IP")
	}
}

func TestAuthenticatePeer_IPMatch(t *testing.T) {
	peers := []PeerConfig{
		{Name: "pbx", Host: "192.168.1.10"},
	}
	result := authenticatePeer(buildPeerMatchers(peers), "192.168.1.10", "")
	if !result.Authenticated || result.Peer != "pbx" {
		t.Fatalf("expected authenticated as pbx, got %+v", result)
	}
}

func TestAuthenticatePeer_Rejected(t *testing.T) {
	peers := []PeerConfig{
		{Name: "pbx", Host: "192.168.1.10"},
	}
	result := authenticatePeer(buildPeerMatchers(peers), "10.0.0.1", "")
	if result.Authenticated || result.Challenge {
		t.Fatalf("expected rejection, got %+v", result)
	}
}

func TestAuthenticatePeer_DigestChallenge(t *testing.T) {
	peers := []PeerConfig{
		{Name: "remote", Auth: &PeerAuthConfig{Username: "user", Password: "pass"}},
	}
	// No Authorization header → should challenge.
	result := authenticatePeer(buildPeerMatchers(peers), "10.0.0.1", "")
	if !result.Challenge {
		t.Fatalf("expected challenge, got %+v", result)
	}
	if result.Nonce == "" {
		t.Fatal("expected non-empty nonce")
	}
}

func TestAuthenticatePeer_DigestAuth(t *testing.T) {
	peers := []PeerConfig{
		{Name: "remote", Auth: &PeerAuthConfig{Username: "alice", Password: "secret"}},
	}

	// Build a valid Authorization header.
	realm := "xphone"
	nonce := "testnonce123"
	uri := "sip:+15551234567@example.com"
	ha1 := md5Hex(fmt.Sprintf("alice:%s:secret", realm))
	ha2 := md5Hex(fmt.Sprintf("INVITE:%s", uri))
	response := md5Hex(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))

	authHeader := fmt.Sprintf(
		`Digest username="alice", realm="%s", nonce="%s", uri="%s", response="%s"`,
		realm, nonce, uri, response,
	)

	result := authenticatePeer(buildPeerMatchers(peers), "10.0.0.1", authHeader)
	if !result.Authenticated || result.Peer != "remote" {
		t.Fatalf("expected authenticated as remote, got %+v", result)
	}
}

func TestAuthenticatePeer_DigestWrongPassword(t *testing.T) {
	peers := []PeerConfig{
		{Name: "remote", Auth: &PeerAuthConfig{Username: "alice", Password: "secret"}},
	}

	// Build Authorization with wrong password.
	realm := "xphone"
	nonce := "testnonce123"
	uri := "sip:+15551234567@example.com"
	ha1 := md5Hex(fmt.Sprintf("alice:%s:wrongpassword", realm))
	ha2 := md5Hex(fmt.Sprintf("INVITE:%s", uri))
	response := md5Hex(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))

	authHeader := fmt.Sprintf(
		`Digest username="alice", realm="%s", nonce="%s", uri="%s", response="%s"`,
		realm, nonce, uri, response,
	)

	result := authenticatePeer(buildPeerMatchers(peers), "10.0.0.1", authHeader)
	if result.Authenticated {
		t.Fatal("expected auth failure for wrong password")
	}
	// Bad credentials should be rejected without re-challenge (RFC 2617).
	if result.Challenge {
		t.Fatal("expected rejection without re-challenge for bad credentials")
	}
}

func TestAuthenticatePeer_DigestWrongRealm(t *testing.T) {
	peers := []PeerConfig{
		{Name: "remote", Auth: &PeerAuthConfig{Username: "alice", Password: "secret"}},
	}

	// Build Authorization with wrong realm.
	wrongRealm := "wrong-realm"
	nonce := "testnonce123"
	uri := "sip:+15551234567@example.com"
	ha1 := md5Hex(fmt.Sprintf("alice:%s:secret", wrongRealm))
	ha2 := md5Hex(fmt.Sprintf("INVITE:%s", uri))
	response := md5Hex(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))

	authHeader := fmt.Sprintf(
		`Digest username="alice", realm="%s", nonce="%s", uri="%s", response="%s"`,
		wrongRealm, nonce, uri, response,
	)

	result := authenticatePeer(buildPeerMatchers(peers), "10.0.0.1", authHeader)
	if result.Authenticated {
		t.Fatal("expected auth failure for wrong realm")
	}
	// Bad credentials — reject without re-challenge.
	if result.Challenge {
		t.Fatal("expected rejection without re-challenge for wrong realm")
	}
}

func TestAuthenticatePeer_IPTakesPriorityOverDigest(t *testing.T) {
	peers := []PeerConfig{
		{Name: "trusted-pbx", Host: "192.168.1.10"},
		{Name: "remote", Auth: &PeerAuthConfig{Username: "alice", Password: "secret"}},
	}
	// IP match should short-circuit without needing auth header.
	result := authenticatePeer(buildPeerMatchers(peers), "192.168.1.10", "")
	if !result.Authenticated || result.Peer != "trusted-pbx" {
		t.Fatalf("expected IP-based auth for trusted-pbx, got %+v", result)
	}
}

func TestParseDigestParams(t *testing.T) {
	header := `Digest username="alice", realm="xphone", nonce="abc123", uri="sip:foo@bar", response="deadbeef"`
	params := parseDigestParams(header)

	tests := map[string]string{
		"username": "alice",
		"realm":    "xphone",
		"nonce":    "abc123",
		"uri":      "sip:foo@bar",
		"response": "deadbeef",
	}
	for k, want := range tests {
		if got := params[k]; got != want {
			t.Errorf("param %q = %q, want %q", k, got, want)
		}
	}
}

func TestBuildWWWAuthenticate(t *testing.T) {
	got := buildWWWAuthenticate("abc123")
	want := `Digest realm="xphone", nonce="abc123", algorithm=MD5`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
