package xphone

import (
	"context"
	"testing"
	"time"

	"github.com/x-phone/xphone-go/testutil"
)

// --- parseSubscriptionState ---

func TestParseSubscriptionState_Active(t *testing.T) {
	state, expires, reason := parseSubscriptionState("active;expires=600")
	if state != SubStateActive {
		t.Errorf("expected SubStateActive, got %d", state)
	}
	if expires != 600 {
		t.Errorf("expected expires 600, got %d", expires)
	}
	if reason != "" {
		t.Errorf("expected empty reason, got %q", reason)
	}
}

func TestParseSubscriptionState_Pending(t *testing.T) {
	state, _, _ := parseSubscriptionState("pending;expires=3600")
	if state != SubStatePending {
		t.Errorf("expected SubStatePending, got %d", state)
	}
}

func TestParseSubscriptionState_Terminated(t *testing.T) {
	state, _, reason := parseSubscriptionState("terminated;reason=deactivated")
	if state != SubStateTerminated {
		t.Errorf("expected SubStateTerminated, got %d", state)
	}
	if reason != "deactivated" {
		t.Errorf("expected reason deactivated, got %q", reason)
	}
}

func TestParseSubscriptionState_Empty(t *testing.T) {
	state, expires, reason := parseSubscriptionState("")
	if state != SubStateActive {
		t.Errorf("expected SubStateActive for empty header, got %d", state)
	}
	if expires != 0 {
		t.Errorf("expected 0 expires, got %d", expires)
	}
	if reason != "" {
		t.Errorf("expected empty reason, got %q", reason)
	}
}

// --- extractSIPUser ---

