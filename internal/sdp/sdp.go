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

	IceUfrag string // a=ice-ufrag (session or media level)
	IcePwd   string // a=ice-pwd (session or media level)
	IceLite  bool   // a=ice-lite (session level)
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

// Media type constants.
const (
	MediaAudio = "audio"
	MediaVideo = "video"
)

// MediaDesc describes a single media line in an SDP session.
type MediaDesc struct {
	Type       string // MediaAudio or MediaVideo
	Port       int
	Profile    string           // ProfileRTP or ProfileSRTP
	Codecs     []int            // payload type numbers
	Direction  string           // DirSendRecv | DirSendOnly | DirRecvOnly | DirInactive
	Crypto     []CryptoAttr     // a=crypto: attributes
	Candidates []string         // a=candidate: values (raw, without prefix)
	Fmtp       map[int]string   // a=fmtp:<pt> <params> per payload type
	RtcpFb     map[int][]string // a=rtcp-fb:<pt> <value> per payload type
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
	0: "PCMU/8000", 8: "PCMA/8000", 9: "G722/8000", 18: "G729/8000", 101: "telephone-event/8000", 111: "opus/48000/2",
	96: "H264/90000", 97: "VP8/90000",
}

// codecFmtp maps payload types to their default fmtp parameters.
var codecFmtp = map[int]string{
	18:  "annexb=no",
	96:  "profile-level-id=42e01f;packetization-mode=1",
	101: "0-16",
	111: "minptime=20;useinbandfec=0",
}

// codecRtcpFb lists the rtcp-fb attributes for payload types that need them.
var codecRtcpFb = map[int][]string{
	96: {"nack", "nack pli", "ccm fir"},
	97: {"nack", "nack pli", "ccm fir"},
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
			// m=<type> <port> <profile> <pt...>
			parts := strings.Fields(val)
			if len(parts) >= 3 {
				md := MediaDesc{
					Type: parts[0],
				}
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
			case val == "ice-lite":
				s.IceLite = true
			case strings.HasPrefix(val, "ice-ufrag:"):
				s.IceUfrag = val[10:]
			case strings.HasPrefix(val, "ice-pwd:"):
				s.IcePwd = val[8:]
			case strings.HasPrefix(val, "candidate:") && curMedia != nil:
				curMedia.Candidates = append(curMedia.Candidates, val[10:])
			case strings.HasPrefix(val, "fmtp:") && curMedia != nil:
				parseFmtp(val[5:], curMedia)
			case strings.HasPrefix(val, "rtcp-fb:") && curMedia != nil:
				parseRtcpFb(val[8:], curMedia)
			}
		}
	}

	if !hasVersion {
		return nil, errors.New("sdp: no v= line found")
	}
	return s, nil
}

// ICEParams holds ICE attributes for SDP encoding.
type ICEParams struct {
	Ufrag      string   // ice-ufrag value
	Pwd        string   // ice-pwd value
	Candidates []string // raw candidate values (without "a=candidate:" prefix)
	Lite       bool     // include a=ice-lite at session level
}

