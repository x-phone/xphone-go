package xphone

import (
	"context"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/internal/sdp"
	"github.com/x-phone/xphone-go/testutil"
)

// testInboundCall creates an inbound call with automatic cleanup.
func testInboundCall(t *testing.T, d ...dialog) *call {
	t.Helper()
	var dlg dialog
	if len(d) > 0 {
		dlg = d[0]
	} else {
		dlg = testutil.NewMockDialog()
	}
	c := newInboundCall(dlg)
	t.Cleanup(c.cleanup)
	return c
}

// testOutboundCall creates an outbound call with automatic cleanup.
func testOutboundCall(t *testing.T, d dialog, opts ...DialOption) *call {
	t.Helper()
	c := newOutboundCall(d, opts...)
	t.Cleanup(c.cleanup)
	return c
}

// --- Inbound: basic state transitions ---

func TestCall_InboundInitialStateIsRinging(t *testing.T) {
	call := testInboundCall(t)
	assert.Equal(t, StateRinging, call.State())
}

func TestCall_AcceptTransitionsToActive(t *testing.T) {
	call := testInboundCall(t)
	require.NoError(t, call.Accept())
	assert.Equal(t, StateActive, call.State())
}

func TestCall_AcceptSendsSDPAnswer(t *testing.T) {
	dialog := testutil.NewMockDialog()
	call := testInboundCall(t, dialog)
	call.Accept()
	assert.Equal(t, 200, dialog.LastResponseCode())
	assert.NotEmpty(t, dialog.LastResponseBody())
}

func TestCall_RejectSendsCorrectSIPCode(t *testing.T) {
	dialog := testutil.NewMockDialog()
	call := testInboundCall(t, dialog)
	call.Reject(486, "Busy Here")
	assert.Equal(t, 486, dialog.LastResponseCode())
	assert.Equal(t, "Busy Here", dialog.LastResponseReason())
}

func TestCall_RejectTransitionsToEnded(t *testing.T) {
	call := testInboundCall(t)
	call.Reject(486, "Busy Here")
	assert.Equal(t, StateEnded, call.State())
}

func TestCall_RejectFiresEndedByRejected(t *testing.T) {
	call := testInboundCall(t)
	ended := make(chan EndReason, 1)
	call.OnEnded(func(r EndReason) { ended <- r })
	call.Reject(486, "Busy Here")
	assert.Equal(t, EndedByRejected, <-ended)
}

func TestCall_CannotAcceptAfterRejected(t *testing.T) {
	call := testInboundCall(t)
	call.Reject(486, "Busy Here")
	assert.ErrorIs(t, call.Accept(), ErrInvalidState)
}

func TestCall_CannotRejectAfterAccepted(t *testing.T) {
	call := testInboundCall(t)
	call.Accept()
	assert.ErrorIs(t, call.Reject(486, "Busy Here"), ErrInvalidState)
}

// --- Outbound: state transitions ---

func TestCall_OutboundInitialStateIsDialing(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog())
	assert.Equal(t, StateDialing, call.State())
}

func TestCall_OutboundTransitionsOnRemoteRinging(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog())
	call.simulateResponse(180, "Ringing")
	assert.Equal(t, StateRemoteRinging, call.State())
}

func TestCall_OutboundTransitionsToActiveOn200(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog())
	call.simulateResponse(180, "Ringing")
	call.simulateResponse(200, "OK")
	assert.Equal(t, StateActive, call.State())
}

// --- 183 / EarlyMedia gating ---

func TestCall_183WithEarlyMediaOptionTransitionsToEarlyMedia(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog(), WithEarlyMedia())
	call.simulateResponse(183, "Session Progress")
	assert.Equal(t, StateEarlyMedia, call.State())
}

func TestCall_183WithoutEarlyMediaOptionStaysRemoteRinging(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog()) // no WithEarlyMedia
	call.simulateResponse(180, "Ringing")
	call.simulateResponse(183, "Session Progress")
	// 183 received but option not set — state must not change to EarlyMedia
	assert.Equal(t, StateRemoteRinging, call.State())
}

func TestCall_183WithEarlyMediaOpensRTPChannels(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog(), WithEarlyMedia())
	call.simulateResponse(183, "Session Progress")
	assert.NotNil(t, call.RTPReader())
	assert.NotNil(t, call.PCMReader())
}

