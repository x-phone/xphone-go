package xphone

import (
	"context"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildRequest assembles a SIP request with common headers for observer tests.
func buildRequest(method sip.RequestMethod) *sip.Request {
	req := sip.NewRequest(method, sip.Uri{Scheme: "sip", User: "bob", Host: "example.com"})
	req.AppendHeader(&sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "alice", Host: "example.com"}, Params: sip.NewParams()})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: "bob", Host: "example.com"}, Params: sip.NewParams()})
	callID := sip.CallIDHeader("call-abc-123")
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 2, MethodName: method})
	req.SetTransport("UDP")
	return req
}

func TestBuildSIPMessage_Request(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	req := buildRequest(sip.INVITE)

	m := buildSIPMessage(req, SIPOutbound, now)

	assert.Equal(t, SIPOutbound, m.Direction)
	assert.False(t, m.IsResponse)
	assert.Equal(t, "INVITE", m.Method)
	assert.Equal(t, 0, m.StatusCode)
	assert.Equal(t, "", m.Reason)
	assert.Equal(t, "call-abc-123", m.CallID)
	assert.Equal(t, "2 INVITE", m.CSeq)
	assert.Equal(t, "UDP", m.Transport)
	assert.Equal(t, now, m.Timestamp)
	assert.Contains(t, m.From, "alice")
	assert.Contains(t, m.To, "bob")
}

func TestBuildSIPMessage_Response(t *testing.T) {
	req := buildRequest(sip.INVITE)
	res := sip.NewResponseFromRequest(req, 200, "OK", nil)

	m := buildSIPMessage(res, SIPInbound, time.Now())

	assert.Equal(t, SIPInbound, m.Direction)
	assert.True(t, m.IsResponse)
	assert.Equal(t, 200, m.StatusCode)
	assert.Equal(t, "OK", m.Reason)
	// A response reports the method of its CSeq, not a request method.
	assert.Equal(t, "INVITE", m.Method)
	assert.Equal(t, "call-abc-123", m.CallID)
}

func TestBuildSIPMessage_RedactsCredentials(t *testing.T) {
	req := buildRequest(sip.REGISTER)
	// The digest response= in these headers is the secret we must never surface.
	req.AppendHeader(sip.NewHeader("Authorization",
		`Digest username="alice", realm="ex", nonce="abc", response="DEADBEEFcafe1234", uri="sip:ex"`))
	req.AppendHeader(sip.NewHeader("Proxy-Authorization",
		`Digest username="alice", response="SECRETdigest99"`))
	// A challenge header is not a secret and must be preserved.
	req.AppendHeader(sip.NewHeader("WWW-Authenticate", `Digest realm="ex", nonce="xyz"`))

	m := buildSIPMessage(req, SIPInbound, time.Now())

	assert.Equal(t, redactedValue, m.Headers["Authorization"])
	assert.Equal(t, redactedValue, m.Headers["Proxy-Authorization"])
	assert.NotContains(t, m.Headers["Authorization"], "DEADBEEF")
	assert.NotContains(t, m.Headers["Proxy-Authorization"], "SECRETdigest99")
	// Challenge (nonce/realm) left intact. Key is canonicalized.
	assert.Contains(t, m.Headers["Www-Authenticate"], "nonce")
}

func TestBuildSIPMessage_RedactionIsCaseInsensitive(t *testing.T) {
	req := buildRequest(sip.REGISTER)
	req.AppendHeader(sip.NewHeader("AUTHORIZATION", `Digest response="topsecret"`))

	m := buildSIPMessage(req, SIPInbound, time.Now())

	// Stored under the canonical key regardless of the wire casing.
	assert.Equal(t, redactedValue, m.Headers["Authorization"])
	assert.NotContains(t, m.Headers["Authorization"], "topsecret")
}

