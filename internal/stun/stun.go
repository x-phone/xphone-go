// Package stun implements a STUN Binding client (RFC 5389) and provides
// shared STUN message primitives used by the TURN and ICE packages.
package stun

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

const (
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

	txnID := GenerateTxnID()

	req := BuildMessage(BindingRequest, txnID, nil)
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

// parseBindingResponse parses a STUN Binding Response and extracts the
// mapped address. Prefers XOR-MAPPED-ADDRESS over MAPPED-ADDRESS.
func parseBindingResponse(data []byte, expectedTxnID [12]byte) (string, int, error) {
	if len(data) < HeaderSize {
		return "", 0, fmt.Errorf("stun: response too short")
	}

	msgType := binary.BigEndian.Uint16(data[0:2])
	if msgType != BindingResponse {
		return "", 0, fmt.Errorf("stun: unexpected message type: 0x%04x", msgType)
	}

	msgLen := int(binary.BigEndian.Uint16(data[2:4]))
	cookie := binary.BigEndian.Uint32(data[4:8])
	if cookie != MagicCookie {
		return "", 0, fmt.Errorf("stun: bad magic cookie")
	}

	var txnID [12]byte
	copy(txnID[:], data[8:20])
	if txnID != expectedTxnID {
		return "", 0, fmt.Errorf("stun: transaction ID mismatch")
	}

	if len(data) < HeaderSize+msgLen {
		return "", 0, fmt.Errorf("stun: truncated response")
	}

	// Parse attributes looking for XOR-MAPPED-ADDRESS (preferred) or MAPPED-ADDRESS.
	attrs := data[HeaderSize : HeaderSize+msgLen]
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
		case AttrXORMappedAddress:
			addr, err := ParseXORAddr(attrData)
			if err != nil {
				return "", 0, err
			}
			return addr.IP.String(), addr.Port, nil
		case AttrMappedAddress:
			if ip, port, err := parseMappedAddress(attrData); err == nil {
				mappedIP = ip
				mappedPort = port
			}
		default:
			if attrType < 0x8000 {
				return "", 0, fmt.Errorf("stun: unknown comprehension-required attribute: 0x%04x", attrType)
			}
		}

		paddedLen := (attrLen + 3) & ^3
		offset = attrStart + paddedLen
	}

	if mappedIP != "" {
		return mappedIP, mappedPort, nil
	}
	return "", 0, fmt.Errorf("stun: no mapped address in response")
}

// parseMappedAddress parses a STUN MAPPED-ADDRESS attribute value.
func parseMappedAddress(data []byte) (string, int, error) {
	if len(data) < 8 {
		return "", 0, fmt.Errorf("stun: MAPPED-ADDRESS too short")
	}

	if data[1] != FamilyIPv4 {
		return "", 0, fmt.Errorf("stun: unsupported address family: %d", data[1])
	}

	port := binary.BigEndian.Uint16(data[2:4])
	addr := net.IPv4(data[4], data[5], data[6], data[7])
	return addr.String(), int(port), nil
}
