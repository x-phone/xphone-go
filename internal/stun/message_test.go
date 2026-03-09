package stun

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateTxnID_Random(t *testing.T) {
	id1 := GenerateTxnID()
	id2 := GenerateTxnID()
	assert.NotEqual(t, id1, id2)
}

func TestIsMessage_Valid(t *testing.T) {
	msg := BuildMessage(BindingRequest, [12]byte{0xAA}, nil)
	assert.True(t, IsMessage(msg))
}

func TestIsMessage_RTP(t *testing.T) {
	rtp := make([]byte, 20)
	rtp[0] = 0x80 // RTP version 2
	assert.False(t, IsMessage(rtp))
}

func TestIsMessage_TooShort(t *testing.T) {
	assert.False(t, IsMessage([]byte{0, 1, 0, 0}))
}

func TestMsgType_And_TxnID(t *testing.T) {
	txnID := [12]byte{0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55}
	msg := BuildMessage(AllocateRequest, txnID, nil)

	mt, ok := MsgType(msg)
	require.True(t, ok)
	assert.Equal(t, AllocateRequest, mt)

	id, ok := TxnID(msg)
	require.True(t, ok)
	assert.Equal(t, txnID, id)
}

func TestBuildMessage_WithAttrs(t *testing.T) {
	txnID := [12]byte{0x11}
	msg := BuildMessage(BindingRequest, txnID, []Attr{{
		Type:  AttrLifetime,
		Value: []byte{0, 0, 0x02, 0x58}, // 600
	}})

	assert.Equal(t, HeaderSize+4+4, len(msg))
	assert.Equal(t, BindingRequest, binary.BigEndian.Uint16(msg[0:2]))
	assert.Equal(t, MagicCookie, binary.BigEndian.Uint32(msg[4:8]))
}

func TestBuildMessage_PaddedAttr(t *testing.T) {
	txnID := [12]byte{0x22}
	msg := BuildMessage(BindingRequest, txnID, []Attr{{
		Type:  AttrUsername,
		Value: []byte("alice"), // 5 bytes → padded to 8
	}})

	// Header(20) + attr header(4) + padded value(8) = 32
	assert.Equal(t, 32, len(msg))
}

func TestParseAttrs_RoundTrip(t *testing.T) {
	txnID := [12]byte{0x33}
	msg := BuildMessage(BindingRequest, txnID, []Attr{
		{Type: AttrLifetime, Value: []byte{0, 0, 0x02, 0x58}},
		{Type: AttrUsername, Value: []byte("alice")},
	})

	attrs := ParseAttrs(msg[HeaderSize:])
	require.Len(t, attrs, 2)
	assert.Equal(t, AttrLifetime, attrs[0].Type)
	assert.Equal(t, uint32(600), binary.BigEndian.Uint32(attrs[0].Value))
	assert.Equal(t, AttrUsername, attrs[1].Type)
	assert.Equal(t, []byte("alice"), attrs[1].Value)
}

func TestXORAddr_RoundTrip(t *testing.T) {
	addr := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 42), Port: 12345}
	encoded := EncodeXORAddr(addr)
	decoded, err := ParseXORAddr(encoded)
	require.NoError(t, err)
	assert.Equal(t, addr.IP.To4(), decoded.IP.To4())
	assert.Equal(t, addr.Port, decoded.Port)
}

func TestEncodeXORAddr_RejectsIPv6(t *testing.T) {
	addr := &net.UDPAddr{IP: net.ParseIP("::1"), Port: 5060}
	assert.Panics(t, func() { EncodeXORAddr(addr) })
}

func TestAppendIntegrity_And_Verify(t *testing.T) {
	txnID := [12]byte{0x44}
	key := []byte("test-key")
	msg := BuildMessage(BindingRequest, txnID, []Attr{{
		Type:  AttrUsername,
		Value: []byte("user"),
	}})
	miOffset := len(msg)
	msg = AppendIntegrity(msg, key)

	assert.True(t, VerifyIntegrity(msg, miOffset, key))
	assert.False(t, VerifyIntegrity(msg, miOffset, []byte("wrong-key")))
}

func TestLongTermKey(t *testing.T) {
	key := LongTermKey("user", "realm", "pass")
	assert.Len(t, key, 16) // MD5 = 16 bytes
	assert.Equal(t, key, LongTermKey("user", "realm", "pass"))
	assert.NotEqual(t, key, LongTermKey("user2", "realm", "pass"))
}

func TestBuildBindingResponse(t *testing.T) {
	txnID := [12]byte{0x55}
	addr := &net.UDPAddr{IP: net.IPv4(10, 20, 30, 40), Port: 5060}
	resp := BuildBindingResponse(txnID, addr, []byte("test-key"))

	assert.True(t, IsMessage(resp))
	mt, _ := MsgType(resp)
	assert.Equal(t, BindingResponse, mt)
}

func TestFindAttr(t *testing.T) {
	attrs := []Attr{
		{Type: AttrLifetime, Value: []byte{0, 0, 0x02, 0x58}},
		{Type: AttrUsername, Value: []byte("bob")},
	}
	assert.Equal(t, []byte{0, 0, 0x02, 0x58}, FindAttr(attrs, AttrLifetime))
	assert.Equal(t, []byte("bob"), FindAttr(attrs, AttrUsername))
	assert.Nil(t, FindAttr(attrs, AttrNonce))
}

func TestParseErrorCode(t *testing.T) {
	errorVal := make([]byte, 4+len("Unauthorized"))
	errorVal[2] = 4 // class = 4
	errorVal[3] = 1 // number = 1 → 401
	copy(errorVal[4:], "Unauthorized")

	attrs := []Attr{{Type: AttrErrorCode, Value: errorVal}}
	code, reason := ParseErrorCode(attrs)
	assert.Equal(t, 401, code)
	assert.Equal(t, "Unauthorized", reason)
}

func TestParseErrorCode_Empty(t *testing.T) {
	code, reason := ParseErrorCode(nil)
	assert.Equal(t, 0, code)
	assert.Empty(t, reason)
}
