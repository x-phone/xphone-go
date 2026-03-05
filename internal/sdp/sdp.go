package sdp

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

// Parse parses a raw SDP string into a Session.
func Parse(raw string) (*Session, error) {
	return &Session{Raw: raw}, nil // stub
}

// BuildOffer creates an SDP offer string.
func BuildOffer(ip string, port int, codecs []int, direction string) string {
	return "" // stub
}

// NegotiateCodec finds the first common codec between local preferences and
// remote offer. Returns -1 if no common codec found.
func NegotiateCodec(localPrefs []int, remoteOffer []int) int {
	return -1 // stub
}
