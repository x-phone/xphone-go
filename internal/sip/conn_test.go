package sip

import (
	"net"
	"testing"
	"time"
)

func TestConn_SendReceive(t *testing.T) {
	// Start a listener.
	c1, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}
	defer c1.Close()

	// Start a second conn to send to c1.
	c2, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}
	defer c2.Close()

	// Build a SIP message.
	msg := &Message{
		Method:     "REGISTER",
		RequestURI: "sip:pbx.local",
	}
	msg.SetHeader("Via", "SIP/2.0/UDP 127.0.0.1;branch=z9hG4bKtest")
	msg.SetHeader("Call-ID", "conn-test@local")
	msg.SetHeader("CSeq", "1 REGISTER")

	// Send from c2 to c1.
	err = c2.Send(msg.Bytes(), c1.LocalAddr())
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	// Receive on c1.
	data, addr, err := c1.Receive(time.Second)
	if err != nil {
		t.Fatalf("Receive() error: %v", err)
	}
	if addr == nil {
		t.Fatal("Receive() returned nil addr")
	}

	// Parse and verify.
	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if parsed.Method != "REGISTER" {
		t.Errorf("Method = %q, want REGISTER", parsed.Method)
	}
	if parsed.Header("Call-ID") != "conn-test@local" {
		t.Errorf("Call-ID = %q", parsed.Header("Call-ID"))
	}
}

func TestConn_ReceiveTimeout(t *testing.T) {
	c, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}
	defer c.Close()

	_, _, err = c.Receive(50 * time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !isTimeout(err) {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

func TestConn_LocalAddr(t *testing.T) {
	c, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}
	defer c.Close()

	addr := c.LocalAddr()
	if addr == nil {
		t.Fatal("LocalAddr() returned nil")
	}
	// Should have a non-zero port.
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		t.Fatalf("LocalAddr() type = %T, want *net.UDPAddr", addr)
	}
	if udpAddr.Port == 0 {
		t.Error("LocalAddr().Port = 0, want non-zero")
	}
}

func TestConn_Close(t *testing.T) {
	c, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	// Sending after close should fail.
	remote := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9999}
	err = c.Send([]byte("test"), remote)
	if err == nil {
		t.Error("Send() after Close() should fail")
	}
}

func TestConn_LargeMessage(t *testing.T) {
	c1, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}
	defer c1.Close()

	c2, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}
	defer c2.Close()

	// Build a message with a large SDP body (~1KB).
	body := make([]byte, 1024)
	for i := range body {
		body[i] = 'x'
	}
	msg := &Message{
		Method:     "INVITE",
		RequestURI: "sip:1002@pbx.local",
		Body:       body,
	}
	msg.SetHeader("Via", "SIP/2.0/UDP 127.0.0.1;branch=z9hG4bKlarge")
	msg.SetHeader("Call-ID", "large-test@local")
	msg.SetHeader("CSeq", "1 INVITE")
	msg.SetHeader("Content-Type", "application/sdp")

	err = c2.Send(msg.Bytes(), c1.LocalAddr())
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	data, _, err := c1.Receive(time.Second)
	if err != nil {
		t.Fatalf("Receive() error: %v", err)
	}

	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if len(parsed.Body) != 1024 {
		t.Errorf("Body length = %d, want 1024", len(parsed.Body))
	}
}

// isTimeout checks if an error is a network timeout.
func isTimeout(err error) bool {
	netErr, ok := err.(net.Error)
	return ok && netErr.Timeout()
}