func TestCall_183WithoutEarlyMediaRTPChannelsRemainClosed(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog())
	call.simulateResponse(183, "Session Progress")
	// channels exist but no RTP session is live yet
	assert.False(t, call.MediaSessionActive())
}

// --- OnMedia event ---

func TestCall_OnMediaFiresAfter200OK(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog())
	mediaReady := make(chan struct{}, 1)
	call.OnMedia(func() { mediaReady <- struct{}{} })

	call.simulateResponse(200, "OK")

	select {
	case <-mediaReady:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnMedia never fired after 200 OK")
	}
}

func TestCall_OnMediaFiresOn183WhenEarlyMediaEnabled(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog(), WithEarlyMedia())
	mediaReady := make(chan struct{}, 1)
	call.OnMedia(func() { mediaReady <- struct{}{} })

	call.simulateResponse(183, "Session Progress")

	select {
	case <-mediaReady:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnMedia never fired on 183 with WithEarlyMedia")
	}
}

func TestCall_OnMediaDoesNotFireOn183WithoutEarlyMediaOption(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog())
	fired := false
	call.OnMedia(func() { fired = true })

	call.simulateResponse(183, "Session Progress")
	time.Sleep(30 * time.Millisecond)

	assert.False(t, fired)
}

// --- End() semantics: CANCEL vs BYE ---

func TestCall_EndBeforeAnswerSendsCancel(t *testing.T) {
	dialog := testutil.NewMockDialog()
	call := testOutboundCall(t, dialog)
	call.simulateResponse(180, "Ringing")

	call.End()

	assert.True(t, dialog.CancelSent())
	assert.False(t, dialog.ByeSent())
}

func TestCall_EndBeforeAnswerFiresEndedByCancelled(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog())
	call.simulateResponse(180, "Ringing")

	ended := make(chan EndReason, 1)
	call.OnEnded(func(r EndReason) { ended <- r })
	call.End()

	assert.Equal(t, EndedByCancelled, <-ended)
}

func TestCall_EndWhileActiveSendsBye(t *testing.T) {
	dialog := testutil.NewMockDialog()
	call := testOutboundCall(t, dialog)
	call.simulateResponse(200, "OK")

	call.End()

	assert.True(t, dialog.ByeSent())
	assert.False(t, dialog.CancelSent())
}

func TestCall_EndWhileActiveFiresEndedByLocal(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog())
	call.simulateResponse(200, "OK")

	ended := make(chan EndReason, 1)
	call.OnEnded(func(r EndReason) { ended <- r })
	call.End()

	assert.Equal(t, EndedByLocal, <-ended)
}

func TestCall_EndWhileOnHoldSendsBye(t *testing.T) {
	dialog := testutil.NewMockDialog()
	call := testOutboundCall(t, dialog)
	call.simulateResponse(200, "OK")
	call.Hold()

	call.End()

	assert.True(t, dialog.ByeSent())
}

func TestCall_RemoteByeFiresEndedByRemote(t *testing.T) {
	call := testInboundCall(t)
	call.Accept()

	ended := make(chan EndReason, 1)
	call.OnEnded(func(r EndReason) { ended <- r })
	call.simulateBye()

	assert.Equal(t, EndedByRemote, <-ended)
}

func TestCall_EndOnAlreadyEndedCallReturnsInvalidState(t *testing.T) {
	call := testInboundCall(t)
	call.Accept()
	call.End()
	assert.ErrorIs(t, call.End(), ErrInvalidState)
}

// --- Dial timeout precedence ---

func TestCall_DialOptionsTimeoutFiresErrDialTimeout(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK") // registration
	tr.OnInvite(func() {
		// never respond — no answer
	})

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)

	_, err := phone.Dial(context.Background(), "sip:1002@pbx",
		WithDialTimeout(30*time.Millisecond),
	)

	assert.ErrorIs(t, err, ErrDialTimeout)
}

