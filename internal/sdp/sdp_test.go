package sdp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleSDP generates a minimal valid SDP for testing via BuildOffer.
func sampleSDP(ip string, port int, dir string, codecs ...int) string {
	return BuildOffer(ip, port, codecs, dir)
}

func TestBuildOffer_SingleCodec(t *testing.T) {
	sdp := BuildOffer("192.168.1.100", 5004, []int{0}, "sendrecv")
	assert.Contains(t, sdp, "m=audio")
	assert.Contains(t, sdp, "PCMU/8000")
}

func TestBuildOffer_MultipleCodecs(t *testing.T) {
	sdp := BuildOffer("192.168.1.100", 5004, []int{0, 8, 9}, "sendrecv")
	assert.Contains(t, sdp, "m=audio 5004 RTP/AVP 0 8 9")
}

func TestBuildOffer_ConnectionLine(t *testing.T) {
	sdp := BuildOffer("10.0.0.1", 5004, []int{0}, "sendrecv")
	assert.Contains(t, sdp, "c=IN IP4 10.0.0.1")
}

func TestBuildOffer_MediaLine(t *testing.T) {
	sdp := BuildOffer("192.168.1.100", 6000, []int{0}, "sendrecv")
	assert.Contains(t, sdp, "m=audio 6000 RTP/AVP")
}

func TestBuildOffer_DirectionSendOnly(t *testing.T) {
	sdp := BuildOffer("192.168.1.100", 5004, []int{0}, "sendonly")
	assert.Contains(t, sdp, "a=sendonly")
}

func TestBuildOffer_DirectionSendRecv(t *testing.T) {
	sdp := BuildOffer("192.168.1.100", 5004, []int{0}, "sendrecv")
	assert.Contains(t, sdp, "a=sendrecv")
}

func TestParse_ExtractsCodec(t *testing.T) {
	raw := sampleSDP("192.168.1.100", 5004, "sendrecv", 0, 8)
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, 0, s.FirstCodec())
}

func TestParse_ExtractsAddress(t *testing.T) {
	raw := sampleSDP("10.0.0.42", 5004, "sendrecv", 0)
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.42", s.Connection)
}

func TestParse_ExtractsPort(t *testing.T) {
	raw := sampleSDP("192.168.1.100", 7000, "sendrecv", 0)
	s, err := Parse(raw)
	require.NoError(t, err)
	require.NotEmpty(t, s.Media)
	assert.Equal(t, 7000, s.Media[0].Port)
}

func TestParse_Direction(t *testing.T) {
	raw := sampleSDP("192.168.1.100", 5004, "sendonly", 0)
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "sendonly", s.Dir())
}

func TestParse_DefaultDirectionIsSendRecv(t *testing.T) {
	raw := "v=0\r\no=xphone 0 0 IN IP4 192.168.1.100\r\ns=xphone\r\nc=IN IP4 192.168.1.100\r\nt=0 0\r\nm=audio 5004 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "sendrecv", s.Dir())
}

func TestParse_InvalidReturnsError(t *testing.T) {
	_, err := Parse("this is not valid SDP")
	assert.Error(t, err)
}

func TestParse_RoundTrip(t *testing.T) {
	offer := BuildOffer("192.168.1.100", 5004, []int{0, 8}, "sendrecv")
	s, err := Parse(offer)
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.100", s.Connection)
	require.NotEmpty(t, s.Media)
	assert.Equal(t, 5004, s.Media[0].Port)
	assert.Equal(t, 0, s.FirstCodec())
}

func TestBuildOffer_G729(t *testing.T) {
	sdp := BuildOffer("192.168.1.100", 5004, []int{18}, "sendrecv")
	assert.Contains(t, sdp, "m=audio 5004 RTP/AVP 18")
	assert.Contains(t, sdp, "a=rtpmap:18 G729/8000")
	assert.Contains(t, sdp, "a=fmtp:18 annexb=no")
}