// BuildOffer creates an SDP offer string.
func BuildOffer(ip string, port int, codecs []int, direction string) string {
	return buildSDP(ip, port, codecs, direction, ProfileRTP, "", nil)
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

// BuildOfferICE creates an SDP offer with ICE attributes.
func BuildOfferICE(ip string, port int, codecs []int, direction string, ice *ICEParams) string {
	return buildSDP(ip, port, codecs, direction, ProfileRTP, "", ice)
}

// BuildAnswerICE creates an SDP answer with ICE attributes.
func BuildAnswerICE(ip string, port int, localPrefs, remoteOffer []int, direction string, ice *ICEParams) string {
	common := intersectCodecs(localPrefs, remoteOffer)
	if len(common) == 0 {
		common = localPrefs
	}
	return buildSDP(ip, port, common, direction, ProfileRTP, "", ice)
}

// BuildOfferSRTPICE creates an SDP offer with SRTP and ICE attributes.
func BuildOfferSRTPICE(ip string, port int, codecs []int, direction, cryptoInlineKey string, ice *ICEParams) string {
	return buildSDP(ip, port, codecs, direction, ProfileSRTP, cryptoInlineKey, ice)
}

// BuildAnswerSRTPICE creates an SDP answer with SRTP and ICE attributes.
func BuildAnswerSRTPICE(ip string, port int, localPrefs, remoteOffer []int, direction, cryptoInlineKey string, ice *ICEParams) string {
	common := intersectCodecs(localPrefs, remoteOffer)
	if len(common) == 0 {
		common = localPrefs
	}
	return buildSDP(ip, port, common, direction, ProfileSRTP, cryptoInlineKey, ice)
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
	return buildSDP(ip, port, codecs, direction, ProfileSRTP, cryptoInlineKey, nil)
}

// BuildAnswerSRTP creates an SDP answer with RTP/SAVP and a=crypto: attribute.
func BuildAnswerSRTP(ip string, port int, localPrefs []int, remoteOffer []int, direction, cryptoInlineKey string) string {
	common := intersectCodecs(localPrefs, remoteOffer)
	if len(common) == 0 {
		common = localPrefs
	}
	return buildSDP(ip, port, common, direction, ProfileSRTP, cryptoInlineKey, nil)
}

// buildSDP is the shared implementation for building audio-only SDP.
func buildSDP(ip string, port int, codecs []int, direction, profile, cryptoInlineKey string, ice *ICEParams) string {
	return buildSDPMulti(ip, port, codecs, direction, profile, cryptoInlineKey, ice, 0, nil)
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

// AudioMedia returns the first audio MediaDesc, or nil if none.
func (s *Session) AudioMedia() *MediaDesc {
	for i := range s.Media {
		if s.Media[i].Type == MediaAudio {
			return &s.Media[i]
		}
	}
	return nil
}

// VideoMedia returns the first video MediaDesc, or nil if none.
func (s *Session) VideoMedia() *MediaDesc {
	for i := range s.Media {
		if s.Media[i].Type == MediaVideo {
			return &s.Media[i]
		}
	}
	return nil
}

// writeMediaSection writes a single m= section (audio or video) to the builder.
func writeMediaSection(b *strings.Builder, mediaType string, port int, profile string, codecs []int, direction, cryptoInlineKey string, ice *ICEParams) {
	b.WriteString(fmt.Sprintf("m=%s %d %s", mediaType, port, profile))
	for _, c := range codecs {
		b.WriteString(fmt.Sprintf(" %d", c))
	}
	b.WriteString("\r\n")
	for _, c := range codecs {
		if name, ok := codecNames[c]; ok {
			b.WriteString(fmt.Sprintf("a=rtpmap:%d %s\r\n", c, name))
			if fmtp, ok := codecFmtp[c]; ok {
				b.WriteString(fmt.Sprintf("a=fmtp:%d %s\r\n", c, fmtp))
			}
			if fbs, ok := codecRtcpFb[c]; ok {
				for _, fb := range fbs {
					b.WriteString(fmt.Sprintf("a=rtcp-fb:%d %s\r\n", c, fb))
				}
			}
		}
	}
	if cryptoInlineKey != "" {
		b.WriteString("a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:")
		b.WriteString(cryptoInlineKey)
		b.WriteString("\r\n")
	}
	if ice != nil {
		b.WriteString("a=ice-ufrag:")
		b.WriteString(ice.Ufrag)
		b.WriteString("\r\n")
		b.WriteString("a=ice-pwd:")
		b.WriteString(ice.Pwd)
		b.WriteString("\r\n")
		for _, c := range ice.Candidates {
			b.WriteString("a=candidate:")
			b.WriteString(c)
			b.WriteString("\r\n")
		}
	}
	b.WriteString("a=")
	b.WriteString(direction)
	b.WriteString("\r\n")
}

// buildSDPMulti builds an SDP with audio and optional video media sections.
// If videoCodecs is non-empty, a video m= line is appended at videoPort.
func buildSDPMulti(ip string, audioPort int, audioCodecs []int, direction, profile, cryptoInlineKey string, ice *ICEParams, videoPort int, videoCodecs []int) string {
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
	if ice != nil && ice.Lite {
		b.WriteString("a=ice-lite\r\n")
	}
	writeMediaSection(&b, MediaAudio, audioPort, profile, audioCodecs, direction, cryptoInlineKey, ice)
	if len(videoCodecs) > 0 {
		writeMediaSection(&b, MediaVideo, videoPort, profile, videoCodecs, direction, cryptoInlineKey, ice)
	}
	return b.String()
}

// BuildAnswerVideo creates an SDP answer with audio and video media lines.
func BuildAnswerVideo(ip string, audioPort int, localPrefs, remoteAudioOffer []int, videoPort int, localVideoPrefs, remoteVideoOffer []int, direction string) string {
	audioCodecs := intersectCodecs(localPrefs, remoteAudioOffer)
	if len(audioCodecs) == 0 {
		audioCodecs = localPrefs
	}
	videoCodecs := intersectCodecs(localVideoPrefs, remoteVideoOffer)
	return buildSDPMulti(ip, audioPort, audioCodecs, direction, ProfileRTP, "", nil, videoPort, videoCodecs)
}

// BuildAnswerVideoSRTP creates an SDP answer with audio, video, and SRTP.
func BuildAnswerVideoSRTP(ip string, audioPort int, localPrefs, remoteAudioOffer []int, videoPort int, localVideoPrefs, remoteVideoOffer []int, direction, cryptoInlineKey string) string {
	audioCodecs := intersectCodecs(localPrefs, remoteAudioOffer)
	if len(audioCodecs) == 0 {
		audioCodecs = localPrefs
	}
	videoCodecs := intersectCodecs(localVideoPrefs, remoteVideoOffer)
	return buildSDPMulti(ip, audioPort, audioCodecs, direction, ProfileSRTP, cryptoInlineKey, nil, videoPort, videoCodecs)
}

// BuildAnswerVideoICE creates an SDP answer with audio, video, and ICE.
func BuildAnswerVideoICE(ip string, audioPort int, localPrefs, remoteAudioOffer []int, videoPort int, localVideoPrefs, remoteVideoOffer []int, direction string, ice *ICEParams) string {
	audioCodecs := intersectCodecs(localPrefs, remoteAudioOffer)
	if len(audioCodecs) == 0 {
		audioCodecs = localPrefs
	}
	videoCodecs := intersectCodecs(localVideoPrefs, remoteVideoOffer)
	return buildSDPMulti(ip, audioPort, audioCodecs, direction, ProfileRTP, "", ice, videoPort, videoCodecs)
}

// BuildAnswerVideoSRTPICE creates an SDP answer with audio, video, SRTP, and ICE.
func BuildAnswerVideoSRTPICE(ip string, audioPort int, localPrefs, remoteAudioOffer []int, videoPort int, localVideoPrefs, remoteVideoOffer []int, direction, cryptoInlineKey string, ice *ICEParams) string {
	audioCodecs := intersectCodecs(localPrefs, remoteAudioOffer)
	if len(audioCodecs) == 0 {
		audioCodecs = localPrefs
	}
	videoCodecs := intersectCodecs(localVideoPrefs, remoteVideoOffer)
	return buildSDPMulti(ip, audioPort, audioCodecs, direction, ProfileSRTP, cryptoInlineKey, ice, videoPort, videoCodecs)
}

// BuildOfferVideo creates an SDP offer with audio and video media lines.
func BuildOfferVideo(ip string, audioPort int, audioCodecs []int, videoPort int, videoCodecs []int, direction string) string {
	return buildSDPMulti(ip, audioPort, audioCodecs, direction, ProfileRTP, "", nil, videoPort, videoCodecs)
}

// BuildOfferVideoICE creates an SDP offer with audio, video, and ICE.
func BuildOfferVideoICE(ip string, audioPort int, audioCodecs []int, videoPort int, videoCodecs []int, direction string, ice *ICEParams) string {
	return buildSDPMulti(ip, audioPort, audioCodecs, direction, ProfileRTP, "", ice, videoPort, videoCodecs)
}

// BuildOfferVideoSRTP creates an SDP offer with audio, video, and SRTP.
func BuildOfferVideoSRTP(ip string, audioPort int, audioCodecs []int, videoPort int, videoCodecs []int, direction, cryptoInlineKey string) string {
	return buildSDPMulti(ip, audioPort, audioCodecs, direction, ProfileSRTP, cryptoInlineKey, nil, videoPort, videoCodecs)
}

// BuildOfferVideoSRTPICE creates an SDP offer with audio, video, SRTP, and ICE.
func BuildOfferVideoSRTPICE(ip string, audioPort int, audioCodecs []int, videoPort int, videoCodecs []int, direction, cryptoInlineKey string, ice *ICEParams) string {
	return buildSDPMulti(ip, audioPort, audioCodecs, direction, ProfileSRTP, cryptoInlineKey, ice, videoPort, videoCodecs)
}

// parseFmtp parses "a=fmtp:<pt> <params>" and stores it in the media desc.
func parseFmtp(val string, md *MediaDesc) {
	space := strings.IndexByte(val, ' ')
	if space < 0 {
		return
	}
	pt, err := strconv.Atoi(val[:space])
	if err != nil {
		return
	}
	if md.Fmtp == nil {
		md.Fmtp = make(map[int]string)
	}
	md.Fmtp[pt] = val[space+1:]
}

// parseRtcpFb parses "a=rtcp-fb:<pt> <value>" and stores it in the media desc.
func parseRtcpFb(val string, md *MediaDesc) {
	space := strings.IndexByte(val, ' ')
	if space < 0 {
		return
	}
	pt, err := strconv.Atoi(val[:space])
	if err != nil {
		return
	}
	if md.RtcpFb == nil {
		md.RtcpFb = make(map[int][]string)
	}
	md.RtcpFb[pt] = append(md.RtcpFb[pt], val[space+1:])
}