func TestBuildSIPMessage_CanonicalizesHeaderKeys(t *testing.T) {
	req := buildRequest(sip.INVITE)
	// A remote may send lowercased header names (RFC 3261 §7.3.1). They must
	// still redact and be reachable under the canonical key.
	req.AppendHeader(sip.NewHeader("authorization", `Digest response="lowersecret"`))
	req.AppendHeader(sip.NewHeader("user-agent", "Weird/1.0"))

	m := buildSIPMessage(req, SIPInbound, time.Now())

	assert.Equal(t, redactedValue, m.Headers["Authorization"], "lowercased authorization must redact under canonical key")
	assert.NotContains(t, m.Headers["Authorization"], "lowersecret")
	assert.Equal(t, "Weird/1.0", m.Headers["User-Agent"])
}

func TestBuildSIPMessage_JoinsRepeatedHeaders(t *testing.T) {
	req := buildRequest(sip.INVITE)
	req.AppendHeader(sip.NewHeader("Via", "SIP/2.0/UDP a.example.com"))
	req.AppendHeader(sip.NewHeader("Via", "SIP/2.0/UDP b.example.com"))

	m := buildSIPMessage(req, SIPInbound, time.Now())

	assert.Contains(t, m.Headers["Via"], "a.example.com")
	assert.Contains(t, m.Headers["Via"], "b.example.com")
	assert.Contains(t, m.Headers["Via"], ", ")
}

func TestEmitSIP_NilObserverAndNilMessage(t *testing.T) {
	// Neither a nil observer nor a nil message may panic.
	assert.NotPanics(t, func() { emitSIP(nil, buildRequest(sip.INVITE), SIPInbound) })
	assert.NotPanics(t, func() {
		emitSIP(func(SIPMessage) { t.Fatal("observer must not fire for nil message") }, nil, SIPInbound)
	})
}

func TestEmitSIP_DeliversWithObserverClock(t *testing.T) {
	pinned := time.Unix(1_234_567_890, 0).UTC()
	orig := observerNow
	observerNow = func() time.Time { return pinned }
	defer func() { observerNow = orig }()

	var got SIPMessage
	emitSIP(func(m SIPMessage) { got = m }, buildRequest(sip.BYE), SIPOutbound)

	assert.Equal(t, "BYE", got.Method)
	assert.Equal(t, SIPOutbound, got.Direction)
	assert.Equal(t, pinned, got.Timestamp)
}

// --- outbound emission through dialogBase send helpers ---

func TestDialogObserve_OutboundInDialogRequests(t *testing.T) {
	var seen []SIPMessage
	mock := &observerMockSession{}
	d := &dialogBase{
		sess:     mock,
		invite:   buildRequest(sip.INVITE),
		response: sip.NewResponseFromRequest(buildRequest(sip.INVITE), 200, "OK", nil),
		observer: func(m SIPMessage) { seen = append(seen, m) },
	}

	require.NoError(t, d.SendRefer("sip:carol@example.com"))
	require.NoError(t, d.SendInfoDTMF("5", 200))

	// REFER and INFO, both outbound.
	require.Len(t, seen, 2)
	assert.Equal(t, "REFER", seen[0].Method)
	assert.Equal(t, SIPOutbound, seen[0].Direction)
	assert.Equal(t, "INFO", seen[1].Method)
	assert.Equal(t, SIPOutbound, seen[1].Direction)
	// The snapshot must be taken AFTER sipgo populates the dialog headers,
	// so every observed outbound request carries the correlating Call-ID/CSeq.
	for _, m := range seen {
		assert.Equal(t, "dialog-call-id-xyz", m.CallID, "%s observed before dialog headers populated", m.Method)
		assert.NotEmpty(t, m.CSeq, "%s missing CSeq", m.Method)
	}
}