func TestBuildOffer_G729WithOtherCodecs(t *testing.T) {
	sdp := BuildOffer("192.168.1.100", 5004, []int{0, 18}, "sendrecv")
	assert.Contains(t, sdp, "m=audio 5004 RTP/AVP 0 18")
	assert.Contains(t, sdp, "a=rtpmap:18 G729/8000")
	assert.Contains(t, sdp, "a=fmtp:18 annexb=no")
	assert.Contains(t, sdp, "a=rtpmap:0 PCMU/8000")
}

func TestParse_G729RoundTrip(t *testing.T) {
	raw := BuildOffer("10.0.0.1", 5004, []int{18}, "sendrecv")
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, 18, s.FirstCodec())
	assert.Equal(t, 5004, s.Media[0].Port)
}

func TestNegotiateCodec(t *testing.T) {
	// local prefers [0,8], remote offers [8,0] → first local pref found in remote = 0
	assert.Equal(t, 0, NegotiateCodec([]int{0, 8}, []int{8, 0}))
	// no common codec
	assert.Equal(t, -1, NegotiateCodec([]int{0}, []int{9}))
	// G.729 negotiation
	assert.Equal(t, 18, NegotiateCodec([]int{0, 18}, []int{18}))
	assert.Equal(t, -1, NegotiateCodec([]int{18}, []int{0, 8}))
}

func TestBuildOfferSRTP(t *testing.T) {
	s := BuildOfferSRTP("10.0.0.1", 5004, []int{0}, "sendrecv", "YWJjZGVmZ2hpamtsbW5vcA==")
	assert.Contains(t, s, "m=audio 5004 RTP/SAVP 0")
	assert.Contains(t, s, "a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:YWJjZGVmZ2hpamtsbW5vcA==")
}

func TestBuildAnswerSRTP(t *testing.T) {
	s := BuildAnswerSRTP("10.0.0.1", 5004, []int{0, 8}, []int{8}, "sendrecv", "base64key==")
	assert.Contains(t, s, "RTP/SAVP")
	assert.Contains(t, s, "a=crypto:")
	// Should only include codec 8 (intersection).
	assert.Contains(t, s, "m=audio 5004 RTP/SAVP 8")
}

func TestParseSRTPOffer(t *testing.T) {
	raw := BuildOfferSRTP("10.0.0.1", 5004, []int{0, 101}, "sendrecv", "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0")
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.True(t, s.IsSRTP())
	require.NotNil(t, s.FirstCrypto())
	assert.Equal(t, 1, s.FirstCrypto().Tag)
	assert.Equal(t, "AES_CM_128_HMAC_SHA1_80", s.FirstCrypto().Suite)
	assert.Equal(t, "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0", s.FirstCrypto().InlineKey())
}

func TestParseProfile(t *testing.T) {
	raw := BuildOffer("10.0.0.1", 5004, []int{0}, "sendrecv")
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.False(t, s.IsSRTP())
	assert.Equal(t, ProfileRTP, s.Media[0].Profile)
}

func TestParseCryptoNone(t *testing.T) {
	raw := BuildOffer("10.0.0.1", 5004, []int{0}, "sendrecv")
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.Nil(t, s.FirstCrypto())
}

func TestCryptoAttrInlineKey(t *testing.T) {
	ca := CryptoAttr{KeyParams: "inline:abc123"}
	assert.Equal(t, "abc123", ca.InlineKey())

	ca2 := CryptoAttr{KeyParams: "noprefix"}
	assert.Equal(t, "noprefix", ca2.InlineKey())
}

// --- ICE SDP tests ---

func TestBuildOfferICE(t *testing.T) {
	ice := &ICEParams{
		Ufrag: "abcd1234",
		Pwd:   "longpasswordstringhere123",
		Candidates: []string{
			"1 1 UDP 2130706431 192.168.1.100 5004 typ host",
			"2 1 UDP 1694498815 203.0.113.42 12345 typ srflx raddr 192.168.1.100 rport 5004",
		},
		Lite: true,
	}
	s := BuildOfferICE("192.168.1.100", 5004, []int{0}, "sendrecv", ice)
	assert.Contains(t, s, "a=ice-lite\r\n")
	assert.Contains(t, s, "a=ice-ufrag:abcd1234\r\n")
	assert.Contains(t, s, "a=ice-pwd:longpasswordstringhere123\r\n")
	assert.Contains(t, s, "a=candidate:1 1 UDP 2130706431 192.168.1.100 5004 typ host\r\n")
	assert.Contains(t, s, "a=candidate:2 1 UDP 1694498815 203.0.113.42 12345 typ srflx")
}

