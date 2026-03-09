package stun

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"net"
)

// STUN message types (RFC 5389 section 6).
const (
	BindingRequest  uint16 = 0x0001
	BindingResponse uint16 = 0x0101
)

// TURN message types (RFC 5766).
const (
	AllocateRequest          uint16 = 0x0003
	AllocateResponse         uint16 = 0x0103
	AllocateError            uint16 = 0x0113
	RefreshRequest           uint16 = 0x0004
	RefreshResponse          uint16 = 0x0104
	CreatePermissionRequest  uint16 = 0x0008
	CreatePermissionResponse uint16 = 0x0108
	ChannelBindRequest       uint16 = 0x0009
	ChannelBindResponse      uint16 = 0x0109
)

// STUN/TURN attribute types (RFC 5389 + RFC 5766 + RFC 8445).
const (
	AttrMappedAddress      uint16 = 0x0001
	AttrUsername           uint16 = 0x0006
	AttrMessageIntegrity   uint16 = 0x0008
	AttrErrorCode          uint16 = 0x0009
	AttrChannelNumber      uint16 = 0x000C
	AttrLifetime           uint16 = 0x000D
	AttrXORPeerAddress     uint16 = 0x0012
	AttrRealm              uint16 = 0x0014
	AttrNonce              uint16 = 0x0015
	AttrXORRelayedAddress  uint16 = 0x0016
	AttrRequestedTransport uint16 = 0x0019
	AttrXORMappedAddress   uint16 = 0x0020
	AttrPriority           uint16 = 0x0024
	AttrUseCandidate       uint16 = 0x0025
)

// Protocol constants.
const (
	MagicCookie uint32 = 0x2112A442
	HeaderSize         = 20
	FamilyIPv4  byte   = 0x01
)

// Attr is a STUN message attribute (type + value).
type Attr struct {
	Type  uint16
	Value []byte
}

// GenerateTxnID generates a random 12-byte STUN transaction ID.
func GenerateTxnID() [12]byte {
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("stun: crypto/rand failed: " + err.Error())
	}
	return id
}

// IsMessage returns true if data looks like a STUN message (first two bits 00,
// magic cookie at bytes 4-7, length >= 20).
func IsMessage(data []byte) bool {
	if len(data) < HeaderSize {
		return false
	}
	if data[0]&0xC0 != 0x00 {
		return false
	}
	return binary.BigEndian.Uint32(data[4:8]) == MagicCookie
}

// MsgType extracts the message type from a STUN message header.
func MsgType(data []byte) (uint16, bool) {
	if len(data) < 2 {
		return 0, false
	}
	return binary.BigEndian.Uint16(data[0:2]), true
}

// TxnID extracts the transaction ID from a STUN message header.
func TxnID(data []byte) ([12]byte, bool) {
	var id [12]byte
	if len(data) < HeaderSize {
		return id, false
	}
	copy(id[:], data[8:20])
	return id, true
}

// BuildMessage constructs a STUN message with the given type, transaction ID,
// and attributes. Handles 4-byte padding per RFC 5389 section 15.
func BuildMessage(msgType uint16, txnID [12]byte, attrs []Attr) []byte {
	bodyLen := 0
	for _, a := range attrs {
		bodyLen += 4 + ((len(a.Value) + 3) & ^3)
	}

	buf := make([]byte, HeaderSize+bodyLen)
	binary.BigEndian.PutUint16(buf[0:2], msgType)
	binary.BigEndian.PutUint16(buf[2:4], uint16(bodyLen))
	binary.BigEndian.PutUint32(buf[4:8], MagicCookie)
	copy(buf[8:20], txnID[:])

	offset := HeaderSize
	for _, a := range attrs {
		binary.BigEndian.PutUint16(buf[offset:offset+2], a.Type)
		binary.BigEndian.PutUint16(buf[offset+2:offset+4], uint16(len(a.Value)))
		copy(buf[offset+4:], a.Value)
		offset += 4 + ((len(a.Value) + 3) & ^3)
	}

	return buf
}

// ParseAttrs parses STUN attributes from the body portion of a message
// (after the 20-byte header).
func ParseAttrs(data []byte) []Attr {
	var result []Attr
	offset := 0
	for offset+4 <= len(data) {
		attrType := binary.BigEndian.Uint16(data[offset : offset+2])
		attrLen := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		attrStart := offset + 4
		if attrStart+attrLen > len(data) {
			break
		}
		value := make([]byte, attrLen)
		copy(value, data[attrStart:attrStart+attrLen])
		result = append(result, Attr{Type: attrType, Value: value})
		offset = attrStart + ((attrLen + 3) & ^3)
	}
	return result
}