func TestCall_ContextCancelledReturnsCtxError(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")
	tr.OnInvite(func() {})

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := phone.Dial(ctx, "sip:1002@pbx",
		WithDialTimeout(60*time.Second), // ctx fires first
	)

	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestCall_EarlierOfCtxAndDialOptionWins(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.RespondWith(200, "OK")
	tr.OnInvite(func() {})

	phone := newPhone(testConfig())
	phone.connectWithTransport(tr)

	// DialOptions.Timeout = 20ms, ctx deadline = 200ms → 20ms fires first
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := phone.Dial(ctx, "sip:1002@pbx",
		WithDialTimeout(20*time.Millisecond),
	)

	elapsed := time.Since(start)
	assert.ErrorIs(t, err, ErrDialTimeout) // DialOptions fired, not ctx
	assert.Less(t, elapsed, 100*time.Millisecond)
}

// --- Hold / Resume ---

func TestCall_HoldSendsReInviteWithSendOnly(t *testing.T) {
	dialog := testutil.NewMockDialog()
	call := testInboundCall(t, dialog)
	call.Accept()
	call.Hold()

	assert.Contains(t, dialog.LastReInviteSDP(), "a=sendonly")
}

func TestCall_HoldTransitionsToOnHold(t *testing.T) {
	call := testInboundCall(t)
	call.Accept()
	call.Hold()
	assert.Equal(t, StateOnHold, call.State())
}

func TestCall_ResumeFromHoldSendsReInviteWithSendRecv(t *testing.T) {
	dialog := testutil.NewMockDialog()
	call := testInboundCall(t, dialog)
	call.Accept()
	call.Hold()
	call.Resume()

	assert.Contains(t, dialog.LastReInviteSDP(), "a=sendrecv")
}

func TestCall_ResumeFromHoldTransitionsToActive(t *testing.T) {
	call := testInboundCall(t)
	call.Accept()
	call.Hold()
	call.Resume()
	assert.Equal(t, StateActive, call.State())
}

func TestCall_HoldWhenNotActiveReturnsInvalidState(t *testing.T) {
	call := testInboundCall(t)
	assert.ErrorIs(t, call.Hold(), ErrInvalidState)
}

// --- Identity & Headers ---

func TestCall_IDIsUniquePerCall(t *testing.T) {
	c1 := testInboundCall(t)
	c2 := testInboundCall(t)
	assert.NotEqual(t, c1.ID(), c2.ID())
}

func TestCall_CallIDMatchesSIPHeader(t *testing.T) {
	dialog := testutil.NewMockDialogWithCallID("test-call-id-xyz")
	call := testInboundCall(t, dialog)
	assert.Equal(t, "test-call-id-xyz", call.CallID())
}

func TestCall_HeadersReturnsCopy(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"X-Custom": {"value1"},
	})
	call := testInboundCall(t, dialog)

	headers := call.Headers()
	headers["X-Custom"] = []string{"mutated"}

	assert.Equal(t, []string{"value1"}, call.Header("X-Custom"))
}

func TestCall_HeaderCaseInsensitive(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"P-Asserted-Identity": {"sip:1001@pbx"},
	})
	call := testInboundCall(t, dialog)
	assert.Equal(t, []string{"sip:1001@pbx"}, call.Header("p-asserted-identity"))
}

func TestCall_HeadersSupportsMultipleValues(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"Route": {"sip:proxy1@pbx", "sip:proxy2@pbx"},
	})
	call := testInboundCall(t, dialog)
	assert.Equal(t, []string{"sip:proxy1@pbx", "sip:proxy2@pbx"}, call.Header("Route"))
}

func TestCall_DirectionInbound(t *testing.T) {
	call := testInboundCall(t)
	assert.Equal(t, DirectionInbound, call.Direction())
}

func TestCall_DirectionOutbound(t *testing.T) {
	call := testOutboundCall(t, testutil.NewMockDialog())
	assert.Equal(t, DirectionOutbound, call.Direction())
}

// --- Timing ---

func TestCall_StartTimeZeroBeforeActive(t *testing.T) {
	call := testInboundCall(t)
	assert.True(t, call.StartTime().IsZero())
}

func TestCall_StartTimeSetOnActive(t *testing.T) {
	call := testInboundCall(t)
	call.Accept()
	assert.False(t, call.StartTime().IsZero())
}

func TestCall_DurationZeroBeforeActive(t *testing.T) {
	call := testInboundCall(t)
	assert.Equal(t, time.Duration(0), call.Duration())
}

func TestCall_DurationGrowsWhileActive(t *testing.T) {
	call := testInboundCall(t)
	call.Accept()
	time.Sleep(30 * time.Millisecond)
	assert.Greater(t, call.Duration(), 20*time.Millisecond)
}

// --- Blind Transfer ---

