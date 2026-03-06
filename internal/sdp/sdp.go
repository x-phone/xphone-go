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
	Connection string      // IP from c= line
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

// MediaDesc describes a single media line in an SDP session.
type MediaDesc struct {
	Port      int
	Codecs    []int  // payload type numbers
	Direction string // DirSendRecv | DirSendOnly | DirRecvOnly | DirInactive
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
				for _, ptStr := range parts[3:] {
					if pt, err := strconv.Atoi(ptStr); err == nil {
						md.Codecs = append(md.Codecs, pt)
					}
				}
				s.Media = append(s.Media, md)
				curMedia = &s.Media[len(s.Media)-1]
			}
		case 'a':
			switch val {
			case DirSendRecv, DirSendOnly, DirRecvOnly, DirInactive:
				if curMedia != nil {
					curMedia.Direction = val
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
	b.WriteString(fmt.Sprintf("m=audio %d RTP/AVP", port))
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
		}
	}
	b.WriteString("a=")
	b.WriteString(direction)
	b.WriteString("\r\n")
	return b.String()
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
