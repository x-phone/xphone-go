package sip

import (
	"net"
	"testing"
	"time"
)

// newTestPair creates two UDP conns connected to each other for testing.
func newTestPair(t *testing.T) (*Conn, *Conn) {
	t.Helper()
	c1, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}
	c2, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		c1.Close()
		t.Fatalf("Listen() error: %v", err)
	}
	return c1, c2
}

// respondWith reads the next request from server, echoes its Via branch, and sends a response.
func respondWith(t *testing.T, server *Conn, code int, reason string) {
	t.Helper()
	data, addr, err := server.Receive(time.Second)
	if err != nil {
		t.Fatalf("server Receive() error: %v", err)
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("server Parse() error: %v", err)
	}

	msg := &Message{StatusCode: code, Reason: reason}
	msg.SetHeader("Via", parsed.Header("Via"))
	msg.SetHeader("Call-ID", parsed.Header("Call-ID"))
	msg.SetHeader("CSeq", parsed.Header("CSeq"))
	err = server.Send(msg.Bytes(), addr)
	if err != nil {
		t.Fatalf("server Send() error: %v", err)
	}
}

func TestTransaction_SendAndReceive(t *testing.T) {
	client, server := newTestPair(t)
	defer client.Close()
	defer server.Close()

	tm := NewTransactionManager(client)
	defer tm.Stop()

	// Build a REGISTER request.
	req := &Message{
		Method:     "REGISTER",
		RequestURI: "sip:pbx.local",
	}
	req.SetHeader("Call-ID", "tx-test-1@local")
	req.SetHeader("CSeq", "1 REGISTER")

	// Send request and expect a response.
	go respondWith(t, server, 200, "OK")

	resp, err := tm.Send(req, server.LocalAddr().(*net.UDPAddr), 2*time.Second)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
}

