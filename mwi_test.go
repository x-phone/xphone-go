package xphone

import (
	"sync"
	"testing"
	"time"

	"github.com/x-phone/xphone-go/testutil"
)

// --- Parser tests ---

func TestParseMessageSummary_Basic(t *testing.T) {
	body := "Messages-Waiting: yes\r\nVoice-Message: 2/8\r\n"
	status, ok := parseMessageSummary(body)
	if !ok {
		t.Fatal("expected ok")
	}
	if !status.MessagesWaiting {
		t.Error("expected MessagesWaiting=true")
	}
	if status.NewMessages != 2 || status.OldMessages != 8 {
		t.Errorf("expected 2/8, got %d/%d", status.NewMessages, status.OldMessages)
	}
}

func TestParseMessageSummary_No(t *testing.T) {
	body := "Messages-Waiting: no\r\n"
	status, ok := parseMessageSummary(body)
	if !ok {
		t.Fatal("expected ok")
	}
	if status.MessagesWaiting {
		t.Error("expected MessagesWaiting=false")
	}
	if status.NewMessages != 0 || status.OldMessages != 0 {
		t.Errorf("expected 0/0, got %d/%d", status.NewMessages, status.OldMessages)
	}
}

func TestParseMessageSummary_WithAccount(t *testing.T) {
	body := "Messages-Waiting: yes\r\nMessage-Account: sip:*97@pbx.local\r\nVoice-Message: 1/3\r\n"
	status, ok := parseMessageSummary(body)
	if !ok {
		t.Fatal("expected ok")
	}
	if status.Account != "sip:*97@pbx.local" {
		t.Errorf("expected account sip:*97@pbx.local, got %q", status.Account)
	}
	if status.NewMessages != 1 || status.OldMessages != 3 {
		t.Errorf("expected 1/3, got %d/%d", status.NewMessages, status.OldMessages)
	}
}

func TestParseMessageSummary_WithUrgent(t *testing.T) {
	body := "Messages-Waiting: yes\r\nVoice-Message: 2/8 (1/0)\r\n"
	status, ok := parseMessageSummary(body)
	if !ok {
		t.Fatal("expected ok")
	}
	if status.NewMessages != 2 || status.OldMessages != 8 {
		t.Errorf("expected 2/8, got %d/%d", status.NewMessages, status.OldMessages)
	}
}

func TestParseMessageSummary_CaseInsensitive(t *testing.T) {
	body := "messages-waiting: YES\r\nvoice-message: 5/10\r\n"
	status, ok := parseMessageSummary(body)
	if !ok {
		t.Fatal("expected ok")
	}
	if !status.MessagesWaiting {
		t.Error("expected MessagesWaiting=true")
	}
	if status.NewMessages != 5 || status.OldMessages != 10 {
		t.Errorf("expected 5/10, got %d/%d", status.NewMessages, status.OldMessages)
	}
}

func TestParseMessageSummary_MissingWaiting(t *testing.T) {
	body := "Voice-Message: 2/8\r\n"
	_, ok := parseMessageSummary(body)
	if ok {
		t.Error("expected not ok when Messages-Waiting missing")
	}
}

func TestParseMessageSummary_EmptyBody(t *testing.T) {
	_, ok := parseMessageSummary("")
	if ok {
		t.Error("expected not ok for empty body")
	}
}

func TestParseMessageSummary_UnixLineEndings(t *testing.T) {
	body := "Messages-Waiting: yes\nVoice-Message: 3/7\n"
	status, ok := parseMessageSummary(body)
	if !ok {
		t.Fatal("expected ok")
	}
	if status.NewMessages != 3 || status.OldMessages != 7 {
		t.Errorf("expected 3/7, got %d/%d", status.NewMessages, status.OldMessages)
	}
}

