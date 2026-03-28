package xphone

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mock serverSession ---

type mockServerSession struct {
	respondCode    int
	respondReason  string
	respondBody    []byte
	respondHeaders []sip.Header
	respondErr     error

	byeCalled atomic.Bool
	byeErr    error

	doReq  *sip.Request
	doResp *sip.Response
	doErr  error

	writeReq *sip.Request
	writeErr error

	closeCalled atomic.Bool
}

func (m *mockServerSession) Respond(statusCode int, reason string, body []byte, headers ...sip.Header) error {
	m.respondCode = statusCode
	m.respondReason = reason
	m.respondBody = body
	m.respondHeaders = headers
	return m.respondErr
}

func (m *mockServerSession) Bye(ctx context.Context) error {
	m.byeCalled.Store(true)
	return m.byeErr
}

func (m *mockServerSession) Do(ctx context.Context, req *sip.Request) (*sip.Response, error) {
	m.doReq = req
	return m.doResp, m.doErr
}

func (m *mockServerSession) WriteRequest(req *sip.Request) error {
	m.writeReq = req
	return m.writeErr
}

func (m *mockServerSession) Close() error {
	m.closeCalled.Store(true)
	return nil
}

// --- test fixture helpers ---

func testServerInviteRequest() *sip.Request {
	req := sip.NewRequest(sip.INVITE, sip.Uri{
		Scheme: "sip",
		User:   "1002",
		Host:   "192.168.1.1",
		Port:   5060,
	})

	callID := sip.CallIDHeader("server-call-id-456")
	req.AppendHeader(&callID)

	from := sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: "1001", Host: "192.168.1.2"},
	}
	from.Params.Add("tag", "from-tag-server")
	req.AppendHeader(&from)

	to := sip.ToHeader{
		Address: sip.Uri{Scheme: "sip", User: "1002", Host: "192.168.1.1"},
	}
	req.AppendHeader(&to)

	cseq := sip.CSeqHeader{SeqNo: 1, MethodName: sip.INVITE}
	req.AppendHeader(&cseq)

	req.AppendHeader(sip.NewHeader("X-Server-Custom", "server-value"))

	// Contact from the remote party (the caller).
	contact := sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: "1001", Host: "192.168.1.2", Port: 5060},
	}
	req.AppendHeader(&contact)

	return req
}

func testServerInviteResponse(req *sip.Request) *sip.Response {
	resp := sip.NewResponseFromRequest(req, 200, "OK", nil)
	resp.AppendHeader(sip.NewHeader("Session-Expires", "3600"))
	return resp
}

func testDialogUAS(sess *mockServerSession) *sipgoDialogUAS {
	inv := testServerInviteRequest()
	resp := testServerInviteResponse(inv)
	return &sipgoDialogUAS{
		dialogBase: dialogBase{
			sess:     sess,
			invite:   inv,
			response: resp,
		},
	}
}

// --- tests ---

func TestSipgoDialogUAS_Respond(t *testing.T) {
	sess := &mockServerSession{}
	d := testDialogUAS(sess)

	sdpBody := []byte("v=0\r\no=- 0 0 IN IP4 10.0.0.1\r\n")
	err := d.Respond(200, "OK", sdpBody)
	require.NoError(t, err)
	assert.Equal(t, 200, sess.respondCode)
	assert.Equal(t, "OK", sess.respondReason)
	assert.Equal(t, sdpBody, sess.respondBody)
}

func TestSipgoDialogUAS_Respond_Provisional(t *testing.T) {
	sess := &mockServerSession{}
	d := testDialogUAS(sess)

	err := d.Respond(180, "Ringing", nil)
	require.NoError(t, err)
	assert.Equal(t, 180, sess.respondCode)
	assert.Equal(t, "Ringing", sess.respondReason)
}

func TestSipgoDialogUAS_Respond_Error(t *testing.T) {
	sess := &mockServerSession{
		respondErr: assert.AnError,
	}
	d := testDialogUAS(sess)

	err := d.Respond(200, "OK", nil)
	assert.ErrorIs(t, err, assert.AnError)
}

func TestSipgoDialogUAS_SendBye(t *testing.T) {
	sess := &mockServerSession{}
	d := testDialogUAS(sess)

	err := d.SendBye()
	require.NoError(t, err)
	assert.True(t, sess.byeCalled.Load(), "Bye should have been called on session")
}

func TestSipgoDialogUAS_SendBye_Error(t *testing.T) {
	sess := &mockServerSession{
		byeErr: assert.AnError,
	}
	d := testDialogUAS(sess)

	err := d.SendBye()
	assert.ErrorIs(t, err, assert.AnError)
}

func TestSipgoDialogUAS_SendCancel_ReturnsError(t *testing.T) {
	sess := &mockServerSession{}
	d := testDialogUAS(sess)

	// UAS cannot send CANCEL — that's a UAC operation.
	err := d.SendCancel()
	assert.ErrorIs(t, err, ErrInvalidState)
}

func TestSipgoDialogUAS_SendReInvite(t *testing.T) {
	sdpBody := []byte("v=0\r\no=- 0 0 IN IP4 10.0.0.1\r\n")
	resp := sip.NewResponseFromRequest(testServerInviteRequest(), 200, "OK", nil)

	sess := &mockServerSession{doResp: resp}
	d := testDialogUAS(sess)

	err := d.SendReInvite(sdpBody)
	require.NoError(t, err)
	require.NotNil(t, sess.doReq, "Do should have been called")
	assert.Equal(t, sip.INVITE, sess.doReq.Method, "request method should be INVITE")
	assert.Equal(t, sdpBody, sess.doReq.Body(), "request body should be the SDP")

	// ACK must be sent after 200 OK.
	require.NotNil(t, sess.writeReq, "WriteRequest should have been called for ACK")
	assert.Equal(t, sip.ACK, sess.writeReq.Method, "ACK should be sent after 200 OK")
}