func TestBuildOfferSRTPICE(t *testing.T) {
	ice := &ICEParams{Ufrag: "u1234567", Pwd: "pwd12345678901234567890", Lite: true}
	s := BuildOfferSRTPICE("10.0.0.1", 5004, []int{0}, "sendrecv", "base64key==", ice)
	assert.Contains(t, s, "RTP/SAVP")
	assert.Contains(t, s, "a=crypto:")
	assert.Contains(t, s, "a=ice-ufrag:u1234567")
	assert.Contains(t, s, "a=ice-lite")
}

func TestParseICE_RoundTrip(t *testing.T) {
	ice := &ICEParams{
		Ufrag:      "testufrag",
		Pwd:        "testpasswordstring123456",
		Candidates: []string{"1 1 UDP 2130706431 192.168.1.100 5004 typ host"},
		Lite:       true,
	}
	raw := BuildOfferICE("192.168.1.100", 5004, []int{0}, "sendrecv", ice)
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.True(t, s.IceLite)
	assert.Equal(t, "testufrag", s.IceUfrag)
	assert.Equal(t, "testpasswordstring123456", s.IcePwd)
	require.NotEmpty(t, s.Media)
	require.Len(t, s.Media[0].Candidates, 1)
	assert.Contains(t, s.Media[0].Candidates[0], "192.168.1.100")
}

func TestParseICE_NotPresent(t *testing.T) {
	raw := BuildOffer("10.0.0.1", 5004, []int{0}, "sendrecv")
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.False(t, s.IceLite)
	assert.Empty(t, s.IceUfrag)
	assert.Empty(t, s.IcePwd)
}

func TestParseICE_FromRawSDP(t *testing.T) {
	raw := "v=0\r\no=- 0 0 IN IP4 10.0.0.1\r\ns=-\r\nc=IN IP4 10.0.0.1\r\nt=0 0\r\na=ice-lite\r\nm=audio 5004 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=ice-ufrag:abc123\r\na=ice-pwd:password1234567890abcdef\r\na=candidate:1 1 UDP 2130706431 10.0.0.1 5004 typ host\r\na=sendrecv\r\n"
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.True(t, s.IceLite)
	assert.Equal(t, "abc123", s.IceUfrag)
	assert.Equal(t, "password1234567890abcdef", s.IcePwd)
	require.Len(t, s.Media[0].Candidates, 1)
}

// --- Video SDP tests ---

func TestBuildOfferVideo_AudioAndVideo(t *testing.T) {
	s := BuildOfferVideo("10.0.0.1", 5004, []int{0, 8}, 5006, []int{96, 97}, "sendrecv")
	assert.Contains(t, s, "m=audio 5004 RTP/AVP 0 8")
	assert.Contains(t, s, "m=video 5006 RTP/AVP 96 97")
	assert.Contains(t, s, "a=rtpmap:96 H264/90000")
	assert.Contains(t, s, "a=rtpmap:97 VP8/90000")
}

func TestBuildOfferVideo_H264Fmtp(t *testing.T) {
	s := BuildOfferVideo("10.0.0.1", 5004, []int{0}, 5006, []int{96}, "sendrecv")
	assert.Contains(t, s, "a=fmtp:96 profile-level-id=42e01f;packetization-mode=1")
}

func TestBuildOfferVideo_RtcpFb(t *testing.T) {
	s := BuildOfferVideo("10.0.0.1", 5004, []int{0}, 5006, []int{96}, "sendrecv")
	assert.Contains(t, s, "a=rtcp-fb:96 nack\r\n")
	assert.Contains(t, s, "a=rtcp-fb:96 nack pli\r\n")
	assert.Contains(t, s, "a=rtcp-fb:96 ccm fir\r\n")
}

