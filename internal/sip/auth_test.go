package sip

import (
	"strings"
	"testing"
)

func TestParseChallenge_Basic(t *testing.T) {
	hdr := `Digest realm="asterisk",nonce="abc123",algorithm=MD5`
	ch, err := ParseChallenge(hdr)
	if err != nil {
		t.Fatalf("ParseChallenge() error: %v", err)
	}
	if ch.Realm != "asterisk" {
		t.Errorf("Realm = %q, want %q", ch.Realm, "asterisk")
	}
	if ch.Nonce != "abc123" {
		t.Errorf("Nonce = %q, want %q", ch.Nonce, "abc123")
	}
	if ch.Algorithm != "MD5" {
		t.Errorf("Algorithm = %q, want %q", ch.Algorithm, "MD5")
	}
}

func TestParseChallenge_WithQop(t *testing.T) {
	hdr := `Digest realm="sip.example.com",nonce="dcd98b",qop="auth",algorithm=MD5`
	ch, err := ParseChallenge(hdr)
	if err != nil {
		t.Fatalf("ParseChallenge() error: %v", err)
	}
	if ch.Realm != "sip.example.com" {
		t.Errorf("Realm = %q", ch.Realm)
	}
	if ch.Nonce != "dcd98b" {
		t.Errorf("Nonce = %q", ch.Nonce)
	}
	if ch.Qop != "auth" {
		t.Errorf("Qop = %q, want %q", ch.Qop, "auth")
	}
}

func TestParseChallenge_WithOpaque(t *testing.T) {
	hdr := `Digest realm="test",nonce="n1",opaque="op1",algorithm=MD5`
	ch, err := ParseChallenge(hdr)
	if err != nil {
		t.Fatalf("ParseChallenge() error: %v", err)
	}
	if ch.Opaque != "op1" {
		t.Errorf("Opaque = %q, want %q", ch.Opaque, "op1")
	}
}

func TestParseChallenge_NotDigest(t *testing.T) {
	_, err := ParseChallenge(`Basic realm="test"`)
	if err == nil {
		t.Fatal("expected error for non-Digest scheme")
	}
}

func TestParseChallenge_Empty(t *testing.T) {
	_, err := ParseChallenge("")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestDigestResponse_RFC2617(t *testing.T) {
	// Verify against known MD5 digest computation.
	// HA1 = MD5(username:realm:password)
	// HA2 = MD5(method:digestURI)
	// response = MD5(HA1:nonce:HA2)
	ch := &Challenge{
		Realm:     "asterisk",
		Nonce:     "abc123",
		Algorithm: "MD5",
	}
	creds := &Credentials{
		Username: "1001",
		Password: "test",
	}
	resp := DigestResponse(ch, creds, "REGISTER", "sip:pbx.local")
	if resp == "" {
		t.Fatal("DigestResponse returned empty")
	}
	// The response should be a 32-char hex MD5 hash.
	if len(resp) != 32 {
		t.Errorf("response length = %d, want 32", len(resp))
	}

	// Same inputs should produce same output (deterministic).
	resp2 := DigestResponse(ch, creds, "REGISTER", "sip:pbx.local")
	if resp != resp2 {
		t.Error("DigestResponse is not deterministic")
	}
}

func TestDigestResponse_DifferentMethod(t *testing.T) {
	ch := &Challenge{
		Realm: "asterisk",
		Nonce: "abc123",
	}
	creds := &Credentials{
		Username: "1001",
		Password: "test",
	}
	reg := DigestResponse(ch, creds, "REGISTER", "sip:pbx.local")
	inv := DigestResponse(ch, creds, "INVITE", "sip:pbx.local")
	if reg == inv {
		t.Error("different methods should produce different responses")
	}
}

func TestBuildAuthorizationHeader(t *testing.T) {
	ch := &Challenge{
		Realm: "asterisk",
		Nonce: "abc123",
	}
	creds := &Credentials{
		Username: "1001",
		Password: "test",
	}
	hdr := BuildAuthorization(ch, creds, "REGISTER", "sip:pbx.local")
	if hdr == "" {
		t.Fatal("BuildAuthorization returned empty")
	}
	// Should start with "Digest ".
	if hdr[:7] != "Digest " {
		t.Errorf("header prefix = %q, want %q", hdr[:7], "Digest ")
	}
	// Should contain username, realm, nonce, uri, response.
	for _, want := range []string{"username=", "realm=", "nonce=", "uri=", "response="} {
		if !strings.Contains(hdr, want) {
			t.Errorf("header missing %q: %s", want, hdr)
		}
	}
}

func TestBuildAuthorizationHeader_WithOpaque(t *testing.T) {
	ch := &Challenge{
		Realm:  "asterisk",
		Nonce:  "abc123",
		Opaque: "opaque-val",
	}
	creds := &Credentials{
		Username: "1001",
		Password: "test",
	}
	hdr := BuildAuthorization(ch, creds, "REGISTER", "sip:pbx.local")
	if !strings.Contains(hdr, "opaque=") {
		t.Errorf("header missing opaque: %s", hdr)
	}
}
