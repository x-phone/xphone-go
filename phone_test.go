package xphone

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/testutil"
)

func TestPhone_OnIncomingFiresOnInvite(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)

	incoming := make(chan Call, 1)
	phone.OnIncoming(func(c Call) { incoming <- c })

	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	select {
	case call := <-incoming:
		assert.Equal(t, StateRinging, call.State())
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnIncoming never fired")
	}
}

func TestPhone_Auto100SentOnInvite(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)
	phone.OnIncoming(func(c Call) {})

	saw100 := tr.WaitForResponse(100, 100*time.Millisecond)
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	assert.True(t, <-saw100, "100 Trying never sent")
}

func TestPhone_Auto180SentAfterOnIncoming(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)
	phone.OnIncoming(func(c Call) {})

	saw180 := tr.WaitForResponse(180, 100*time.Millisecond)
	tr.SimulateInvite("sip:1001@pbx", "sip:1002@pbx")

	assert.True(t, <-saw180, "180 Ringing never sent")
}

func TestPhone_DialReturnsActiveCall(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")
	tr.OnInvite(func() {
		tr.RespondWith(180, "Ringing")
		tr.RespondWith(200, "OK")
	})

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)

	call, err := phone.Dial(context.Background(), "sip:1002@pbx")

	require.NoError(t, err)
	assert.Equal(t, StateActive, call.State())
}

func TestPhone_DialFailsWhenNotRegistered(t *testing.T) {
	phone := newPhone(testConfig())
	// not connected

	_, err := phone.Dial(context.Background(), "sip:1002@pbx")
	assert.ErrorIs(t, err, ErrNotRegistered)
}

func TestPhone_StateIsRegisteredAfterConnect(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)

	assert.Equal(t, PhoneStateRegistered, phone.State())
}
