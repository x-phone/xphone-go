package turn

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/internal/stun"
)

// --- ChannelData framing ---

func TestChannelData_RoundTrip(t *testing.T) {
	payload := []byte("hello RTP")
	frame := WrapChannelData(0x4000, payload)
	assert.Equal(t, 4+len(payload), len(frame))

	ch, data, ok := ParseChannelData(frame)
	require.True(t, ok)
	assert.Equal(t, uint16(0x4000), ch)
	assert.Equal(t, payload, data)
}

func TestChannelData_EmptyPayload(t *testing.T) {
	frame := WrapChannelData(0x4001, nil)
	ch, data, ok := ParseChannelData(frame)
	require.True(t, ok)
	assert.Equal(t, uint16(0x4001), ch)
	assert.Empty(t, data)
}

func TestChannelData_TooShort(t *testing.T) {
	_, _, ok := ParseChannelData([]byte{0x40, 0x00})
	assert.False(t, ok)
	_, _, ok = ParseChannelData(nil)
	assert.False(t, ok)
}

func TestChannelData_TruncatedPayload(t *testing.T) {
	frame := []byte{0x40, 0x00, 0x00, 0x0A}   // claims 10 bytes
	frame = append(frame, make([]byte, 5)...) // only 5
	_, _, ok := ParseChannelData(frame)
	assert.False(t, ok)
}

func TestIsChannelData_Valid(t *testing.T) {
	assert.True(t, IsChannelData([]byte{0x40, 0x00, 0x00, 0x00}))
	assert.True(t, IsChannelData([]byte{0x7F, 0x00, 0x00, 0x00}))
}

func TestIsChannelData_Invalid(t *testing.T) {
	// STUN (first byte < 0x40)
	assert.False(t, IsChannelData([]byte{0x00, 0x01}))
	// RTP (first byte >= 0x80)
	assert.False(t, IsChannelData([]byte{0x80, 0x00}))
	assert.False(t, IsChannelData(nil))
}

// --- Request format tests ---

func TestAllocateRequest_Format(t *testing.T) {
	txnID := [12]byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA}
	msg := stun.BuildMessage(stun.AllocateRequest, txnID, []stun.Attr{{
		Type:  stun.AttrRequestedTransport,
		Value: []byte{17, 0, 0, 0},
	}})

	assert.True(t, stun.IsMessage(msg))
	mt, ok := stun.MsgType(msg)
	require.True(t, ok)
	assert.Equal(t, stun.AllocateRequest, mt)

	attrs := stun.ParseAttrs(msg[stun.HeaderSize:])
	require.Len(t, attrs, 1)
	assert.Equal(t, stun.AttrRequestedTransport, attrs[0].Type)
	assert.Equal(t, byte(17), attrs[0].Value[0]) // UDP
}

func TestRefreshRequest_Format(t *testing.T) {
	txnID := [12]byte{0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB}
	lt := make([]byte, 4)
	binary.BigEndian.PutUint32(lt, 600)

	msg := stun.BuildMessage(stun.RefreshRequest, txnID, []stun.Attr{{
		Type:  stun.AttrLifetime,
		Value: lt,
	}})

	mt, _ := stun.MsgType(msg)
	assert.Equal(t, stun.RefreshRequest, mt)

	attrs := stun.ParseAttrs(msg[stun.HeaderSize:])
	assert.Equal(t, uint32(600), binary.BigEndian.Uint32(attrs[0].Value))
}

func TestDeallocateRequest_ZeroLifetime(t *testing.T) {
	txnID := [12]byte{0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC}
	msg := stun.BuildMessage(stun.RefreshRequest, txnID, []stun.Attr{{
		Type:  stun.AttrLifetime,
		Value: []byte{0, 0, 0, 0},
	}})

	attrs := stun.ParseAttrs(msg[stun.HeaderSize:])
	assert.Equal(t, uint32(0), binary.BigEndian.Uint32(attrs[0].Value))
}

func TestCreatePermissionRequest_Format(t *testing.T) {
	txnID := [12]byte{0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD}
	peer := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5000}

	msg := stun.BuildMessage(stun.CreatePermissionRequest, txnID, []stun.Attr{{
		Type:  stun.AttrXORPeerAddress,
		Value: stun.EncodeXORAddr(peer),
	}})

	mt, _ := stun.MsgType(msg)
	assert.Equal(t, stun.CreatePermissionRequest, mt)
}

