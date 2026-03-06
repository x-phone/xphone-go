package sip

import (
	"context"
	"net"
	"testing"
	"time"
)

// fakeServer is a test helper that acts as a SIP server.
// It reads requests and sends pre-programmed responses.
type fakeServer struct {
	conn *Conn
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	c, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}
	return &fakeServer{conn: c}
}

func (s *fakeServer) Addr() *net.UDPAddr {
	return s.conn.LocalAddr().(*net.UDPAddr)
}

func (s *fakeServer) Close() {
	s.conn.Close()
}

// readAndRespond reads one request and sends a response with matching dialog headers.
func (s *fakeServer) readAndRespond(t *testing.T, code int, reason string, extraHeaders map[string]string) *Message {
	t.Helper()
	data, addr, err := s.conn.Receive(2 * time.Second)
	if err != nil {
		t.Errorf("server Receive() error: %v", err)
		return nil
	}
	req, err := Parse(data)
	if err != nil {
		t.Errorf("server Parse() error: %v", err)
		return nil
	}

	resp := &Message{StatusCode: code, Reason: reason}
	resp.SetHeader("Via", req.Header("Via"))
	resp.SetHeader("From", req.Header("From"))
	resp.SetHeader("To", req.Header("To"))
	resp.SetHeader("Call-ID", req.Header("Call-ID"))
	resp.SetHeader("CSeq", req.Header("CSeq"))
	for k, v := range extraHeaders {
		resp.SetHeader(k, v)
	}
	if err := s.conn.Send(resp.Bytes(), addr); err != nil {
		t.Errorf("server Send() error: %v", err)
		return nil
	}
	return req
}

// readAndChallenge reads a REGISTER and responds with 401 + challenge.
func (s *fakeServer) readAndChallenge(t *testing.T, realm, nonce string) *Message {
	t.Helper()
	return s.readAndRespond(t, 401, "Unauthorized", map[string]string{
		"WWW-Authenticate": `Digest realm="` + realm + `",nonce="` + nonce + `",algorithm=MD5`,
	})
}

func TestClient_Register(t *testing.T) {
	srv := newFakeServer(t)
	defer srv.Close()

	c, err := NewClient(ClientConfig{
		LocalAddr:  "127.0.0.1:0",
		ServerAddr: srv.Addr(),
		Username:   "1001",
		Password:   "test",
		Domain:     "pbx.local",
	})
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}
	defer c.Close()

	// Server will respond 200 OK.
	go srv.readAndRespond(t, 200, "OK", nil)

	code, _, err := c.SendRegister(context.Background())
	if err != nil {
		t.Fatalf("SendRegister() error: %v", err)
	}
	if code != 200 {
		t.Errorf("code = %d, want 200", code)
	}
}

func TestClient_RegisterWithAuth(t *testing.T) {
	srv := newFakeServer(t)
	defer srv.Close()

	c, err := NewClient(ClientConfig{
		LocalAddr:  "127.0.0.1:0",
		ServerAddr: srv.Addr(),
		Username:   "1001",
		Password:   "test",
		Domain:     "pbx.local",
	})
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}
	defer c.Close()

	// Server: first 401, then 200.
	go func() {
		srv.readAndChallenge(t, "asterisk", "nonce123")
		req := srv.readAndRespond(t, 200, "OK", nil)
		// The retry should have an Authorization header.
		auth := req.Header("Authorization")
		if auth == "" {
			t.Error("retry missing Authorization header")
		}
	}()

	code, _, err := c.SendRegister(context.Background())
	if err != nil {
		t.Fatalf("SendRegister() error: %v", err)
	}
	if code != 200 {
		t.Errorf("code = %d, want 200", code)
	}
}