func TestCall_BlindTransferSendsRefer(t *testing.T) {
	dialog := testutil.NewMockDialog()
	call := testInboundCall(t, dialog)
	call.Accept()

	err := call.BlindTransfer("sip:1003@pbx")

	require.NoError(t, err)
	assert.True(t, dialog.ReferSent())
	assert.Equal(t, "sip:1003@pbx", dialog.LastReferTarget())
}

func TestCall_BlindTransferFiresEndedByTransfer(t *testing.T) {
	dialog := testutil.NewMockDialog()
	call := testInboundCall(t, dialog)
	call.Accept()

	ended := make(chan EndReason, 1)
	call.OnEnded(func(r EndReason) { ended <- r })

	call.BlindTransfer("sip:1003@pbx")
	dialog.SimulateNotify(200)

	assert.Equal(t, EndedByTransfer, <-ended)
}

func TestCall_BlindTransferFiresEndedByTransferFailed(t *testing.T) {
	dialog := testutil.NewMockDialog()
	call := testInboundCall(t, dialog)
	call.Accept()

	ended := make(chan EndReason, 1)
	call.OnEnded(func(r EndReason) { ended <- r })

	call.BlindTransfer("sip:1003@pbx")
	dialog.SimulateNotify(503)

	assert.Equal(t, EndedByTransferFailed, <-ended)
}

func TestCall_BlindTransferNotify1xxKeepsCallActive(t *testing.T) {
	dialog := testutil.NewMockDialog()
	call := testInboundCall(t, dialog)
	call.Accept()

	call.BlindTransfer("sip:1003@pbx")
	dialog.SimulateNotify(100)
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, StateActive, call.State())
}

func TestCall_BlindTransferWhenNotActiveReturnsInvalidState(t *testing.T) {
	call := testInboundCall(t)
	assert.ErrorIs(t, call.BlindTransfer("sip:1003@pbx"), ErrInvalidState)
}

// --- ReplaceAudioWriter ---

func TestCall_ReplaceAudioWriter_BasicSwap(t *testing.T) {
	call := testInboundCall(t)
	call.Accept()

	src := make(chan []int16, 1)
	err := call.ReplaceAudioWriter(src)
	require.NoError(t, err)
}

func TestCall_ReplaceAudioWriter_NilPauses(t *testing.T) {
	call := testInboundCall(t)
	call.Accept()

	err := call.ReplaceAudioWriter(nil)
	require.NoError(t, err)
}

func TestCall_ReplaceAudioWriter_ErrorOnEndedCall(t *testing.T) {
	call := testInboundCall(t)
	call.Accept()
	call.End()
	time.Sleep(50 * time.Millisecond)

	err := call.ReplaceAudioWriter(make(chan []int16))
	assert.ErrorIs(t, err, ErrInvalidState)
}

func TestCall_ReplaceAudioWriter_DoubleSwap(t *testing.T) {
	call := testInboundCall(t)
	call.Accept()

	src1 := make(chan []int16, 1)
	src2 := make(chan []int16, 1)
	require.NoError(t, call.ReplaceAudioWriter(src1))
	require.NoError(t, call.ReplaceAudioWriter(src2))
}

// --- sipHeaderTag ---

func TestSipHeaderTag(t *testing.T) {
	assert.Equal(t, "abc123", sipHeaderTag("<sip:1001@host>;tag=abc123"))
	assert.Equal(t, "xyz", sipHeaderTag(`"Alice" <sip:1001@host>;tag=xyz`))
	assert.Equal(t, "t1", sipHeaderTag("<sip:1001@host>;tag=t1;other=val"))
	assert.Equal(t, "", sipHeaderTag("<sip:1001@host>"))
	assert.Equal(t, "CaSe", sipHeaderTag("<sip:u@h>;Tag=CaSe")) // case-insensitive param name
}

// --- uriEncode ---

func TestURIEncode(t *testing.T) {
	assert.Equal(t, "abc%40host", uriEncode("abc@host"))
	assert.Equal(t, "hello%20world", uriEncode("hello world"))
	assert.Equal(t, "100%25done", uriEncode("100%done"))
	assert.Equal(t, "simple", uriEncode("simple"))
	assert.Equal(t, "a%3Bb%3Dc%3Fd%26e%2Bf", uriEncode("a;b=c?d&e+f"))
	assert.Equal(t, "sip%3Auser", uriEncode("sip:user"))
}

// --- SDP integration ---

// testSDP generates a minimal valid SDP for call-level tests.
func testSDP(ip string, port int, dir string, codecs ...int) string {
	return sdp.BuildOffer(ip, port, codecs, dir)
}

