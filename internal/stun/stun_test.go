package stun

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test helpers ---

func buildTestResponse(txnID [12]byte, attrs []byte) []byte {
	resp := make([]byte, headerSize+len(attrs))
	binary.BigEndian.PutUint16(resp[0:2], bindingResponse)
	binary.BigEndian.PutUint16(resp[2:4], uint16(len(attrs)))
	binary.BigEndian.PutUint32(resp[4:8], magicCookie)
	copy(resp[8:20], txnID[:])
	copy(resp[headerSize:], attrs)
	return resp
}

func buildXORMappedAttr(ip [4]byte, port uint16) []byte {
	ipVal := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
	xorPort := port ^ uint16(magicCookie>>16)
	xorIP := ipVal ^ magicCookie

	attr := make([]byte, 12) // 4 header + 8 value
	binary.BigEndian.PutUint16(attr[0:2], attrXORMappedAddress)
	binary.BigEndian.PutUint16(attr[2:4], 8)
	attr[4] = 0x00 // reserved
	attr[5] = familyIPv4
	binary.BigEndian.PutUint16(attr[6:8], xorPort)
	binary.BigEndian.PutUint32(attr[8:12], xorIP)
	return attr
}

func buildMappedAttr(ip [4]byte, port uint16) []byte {
	attr := make([]byte, 12)
	binary.BigEndian.PutUint16(attr[0:2], attrMappedAddress)
	binary.BigEndian.PutUint16(attr[2:4], 8)
	attr[4] = 0x00 // reserved
	attr[5] = familyIPv4
	binary.BigEndian.PutUint16(attr[6:8], port)
	copy(attr[8:12], ip[:])
	return attr
}

// --- Tests ---

func TestBuildRequestFormat(t *testing.T) {
	txnID := [12]byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	req := buildBindingRequest(txnID)

	assert.Equal(t, headerSize, len(req))
	assert.Equal(t, bindingRequest, binary.BigEndian.Uint16(req[0:2]))
	assert.Equal(t, uint16(0), binary.BigEndian.Uint16(req[2:4]))
	assert.Equal(t, magicCookie, binary.BigEndian.Uint32(req[4:8]))
	assert.Equal(t, txnID[:], req[8:20])
}

func TestParseXORMappedAddress(t *testing.T) {
	txnID := [12]byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA}
	attrs := buildXORMappedAttr([4]byte{203, 0, 113, 42}, 12345)
	resp := buildTestResponse(txnID, attrs)

	ip, port, err := parseBindingResponse(resp, txnID)
	require.NoError(t, err)
	assert.Equal(t, "203.0.113.42", ip)
	assert.Equal(t, 12345, port)
}

func TestParseMappedAddressFallback(t *testing.T) {
	txnID := [12]byte{0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB}
	attrs := buildMappedAttr([4]byte{198, 51, 100, 7}, 54321)
	resp := buildTestResponse(txnID, attrs)

	ip, port, err := parseBindingResponse(resp, txnID)
	require.NoError(t, err)
	assert.Equal(t, "198.51.100.7", ip)
	assert.Equal(t, 54321, port)
}

