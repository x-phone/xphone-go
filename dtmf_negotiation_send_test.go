package xphone

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/internal/sdp"
	"github.com/x-phone/xphone-go/testutil"
)

// SendDTMF must not transmit RFC 4733 RTP the remote never negotiated (#103),
// and DtmfBoth must genuinely fall back to SIP INFO in that case (#104).

func activeDTMFCall(mode DtmfMode, rtpOk bool) (*call, *testutil.MockDialog) {
	d := testutil.NewMockDialog()
	c := newInboundCall(d)
	c.state = StateActive
	c.dtmfMode = mode
	c.dtmfRTPOk = rtpOk
	return c, d
}

func TestSendDTMF_Rfc4733_ErrorsWhenNotNegotiated(t *testing.T) {
	c, d := activeDTMFCall(DtmfRfc4733, false)
	err := c.SendDTMF("5")
	assert.True(t, errors.Is(err, ErrDTMFNotNegotiated), "want ErrDTMFNotNegotiated, got %v", err)
	assert.False(t, d.InfoSent(), "Rfc4733 must not fall back to SIP INFO")
}

func TestSendDTMF_Both_FallsBackToSipInfoWhenNotNegotiated(t *testing.T) {
	c, d := activeDTMFCall(DtmfBoth, false)
	require.NoError(t, c.SendDTMF("7"))
	assert.True(t, d.InfoSent(), "DtmfBoth must fall back to SIP INFO")
	assert.Equal(t, "7", d.LastInfoDigit())
}

func TestSendDTMF_SipInfo_AlwaysUsesInfo(t *testing.T) {
	c, d := activeDTMFCall(DtmfSipInfo, false)
	require.NoError(t, c.SendDTMF("3"))
	assert.True(t, d.InfoSent())
	assert.Equal(t, "3", d.LastInfoDigit())
}

func TestSendDTMF_Rfc4733_ProceedsWhenNegotiated(t *testing.T) {
	// rtpOk=true but no rtpConn wired: exercises the negotiation gate only,
	// which must pass (no ErrDTMFNotNegotiated) and not touch SIP INFO.
	c, d := activeDTMFCall(DtmfRfc4733, true)
	assert.NoError(t, c.SendDTMF("9"))
	assert.False(t, d.InfoSent(), "negotiated Rfc4733 must not use SIP INFO")
}

func TestNegotiateAudioCodec_SetsDtmfRTPOk(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	// remote offer with telephone-event present
	withTE, err := sdp.Parse(sdp.BuildOffer("1.2.3.4", 40000, []int{0, 101}, "sendrecv"))
	require.NoError(t, err)
	c.mu.Lock()
	c.negotiateAudioCodec(withTE)
	c.mu.Unlock()
	assert.True(t, c.dtmfRTPOk, "101 in remote offer must set dtmfRTPOk")

	// remote offer without it
	c2 := newInboundCall(testutil.NewMockDialog())
	without, err := sdp.Parse(sdp.BuildOffer("1.2.3.4", 40000, []int{0}, "sendrecv"))
	require.NoError(t, err)
	c2.mu.Lock()
	c2.negotiateAudioCodec(without)
	c2.mu.Unlock()
	assert.False(t, c2.dtmfRTPOk, "no 101 → dtmfRTPOk stays false")
}