func TestParseMessageSummary_ExtraWhitespace(t *testing.T) {
	body := "  Messages-Waiting :  yes  \r\n  Voice-Message :  4 / 6  \r\n"
	status, ok := parseMessageSummary(body)
	if !ok {
		t.Fatal("expected ok")
	}
	if !status.MessagesWaiting {
		t.Error("expected MessagesWaiting=true")
	}
	if status.NewMessages != 4 || status.OldMessages != 6 {
		t.Errorf("expected 4/6, got %d/%d", status.NewMessages, status.OldMessages)
	}
}

func TestParseMessageSummary_DefaultCounts(t *testing.T) {
	body := "Messages-Waiting: yes\r\n"
	status, ok := parseMessageSummary(body)
	if !ok {
		t.Fatal("expected ok")
	}
	if status.NewMessages != 0 || status.OldMessages != 0 {
		t.Errorf("expected 0/0 when Voice-Message absent, got %d/%d", status.NewMessages, status.OldMessages)
	}
}

func TestParseMessageSummary_InvalidCounts(t *testing.T) {
	body := "Messages-Waiting: yes\r\nVoice-Message: abc/def\r\n"
	status, ok := parseMessageSummary(body)
	if !ok {
		t.Fatal("expected ok")
	}
	if status.NewMessages != 0 || status.OldMessages != 0 {
		t.Errorf("expected 0/0 for invalid counts, got %d/%d", status.NewMessages, status.OldMessages)
	}
}

func TestParseMessageCounts(t *testing.T) {
	tests := []struct {
		input   string
		wantNew int
		wantOld int
	}{
		{"2/8", 2, 8},
		{"0/0", 0, 0},
		{"2/8 (1/0)", 2, 8},
		{"abc", 0, 0},
		{"", 0, 0},
		{"1/", 0, 0},
	}
	for _, tt := range tests {
		n, o := parseMessageCounts(tt.input)
		if n != tt.wantNew || o != tt.wantOld {
			t.Errorf("parseMessageCounts(%q) = %d/%d, want %d/%d", tt.input, n, o, tt.wantNew, tt.wantOld)
		}
	}
}

// --- Integration tests ---

func TestMWI_SubscribesOnConnect(t *testing.T) {
	p := newPhone(Config{
		VoicemailURI: "sip:*97@pbx.local",
	})
	applyDefaults(&p.cfg)

	tr := testutil.NewMockTransport()
	// Queue: REGISTER 200, SUBSCRIBE 200, unsubscribe 200, un-REGISTER 200.
	tr.RespondSequence(
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
	)

	p.connectWithTransport(tr)
	defer p.Disconnect()

	// Wait for the background goroutine to send the initial SUBSCRIBE.
	time.Sleep(50 * time.Millisecond)

	// Verify SUBSCRIBE was sent.
	if count := tr.CountSent("SUBSCRIBE"); count < 1 {
		t.Errorf("expected at least 1 SUBSCRIBE sent, got %d", count)
	}

	sub := tr.LastSent("SUBSCRIBE")
	if sub == nil {
		t.Fatal("no SUBSCRIBE message sent")
	}
	if sub.Header("Event") != "message-summary" {
		t.Errorf("expected Event: message-summary, got %q", sub.Header("Event"))
	}
}

func TestMWI_NoSubscribeWithoutURI(t *testing.T) {
	p := newPhone(Config{})
	applyDefaults(&p.cfg)

	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.RespondWith(200, "OK") // un-REGISTER

	p.connectWithTransport(tr)
	defer p.Disconnect()

	if count := tr.CountSent("SUBSCRIBE"); count != 0 {
		t.Errorf("expected 0 SUBSCRIBE without VoicemailURI, got %d", count)
	}
}

