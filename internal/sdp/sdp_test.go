package sdp

import (
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

func TestNegotiateCodec(t *testing.T) {
	// local prefers [0,8], remote offers [8,0] → first local pref found in remote = 0
	assert.Equal(t, 0, NegotiateCodec([]int{0, 8}, []int{8, 0}))
	// no common codec
	assert.Equal(t, -1, NegotiateCodec([]int{0}, []int{9}))
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
