package xphone

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDTMFDigitCode_ValidDigits(t *testing.T) {
	tests := []struct {
		digit string
		code  int
	}{
		{"0", 0}, {"1", 1}, {"2", 2}, {"3", 3}, {"4", 4},
		{"5", 5}, {"6", 6}, {"7", 7}, {"8", 8}, {"9", 9},
		{"*", 10}, {"#", 11},
		{"A", 12}, {"B", 13}, {"C", 14}, {"D", 15},
	}
	for _, tt := range tests {
		t.Run(tt.digit, func(t *testing.T) {
			assert.Equal(t, tt.code, DTMFDigitCode(tt.digit))
		})
	}
}

func TestDTMFDigitCode_InvalidReturnsNeg1(t *testing.T) {
	assert.Equal(t, -1, DTMFDigitCode("X"))
	assert.Equal(t, -1, DTMFDigitCode(""))
	assert.Equal(t, -1, DTMFDigitCode("10"))
}

func TestDTMFCodeDigit_RoundTrip(t *testing.T) {
	digits := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "#", "A", "B", "C", "D"}
	for _, digit := range digits {
		code := DTMFDigitCode(digit)
		assert.Equal(t, digit, DTMFCodeDigit(code), "round-trip failed for digit %s", digit)
	}
}

func TestDecodeDTMF_ValidPayload(t *testing.T) {
	// RFC 4733 payload: event=5, E=0, volume=10, duration=1000
	// byte 0: event code (5)
	// byte 1: E(0) | R(0) | volume(10) = 0x0A
	// bytes 2-3: duration (1000) = 0x03E8
	payload := []byte{5, 0x0A, 0x03, 0xE8}
	ev := DecodeDTMF(payload)
	require.NotNil(t, ev)
	assert.Equal(t, "5", ev.Digit)
	assert.Equal(t, uint8(10), ev.Volume)
	assert.Equal(t, uint16(1000), ev.Duration)
	assert.False(t, ev.End)
}

func TestDecodeDTMF_EndBitSet(t *testing.T) {
	// E bit is MSB of byte 1: 0x80 | volume(10) = 0x8A
	payload := []byte{5, 0x8A, 0x03, 0xE8}
	ev := DecodeDTMF(payload)
	require.NotNil(t, ev)
	assert.True(t, ev.End)
}

func TestDecodeDTMF_ShortPayloadReturnsNil(t *testing.T) {
	assert.Nil(t, DecodeDTMF([]byte{1, 2, 3}))
	assert.Nil(t, DecodeDTMF([]byte{}))
	assert.Nil(t, DecodeDTMF(nil))
}

func TestEncodeDTMF_ProducesPackets(t *testing.T) {
	pkts, err := EncodeDTMF("5", 0, 0, 0x12345678)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(pkts), 3)
}

func TestEncodeDTMF_AllPacketsHavePT101(t *testing.T) {
	pkts, err := EncodeDTMF("5", 0, 0, 0x12345678)
	require.NoError(t, err)
	for i, pkt := range pkts {
		assert.Equal(t, uint8(DTMFPayloadType), pkt.PayloadType, "packet %d", i)
	}
}

func TestEncodeDTMF_LastPacketHasEndBit(t *testing.T) {
	pkts, err := EncodeDTMF("5", 0, 0, 0x12345678)
	require.NoError(t, err)
	require.NotEmpty(t, pkts)
	last := pkts[len(pkts)-1]
	require.GreaterOrEqual(t, len(last.Payload), 4)
	assert.True(t, last.Payload[1]&0x80 != 0, "last packet should have E bit set")
}

func TestEncodeDTMF_InvalidDigitReturnsError(t *testing.T) {
	_, err := EncodeDTMF("X", 0, 0, 0x12345678)
	assert.ErrorIs(t, err, ErrInvalidDTMFDigit)
}

// --- SIP INFO DTMF ---

func TestEncodeInfoDTMF_ValidDigit(t *testing.T) {
	body, err := EncodeInfoDTMF("5", 160)
	require.NoError(t, err)
	assert.Equal(t, "Signal=5\r\nDuration=160\r\n", body)
}

func TestEncodeInfoDTMF_Star(t *testing.T) {
	body, err := EncodeInfoDTMF("*", 200)
	require.NoError(t, err)
	assert.Equal(t, "Signal=*\r\nDuration=200\r\n", body)
}

func TestEncodeInfoDTMF_InvalidDigitReturnsError(t *testing.T) {
	_, err := EncodeInfoDTMF("X", 160)
	assert.ErrorIs(t, err, ErrInvalidDTMFDigit)
}

func TestParseInfoDTMF_BasicSignal(t *testing.T) {
	assert.Equal(t, "5", ParseInfoDTMF("Signal=5\r\nDuration=160\r\n"))
}

func TestParseInfoDTMF_StarAndHash(t *testing.T) {
	assert.Equal(t, "*", ParseInfoDTMF("Signal=*\r\nDuration=160\r\n"))
	assert.Equal(t, "#", ParseInfoDTMF("Signal=#\r\nDuration=160\r\n"))
}

func TestParseInfoDTMF_CaseInsensitiveKey(t *testing.T) {
	assert.Equal(t, "5", ParseInfoDTMF("SIGNAL=5\r\nDuration=160\r\n"))
	assert.Equal(t, "5", ParseInfoDTMF("signal=5\r\nDuration=160\r\n"))
}

func TestParseInfoDTMF_LowercaseDigitNormalized(t *testing.T) {
	assert.Equal(t, "A", ParseInfoDTMF("Signal=a\r\nDuration=160\r\n"))
	assert.Equal(t, "D", ParseInfoDTMF("Signal=d\r\n"))
}

func TestParseInfoDTMF_WithSpaces(t *testing.T) {
	assert.Equal(t, "5", ParseInfoDTMF("Signal = 5 \r\nDuration = 160\r\n"))
}

func TestParseInfoDTMF_EmptyBody(t *testing.T) {
	assert.Equal(t, "", ParseInfoDTMF(""))
}

func TestParseInfoDTMF_NoSignalLine(t *testing.T) {
	assert.Equal(t, "", ParseInfoDTMF("Duration=160\r\n"))
}

func TestParseInfoDTMF_InvalidDigit(t *testing.T) {
	assert.Equal(t, "", ParseInfoDTMF("Signal=X\r\n"))
}
