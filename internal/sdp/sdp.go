package sdp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Session represents a parsed SDP session description.
type Session struct {
	Origin     string
	Connection string // IP from c= line
	Media      []MediaDesc
	Raw        string // original SDP text
}

// SDP direction constants.
const (
	DirSendRecv = "sendrecv"
	DirSendOnly = "sendonly"
	DirRecvOnly = "recvonly"
	DirInactive = "inactive"
)

// SDP media profile constants.
const (
	ProfileRTP  = "RTP/AVP"
	ProfileSRTP = "RTP/SAVP"
)

// CryptoAttr represents an SDP a=crypto: attribute (RFC 4568).
type CryptoAttr struct {
	Tag       int
	Suite     string
	KeyParams string // inline:<base64key>
}

// MediaDesc describes a single media line in an SDP session.
type MediaDesc struct {
	Port      int
	Profile   string       // ProfileRTP or ProfileSRTP
	Codecs    []int        // payload type numbers
	Direction string       // DirSendRecv | DirSendOnly | DirRecvOnly | DirInactive
	Crypto    []CryptoAttr // a=crypto: attributes
}

// FirstCodec returns the first payload type from the first m= line, or -1 if none.
func (s *Session) FirstCodec() int {
	if len(s.Media) > 0 && len(s.Media[0].Codecs) > 0 {
		return s.Media[0].Codecs[0]
	}
	return -1
}

// Dir returns the media direction, defaulting to DirSendRecv.
func (s *Session) Dir() string {
	if len(s.Media) > 0 && s.Media[0].Direction != "" {
		return s.Media[0].Direction
	}
	return DirSendRecv
}

// IsSRTP returns true if the first media line uses RTP/SAVP profile.
func (s *Session) IsSRTP() bool {
	return len(s.Media) > 0 && s.Media[0].Profile == ProfileSRTP
}

// FirstCrypto returns the first crypto attribute from the first media line, or nil.
func (s *Session) FirstCrypto() *CryptoAttr {
	if len(s.Media) > 0 && len(s.Media[0].Crypto) > 0 {
		return &s.Media[0].Crypto[0]
	}
	return nil
}

var codecNames = map[int]string{
	0: "PCMU/8000", 8: "PCMA/8000", 9: "G722/8000", 101: "telephone-event/8000", 111: "opus/48000/2",
}

// Parse parses a raw SDP string into a Session.
func Parse(raw string) (*Session, error) {
	s := &Session{Raw: raw}
	hasVersion := false
	var curMedia *MediaDesc

	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if len(line) < 2 || line[1] != '=' {
			continue
		}
		key := line[0]
		val := line[2:]

		switch key {
		case 'v':
			hasVersion = true
		case 'o':
			s.Origin = val
		case 'c':
			// c=IN IP4 <ip>
			parts := strings.Fields(val)
			if len(parts) >= 3 {
				s.Connection = parts[2]
			}
		case 'm':
			// m=audio <port> RTP/AVP <pt...>
			parts := strings.Fields(val)
			if len(parts) >= 3 {
				md := MediaDesc{}
				md.Port, _ = strconv.Atoi(parts[1])
				md.Profile = parts[2]
				for _, ptStr := range parts[3:] {
					if pt, err := strconv.Atoi(ptStr); err == nil {
						md.Codecs = append(md.Codecs, pt)
					}
				}
				s.Media = append(s.Media, md)
				curMedia = &s.Media[len(s.Media)-1]
			}
		case 'a':
			switch {
			case val == DirSendRecv || val == DirSendOnly || val == DirRecvOnly || val == DirInactive:
				if curMedia != nil {
					curMedia.Direction = val
				}
			case strings.HasPrefix(val, "crypto:") && curMedia != nil:
				if ca, err := parseCryptoVal(val[7:]); err == nil {
					curMedia.Crypto = append(curMedia.Crypto, ca)
				}
			}
		}
	}

	if !hasVersion {
		return nil, errors.New("sdp: no v= line found")
	}
	return s, nil
}

// BuildOffer creates an SDP offer string.
func BuildOffer(ip string, port int, codecs []int, direction string) string {
	return buildSDP(ip, port, codecs, direction, ProfileRTP, "")
}