func TestCall_LocalSDPEmptyBeforeActive(t *testing.T) {
	c := testInboundCall(t)
	assert.Equal(t, "", c.LocalSDP())
}

func TestCall_RemoteSDPEmptyBeforeActive(t *testing.T) {
	c := testInboundCall(t)
	assert.Equal(t, "", c.RemoteSDP())
}

func TestCall_LocalSDPPopulatedAfterAccept(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	assert.Contains(t, c.LocalSDP(), "v=0")
}

func TestCall_CodecNegotiatedFromSDP(t *testing.T) {
	// Remote offers codecs [0,8]. Implementation should negotiate using
	// local preference order (default or config-driven). With local prefs
	// favouring PCMA over PCMU, the negotiated codec should be CodecPCMA.
	remoteSDP := testSDP("192.168.1.200", 5004, "sendrecv", 0, 8)
	c := testInboundCall(t)
	c.remoteSDP = remoteSDP
	c.Accept()
	assert.Equal(t, CodecPCMA, c.Codec())
}

func TestCall_HoldSendsSDPWithSendOnly(t *testing.T) {
	dialog := testutil.NewMockDialog()
	c := testInboundCall(t, dialog)
	c.Accept()
	c.Hold()

	raw := dialog.LastReInviteSDP()
	s, err := sdp.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "sendonly", s.Dir())
}

func TestCall_ResumeSendsSDPWithSendRecv(t *testing.T) {
	dialog := testutil.NewMockDialog()
	c := testInboundCall(t, dialog)
	c.Accept()
	c.Hold()
	c.Resume()

	raw := dialog.LastReInviteSDP()
	s, err := sdp.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "sendrecv", s.Dir())
}

// --- Re-INVITE handling ---

func TestCall_InboundReInviteHold(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	holdSDP := testSDP("192.168.1.200", 5004, "sendonly", 0)
	c.simulateReInvite(holdSDP)
	assert.Equal(t, StateOnHold, c.State())
}

func TestCall_InboundReInviteResume(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	c.simulateReInvite(testSDP("192.168.1.200", 5004, "sendonly", 0))
	c.simulateReInvite(testSDP("192.168.1.200", 5004, "sendrecv", 0))
	assert.Equal(t, StateActive, c.State())
}

func TestCall_OnHoldCallbackFires(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	held := make(chan struct{}, 1)
	c.OnHold(func() { held <- struct{}{} })
	c.simulateReInvite(testSDP("192.168.1.200", 5004, "sendonly", 0))

	select {
	case <-held:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnHold callback never fired")
	}
}

func TestCall_OnResumeCallbackFires(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	resumed := make(chan struct{}, 1)
	c.OnResume(func() { resumed <- struct{}{} })
	c.simulateReInvite(testSDP("192.168.1.200", 5004, "sendonly", 0))
	c.simulateReInvite(testSDP("192.168.1.200", 5004, "sendrecv", 0))

	select {
	case <-resumed:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnResume callback never fired")
	}
}

func TestCall_ReInviteCodecChange(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	g722SDP := testSDP("192.168.1.200", 5004, "sendrecv", 9)
	c.simulateReInvite(g722SDP)
	assert.Equal(t, CodecG722, c.Codec())
}

func TestCall_ReInviteOnEndedCallIgnored(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	c.End()
	holdSDP := testSDP("192.168.1.200", 5004, "sendonly", 0)
	c.simulateReInvite(holdSDP) // should not panic
	assert.Equal(t, StateEnded, c.State())
}

// --- DTMF call-level ---

func TestCall_SendDTMF_ProducesRTPPackets(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	err := c.SendDTMF("5")
	require.NoError(t, err)

	// Wait for DTMF packets on sentRTP.
	time.Sleep(50 * time.Millisecond)
	pkts := drainPackets(c.sentRTP)
	var dtmfPkts int
	for _, pkt := range pkts {
		if pkt.PayloadType == DTMFPayloadType {
			dtmfPkts++
		}
	}
	assert.Greater(t, dtmfPkts, 0, "expected DTMF RTP packets with PT=101")
}

func TestCall_SendDTMF_InvalidDigitReturnsError(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	assert.ErrorIs(t, c.SendDTMF("X"), ErrInvalidDTMFDigit)
}