func TestClient_RegisterTimeout(t *testing.T) {
	srv := newFakeServer(t)
	defer srv.Close()

	c, err := NewClient(ClientConfig{
		LocalAddr:  "127.0.0.1:0",
		ServerAddr: srv.Addr(),
		Username:   "1001",
		Password:   "test",
		Domain:     "pbx.local",
	})
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}
	defer c.Close()

	// No server response — should timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err = c.SendRegister(ctx)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestClient_RegisterHeaders(t *testing.T) {
	srv := newFakeServer(t)
	defer srv.Close()

	c, err := NewClient(ClientConfig{
		LocalAddr:  "127.0.0.1:0",
		ServerAddr: srv.Addr(),
		Username:   "1001",
		Password:   "test",
		Domain:     "pbx.local",
	})
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}
	defer c.Close()

	// Capture the request on the server side via channel.
	capturedCh := make(chan *Message, 1)
	go func() {
		capturedCh <- srv.readAndRespond(t, 200, "OK", nil)
	}()

	c.SendRegister(context.Background())

	var captured *Message
	select {
	case captured = <-capturedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for captured request")
	}
	if captured.Method != "REGISTER" {
		t.Errorf("Method = %q, want REGISTER", captured.Method)
	}
	if captured.RequestURI != "sip:pbx.local" {
		t.Errorf("RequestURI = %q, want sip:pbx.local", captured.RequestURI)
	}
	if captured.Header("From") == "" {
		t.Error("missing From header")
	}
	if captured.Header("To") == "" {
		t.Error("missing To header")
	}
	if captured.Header("Call-ID") == "" {
		t.Error("missing Call-ID header")
	}
	if captured.Header("CSeq") == "" {
		t.Error("missing CSeq header")
	}
	if captured.Header("Contact") == "" {
		t.Error("missing Contact header")
	}
}

func TestClient_Keepalive(t *testing.T) {
	srv := newFakeServer(t)
	defer srv.Close()

	c, err := NewClient(ClientConfig{
		LocalAddr:  "127.0.0.1:0",
		ServerAddr: srv.Addr(),
		Username:   "1001",
		Password:   "test",
		Domain:     "pbx.local",
	})
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}
	defer c.Close()

	// SendKeepalive should not error.
	if err := c.SendKeepalive(); err != nil {
		t.Fatalf("SendKeepalive() error: %v", err)
	}
}

func TestClient_OnIncoming(t *testing.T) {
	srv := newFakeServer(t)
	defer srv.Close()

	c, err := NewClient(ClientConfig{
		LocalAddr:  "127.0.0.1:0",
		ServerAddr: srv.Addr(),
		Username:   "1001",
		Password:   "test",
		Domain:     "pbx.local",
	})
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}
	defer c.Close()

	// Register incoming handler.
	gotIncoming := make(chan *Message, 1)
	c.OnIncoming(func(msg *Message) {
		gotIncoming <- msg
	})

	// Simulate an incoming INVITE from the server.
	invite := &Message{
		Method:     "INVITE",
		RequestURI: "sip:1001@127.0.0.1",
	}
	invite.SetHeader("Via", "SIP/2.0/UDP "+srv.Addr().String()+";branch=z9hG4bKincoming")
	invite.SetHeader("From", "<sip:1002@pbx.local>;tag=srv1")
	invite.SetHeader("To", "<sip:1001@pbx.local>")
	invite.SetHeader("Call-ID", "incoming-test@srv")
	invite.SetHeader("CSeq", "1 INVITE")
	srv.conn.Send(invite.Bytes(), c.LocalAddr())

	select {
	case msg := <-gotIncoming:
		if msg.Method != "INVITE" {
			t.Errorf("Method = %q, want INVITE", msg.Method)
		}
		if msg.Header("From") == "" {
			t.Error("missing From header")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for incoming INVITE")
	}
}

func TestClient_Close(t *testing.T) {
	srv := newFakeServer(t)
	defer srv.Close()

	c, err := NewClient(ClientConfig{
		LocalAddr:  "127.0.0.1:0",
		ServerAddr: srv.Addr(),
		Username:   "1001",
		Password:   "test",
		Domain:     "pbx.local",
	})
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	// SendRegister after close should fail.
	_, _, err = c.SendRegister(context.Background())
	if err == nil {
		t.Fatal("SendRegister() after Close() should fail")
	}
}

func TestClient_CSeqIncrements(t *testing.T) {
	srv := newFakeServer(t)
	defer srv.Close()

	c, err := NewClient(ClientConfig{
		LocalAddr:  "127.0.0.1:0",
		ServerAddr: srv.Addr(),
		Username:   "1001",
		Password:   "test",
		Domain:     "pbx.local",
	})
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}
	defer c.Close()

	// First REGISTER.
	ch1 := make(chan *Message, 1)
	go func() { ch1 <- srv.readAndRespond(t, 200, "OK", nil) }()
	c.SendRegister(context.Background())
	req1 := <-ch1

	// Second REGISTER — CSeq should be higher.
	ch2 := make(chan *Message, 1)
	go func() { ch2 <- srv.readAndRespond(t, 200, "OK", nil) }()
	c.SendRegister(context.Background())
	req2 := <-ch2

	seq1, _ := req1.CSeq()
	seq2, _ := req2.CSeq()
	if seq2 <= seq1 {
		t.Errorf("CSeq did not increment: %d -> %d", seq1, seq2)
	}
}