func TestBuildOfferVideo_VP8RtcpFb(t *testing.T) {
	s := BuildOfferVideo("10.0.0.1", 5004, []int{0}, 5006, []int{97}, "sendrecv")
	assert.Contains(t, s, "a=rtcp-fb:97 nack\r\n")
	assert.Contains(t, s, "a=rtcp-fb:97 nack pli\r\n")
	assert.Contains(t, s, "a=rtcp-fb:97 ccm fir\r\n")
}

func TestBuildOfferVideo_SRTP(t *testing.T) {
	s := BuildOfferVideoSRTP("10.0.0.1", 5004, []int{0}, 5006, []int{96}, "sendrecv", "base64key==")
	assert.Contains(t, s, "m=audio 5004 RTP/SAVP 0")
	assert.Contains(t, s, "m=video 5006 RTP/SAVP 96")
	// Both media sections should have crypto.
	// Count occurrences of a=crypto:
	assert.Equal(t, 2, strings.Count(s, "a=crypto:1 AES_CM_128_HMAC_SHA1_80"))
}

func TestBuildOfferVideo_ICE(t *testing.T) {
	ice := &ICEParams{Ufrag: "uf", Pwd: "pw", Lite: true}
	s := BuildOfferVideoICE("10.0.0.1", 5004, []int{0}, 5006, []int{96}, "sendrecv", ice)
	assert.Contains(t, s, "a=ice-lite")
	assert.Contains(t, s, "m=audio 5004 RTP/AVP 0")
	assert.Contains(t, s, "m=video 5006 RTP/AVP 96")
	// Both media sections should have ICE attributes.
	assert.Equal(t, 2, strings.Count(s, "a=ice-ufrag:uf"))
}

func TestParseVideo_TwoMediaLines(t *testing.T) {
	raw := BuildOfferVideo("10.0.0.1", 5004, []int{0, 8}, 5006, []int{96, 97}, "sendrecv")
	s, err := Parse(raw)
	require.NoError(t, err)
	require.Len(t, s.Media, 2)

	assert.Equal(t, MediaAudio, s.Media[0].Type)
	assert.Equal(t, 5004, s.Media[0].Port)
	assert.Equal(t, []int{0, 8}, s.Media[0].Codecs)

	assert.Equal(t, MediaVideo, s.Media[1].Type)
	assert.Equal(t, 5006, s.Media[1].Port)
	assert.Equal(t, []int{96, 97}, s.Media[1].Codecs)
}

func TestParseVideo_AudioMediaHelper(t *testing.T) {
	raw := BuildOfferVideo("10.0.0.1", 5004, []int{0}, 5006, []int{96}, "sendrecv")
	s, err := Parse(raw)
	require.NoError(t, err)
	audio := s.AudioMedia()
	require.NotNil(t, audio)
	assert.Equal(t, 5004, audio.Port)
	assert.Equal(t, []int{0}, audio.Codecs)
}

func TestParseVideo_VideoMediaHelper(t *testing.T) {
	raw := BuildOfferVideo("10.0.0.1", 5004, []int{0}, 5006, []int{96, 97}, "sendrecv")
	s, err := Parse(raw)
	require.NoError(t, err)
	video := s.VideoMedia()
	require.NotNil(t, video)
	assert.Equal(t, 5006, video.Port)
	assert.Equal(t, []int{96, 97}, video.Codecs)
}

func TestParseVideo_AudioOnly_NoVideoMedia(t *testing.T) {
	raw := BuildOffer("10.0.0.1", 5004, []int{0}, "sendrecv")
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.NotNil(t, s.AudioMedia())
	assert.Nil(t, s.VideoMedia())
}

func TestParseVideo_Fmtp(t *testing.T) {
	raw := BuildOfferVideo("10.0.0.1", 5004, []int{0}, 5006, []int{96}, "sendrecv")
	s, err := Parse(raw)
	require.NoError(t, err)
	video := s.VideoMedia()
	require.NotNil(t, video)
	assert.Equal(t, "profile-level-id=42e01f;packetization-mode=1", video.Fmtp[96])
}