func TestSipgoDialogUAS_SendReInvite_Error(t *testing.T) {
	sess := &mockServerSession{doErr: assert.AnError}
	d := testDialogUAS(sess)

	err := d.SendReInvite([]byte("v=0\r\n"))
	assert.ErrorIs(t, err, assert.AnError)
}

func TestSipgoDialogUAS_SendRefer(t *testing.T) {
	resp := sip.NewResponseFromRequest(testServerInviteRequest(), 202, "Accepted", nil)

	sess := &mockServerSession{doResp: resp}
	d := testDialogUAS(sess)

	err := d.SendRefer("sip:transfer@example.com")
	require.NoError(t, err)
	require.NotNil(t, sess.doReq, "Do should have been called")
	assert.Equal(t, sip.REFER, sess.doReq.Method, "request method should be REFER")
	referTo := sess.doReq.GetHeader("Refer-To")
	require.NotNil(t, referTo, "Refer-To header should be present")
	assert.Equal(t, "sip:transfer@example.com", referTo.Value())
}

func TestSipgoDialogUAS_SendRefer_UsesRemoteContact(t *testing.T) {
	resp := sip.NewResponseFromRequest(testServerInviteRequest(), 202, "Accepted", nil)
	sess := &mockServerSession{doResp: resp}
	d := testDialogUAS(sess)

	err := d.SendRefer("sip:transfer@example.com")
	require.NoError(t, err)
	require.NotNil(t, sess.doReq)

	// The REFER Request-URI must be the remote party's Contact (192.168.1.2),
	// NOT the server's own address (192.168.1.1) from the INVITE Request-URI.
	assert.Equal(t, "192.168.1.2", sess.doReq.Recipient.Host)
	assert.Equal(t, "1001", sess.doReq.Recipient.User)
}

func TestSipgoDialogUAS_SendReInvite_UsesRemoteContact(t *testing.T) {
	resp := sip.NewResponseFromRequest(testServerInviteRequest(), 200, "OK", nil)
	sess := &mockServerSession{doResp: resp}
	d := testDialogUAS(sess)

	err := d.SendReInvite([]byte("v=0\r\n"))
	require.NoError(t, err)
	require.NotNil(t, sess.doReq)

	assert.Equal(t, "192.168.1.2", sess.doReq.Recipient.Host)
	assert.Equal(t, "1001", sess.doReq.Recipient.User)
}

func TestSipgoDialogUAS_SendInfoDTMF_UsesRemoteContact(t *testing.T) {
	resp := sip.NewResponseFromRequest(testServerInviteRequest(), 200, "OK", nil)
	sess := &mockServerSession{doResp: resp}
	d := testDialogUAS(sess)

	err := d.SendInfoDTMF("1", 160)
	require.NoError(t, err)
	require.NotNil(t, sess.doReq)

	assert.Equal(t, "192.168.1.2", sess.doReq.Recipient.Host)
	assert.Equal(t, "1001", sess.doReq.Recipient.User)
}

func TestSipgoDialogUAS_CallID(t *testing.T) {
	sess := &mockServerSession{}
	d := testDialogUAS(sess)

	assert.Equal(t, "server-call-id-456", d.CallID())
}

func TestSipgoDialogUAS_Header(t *testing.T) {
	sess := &mockServerSession{}
	d := testDialogUAS(sess)

	// X-Server-Custom is on the INVITE request
	vals := d.Header("X-Server-Custom")
	require.Len(t, vals, 1)
	assert.Equal(t, "server-value", vals[0])

	// Session-Expires is on the response
	vals = d.Header("Session-Expires")
	require.Len(t, vals, 1)
	assert.Equal(t, "3600", vals[0])
}

func TestSipgoDialogUAS_Header_CaseInsensitive(t *testing.T) {
	sess := &mockServerSession{}
	d := testDialogUAS(sess)

	vals := d.Header("x-server-custom")
	require.Len(t, vals, 1)
	assert.Equal(t, "server-value", vals[0])

	vals = d.Header("session-expires")
	require.Len(t, vals, 1)
	assert.Equal(t, "3600", vals[0])
}

func TestSipgoDialogUAS_Headers_DeepCopy(t *testing.T) {
	sess := &mockServerSession{}
	d := testDialogUAS(sess)

	h1 := d.Headers()
	require.NotNil(t, h1, "Headers() should return non-nil map")

	// Verify Call-ID is present
	callIDs, ok := h1["Call-ID"]
	require.True(t, ok, "Call-ID should be in headers")
	require.Len(t, callIDs, 1)
	assert.Equal(t, "server-call-id-456", callIDs[0])

	// Mutate and verify independent copy
	h1["Call-ID"] = []string{"mutated"}
	h2 := d.Headers()
	assert.Equal(t, "server-call-id-456", h2["Call-ID"][0], "mutation should not affect internal state")
}

func TestSipgoDialogUAS_OnNotify(t *testing.T) {
	sess := &mockServerSession{}
	d := testDialogUAS(sess)

	var received int
	d.OnNotify(func(code int) {
		received = code
	})

	// Simulate a NOTIFY by invoking the stored callback
	d.mu.Lock()
	fn := d.onNotify
	d.mu.Unlock()
	require.NotNil(t, fn, "onNotify should be set")

	fn(200)
	assert.Equal(t, 200, received)
}