func TestExtractSIPUser(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sip:1001@pbx.local", "1001"},
		{"sips:1001@pbx.local", "1001"},
		{"1001@pbx.local", "1001"},
		{"1001", "1001"},
		{"sip:alice@example.com", "alice"},
	}
	for _, tt := range tests {
		got := extractSIPUser(tt.input)
		if got != tt.want {
			t.Errorf("extractSIPUser(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Watch ---

func TestWatch_Success(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.RespondWith(200, "OK") // SUBSCRIBE

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	var gotExt string
	var gotState, gotPrev ExtensionState
	done := make(chan struct{})

	id, err := p.Watch(context.Background(), "1002", func(ext string, state, prev ExtensionState) {
		gotExt = ext
		gotState = state
		gotPrev = prev
		close(done)
	})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty subscription ID")
	}

	// Verify SUBSCRIBE was sent.
	sub := tr.LastSent("SUBSCRIBE")
	if sub == nil {
		t.Fatal("expected SUBSCRIBE to be sent")
	}
	if sub.URI != "sip:1002@127.0.0.1" {
		t.Errorf("expected URI sip:1002@127.0.0.1, got %q", sub.URI)
	}
	if sub.Header("Event") != "dialog" {
		t.Errorf("expected Event: dialog, got %q", sub.Header("Event"))
	}
	if sub.Header("Accept") != "application/dialog-info+xml" {
		t.Errorf("expected Accept: application/dialog-info+xml, got %q", sub.Header("Accept"))
	}

	// Simulate a NOTIFY with dialog-info.
	dialogInfoBody := `<?xml version="1.0"?><dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="0" state="full" entity="sip:1002@127.0.0.1"><dialog id="abc"><state>confirmed</state></dialog></dialog-info>`
	tr.SimulateNotify("dialog", "sip:1002@127.0.0.1", "application/dialog-info+xml", dialogInfoBody, "active;expires=600")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for watch callback")
	}

	if gotExt != "1002" {
		t.Errorf("expected ext 1002, got %q", gotExt)
	}
	if gotState != ExtensionOnThePhone {
		t.Errorf("expected ExtensionOnThePhone, got %d", gotState)
	}
	if gotPrev != ExtensionUnknown {
		t.Errorf("expected prev ExtensionUnknown, got %d", gotPrev)
	}
}

func TestWatch_Rejected(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.RespondWith(403, "Forbidden")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	_, err := p.Watch(context.Background(), "1002", func(string, ExtensionState, ExtensionState) {})
	if err != ErrSubscriptionRejected {
		t.Errorf("expected ErrSubscriptionRejected, got %v", err)
	}
}

func TestWatch_NotConnected(t *testing.T) {
	p := newPhone(testConfig())
	_, err := p.Watch(context.Background(), "1002", func(string, ExtensionState, ExtensionState) {})
	if err != ErrNotConnected {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
}

func TestUnwatch_Success(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.RespondWith(200, "OK") // SUBSCRIBE
	tr.RespondWith(200, "OK") // unsubscribe (Expires: 0)

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	id, err := p.Watch(context.Background(), "1002", func(string, ExtensionState, ExtensionState) {})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	err = p.Unwatch(id)
	if err != nil {
		t.Fatalf("Unwatch failed: %v", err)
	}

	// Verify unsubscribe SUBSCRIBE with Expires: 0 was sent.
	if tr.CountSent("SUBSCRIBE") != 2 {
		t.Errorf("expected 2 SUBSCRIBEs (subscribe + unsubscribe), got %d", tr.CountSent("SUBSCRIBE"))
	}
}

func TestUnwatch_NotFound(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	err := p.Unwatch("nonexistent-id")
	if err != ErrSubscriptionNotFound {
		t.Errorf("expected ErrSubscriptionNotFound, got %v", err)
	}
}

// --- Watch prev state tracking ---

func TestWatch_PrevStateTracking(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.RespondWith(200, "OK") // SUBSCRIBE

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	var states []ExtensionState
	var prevs []ExtensionState
	notify := make(chan struct{}, 5)

	_, err := p.Watch(context.Background(), "1002", func(_ string, state, prev ExtensionState) {
		states = append(states, state)
		prevs = append(prevs, prev)
		notify <- struct{}{}
	})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	// First NOTIFY: idle (no dialogs).
	bodyAvailable := `<?xml version="1.0"?><dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="0" state="full" entity="sip:1002@127.0.0.1"></dialog-info>`
	tr.SimulateNotify("dialog", "sip:1002@127.0.0.1", "application/dialog-info+xml", bodyAvailable, "active;expires=600")
	<-notify

	// Second NOTIFY: ringing.
	bodyRinging := `<?xml version="1.0"?><dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="1" state="full" entity="sip:1002@127.0.0.1"><dialog id="1"><state>early</state></dialog></dialog-info>`
	tr.SimulateNotify("dialog", "sip:1002@127.0.0.1", "application/dialog-info+xml", bodyRinging, "active;expires=600")
	<-notify

	// Third NOTIFY: confirmed.
	bodyConfirmed := `<?xml version="1.0"?><dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="2" state="full" entity="sip:1002@127.0.0.1"><dialog id="1"><state>confirmed</state></dialog></dialog-info>`
	tr.SimulateNotify("dialog", "sip:1002@127.0.0.1", "application/dialog-info+xml", bodyConfirmed, "active;expires=600")
	<-notify

	// Verify state transitions: Unknown→Available→Ringing→OnThePhone.
	if len(states) != 3 {
		t.Fatalf("expected 3 callbacks, got %d", len(states))
	}
	if prevs[0] != ExtensionUnknown || states[0] != ExtensionAvailable {
		t.Errorf("first: expected Unknown→Available, got %d→%d", prevs[0], states[0])
	}
	if prevs[1] != ExtensionAvailable || states[1] != ExtensionRinging {
		t.Errorf("second: expected Available→Ringing, got %d→%d", prevs[1], states[1])
	}
	if prevs[2] != ExtensionRinging || states[2] != ExtensionOnThePhone {
		t.Errorf("third: expected Ringing→OnThePhone, got %d→%d", prevs[2], states[2])
	}
}

// --- SubscribeEvent ---

func TestSubscribeEvent_Success(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.RespondWith(200, "OK") // SUBSCRIBE

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	var gotEvent NotifyEvent
	done := make(chan struct{})

	id, err := p.SubscribeEvent(context.Background(), "sip:1002@127.0.0.1", "presence", 300, func(ev NotifyEvent) {
		gotEvent = ev
		close(done)
	})
	if err != nil {
		t.Fatalf("SubscribeEvent failed: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty subscription ID")
	}

	// Simulate a NOTIFY.
	tr.SimulateNotify("presence", "sip:1002@127.0.0.1", "application/pidf+xml", "<presence/>", "active;expires=300")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for subscribe event callback")
	}

	if gotEvent.Event != "presence" {
		t.Errorf("expected event presence, got %q", gotEvent.Event)
	}
	if gotEvent.Body != "<presence/>" {
		t.Errorf("expected body <presence/>, got %q", gotEvent.Body)
	}
	if gotEvent.SubscriptionState != SubStateActive {
		t.Errorf("expected SubStateActive, got %d", gotEvent.SubscriptionState)
	}
	if gotEvent.Expires != 300 {
		t.Errorf("expected expires 300, got %d", gotEvent.Expires)
	}
}

func TestSubscribeEvent_Rejected(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.RespondWith(489, "Bad Event")

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	_, err := p.SubscribeEvent(context.Background(), "sip:1002@127.0.0.1", "badEvent", 0, func(NotifyEvent) {})
	if err != ErrSubscriptionRejected {
		t.Errorf("expected ErrSubscriptionRejected, got %v", err)
	}
}

func TestUnsubscribeEvent_Success(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.RespondWith(200, "OK") // SUBSCRIBE
	tr.RespondWith(200, "OK") // unsubscribe

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	id, err := p.SubscribeEvent(context.Background(), "sip:1002@127.0.0.1", "dialog", 300, func(NotifyEvent) {})
	if err != nil {
		t.Fatalf("SubscribeEvent failed: %v", err)
	}

	err = p.UnsubscribeEvent(id)
	if err != nil {
		t.Fatalf("UnsubscribeEvent failed: %v", err)
	}

	// Check Expires: 0 was sent.
	last := tr.LastSent("SUBSCRIBE")
	if last == nil {
		t.Fatal("expected SUBSCRIBE")
	}
	if last.Header("Expires") != "0" {
		t.Errorf("expected Expires: 0 for unsubscribe, got %q", last.Header("Expires"))
	}
}

func TestUnsubscribeEvent_NotFound(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	err := p.UnsubscribeEvent("nonexistent")
	if err != ErrSubscriptionNotFound {
		t.Errorf("expected ErrSubscriptionNotFound, got %v", err)
	}
}

// --- Auto-resubscribe on deactivated ---

func TestWatch_ResubscribeOnDeactivated(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.RespondWith(200, "OK") // initial SUBSCRIBE
	tr.RespondWith(200, "OK") // resubscribe SUBSCRIBE

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	_, err := p.Watch(context.Background(), "1002", func(string, ExtensionState, ExtensionState) {})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	initialCount := tr.CountSent("SUBSCRIBE")

	// Simulate terminated;reason=deactivated — should auto-resubscribe.
	tr.SimulateNotify("dialog", "sip:1002@127.0.0.1", "application/dialog-info+xml",
		`<?xml version="1.0"?><dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="0" state="full" entity="sip:1002@127.0.0.1"></dialog-info>`,
		"terminated;reason=deactivated")

	// Wait for the resubscribe goroutine to send the SUBSCRIBE.
	time.Sleep(200 * time.Millisecond)

	if tr.CountSent("SUBSCRIBE") <= initialCount {
		t.Error("expected resubscribe SUBSCRIBE after terminated;reason=deactivated")
	}
}

// --- Disconnect stops subscriptions ---

func TestDisconnect_StopsSubscriptions(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.RespondWith(200, "OK") // SUBSCRIBE
	tr.RespondWith(200, "OK") // unsubscribe on Disconnect
	tr.RespondWith(200, "OK") // un-REGISTER on Disconnect

	p := newPhone(testConfig())
	p.connectWithTransport(tr)

	_, err := p.Watch(context.Background(), "1002", func(string, ExtensionState, ExtensionState) {})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	p.Disconnect()

	// Should have sent unsubscribe (Expires: 0).
	if tr.CountSent("SUBSCRIBE") < 2 {
		t.Error("expected at least 2 SUBSCRIBEs (initial + unsubscribe on disconnect)")
	}
}