func TestParseVideo_RtcpFb(t *testing.T) {
	raw := BuildOfferVideo("10.0.0.1", 5004, []int{0}, 5006, []int{96}, "sendrecv")
	s, err := Parse(raw)
	require.NoError(t, err)
	video := s.VideoMedia()
	require.NotNil(t, video)
	require.Len(t, video.RtcpFb[96], 3)
	assert.Equal(t, "nack", video.RtcpFb[96][0])
	assert.Equal(t, "nack pli", video.RtcpFb[96][1])
	assert.Equal(t, "ccm fir", video.RtcpFb[96][2])
}

func TestParseVideo_Direction(t *testing.T) {
	raw := BuildOfferVideo("10.0.0.1", 5004, []int{0}, 5006, []int{96}, "sendonly")
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "sendonly", s.AudioMedia().Direction)
	assert.Equal(t, "sendonly", s.VideoMedia().Direction)
}

func TestNegotiateCodec_Video(t *testing.T) {
	assert.Equal(t, 96, NegotiateCodec([]int{96, 97}, []int{97, 96}))
	assert.Equal(t, 97, NegotiateCodec([]int{97, 96}, []int{96, 97}))
	assert.Equal(t, -1, NegotiateCodec([]int{96}, []int{97}))
}

func TestParseVideo_MediaType(t *testing.T) {
	// Existing audio-only SDP should parse Type=audio.
	raw := BuildOffer("10.0.0.1", 5004, []int{0}, "sendrecv")
	s, err := Parse(raw)
	require.NoError(t, err)
	require.NotEmpty(t, s.Media)
	assert.Equal(t, MediaAudio, s.Media[0].Type)
}

func TestParseVideo_ExternalSDP(t *testing.T) {
	// Simulate an external SDP with audio + video from a different UA.
	raw := "v=0\r\n" +
		"o=other 123 456 IN IP4 203.0.113.1\r\n" +
		"s=-\r\n" +
		"c=IN IP4 203.0.113.1\r\n" +
		"t=0 0\r\n" +
		"m=audio 10000 RTP/AVP 0 8\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n" +
		"a=sendrecv\r\n" +
		"m=video 10002 RTP/AVP 96 97\r\n" +
		"a=rtpmap:96 H264/90000\r\n" +
		"a=fmtp:96 profile-level-id=42e01f;packetization-mode=1\r\n" +
		"a=rtcp-fb:96 nack\r\n" +
		"a=rtcp-fb:96 nack pli\r\n" +
		"a=rtpmap:97 VP8/90000\r\n" +
		"a=rtcp-fb:97 nack\r\n" +
		"a=sendrecv\r\n"

	s, err := Parse(raw)
	require.NoError(t, err)
	require.Len(t, s.Media, 2)

	audio := s.AudioMedia()
	require.NotNil(t, audio)
	assert.Equal(t, 10000, audio.Port)
	assert.Equal(t, []int{0, 8}, audio.Codecs)

	video := s.VideoMedia()
	require.NotNil(t, video)
	assert.Equal(t, 10002, video.Port)
	assert.Equal(t, []int{96, 97}, video.Codecs)
	assert.Equal(t, "profile-level-id=42e01f;packetization-mode=1", video.Fmtp[96])
	assert.Equal(t, []string{"nack", "nack pli"}, video.RtcpFb[96])
	assert.Equal(t, []string{"nack"}, video.RtcpFb[97])
}

func TestBuildOfferVideo_BackwardsCompat(t *testing.T) {
	// Audio-only BuildOffer should still work exactly as before.
	s := BuildOffer("10.0.0.1", 5004, []int{0, 8}, "sendrecv")
	parsed, err := Parse(s)
	require.NoError(t, err)
	require.Len(t, parsed.Media, 1)
	assert.Equal(t, MediaAudio, parsed.Media[0].Type)
	assert.Nil(t, parsed.VideoMedia())
}