func TestDialogObserve_ReInviteEmitsInviteThenAck(t *testing.T) {
	var seen []SIPMessage
	mock := &observerMockSession{ok: true}
	d := &dialogBase{
		sess:     mock,
		invite:   buildRequest(sip.INVITE),
		response: sip.NewResponseFromRequest(buildRequest(sip.INVITE), 200, "OK", nil),
		observer: func(m SIPMessage) { seen = append(seen, m) },
	}

	require.NoError(t, d.SendReInvite([]byte("v=0\r\n")))

	require.Len(t, seen, 2)
	assert.Equal(t, "INVITE", seen[0].Method)
	assert.Equal(t, "ACK", seen[1].Method)
	// Both must carry the dialog Call-ID (populated by the send, snapshotted after).
	for _, m := range seen {
		assert.Equal(t, "dialog-call-id-xyz", m.CallID, "%s observed before headers populated", m.Method)
	}
}

func TestDialogObserve_OutboundTimestampedAtSendInitiation(t *testing.T) {
	// A monotonic fake clock: send-initiation reads t=1; the mock's Do advances
	// it to simulate time passing while awaiting the response. The observed
	// outbound request must carry the earlier (send-initiation) timestamp, so a
	// response observed later sorts after it.
	var tick int64
	orig := observerNow
	observerNow = func() time.Time { tick++; return time.Unix(tick, 0).UTC() }
	defer func() { observerNow = orig }()

	var got SIPMessage
	mock := &observerMockSession{advanceClock: func() { observerNow() }} // burn ticks during Do
	d := &dialogBase{
		sess:     mock,
		invite:   buildRequest(sip.INVITE),
		response: sip.NewResponseFromRequest(buildRequest(sip.INVITE), 200, "OK", nil),
		observer: func(m SIPMessage) { got = m },
	}

	require.NoError(t, d.SendRefer("sip:carol@example.com"))

	// sentAt was read first (tick=1). Do burned a tick (tick=2). A later
	// observation would be tick>=3. The request must hold the send-time stamp.
	assert.Equal(t, time.Unix(1, 0).UTC(), got.Timestamp)
	assert.True(t, got.Timestamp.Before(observerNow()), "outbound stamp must precede a later clock read")
}

func TestDialogObserve_NilObserverNoPanic(t *testing.T) {
	d := &dialogBase{
		sess:   &observerMockSession{},
		invite: buildRequest(sip.INVITE),
	}
	assert.NotPanics(t, func() { _ = d.SendRefer("sip:x@y") })
}

// observerMockSession is a dialogSession that mimics sipgo's in-place header
// population (buildReq): Do/WriteRequest stamp the dialog's From/To/Call-ID/
// CSeq onto the passed request *before* returning. This models the real
// library so the observer tests can prove the snapshot is taken after the
// identifiers exist — guarding the "observed before headers populated" bug.
type observerMockSession struct {
	ok           bool
	advanceClock func() // optional: called inside Do to simulate elapsed time
}

func (m *observerMockSession) stampDialogHeaders(req *sip.Request) {
	if req.From() == nil {
		req.AppendHeader(&sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "alice", Host: "example.com"}, Params: sip.NewParams()})
	}
	if req.To() == nil {
		req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: "bob", Host: "example.com"}, Params: sip.NewParams()})
	}
	if req.CallID() == nil {
		cid := sip.CallIDHeader("dialog-call-id-xyz")
		req.AppendHeader(&cid)
	}
	if req.CSeq() == nil {
		req.AppendHeader(&sip.CSeqHeader{SeqNo: 3, MethodName: req.Method})
	}
}

func (m *observerMockSession) Bye(ctx context.Context) error { return nil }
func (m *observerMockSession) Do(ctx context.Context, req *sip.Request) (*sip.Response, error) {
	m.stampDialogHeaders(req)
	if m.advanceClock != nil {
		m.advanceClock()
	}
	return sip.NewResponseFromRequest(req, 200, "OK", nil), nil
}
func (m *observerMockSession) WriteRequest(req *sip.Request) error {
	m.stampDialogHeaders(req)
	return nil
}
