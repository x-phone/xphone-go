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

// MediaDesc describes a single media line in an SDP session.
type MediaDesc struct {
	Port      int
	Codecs    []int  // payload type numbers
	Direction string // "sendrecv" | "sendonly" | "recvonly" | "inactive"
}

// FirstCodec returns the first payload type from the first m= line, or -1 if none.
func (s *Session) FirstCodec() int {
	if len(s.Media) > 0 && len(s.Media[0].Codecs) > 0 {
		return s.Media[0].Codecs[0]
	}
	return -1
}

// Dir returns the media direction, defaulting to "sendrecv".
func (s *Session) Dir() string {
	if len(s.Media) > 0 && s.Media[0].Direction != "" {
		return s.Media[0].Direction
	}
	return "sendrecv"
}

var codecNames = map[int]string{
	0: "PCMU/8000", 8: "PCMA/8000", 9: "G722/8000", 111: "opus/48000/2",
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
			case "sendrecv", "sendonly", "recvonly", "inactive":
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
		}
	}
	b.WriteString("a=")
	b.WriteString(direction)
	b.WriteString("\r\n")
	return b.String()
}

// NegotiateCodec finds the first common codec between local preferences and
// remote offer. Returns -1 if no common codec found.
func NegotiateCodec(localPrefs []int, remoteOffer []int) int {
	for _, lc := range localPrefs {
		for _, rc := range remoteOffer {
			if lc == rc {
				return lc
			}
		}
	}
	return -1
}