// BuildAnswer creates an SDP answer string that only includes codecs
// present in both localPrefs and remoteOffer (in local preference order).
// This complies with RFC 3264: the answer must be a subset of the offer.
func BuildAnswer(ip string, port int, localPrefs []int, remoteOffer []int, direction string) string {
	common := intersectCodecs(localPrefs, remoteOffer)
	if len(common) == 0 {
		// Fallback: include all local prefs (shouldn't happen in practice).
		common = localPrefs
	}
	return BuildOffer(ip, port, common, direction)
}

// intersectCodecs returns codecs present in both lists, in localPrefs order.
func intersectCodecs(localPrefs []int, remote []int) []int {
	set := make(map[int]bool, len(remote))
	for _, c := range remote {
		set[c] = true
	}
	var out []int
	for _, c := range localPrefs {
		if set[c] {
			out = append(out, c)
		}
	}
	return out
}

// NegotiateCodec finds the first common codec between local preferences and
// remote offer. Returns -1 if no common codec found.
func NegotiateCodec(localPrefs []int, remoteOffer []int) int {
	common := intersectCodecs(localPrefs, remoteOffer)
	if len(common) == 0 {
		return -1
	}
	return common[0]
}

// BuildOfferSRTP creates an SDP offer with RTP/SAVP and a=crypto: attribute.
func BuildOfferSRTP(ip string, port int, codecs []int, direction, cryptoInlineKey string) string {
	return buildSDP(ip, port, codecs, direction, ProfileSRTP, cryptoInlineKey)
}

// BuildAnswerSRTP creates an SDP answer with RTP/SAVP and a=crypto: attribute.
func BuildAnswerSRTP(ip string, port int, localPrefs []int, remoteOffer []int, direction, cryptoInlineKey string) string {
	common := intersectCodecs(localPrefs, remoteOffer)
	if len(common) == 0 {
		common = localPrefs
	}
	return buildSDP(ip, port, common, direction, ProfileSRTP, cryptoInlineKey)
}

// buildSDP is the shared implementation for building SDP with configurable profile.
func buildSDP(ip string, port int, codecs []int, direction, profile, cryptoInlineKey string) string {
	var b strings.Builder
	b.WriteString("v=0\r\n")
	b.WriteString("o=xphone 0 0 IN IP4 ")
	b.WriteString(ip)
	b.WriteString("\r\n")
	b.WriteString("s=xphone\r\n")
	b.WriteString("c=IN IP4 ")
	b.WriteString(ip)
	b.WriteString("\r\n")
	b.WriteString("t=0 0\r\n")
	b.WriteString(fmt.Sprintf("m=audio %d %s", port, profile))
	for _, c := range codecs {
		b.WriteString(fmt.Sprintf(" %d", c))
	}
	b.WriteString("\r\n")
	for _, c := range codecs {
		if name, ok := codecNames[c]; ok {
			b.WriteString(fmt.Sprintf("a=rtpmap:%d %s\r\n", c, name))
			if c == 101 {
				b.WriteString("a=fmtp:101 0-16\r\n")
			}
			if c == 111 {
				b.WriteString("a=fmtp:111 minptime=20;useinbandfec=0\r\n")
			}
		}
	}
	if cryptoInlineKey != "" {
		b.WriteString("a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:")
		b.WriteString(cryptoInlineKey)
		b.WriteString("\r\n")
	}
	b.WriteString("a=")
	b.WriteString(direction)
	b.WriteString("\r\n")
	return b.String()
}

// parseCryptoVal parses the value portion of an a=crypto: attribute.
// Format: "<tag> <suite> inline:<key>"
func parseCryptoVal(val string) (CryptoAttr, error) {
	parts := strings.Fields(val)
	if len(parts) < 3 {
		return CryptoAttr{}, fmt.Errorf("sdp: malformed crypto attribute")
	}
	tag, _ := strconv.Atoi(parts[0])
	suite := parts[1]
	keyParam := parts[2]
	return CryptoAttr{Tag: tag, Suite: suite, KeyParams: keyParam}, nil
}

// InlineKey extracts the base64 key from key params (removes "inline:" prefix).
func (c *CryptoAttr) InlineKey() string {
	if strings.HasPrefix(c.KeyParams, "inline:") {
		return c.KeyParams[7:]
	}
	return c.KeyParams
}