func TestCall_SendDTMF_WhenNotActiveReturnsError(t *testing.T) {
	c := testInboundCall(t)
	assert.ErrorIs(t, c.SendDTMF("1"), ErrInvalidState)
}

func TestCall_OnDTMF_FiresOnInboundPacket(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	got := make(chan string, 1)
	c.OnDTMF(func(digit string) { got <- digit })

	// Inject a DTMF RTP packet (PT=101, event=5, E bit, volume=10, duration=1000).
	payload := []byte{5, 0x8A, 0x03, 0xE8}
	c.injectRTP(&rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, PayloadType: DTMFPayloadType},
		Payload: payload,
	})

	select {
	case digit := <-got:
		assert.Equal(t, "5", digit)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnDTMF callback never fired")
	}
}

func TestCall_OnDTMF_NilCallbackNoPanic(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// No OnDTMF callback registered — should not panic.
	payload := []byte{5, 0x8A, 0x03, 0xE8}
	c.injectRTP(&rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, PayloadType: DTMFPayloadType},
		Payload: payload,
	})

	time.Sleep(50 * time.Millisecond)
}

// --- SIP INFO DTMF ---

func TestCall_SendDTMF_SipInfoMode_SendsInfoRequest(t *testing.T) {
	dlg := testutil.NewMockDialog()
	c := testInboundCall(t, dlg)
	c.dtmfMode = DtmfSipInfo
	c.Accept()

	err := c.SendDTMF("5")
	require.NoError(t, err)
	assert.True(t, dlg.InfoSent())
	assert.Equal(t, "5", dlg.LastInfoDigit())
	assert.Equal(t, 160, dlg.LastInfoDuration())
}

func TestCall_SendDTMF_BothMode_SendsRTP(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()
	c.dtmfMode = DtmfBoth

	err := c.SendDTMF("5")
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	pkts := drainPackets(c.sentRTP)
	var dtmfPkts int
	for _, pkt := range pkts {
		if pkt.PayloadType == DTMFPayloadType {
			dtmfPkts++
		}
	}
	assert.Greater(t, dtmfPkts, 0, "Both mode should send via RTP")
}

func TestCall_SendDTMF_Rfc4733Mode_DoesNotSendInfo(t *testing.T) {
	dlg := testutil.NewMockDialog()
	c := testInboundCall(t, dlg)
	c.dtmfMode = DtmfRfc4733
	c.Accept()

	c.SendDTMF("5")
	assert.False(t, dlg.InfoSent())
}

func TestCall_SendDTMF_SipInfoMode_InvalidDigitReturnsError(t *testing.T) {
	c := testInboundCall(t)
	c.dtmfMode = DtmfSipInfo
	c.Accept()
	assert.ErrorIs(t, c.SendDTMF("X"), ErrInvalidDTMFDigit)
}

// --- Session timers ---

func TestCall_SessionTimer_SendsRefreshReInvite(t *testing.T) {
	dialog := testutil.NewMockDialogWithSessionExpires(1)
	c := testInboundCall(t, dialog)
	c.Accept()

	// Session timer should fire around 500ms (half of Session-Expires).
	time.Sleep(600 * time.Millisecond)
	assert.NotEmpty(t, dialog.LastReInviteSDP(), "expected session refresh re-INVITE")
}

func TestCall_SessionTimer_NoHeaderNoTimer(t *testing.T) {
	dialog := testutil.NewMockDialog()
	c := testInboundCall(t, dialog)
	c.Accept()

	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, dialog.LastReInviteSDP(), "no session timer should fire without Session-Expires")
}

func TestCall_SessionTimer_CancelledOnEnd(t *testing.T) {
	dialog := testutil.NewMockDialogWithSessionExpires(1)
	c := testInboundCall(t, dialog)
	c.Accept()
	c.End()

	time.Sleep(600 * time.Millisecond)
	assert.Empty(t, dialog.LastReInviteSDP(), "session timer should be cancelled on End")
}

// --- Mute / Unmute state ---

func TestCall_Mute_WhenNotActiveReturnsError(t *testing.T) {
	c := testInboundCall(t)
	assert.ErrorIs(t, c.Mute(), ErrInvalidState)
}

func TestCall_Unmute_WhenNotActiveReturnsError(t *testing.T) {
	c := testInboundCall(t)
	assert.ErrorIs(t, c.Unmute(), ErrInvalidState)
}

