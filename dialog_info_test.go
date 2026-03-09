package xphone

import "testing"

func TestParseDialogInfo_NoDialogs(t *testing.T) {
	body := `<?xml version="1.0"?><dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="0" state="full" entity="sip:1001@pbx.local"></dialog-info>`
	state, err := parseDialogInfo(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != ExtensionAvailable {
		t.Errorf("expected ExtensionAvailable, got %d", state)
	}
}

func TestParseDialogInfo_Confirmed(t *testing.T) {
	body := `<?xml version="1.0"?>
<dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="1" state="full" entity="sip:1001@pbx.local">
  <dialog id="abc123">
    <state>confirmed</state>
  </dialog>
</dialog-info>`
	state, err := parseDialogInfo(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != ExtensionOnThePhone {
		t.Errorf("expected ExtensionOnThePhone, got %d", state)
	}
}

func TestParseDialogInfo_Early(t *testing.T) {
	body := `<?xml version="1.0"?>
<dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="1" state="full" entity="sip:1001@pbx.local">
  <dialog id="abc123">
    <state>early</state>
  </dialog>
</dialog-info>`
	state, err := parseDialogInfo(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != ExtensionRinging {
		t.Errorf("expected ExtensionRinging, got %d", state)
	}
}

func TestParseDialogInfo_Proceeding(t *testing.T) {
	body := `<?xml version="1.0"?>
<dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="1" state="full" entity="sip:1001@pbx.local">
  <dialog id="abc123">
    <state>proceeding</state>
  </dialog>
</dialog-info>`
	state, err := parseDialogInfo(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != ExtensionRinging {
		t.Errorf("expected ExtensionRinging, got %d", state)
	}
}

func TestParseDialogInfo_AllTerminated(t *testing.T) {
	body := `<?xml version="1.0"?>
<dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="2" state="full" entity="sip:1001@pbx.local">
  <dialog id="abc123">
    <state>terminated</state>
  </dialog>
  <dialog id="def456">
    <state>terminated</state>
  </dialog>
</dialog-info>`
	state, err := parseDialogInfo(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != ExtensionAvailable {
		t.Errorf("expected ExtensionAvailable, got %d", state)
	}
}

func TestParseDialogInfo_MixedConfirmedTerminated(t *testing.T) {
	body := `<?xml version="1.0"?>
<dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="3" state="full" entity="sip:1001@pbx.local">
  <dialog id="abc123">
    <state>terminated</state>
  </dialog>
  <dialog id="def456">
    <state>confirmed</state>
  </dialog>
</dialog-info>`
	state, err := parseDialogInfo(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != ExtensionOnThePhone {
		t.Errorf("expected ExtensionOnThePhone, got %d", state)
	}
}

func TestParseDialogInfo_InvalidXML(t *testing.T) {
	_, err := parseDialogInfo("not xml at all")
	if err == nil {
		t.Error("expected error for invalid XML")
	}
}

func TestParseDialogInfo_ConfirmedBeatsEarly(t *testing.T) {
	body := `<?xml version="1.0"?>
<dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="1" state="full" entity="sip:1001@pbx.local">
  <dialog id="abc123">
    <state>early</state>
  </dialog>
  <dialog id="def456">
    <state>confirmed</state>
  </dialog>
</dialog-info>`
	state, err := parseDialogInfo(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != ExtensionOnThePhone {
		t.Errorf("expected ExtensionOnThePhone (confirmed beats early), got %d", state)
	}
}