// AppendIntegrity appends a MESSAGE-INTEGRITY attribute (HMAC-SHA1) to a STUN
// message. Per RFC 5389 section 15.4, the message length is adjusted before
// computing the HMAC. Returns the extended message.
func AppendIntegrity(msg []byte, key []byte) []byte {
	// MESSAGE-INTEGRITY adds 24 bytes (4-byte attr header + 20-byte HMAC).
	newLen := uint16(len(msg) - HeaderSize + 24)
	binary.BigEndian.PutUint16(msg[2:4], newLen)

	mac := hmac.New(sha1.New, key)
	mac.Write(msg)
	hash := mac.Sum(nil)

	var attr [24]byte
	binary.BigEndian.PutUint16(attr[0:2], AttrMessageIntegrity)
	binary.BigEndian.PutUint16(attr[2:4], 20)
	copy(attr[4:], hash)

	return append(msg, attr[:]...)
}

// VerifyIntegrity verifies the MESSAGE-INTEGRITY attribute in a STUN message.
// miOffset is the byte offset of the MESSAGE-INTEGRITY attribute.
func VerifyIntegrity(msg []byte, miOffset int, key []byte) bool {
	if miOffset+24 > len(msg) || miOffset < HeaderSize {
		return false
	}

	buf := make([]byte, miOffset)
	copy(buf, msg[:miOffset])
	newLen := uint16(miOffset - HeaderSize + 24)
	binary.BigEndian.PutUint16(buf[2:4], newLen)

	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	expected := mac.Sum(nil)

	return hmac.Equal(msg[miOffset+4:miOffset+24], expected)
}

// EncodeXORAddr encodes a UDP address as a STUN XOR-MAPPED-ADDRESS attribute
// value. IPv4 only; panics on IPv6.
func EncodeXORAddr(addr *net.UDPAddr) []byte {
	ip4 := addr.IP.To4()
	if ip4 == nil {
		panic("stun: EncodeXORAddr: IPv6 not supported")
	}

	buf := make([]byte, 8)
	buf[1] = FamilyIPv4
	binary.BigEndian.PutUint16(buf[2:4], uint16(addr.Port)^uint16(MagicCookie>>16))
	binary.BigEndian.PutUint32(buf[4:8], binary.BigEndian.Uint32(ip4)^MagicCookie)
	return buf
}

// ParseXORAddr decodes a STUN XOR-MAPPED-ADDRESS (or XOR-RELAYED-ADDRESS,
// XOR-PEER-ADDRESS) attribute value. IPv4 only.
func ParseXORAddr(data []byte) (*net.UDPAddr, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("stun: XOR address too short")
	}
	if data[1] != FamilyIPv4 {
		return nil, fmt.Errorf("stun: unsupported address family: %d", data[1])
	}

	port := binary.BigEndian.Uint16(data[2:4]) ^ uint16(MagicCookie>>16)
	ip := binary.BigEndian.Uint32(data[4:8]) ^ MagicCookie

	return &net.UDPAddr{
		IP:   net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip)),
		Port: int(port),
	}, nil
}

// LongTermKey computes the TURN long-term credential key: MD5(username:realm:password).
func LongTermKey(username, realm, password string) []byte {
	h := md5.Sum([]byte(username + ":" + realm + ":" + password))
	return h[:]
}

// BuildBindingResponse constructs a STUN Binding Response with XOR-MAPPED-ADDRESS
// and MESSAGE-INTEGRITY. Used by ICE-Lite to respond to connectivity checks.
func BuildBindingResponse(txnID [12]byte, addr *net.UDPAddr, key []byte) []byte {
	msg := BuildMessage(BindingResponse, txnID, []Attr{{
		Type:  AttrXORMappedAddress,
		Value: EncodeXORAddr(addr),
	}})
	return AppendIntegrity(msg, key)
}

// FindAttr returns the first attribute with the given type, or nil if not found.
func FindAttr(attrs []Attr, attrType uint16) []byte {
	for _, a := range attrs {
		if a.Type == attrType {
			return a.Value
		}
	}
	return nil
}

// FindAttrOffset finds the byte offset of a specific STUN attribute within a raw message.
// Returns -1 if not found.
func FindAttrOffset(msg []byte, targetType uint16) int {
	if len(msg) < HeaderSize {
		return -1
	}
	offset := HeaderSize
	for offset+4 <= len(msg) {
		attrType := uint16(msg[offset])<<8 | uint16(msg[offset+1])
		attrLen := int(uint16(msg[offset+2])<<8 | uint16(msg[offset+3]))
		if attrType == targetType {
			return offset
		}
		offset += 4 + ((attrLen + 3) & ^3)
	}
	return -1
}

// ParseErrorCode extracts the error code and reason from an ERROR-CODE attribute.
func ParseErrorCode(attrs []Attr) (int, string) {
	val := FindAttr(attrs, AttrErrorCode)
	if len(val) < 4 {
		return 0, ""
	}
	class := int(val[2]&0x07) * 100
	number := int(val[3])
	reason := ""
	if len(val) > 4 {
		reason = string(val[4:])
	}
	return class + number, reason
}
