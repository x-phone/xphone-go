package xphone

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/x-phone/xphone-go/testutil"
)

func TestSendMessage_Success(t *testing.T) {
	p := newPhone(Config{})
	applyDefaults(&p.cfg)

	tr := testutil.NewMockTransport()
	// Queue: REGISTER 200, MESSAGE 200, un-REGISTER 200.
	tr.RespondSequence(
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
	)

	p.connectWithTransport(tr)
	defer p.Disconnect()

	err := p.SendMessage(context.Background(), "sip:bob@example.com", "Hello")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if count := tr.CountSent("MESSAGE"); count != 1 {
		t.Errorf("expected 1 MESSAGE sent, got %d", count)
	}

	msg := tr.LastSent("MESSAGE")
	if msg == nil {
		t.Fatal("no MESSAGE sent")
	}
	if msg.URI != "sip:bob@example.com" {
		t.Errorf("expected URI sip:bob@example.com, got %q", msg.URI)
	}
	if msg.Header("Content-Type") != "text/plain" {
		t.Errorf("expected Content-Type text/plain, got %q", msg.Header("Content-Type"))
	}
}

func TestSendMessage_Rejected(t *testing.T) {
	p := newPhone(Config{})
	applyDefaults(&p.cfg)

	tr := testutil.NewMockTransport()
	// Queue: REGISTER 200, MESSAGE 403, un-REGISTER 200.
	tr.RespondSequence(
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 403, Header: "Forbidden"},
		testutil.Response{Code: 200, Header: "OK"},
	)

	p.connectWithTransport(tr)
	defer p.Disconnect()

	err := p.SendMessage(context.Background(), "sip:bob@example.com", "Hello")
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	if err.Error() != "MESSAGE 403 Forbidden" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSendMessage_NotConnected(t *testing.T) {
	p := newPhone(Config{})
	applyDefaults(&p.cfg)

	err := p.SendMessage(context.Background(), "sip:bob@example.com", "Hello")
	if err != ErrNotConnected {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
}

func TestSendMessageWithType_CustomContentType(t *testing.T) {
	p := newPhone(Config{})
	applyDefaults(&p.cfg)

	tr := testutil.NewMockTransport()
	// Queue: REGISTER 200, MESSAGE 200, un-REGISTER 200.
	tr.RespondSequence(
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
	)

	p.connectWithTransport(tr)
	defer p.Disconnect()

	err := p.SendMessageWithType(context.Background(), "sip:bob@example.com", "application/json", `{"text":"hi"}`)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	msg := tr.LastSent("MESSAGE")
	if msg == nil {
		t.Fatal("no MESSAGE sent")
	}
	if msg.Header("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", msg.Header("Content-Type"))
	}
}

func TestOnMessage_IncomingMessage(t *testing.T) {
	p := newPhone(Config{})
	applyDefaults(&p.cfg)

	var mu sync.Mutex
	var got SipMessage
	done := make(chan struct{})

	p.OnMessage(func(msg SipMessage) {
		mu.Lock()
		got = msg
		mu.Unlock()
		close(done)
	})

	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.RespondWith(200, "OK") // un-REGISTER

	p.connectWithTransport(tr)
	defer p.Disconnect()

	tr.SimulateMessage("sip:alice@example.com", "sip:bob@example.com", "text/plain", "Hello Bob")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OnMessage callback")
	}

	mu.Lock()
	defer mu.Unlock()
	if got.From != "sip:alice@example.com" {
		t.Errorf("expected From sip:alice@example.com, got %q", got.From)
	}
	if got.To != "sip:bob@example.com" {
		t.Errorf("expected To sip:bob@example.com, got %q", got.To)
	}
	if got.ContentType != "text/plain" {
		t.Errorf("expected ContentType text/plain, got %q", got.ContentType)
	}
	if got.Body != "Hello Bob" {
		t.Errorf("expected Body 'Hello Bob', got %q", got.Body)
	}
}

func TestOnMessage_CallbackSetAfterConnect(t *testing.T) {
	p := newPhone(Config{})
	applyDefaults(&p.cfg)

	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.RespondWith(200, "OK") // un-REGISTER

	p.connectWithTransport(tr)
	defer p.Disconnect()

	var mu sync.Mutex
	var got SipMessage
	done := make(chan struct{})
	p.OnMessage(func(msg SipMessage) {
		mu.Lock()
		got = msg
		mu.Unlock()
		close(done)
	})

	tr.SimulateMessage("sip:alice@example.com", "sip:bob@example.com", "text/plain", "Late callback")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OnMessage callback")
	}

	mu.Lock()
	defer mu.Unlock()
	if got.From != "sip:alice@example.com" {
		t.Errorf("expected From sip:alice@example.com, got %q", got.From)
	}
	if got.To != "sip:bob@example.com" {
		t.Errorf("expected To sip:bob@example.com, got %q", got.To)
	}
	if got.ContentType != "text/plain" {
		t.Errorf("expected ContentType text/plain, got %q", got.ContentType)
	}
	if got.Body != "Late callback" {
		t.Errorf("expected Body 'Late callback', got %q", got.Body)
	}
}

func TestMockPhone_SimulateMessage(t *testing.T) {
	p := NewMockPhone()

	var got SipMessage
	done := make(chan struct{})
	p.OnMessage(func(msg SipMessage) {
		got = msg
		close(done)
	})

	p.SimulateMessage(SipMessage{
		From:        "sip:alice@example.com",
		To:          "sip:bob@example.com",
		ContentType: "text/plain",
		Body:        "Test message",
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for MockPhone OnMessage callback")
	}

	if got.From != "sip:alice@example.com" {
		t.Errorf("expected From sip:alice@example.com, got %q", got.From)
	}
	if got.Body != "Test message" {
		t.Errorf("expected Body 'Test message', got %q", got.Body)
	}
}

func TestMockPhone_SendMessage(t *testing.T) {
	p := NewMockPhone()
	if err := p.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Disconnect()

	// MockPhone.SendMessage always returns nil.
	err := p.SendMessage(context.Background(), "sip:bob@example.com", "Hello")
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}