func TestChannelBindRequest_Format(t *testing.T) {
	txnID := [12]byte{0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE}
	peer := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 6000}
	channel := uint16(0x4000)

	channelVal := make([]byte, 4)
	binary.BigEndian.PutUint16(channelVal[0:2], channel)

	msg := stun.BuildMessage(stun.ChannelBindRequest, txnID, []stun.Attr{
		{Type: stun.AttrChannelNumber, Value: channelVal},
		{Type: stun.AttrXORPeerAddress, Value: stun.EncodeXORAddr(peer)},
	})

	mt, _ := stun.MsgType(msg)
	assert.Equal(t, stun.ChannelBindRequest, mt)

	attrs := stun.ParseAttrs(msg[stun.HeaderSize:])
	require.Len(t, attrs, 2)
	assert.Equal(t, stun.AttrChannelNumber, attrs[0].Type)
	assert.Equal(t, channel, binary.BigEndian.Uint16(attrs[0].Value[0:2]))
}

func TestAuthenticatedMessage_HasIntegrity(t *testing.T) {
	txnID := [12]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	key := stun.LongTermKey("alice", "example.com", "secret")

	msg := stun.BuildMessage(stun.AllocateRequest, txnID, []stun.Attr{
		{Type: stun.AttrUsername, Value: []byte("alice")},
		{Type: stun.AttrRealm, Value: []byte("example.com")},
	})
	miOffset := len(msg)
	msg = stun.AppendIntegrity(msg, key)

	assert.True(t, stun.VerifyIntegrity(msg, miOffset, key))
}

// --- Error parsing ---

func TestExtractErrorCode(t *testing.T) {
	txnID := [12]byte{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}
	errorVal := make([]byte, 4+len("Unauthorized"))
	errorVal[2] = 4 // class = 4
	errorVal[3] = 1 // number = 1 → 401
	copy(errorVal[4:], "Unauthorized")

	msg := stun.BuildMessage(stun.AllocateError, txnID, []stun.Attr{{
		Type:  stun.AttrErrorCode,
		Value: errorVal,
	}})

	attrs := stun.ParseAttrs(msg[stun.HeaderSize:])
	code, reason := stun.ParseErrorCode(attrs)
	assert.Equal(t, 401, code)
	assert.Contains(t, reason, "Unauthorized")
}

func TestExtractRealmNonce(t *testing.T) {
	txnID := [12]byte{0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22}
	errorVal := make([]byte, 4)
	errorVal[2] = 4
	errorVal[3] = 1

	msg := stun.BuildMessage(stun.AllocateError, txnID, []stun.Attr{
		{Type: stun.AttrErrorCode, Value: errorVal},
		{Type: stun.AttrRealm, Value: []byte("example.com")},
		{Type: stun.AttrNonce, Value: []byte("abc123")},
	})

	conn, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer conn.Close()
	serverAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3478}
	c := NewClient(conn, serverAddr, "user", "pass", nil)

	err := c.extractRealmNonce(msg)
	require.NoError(t, err)
	assert.Equal(t, "example.com", c.realm)
	assert.Equal(t, "abc123", c.nonce)
	assert.Equal(t, stun.LongTermKey("user", "example.com", "pass"), c.key)
}

// --- Demux tests ---

func TestDemux_STUN_vs_ChannelData_vs_RTP(t *testing.T) {
	// STUN Binding Request
	stunMsg := stun.BuildMessage(stun.BindingRequest, [12]byte{}, nil)
	assert.True(t, stun.IsMessage(stunMsg))
	assert.False(t, IsChannelData(stunMsg))

	// ChannelData
	cd := WrapChannelData(0x4000, []byte("rtp payload"))
	assert.True(t, IsChannelData(cd))
	assert.False(t, stun.IsMessage(cd))

	// RTP (version 2, first byte = 0x80)
	rtp := []byte{0x80, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xDE, 0xAD}
	assert.False(t, stun.IsMessage(rtp))
	assert.False(t, IsChannelData(rtp))
}