func TestCall_Mute_WhenAlreadyMutedReturnsError(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	require.NoError(t, c.Mute())
	assert.ErrorIs(t, c.Mute(), ErrAlreadyMuted)
}

func TestCall_Unmute_WhenNotMutedReturnsError(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	assert.ErrorIs(t, c.Unmute(), ErrNotMuted)
}

func TestCall_Mute_WhenOnHoldReturnsError(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	c.Hold()
	assert.ErrorIs(t, c.Mute(), ErrInvalidState)
}

func TestCall_Unmute_WhenEndedReturnsError(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	c.End()
	assert.ErrorIs(t, c.Unmute(), ErrInvalidState)
}

func TestCall_OnMute_CallbackFires(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	fired := make(chan struct{}, 1)
	c.OnMute(func() { fired <- struct{}{} })

	c.Mute()

	select {
	case <-fired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnMute callback never fired")
	}
}

func TestCall_OnUnmute_CallbackFires(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	fired := make(chan struct{}, 1)
	c.OnUnmute(func() { fired <- struct{}{} })

	c.Mute()
	c.Unmute()

	select {
	case <-fired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnUnmute callback never fired")
	}
}

// --- RemoteURI / RemoteIP / RemotePort ---

func TestCall_RemoteURI_FromDialogHeader(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"From": {"<sip:1001@pbx.example.com>"},
	})
	c := testInboundCall(t, dialog)
	assert.Equal(t, "sip:1001@pbx.example.com", c.RemoteURI())
}

func TestCall_RemoteURI_EmptyWhenNoFromHeader(t *testing.T) {
	c := testInboundCall(t)
	assert.Equal(t, "", c.RemoteURI())
}

func TestCall_RemoteURI_StripsDisplayName(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"From": {"\"Alice\" <sip:alice@example.com>"},
	})
	c := testInboundCall(t, dialog)
	assert.Equal(t, "sip:alice@example.com", c.RemoteURI())
}

// --- From / To / FromName ---

func TestCall_From_ExtractsUserPart(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"From": {"\"Alice\" <sip:+15551234567@pbx.example.com>;tag=abc"},
	})
	c := testInboundCall(t, dialog)
	assert.Equal(t, "+15551234567", c.From())
}

func TestCall_From_Extension(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"From": {"<sip:1001@10.200.1.2>"},
	})
	c := testInboundCall(t, dialog)
	assert.Equal(t, "1001", c.From())
}

func TestCall_From_EmptyWhenNoHeader(t *testing.T) {
	c := testInboundCall(t)
	assert.Equal(t, "", c.From())
}

func TestCall_To_ExtractsUserPart(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"To": {"<sip:1002@pbx.example.com>"},
	})
	c := testInboundCall(t, dialog)
	assert.Equal(t, "1002", c.To())
}

func TestCall_To_EmptyWhenNoHeader(t *testing.T) {
	c := testInboundCall(t)
	assert.Equal(t, "", c.To())
}

func TestCall_FromName_QuotedDisplayName(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"From": {"\"Alice Smith\" <sip:alice@example.com>"},
	})
	c := testInboundCall(t, dialog)
	assert.Equal(t, "Alice Smith", c.FromName())
}

func TestCall_FromName_UnquotedDisplayName(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"From": {"Alice <sip:alice@example.com>"},
	})
	c := testInboundCall(t, dialog)
	assert.Equal(t, "Alice", c.FromName())
}

func TestCall_FromName_EmptyWhenNoDisplayName(t *testing.T) {
	dialog := testutil.NewMockDialogWithHeaders(map[string][]string{
		"From": {"<sip:1001@pbx.example.com>"},
	})
	c := testInboundCall(t, dialog)
	assert.Equal(t, "", c.FromName())
}

func TestCall_FromName_EmptyWhenNoHeader(t *testing.T) {
	c := testInboundCall(t)
	assert.Equal(t, "", c.FromName())
}

func TestCall_RemoteIP_FromRemoteSDP(t *testing.T) {
	c := testInboundCall(t)
	c.remoteSDP = testSDP("192.168.1.200", 5004, "sendrecv", 0)
	assert.Equal(t, "192.168.1.200", c.RemoteIP())
}

func TestCall_RemoteIP_EmptyBeforeSDP(t *testing.T) {
	c := testInboundCall(t)
	assert.Equal(t, "", c.RemoteIP())
}