func TestRejectWrongTxnID(t *testing.T) {
	txnID := [12]byte{0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC}
	wrongID := [12]byte{0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD}
	resp := buildTestResponse(wrongID, nil)

	_, _, err := parseBindingResponse(resp, txnID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transaction ID mismatch")
}

func TestRejectWrongMessageType(t *testing.T) {
	txnID := [12]byte{0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE}
	resp := make([]byte, headerSize)
	binary.BigEndian.PutUint16(resp[0:2], 0x0111) // Binding Error Response
	binary.BigEndian.PutUint16(resp[2:4], 0)
	binary.BigEndian.PutUint32(resp[4:8], magicCookie)
	copy(resp[8:20], txnID[:])

	_, _, err := parseBindingResponse(resp, txnID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected message type")
}

func TestRejectTruncatedResponse(t *testing.T) {
	_, _, err := parseBindingResponse(make([]byte, 10), [12]byte{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestXORMappedAddressTooShort(t *testing.T) {
	_, _, err := parseXORMappedAddress(make([]byte, 4))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestMappedAddressTooShort(t *testing.T) {
	_, _, err := parseMappedAddress(make([]byte, 4))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestMultipleAttributesPrefersXOR(t *testing.T) {
	txnID := [12]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

	mapped := buildMappedAttr([4]byte{1, 2, 3, 4}, 1111)
	xor := buildXORMappedAttr([4]byte{5, 6, 7, 8}, 2222)

	attrs := append(mapped, xor...)
	resp := buildTestResponse(txnID, attrs)

	ip, port, err := parseBindingResponse(resp, txnID)
	require.NoError(t, err)
	assert.Equal(t, "5.6.7.8", ip)
	assert.Equal(t, 2222, port)
}

func TestPaddedAttributes(t *testing.T) {
	txnID := [12]byte{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}

	// Unknown comprehension-optional attribute with 5-byte value (padded to 8).
	unknown := make([]byte, 4+5+3) // header(4) + value(5) + padding(3)
	binary.BigEndian.PutUint16(unknown[0:2], 0x8000)
	binary.BigEndian.PutUint16(unknown[2:4], 5)
	unknown[4] = 1
	unknown[5] = 2
	unknown[6] = 3
	unknown[7] = 4
	unknown[8] = 5

	xor := buildXORMappedAttr([4]byte{10, 20, 30, 40}, 9999)

	attrs := append(unknown, xor...)
	resp := buildTestResponse(txnID, attrs)

	ip, port, err := parseBindingResponse(resp, txnID)
	require.NoError(t, err)
	assert.Equal(t, "10.20.30.40", ip)
	assert.Equal(t, 9999, port)
}

func TestRejectBadMagicCookie(t *testing.T) {
	txnID := [12]byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA}
	resp := make([]byte, headerSize)
	binary.BigEndian.PutUint16(resp[0:2], bindingResponse)
	binary.BigEndian.PutUint16(resp[2:4], 0)
	binary.BigEndian.PutUint32(resp[4:8], 0xDEADBEEF) // wrong cookie
	copy(resp[8:20], txnID[:])

	_, _, err := parseBindingResponse(resp, txnID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad magic cookie")
}

func TestRejectIPv6Family(t *testing.T) {
	data := []byte{0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

	_, _, err := parseXORMappedAddress(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported address family")

	_, _, err = parseMappedAddress(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported address family")
}

func TestRejectTruncatedAttribute(t *testing.T) {
	txnID := [12]byte{0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB}

	// Attribute header claims 100 bytes but only 4 bytes follow.
	attr := make([]byte, 8)
	binary.BigEndian.PutUint16(attr[0:2], attrMappedAddress)
	binary.BigEndian.PutUint16(attr[2:4], 100)

	resp := buildTestResponse(txnID, attr)

	_, _, err := parseBindingResponse(resp, txnID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated attribute")
}

func TestRejectUnknownComprehensionRequired(t *testing.T) {
	txnID := [12]byte{0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC}

	attr := make([]byte, 8)
	binary.BigEndian.PutUint16(attr[0:2], 0x0099) // unknown comprehension-required
	binary.BigEndian.PutUint16(attr[2:4], 4)

	resp := buildTestResponse(txnID, attr)

	_, _, err := parseBindingResponse(resp, txnID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "comprehension-required")
}

func TestNoMappedAddressInResponse(t *testing.T) {
	txnID := [12]byte{0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD, 0xDD}

	attr := make([]byte, 8)
	binary.BigEndian.PutUint16(attr[0:2], 0x8028) // FINGERPRINT (optional)
	binary.BigEndian.PutUint16(attr[2:4], 4)

	resp := buildTestResponse(txnID, attr)

	_, _, err := parseBindingResponse(resp, txnID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no mapped address")
}

func TestResolveServerInvalid(t *testing.T) {
	_, err := resolveServer("not-a-valid-host:99999")
	require.Error(t, err)
}

func TestResolveServerBadFormat(t *testing.T) {
	_, err := resolveServer("no-port")
	require.Error(t, err)
}
