// Package stun implements a minimal STUN Binding client (RFC 5389).
//
// It sends a STUN Binding Request to a public STUN server and parses the
// response to discover the NAT-mapped (server-reflexive) address.
// Only the bare minimum of RFC 5389 is implemented — no authentication,
// no FINGERPRINT, no long-term credentials. This covers the common case
// of discovering a mapped address for SIP/RTP NAT traversal.
package stun

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

const (
	bindingRequest  uint16 = 0x0001
	bindingResponse uint16 = 0x0101
	magicCookie     uint32 = 0x2112A442
	headerSize             = 20

	attrMappedAddress    uint16 = 0x0001
	attrXORMappedAddress uint16 = 0x0020

	familyIPv4 byte = 0x01

	// DefaultServer is Google's public STUN server.
	DefaultServer = "stun.l.google.com:19302"

	// DefaultTimeout is the default STUN request timeout.
	DefaultTimeout = 3 * time.Second
)

// MappedAddr sends a STUN Binding Request to discover the NAT-mapped address.
// server is "host:port" (e.g. "stun.l.google.com:19302").
// Returns the mapped IP and port as seen by the STUN server.
func MappedAddr(server string, timeout time.Duration) (ip string, port int, err error) {
	addr, err := resolveServer(server)
	if err != nil {
		return "", 0, err
	}

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return "", 0, fmt.Errorf("stun: listen: %w", err)
	}
	defer conn.Close()

	var txnID [12]byte
	if _, err := rand.Read(txnID[:]); err != nil {
		return "", 0, fmt.Errorf("stun: random: %w", err)
	}

	req := buildBindingRequest(txnID)
	if _, err := conn.WriteTo(req, addr); err != nil {
		return "", 0, fmt.Errorf("stun: send: %w", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", 0, fmt.Errorf("stun: set deadline: %w", err)
	}
	buf := make([]byte, 576) // RFC 5389 §7.1: responses fit in 576 bytes
	n, from, err := conn.ReadFrom(buf)
	if err != nil {
		return "", 0, fmt.Errorf("stun: recv: %w", err)
	}

	// Validate source address (RFC 5389 §7.3.1).
	fromUDP, ok := from.(*net.UDPAddr)
	if !ok || !fromUDP.IP.Equal(addr.IP) {
		return "", 0, fmt.Errorf("stun: response from %s, expected %s", from, addr)
	}

	return parseBindingResponse(buf[:n], txnID)
}

// resolveServer resolves a STUN server address string (host:port) to a
// *net.UDPAddr. Prefers IPv4 addresses.
func resolveServer(server string) (*net.UDPAddr, error) {
	host, portStr, err := net.SplitHostPort(server)
	if err != nil {
		return nil, fmt.Errorf("stun: invalid server address %q: %w", server, err)
	}

	port, err := net.LookupPort("udp", portStr)
	if err != nil {
		return nil, fmt.Errorf("stun: invalid port in %q: %w", server, err)
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("stun: resolve %q: %w", server, err)
	}

	// Prefer IPv4.
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			return &net.UDPAddr{IP: ip4, Port: port}, nil
		}
	}
	if len(ips) > 0 {
		return &net.UDPAddr{IP: ips[0], Port: port}, nil
	}

	return nil, fmt.Errorf("stun: no addresses for %q", server)
}

// buildBindingRequest creates a STUN Binding Request message.
func buildBindingRequest(txnID [12]byte) []byte {
	buf := make([]byte, headerSize)
	binary.BigEndian.PutUint16(buf[0:2], bindingRequest)
	binary.BigEndian.PutUint16(buf[2:4], 0) // no attributes
	binary.BigEndian.PutUint32(buf[4:8], magicCookie)
	copy(buf[8:20], txnID[:])
	return buf
}