func TestTransaction_Timeout(t *testing.T) {
	client, server := newTestPair(t)
	defer client.Close()
	defer server.Close()

	tm := NewTransactionManager(client)
	defer tm.Stop()

	// Build a request — nobody will respond.
	req := &Message{
		Method:     "REGISTER",
		RequestURI: "sip:pbx.local",
	}
	req.SetHeader("Call-ID", "tx-timeout@local")
	req.SetHeader("CSeq", "1 REGISTER")

	_, err := tm.Send(req, server.LocalAddr().(*net.UDPAddr), 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestTransaction_BranchMatching(t *testing.T) {
	client, server := newTestPair(t)
	defer client.Close()
	defer server.Close()

	tm := NewTransactionManager(client)
	defer tm.Stop()

	// Send a request.
	req := &Message{
		Method:     "REGISTER",
		RequestURI: "sip:pbx.local",
	}
	req.SetHeader("Call-ID", "tx-branch@local")
	req.SetHeader("CSeq", "1 REGISTER")

	go func() {
		// Read the request from server side, extract the branch from Via.
		data, addr, err := server.Receive(time.Second)
		if err != nil {
			return
		}
		parsed, err := Parse(data)
		if err != nil {
			return
		}
		branch := parsed.ViaBranch()

		// Send response with matching branch.
		resp := &Message{StatusCode: 200, Reason: "OK"}
		resp.SetHeader("Via", "SIP/2.0/UDP 127.0.0.1;branch="+branch)
		resp.SetHeader("Call-ID", "tx-branch@local")
		resp.SetHeader("CSeq", "1 REGISTER")
		server.Send(resp.Bytes(), addr)
	}()

	resp, err := tm.Send(req, server.LocalAddr().(*net.UDPAddr), 2*time.Second)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
}

func TestTransaction_ProvisionalThenFinal(t *testing.T) {
	client, server := newTestPair(t)
	defer client.Close()
	defer server.Close()

	tm := NewTransactionManager(client)
	defer tm.Stop()

	req := &Message{
		Method:     "INVITE",
		RequestURI: "sip:1002@pbx.local",
	}
	req.SetHeader("Call-ID", "tx-prov@local")
	req.SetHeader("CSeq", "1 INVITE")

	go func() {
		data, addr, err := server.Receive(time.Second)
		if err != nil {
			return
		}
		parsed, _ := Parse(data)
		branch := parsed.ViaBranch()

		// Send 100 Trying.
		trying := &Message{StatusCode: 100, Reason: "Trying"}
		trying.SetHeader("Via", "SIP/2.0/UDP 127.0.0.1;branch="+branch)
		trying.SetHeader("Call-ID", "tx-prov@local")
		trying.SetHeader("CSeq", "1 INVITE")
		server.Send(trying.Bytes(), addr)

		time.Sleep(20 * time.Millisecond)

		// Send 180 Ringing.
		ringing := &Message{StatusCode: 180, Reason: "Ringing"}
		ringing.SetHeader("Via", "SIP/2.0/UDP 127.0.0.1;branch="+branch)
		ringing.SetHeader("Call-ID", "tx-prov@local")
		ringing.SetHeader("CSeq", "1 INVITE")
		server.Send(ringing.Bytes(), addr)

		time.Sleep(20 * time.Millisecond)

		// Send 200 OK.
		ok := &Message{StatusCode: 200, Reason: "OK"}
		ok.SetHeader("Via", "SIP/2.0/UDP 127.0.0.1;branch="+branch)
		ok.SetHeader("Call-ID", "tx-prov@local")
		ok.SetHeader("CSeq", "1 INVITE")
		server.Send(ok.Bytes(), addr)
	}()

	// Send should return the first response (100 Trying).
	resp, err := tm.Send(req, server.LocalAddr().(*net.UDPAddr), 2*time.Second)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if resp.StatusCode != 100 {
		t.Errorf("first response StatusCode = %d, want 100", resp.StatusCode)
	}

	// ReadResponse should get subsequent responses.
	resp2, err := tm.ReadResponse(parsed(req).ViaBranch(), 2*time.Second)
	if err != nil {
		t.Fatalf("ReadResponse() error: %v", err)
	}
	// Could be 180 or 200 depending on timing; accept either.
	if resp2.StatusCode < 180 {
		t.Errorf("second response StatusCode = %d, want >= 180", resp2.StatusCode)
	}
}

func TestTransaction_MultipleInFlight(t *testing.T) {
	client, server := newTestPair(t)
	defer client.Close()
	defer server.Close()

	tm := NewTransactionManager(client)
	defer tm.Stop()

	// Server: respond to every request it receives.
	go func() {
		for i := 0; i < 2; i++ {
			data, addr, err := server.Receive(time.Second)
			if err != nil {
				return
			}
			parsed, _ := Parse(data)
			branch := parsed.ViaBranch()
			callID := parsed.Header("Call-ID")

			resp := &Message{StatusCode: 200, Reason: "OK"}
			resp.SetHeader("Via", "SIP/2.0/UDP 127.0.0.1;branch="+branch)
			resp.SetHeader("Call-ID", callID)
			resp.SetHeader("CSeq", parsed.Header("CSeq"))
			server.Send(resp.Bytes(), addr)
		}
	}()

	// Send two concurrent transactions.
	type result struct {
		resp *Message
		err  error
	}
	ch1 := make(chan result, 1)
	ch2 := make(chan result, 1)

	req1 := &Message{Method: "REGISTER", RequestURI: "sip:pbx.local"}
	req1.SetHeader("Call-ID", "multi-1@local")
	req1.SetHeader("CSeq", "1 REGISTER")

	req2 := &Message{Method: "REGISTER", RequestURI: "sip:pbx.local"}
	req2.SetHeader("Call-ID", "multi-2@local")
	req2.SetHeader("CSeq", "2 REGISTER")

	go func() {
		resp, err := tm.Send(req1, server.LocalAddr().(*net.UDPAddr), 2*time.Second)
		ch1 <- result{resp, err}
	}()
	go func() {
		resp, err := tm.Send(req2, server.LocalAddr().(*net.UDPAddr), 2*time.Second)
		ch2 <- result{resp, err}
	}()

	r1 := <-ch1
	r2 := <-ch2

	if r1.err != nil {
		t.Fatalf("Send(req1) error: %v", r1.err)
	}
	if r2.err != nil {
		t.Fatalf("Send(req2) error: %v", r2.err)
	}
	if r1.resp.StatusCode != 200 {
		t.Errorf("req1 StatusCode = %d, want 200", r1.resp.StatusCode)
	}
	if r2.resp.StatusCode != 200 {
		t.Errorf("req2 StatusCode = %d, want 200", r2.resp.StatusCode)
	}
}

func TestTransactionManager_Stop(t *testing.T) {
	client, _ := newTestPair(t)
	defer client.Close()

	tm := NewTransactionManager(client)
	tm.Stop()

	// After Stop, Send should fail.
	req := &Message{Method: "REGISTER", RequestURI: "sip:pbx.local"}
	req.SetHeader("Call-ID", "stop@local")
	req.SetHeader("CSeq", "1 REGISTER")
	remote := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9999}
	_, err := tm.Send(req, remote, time.Second)
	if err == nil {
		t.Fatal("Send() after Stop() should fail")
	}
}

// parsed is a helper that parses a message's serialized form to extract auto-generated fields.
func parsed(msg *Message) *Message {
	p, _ := Parse(msg.Bytes())
	return p
}
