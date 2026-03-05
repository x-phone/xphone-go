package sip

import (
	"net"
	"time"
)

// maxSIPMessageSize is the maximum size of a SIP message over UDP (RFC 3261 recommends MTU-safe sizes,
// but we allow up to 64KB for practical use).
const maxSIPMessageSize = 65535

// Conn wraps a UDP connection for sending and receiving SIP messages.
type Conn struct {
	conn   *net.UDPConn
	recvBuf []byte // reusable receive buffer
}

// Listen creates a new SIP UDP connection bound to the given address.
// Use "127.0.0.1:0" for ephemeral port.
func Listen(network, address string) (*Conn, error) {
	addr, err := net.ResolveUDPAddr(network, address)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP(network, addr)
	if err != nil {
		return nil, err
	}
	return &Conn{conn: conn, recvBuf: make([]byte, maxSIPMessageSize)}, nil
}

// Send sends raw data to the given address.
func (c *Conn) Send(data []byte, addr net.Addr) error {
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		return &net.AddrError{Err: "not a UDP address", Addr: addr.String()}
	}
	_, err := c.conn.WriteToUDP(data, udpAddr)
	return err
}

// Receive reads the next UDP packet with a timeout.
// Returns a copy of the raw data and the sender's address.
// Not safe for concurrent calls (single reusable buffer).
func (c *Conn) Receive(timeout time.Duration) ([]byte, net.Addr, error) {
	if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, nil, err
	}
	n, addr, err := c.conn.ReadFromUDP(c.recvBuf)
	if err != nil {
		return nil, nil, err
	}
	// Return a copy so the caller can hold the data safely.
	data := make([]byte, n)
	copy(data, c.recvBuf[:n])
	return data, addr, nil
}

// LocalAddr returns the local address the connection is bound to.
func (c *Conn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

// Close closes the underlying UDP connection.
func (c *Conn) Close() error {
	return c.conn.Close()
}
