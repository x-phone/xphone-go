package sip

import (
	"crypto/md5"
	"errors"
	"fmt"
	"strings"
)

// Challenge represents a parsed WWW-Authenticate or Proxy-Authenticate header.
type Challenge struct {
	Realm     string
	Nonce     string
	Algorithm string
	Qop       string
	Opaque    string
}

// Credentials holds SIP authentication credentials.
type Credentials struct {
	Username string
	Password string
}

// ParseChallenge parses a WWW-Authenticate header value.
// Example: `Digest realm="asterisk",nonce="abc123",algorithm=MD5`
func ParseChallenge(header string) (*Challenge, error) {
	if header == "" {
		return nil, errors.New("sip: empty challenge header")
	}
	if !strings.HasPrefix(header, "Digest ") {
		return nil, errors.New("sip: unsupported auth scheme (only Digest supported)")
	}
	params := header[7:] // after "Digest "
	ch := &Challenge{}
	for _, part := range strings.Split(params, ",") {
		part = strings.TrimSpace(part)
		eqIdx := strings.IndexByte(part, '=')
		if eqIdx < 0 {
			continue
		}
		key := strings.TrimSpace(part[:eqIdx])
		val := strings.TrimSpace(part[eqIdx+1:])
		val = strings.Trim(val, `"`)
		switch strings.ToLower(key) {
		case "realm":
			ch.Realm = val
		case "nonce":
			ch.Nonce = val
		case "algorithm":
			ch.Algorithm = val
		case "qop":
			ch.Qop = val
		case "opaque":
			ch.Opaque = val
		}
	}
	return ch, nil
}

// DigestResponse computes the digest response hash per RFC 2617.
// HA1 = MD5(username:realm:password)
// HA2 = MD5(method:digestURI)
// response = MD5(HA1:nonce:HA2)
func DigestResponse(ch *Challenge, creds *Credentials, method, digestURI string) string {
	ha1 := md5Hex(creds.Username + ":" + ch.Realm + ":" + creds.Password)
	ha2 := md5Hex(method + ":" + digestURI)
	return md5Hex(ha1 + ":" + ch.Nonce + ":" + ha2)
}

// BuildAuthorization builds a complete Authorization header value.
func BuildAuthorization(ch *Challenge, creds *Credentials, method, digestURI string) string {
	resp := DigestResponse(ch, creds, method, digestURI)
	var b strings.Builder
	b.WriteString("Digest ")
	b.WriteString(fmt.Sprintf(`username="%s"`, creds.Username))
	b.WriteString(fmt.Sprintf(`,realm="%s"`, ch.Realm))
	b.WriteString(fmt.Sprintf(`,nonce="%s"`, ch.Nonce))
	b.WriteString(fmt.Sprintf(`,uri="%s"`, digestURI))
	b.WriteString(fmt.Sprintf(`,response="%s"`, resp))
	b.WriteString(`,algorithm=MD5`)
	if ch.Opaque != "" {
		b.WriteString(fmt.Sprintf(`,opaque="%s"`, ch.Opaque))
	}
	return b.String()
}

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", h)
}
