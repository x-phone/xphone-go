package xphone

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/x-phone/xphone-go/testutil"
)

// Regression tests for the telephone-event negotiation trap: custom
// CodecPrefs that omit payload 101 must not silently disable RFC 4733 DTMF
// while SendDTMF keeps transmitting. resolveCodecPrefs guarantees 101 is
// offered whenever the call's DTMF mode sends RTP digits.

func prefsCall(prefs []int, mode DtmfMode) *call {
	c := newInboundCall(testutil.NewMockDialog())
	c.codecPrefs = prefs
	c.dtmfMode = mode
	return c
}

func TestResolveCodecPrefs_AppendsTelephoneEventForRTPModes(t *testing.T) {
	for _, mode := range []DtmfMode{DtmfRfc4733, DtmfBoth} {
		c := prefsCall([]int{int(CodecPCMU)}, mode)
		got := c.resolveCodecPrefs()
		assert.Equal(t, []int{0, DTMFPayloadType}, got, "mode %v must offer telephone-event", mode)
	}
}

func TestResolveCodecPrefs_SipInfoLeavesPrefsUntouched(t *testing.T) {
	c := prefsCall([]int{int(CodecPCMU), int(CodecPCMA)}, DtmfSipInfo)
	assert.Equal(t, []int{0, 8}, c.resolveCodecPrefs(),
		"DtmfSipInfo must not force telephone-event into the offer")
}

func TestResolveCodecPrefs_NoDuplicateWhenAlreadyPresent(t *testing.T) {
	c := prefsCall([]int{int(CodecPCMA), int(CodecTelephoneEvent)}, DtmfRfc4733)
	assert.Equal(t, []int{8, 101}, c.resolveCodecPrefs())
}

func TestResolveCodecPrefs_DoesNotMutateCallerSlice(t *testing.T) {
	prefs := []int{int(CodecPCMU)}
	c := prefsCall(prefs, DtmfRfc4733)
	_ = c.resolveCodecPrefs()
	assert.Equal(t, []int{0}, prefs, "caller's CodecPrefs slice must not be modified")
}

func TestResolveCodecPrefs_DefaultsAlreadyIncludeTelephoneEvent(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	got := c.resolveCodecPrefs()
	assert.Equal(t, defaultCodecPrefs, got)
	assert.Contains(t, got, DTMFPayloadType)
}

func TestBuildLocalSDP_CustomPrefsOfferTelephoneEvent(t *testing.T) {
	c := prefsCall([]int{int(CodecPCMU)}, DtmfRfc4733)
	c.mu.Lock()
	offer := c.buildLocalSDP("sendrecv")
	c.mu.Unlock()
	assert.True(t, strings.Contains(offer, "telephone-event"),
		"SDP offer must advertise telephone-event, got:\n%s", offer)
	assert.True(t, strings.Contains(offer, " 101"),
		"SDP m= line must list payload 101, got:\n%s", offer)
}

func TestBuildLocalSDP_SipInfoOmitsTelephoneEventForCustomPrefs(t *testing.T) {
	c := prefsCall([]int{int(CodecPCMU)}, DtmfSipInfo)
	c.mu.Lock()
	offer := c.buildLocalSDP("sendrecv")
	c.mu.Unlock()
	assert.False(t, strings.Contains(offer, "telephone-event"),
		"DtmfSipInfo custom prefs should not advertise telephone-event, got:\n%s", offer)
}