func TestMWI_FiresOnVoicemailCallback(t *testing.T) {
	p := newPhone(Config{
		VoicemailURI: "sip:*97@pbx.local",
	})
	applyDefaults(&p.cfg)

	var mu sync.Mutex
	var got VoicemailStatus
	done := make(chan struct{})

	p.OnVoicemail(func(status VoicemailStatus) {
		mu.Lock()
		got = status
		mu.Unlock()
		close(done)
	})

	tr := testutil.NewMockTransport()
	// Queue: REGISTER 200, SUBSCRIBE 200, unsubscribe 200, un-REGISTER 200.
	tr.RespondSequence(
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
	)

	p.connectWithTransport(tr)
	defer p.Disconnect()

	// Wait for background SUBSCRIBE to complete before simulating NOTIFY.
	time.Sleep(50 * time.Millisecond)

	// Simulate MWI NOTIFY.
	tr.SimulateMWINotify("Messages-Waiting: yes\r\nVoice-Message: 2/8\r\n")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OnVoicemail callback")
	}

	mu.Lock()
	defer mu.Unlock()
	if !got.MessagesWaiting {
		t.Error("expected MessagesWaiting=true")
	}
	if got.NewMessages != 2 || got.OldMessages != 8 {
		t.Errorf("expected 2/8, got %d/%d", got.NewMessages, got.OldMessages)
	}
}

func TestMWI_CallbackSetAfterConnect(t *testing.T) {
	p := newPhone(Config{
		VoicemailURI: "sip:*97@pbx.local",
	})
	applyDefaults(&p.cfg)

	tr := testutil.NewMockTransport()
	// Queue: REGISTER 200, SUBSCRIBE 200, unsubscribe 200, un-REGISTER 200.
	tr.RespondSequence(
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
	)

	p.connectWithTransport(tr)
	defer p.Disconnect()

	// Wait for background SUBSCRIBE to complete before setting callback.
	time.Sleep(50 * time.Millisecond)

	// Set callback after connect.
	var mu sync.Mutex
	var got VoicemailStatus
	done := make(chan struct{})
	p.OnVoicemail(func(status VoicemailStatus) {
		mu.Lock()
		got = status
		mu.Unlock()
		close(done)
	})

	// Simulate MWI NOTIFY.
	tr.SimulateMWINotify("Messages-Waiting: yes\r\nVoice-Message: 1/5\r\n")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OnVoicemail callback")
	}

	mu.Lock()
	defer mu.Unlock()
	if got.NewMessages != 1 || got.OldMessages != 5 {
		t.Errorf("expected 1/5, got %d/%d", got.NewMessages, got.OldMessages)
	}
}

func TestMWI_DisconnectStopsSubscription(t *testing.T) {
	p := newPhone(Config{
		VoicemailURI: "sip:*97@pbx.local",
	})
	applyDefaults(&p.cfg)

	tr := testutil.NewMockTransport()
	// Queue: REGISTER 200, initial SUBSCRIBE 200, unsubscribe SUBSCRIBE 200, un-REGISTER 200.
	tr.RespondSequence(
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
		testutil.Response{Code: 200, Header: "OK"},
	)

	p.connectWithTransport(tr)

	// Wait for the background goroutine to send the initial SUBSCRIBE.
	time.Sleep(50 * time.Millisecond)

	p.Disconnect()

	// Should see at least 2 SUBSCRIBEs: initial + unsubscribe (Expires: 0).
	if count := tr.CountSent("SUBSCRIBE"); count < 2 {
		t.Errorf("expected at least 2 SUBSCRIBEs (subscribe + unsubscribe), got %d", count)
	}

	last := tr.LastSent("SUBSCRIBE")
	if last.Header("Expires") != "0" {
		t.Errorf("expected last SUBSCRIBE Expires: 0, got %q", last.Header("Expires"))
	}
}

func TestMockPhone_SimulateMWI(t *testing.T) {
	p := NewMockPhone()

	var got VoicemailStatus
	done := make(chan struct{})
	p.OnVoicemail(func(status VoicemailStatus) {
		got = status
		close(done)
	})

	p.SimulateMWI(VoicemailStatus{
		MessagesWaiting: true,
		NewMessages:     3,
		OldMessages:     10,
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for MockPhone OnVoicemail callback")
	}

	if !got.MessagesWaiting || got.NewMessages != 3 || got.OldMessages != 10 {
		t.Errorf("unexpected status: %+v", got)
	}
}