// parseBindingResponse parses a STUN Binding Response and extracts the
// mapped address. Prefers XOR-MAPPED-ADDRESS over MAPPED-ADDRESS.
func parseBindingResponse(data []byte, expectedTxnID [12]byte) (string, int, error) {
	if len(data) < headerSize {
		return "", 0, fmt.Errorf("stun: response too short")
	}

	msgType := binary.BigEndian.Uint16(data[0:2])
	if msgType != bindingResponse {
		return "", 0, fmt.Errorf("stun: unexpected message type: 0x%04x", msgType)
	}

	msgLen := int(binary.BigEndian.Uint16(data[2:4]))
	cookie := binary.BigEndian.Uint32(data[4:8])
	if cookie != magicCookie {
		return "", 0, fmt.Errorf("stun: bad magic cookie")
	}

	var txnID [12]byte
	copy(txnID[:], data[8:20])
	if txnID != expectedTxnID {
		return "", 0, fmt.Errorf("stun: transaction ID mismatch")
	}

	if len(data) < headerSize+msgLen {
		return "", 0, fmt.Errorf("stun: truncated response")
	}

	// Parse attributes looking for XOR-MAPPED-ADDRESS (preferred) or MAPPED-ADDRESS.
	attrs := data[headerSize : headerSize+msgLen]
	var mappedIP string
	var mappedPort int

	offset := 0
	for offset+4 <= len(attrs) {
		attrType := binary.BigEndian.Uint16(attrs[offset : offset+2])
		attrLen := int(binary.BigEndian.Uint16(attrs[offset+2 : offset+4]))
		attrStart := offset + 4

		if attrStart+attrLen > len(attrs) {
			return "", 0, fmt.Errorf("stun: truncated attribute")
		}

		attrData := attrs[attrStart : attrStart+attrLen]

		switch attrType {
		case attrXORMappedAddress:
			// XOR-MAPPED-ADDRESS is preferred — return immediately.
			return parseXORMappedAddress(attrData)
		case attrMappedAddress:
			// Fall back to MAPPED-ADDRESS if no XOR variant found.
			if ip, port, err := parseMappedAddress(attrData); err == nil {
				mappedIP = ip
				mappedPort = port
			}
		default:
			if attrType < 0x8000 {
				// Unknown comprehension-required attribute (RFC 5389 §15).
				return "", 0, fmt.Errorf("stun: unknown comprehension-required attribute: 0x%04x", attrType)
			}
			// Skip comprehension-optional attributes.
		}

		// Attributes are padded to 4-byte boundaries (RFC 5389 §15).
		paddedLen := (attrLen + 3) & ^3
		offset = attrStart + paddedLen
	}

	if mappedIP != "" {
		return mappedIP, mappedPort, nil
	}
	return "", 0, fmt.Errorf("stun: no mapped address in response")
}

// parseXORMappedAddress parses a STUN XOR-MAPPED-ADDRESS attribute value.
func parseXORMappedAddress(data []byte) (string, int, error) {
	if len(data) < 8 {
		return "", 0, fmt.Errorf("stun: XOR-MAPPED-ADDRESS too short")
	}

	family := data[1]
	if family != familyIPv4 {
		return "", 0, fmt.Errorf("stun: unsupported address family: %d", family)
	}

	// Port is XOR'd with top 16 bits of magic cookie.
	xorPort := binary.BigEndian.Uint16(data[2:4])
	port := xorPort ^ uint16(magicCookie>>16)

	// IPv4 address is XOR'd with the magic cookie.
	xorIP := binary.BigEndian.Uint32(data[4:8])
	ip := xorIP ^ magicCookie

	addr := net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip))
	return addr.String(), int(port), nil
}

// parseMappedAddress parses a STUN MAPPED-ADDRESS attribute value.
func parseMappedAddress(data []byte) (string, int, error) {
	if len(data) < 8 {
		return "", 0, fmt.Errorf("stun: MAPPED-ADDRESS too short")
	}

	family := data[1]
	if family != familyIPv4 {
		return "", 0, fmt.Errorf("stun: unsupported address family: %d", family)
	}

	port := binary.BigEndian.Uint16(data[2:4])
	addr := net.IPv4(data[4], data[5], data[6], data[7])
	return addr.String(), int(port), nil
}