func TestCall_RemotePort_FromRemoteSDP(t *testing.T) {
	c := testInboundCall(t)
	c.remoteSDP = testSDP("192.168.1.200", 5004, "sendrecv", 0)
	assert.Equal(t, 5004, c.RemotePort())
}

func TestCall_RemotePort_ZeroBeforeSDP(t *testing.T) {
	c := testInboundCall(t)
	assert.Equal(t, 0, c.RemotePort())
}

func TestCall_RemoteMedia_UpdatesAfterReInvite(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	c.remoteSDP = testSDP("192.168.1.100", 5000, "sendrecv", 0)
	assert.Equal(t, "192.168.1.100", c.RemoteIP())

	// Re-INVITE changes remote IP and port.
	c.simulateReInvite(testSDP("10.0.0.50", 6000, "sendrecv", 0))
	assert.Equal(t, "10.0.0.50", c.RemoteIP())
	assert.Equal(t, 6000, c.RemotePort())
}

// --- OnState callback ---

func TestCall_OnState_FiresOnAccept(t *testing.T) {
	c := testInboundCall(t)
	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.Accept()

	select {
	case s := <-states:
		assert.Equal(t, StateActive, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on Accept")
	}
}

func TestCall_OnState_FiresOnReject(t *testing.T) {
	c := testInboundCall(t)
	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.Reject(486, "Busy Here")

	select {
	case s := <-states:
		assert.Equal(t, StateEnded, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on Reject")
	}
}

func TestCall_OnState_FiresOnEnd(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.End()

	select {
	case s := <-states:
		assert.Equal(t, StateEnded, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on End")
	}
}

func TestCall_OnState_FiresOnHold(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.Hold()

	select {
	case s := <-states:
		assert.Equal(t, StateOnHold, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on Hold")
	}
}

func TestCall_OnState_FiresOnResume(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	c.Hold()

	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.Resume()

	select {
	case s := <-states:
		assert.Equal(t, StateActive, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on Resume")
	}
}

func TestCall_OnState_FiresOnRemoteBye(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.simulateBye()

	select {
	case s := <-states:
		assert.Equal(t, StateEnded, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on remote BYE")
	}
}

func TestCall_OnState_FiresOnOutboundRinging(t *testing.T) {
	c := testOutboundCall(t, testutil.NewMockDialog())
	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.simulateResponse(180, "Ringing")

	select {
	case s := <-states:
		assert.Equal(t, StateRemoteRinging, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on 180")
	}
}

func TestCall_OnState_FiresOnOutbound200(t *testing.T) {
	c := testOutboundCall(t, testutil.NewMockDialog())
	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.simulateResponse(200, "OK")

	select {
	case s := <-states:
		assert.Equal(t, StateActive, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on 200 OK")
	}
}

func TestCall_OnState_FiresOnEarlyMedia(t *testing.T) {
	c := testOutboundCall(t, testutil.NewMockDialog(), WithEarlyMedia())
	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.simulateResponse(183, "Session Progress")

	select {
	case s := <-states:
		assert.Equal(t, StateEarlyMedia, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired on 183 EarlyMedia")
	}
}

func TestCall_OnState_DoesNotFireOnMute(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	c.Mute()

	// Mute does not change CallState — no OnState should fire.
	time.Sleep(100 * time.Millisecond)
	select {
	case s := <-states:
		t.Fatalf("OnState should not fire on Mute, got %v", s)
	default:
		// correct
	}
}

func TestCall_OnState_TracksFullLifecycle(t *testing.T) {
	c := testOutboundCall(t, testutil.NewMockDialog())
	states := make(chan CallState, 10)
	c.OnState(func(s CallState) { states <- s })

	// Read each state after each operation to avoid goroutine ordering assumptions.
	c.simulateResponse(180, "Ringing")
	select {
	case s := <-states:
		assert.Equal(t, StateRemoteRinging, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired for StateRemoteRinging")
	}

	c.simulateResponse(200, "OK")
	select {
	case s := <-states:
		assert.Equal(t, StateActive, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired for StateActive")
	}

	c.Hold()
	select {
	case s := <-states:
		assert.Equal(t, StateOnHold, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired for StateOnHold")
	}

	c.Resume()
	select {
	case s := <-states:
		assert.Equal(t, StateActive, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired for StateActive after Resume")
	}

	c.End()
	select {
	case s := <-states:
		assert.Equal(t, StateEnded, s)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnState never fired for StateEnded")
	}
}
