package xphone

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mock clientSession ---

type mockClientSession struct {
	byeCalled atomic.Bool
	byeErr    error

	doReq  *sip.Request
	doResp *sip.Response
	doErr  error

	writeReq *sip.Request
	writeErr error
}

func (m *mockClientSession) Bye(ctx context.Context) error {
	m.byeCalled.Store(true)
	return m.byeErr
}

func (m *mockClientSession) Do(ctx context.Context, req *sip.Request) (*sip.Response, error) {
	m.doReq = req
	return m.doResp, m.doErr
}

func (m *mockClientSession) WriteRequest(req *sip.Request) error {
	m.writeReq = req
	return m.writeErr
}

// --- test fixture helpers ---

func testInviteRequest() *sip.Request {
	req := sip.NewRequest(sip.INVITE, sip.Uri{
		Scheme: "sip",
		User:   "1001",
		Host:   "192.168.1.1",
		Port:   5060,
	})

	callID := sip.CallIDHeader("test-call-id-123")
	req.AppendHeader(&callID)

	from := sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: "1000", Host: "192.168.1.2"},
	}
	from.Params.Add("tag", "from-tag-abc")
	req.AppendHeader(&from)

	to := sip.ToHeader{
		Address: sip.Uri{Scheme: "sip", User: "1001", Host: "192.168.1.1"},
	}
	req.AppendHeader(&to)

	cseq := sip.CSeqHeader{SeqNo: 1, MethodName: sip.INVITE}
	req.AppendHeader(&cseq)

	req.AppendHeader(sip.NewHeader("X-Custom", "custom-value"))

	return req
}

func testInviteResponse(req *sip.Request) *sip.Response {
	resp := sip.NewResponseFromRequest(req, 200, "OK", nil)
	resp.AppendHeader(sip.NewHeader("Session-Expires", "1800"))
	// Contact from the remote party (the callee).
	contact := sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: "1001", Host: "10.0.0.5", Port: 5060},
	}
	resp.AppendHeader(&contact)
	return resp
}

func testDialogUAC(sess *mockClientSession) *sipgoDialogUAC {
	inv := testInviteRequest()
	resp := testInviteResponse(inv)
	return &sipgoDialogUAC{
		dialogBase: dialogBase{
			sess:     sess,
			invite:   inv,
			response: resp,
		},
	}
}

// --- tests ---

func TestSipgoDialogUAC_SendBye(t *testing.T) {
	sess := &mockClientSession{}
	d := testDialogUAC(sess)

	err := d.SendBye()
	require.NoError(t, err)
	assert.True(t, sess.byeCalled.Load(), "Bye should have been called on session")
}

func TestSipgoDialogUAC_SendBye_Error(t *testing.T) {
	sess := &mockClientSession{
		byeErr: assert.AnError,
	}
	d := testDialogUAC(sess)

	err := d.SendBye()
	assert.ErrorIs(t, err, assert.AnError)
}

func TestSipgoDialogUAC_SendCancel(t *testing.T) {
	sess := &mockClientSession{}
	cancelled := atomic.Bool{}
	cancelFn := func() { cancelled.Store(true) }

	d := testDialogUAC(sess)
	d.cancelFn = cancelFn

	err := d.SendCancel()
	require.NoError(t, err)
	assert.True(t, cancelled.Load(), "cancelFn should have been invoked")
}

func TestSipgoDialogUAC_SendCancel_NilCancelFn(t *testing.T) {
	sess := &mockClientSession{}
	d := testDialogUAC(sess)
	d.cancelFn = nil

	err := d.SendCancel()
	assert.Error(t, err, "SendCancel with nil cancelFn should return error")
}

func TestSipgoDialogUAC_SendReInvite(t *testing.T) {
	sdpBody := []byte("v=0\r\no=- 0 0 IN IP4 10.0.0.1\r\n")
	resp := sip.NewResponseFromRequest(testInviteRequest(), 200, "OK", nil)

	sess := &mockClientSession{doResp: resp}
	d := testDialogUAC(sess)

	err := d.SendReInvite(sdpBody)
	require.NoError(t, err)
	require.NotNil(t, sess.doReq, "Do should have been called")
	assert.Equal(t, sip.INVITE, sess.doReq.Method, "request method should be INVITE")
	assert.Equal(t, sdpBody, sess.doReq.Body(), "request body should be the SDP")
}

func TestSipgoDialogUAC_SendRefer(t *testing.T) {
	resp := sip.NewResponseFromRequest(testInviteRequest(), 202, "Accepted", nil)

	sess := &mockClientSession{doResp: resp}
	d := testDialogUAC(sess)

	err := d.SendRefer("sip:transfer@example.com")
	require.NoError(t, err)
	require.NotNil(t, sess.doReq, "Do should have been called")
	assert.Equal(t, sip.REFER, sess.doReq.Method, "request method should be REFER")
	referTo := sess.doReq.GetHeader("Refer-To")
	require.NotNil(t, referTo, "Refer-To header should be present")
	assert.Equal(t, "sip:transfer@example.com", referTo.Value())
}

func TestSipgoDialogUAC_SendRefer_UsesRemoteContact(t *testing.T) {
	resp := sip.NewResponseFromRequest(testInviteRequest(), 202, "Accepted", nil)
	sess := &mockClientSession{doResp: resp}
	d := testDialogUAC(sess)

	err := d.SendRefer("sip:transfer@example.com")
	require.NoError(t, err)
	require.NotNil(t, sess.doReq)

	// The REFER Request-URI must be the remote party's Contact from the 200 OK.
	assert.Equal(t, "10.0.0.5", sess.doReq.Recipient.Host)
	assert.Equal(t, "1001", sess.doReq.Recipient.User)
}

func TestDialogBase_RemoteTarget_Fallback(t *testing.T) {
	inv := sip.NewRequest(sip.INVITE, sip.Uri{
		Scheme: "sip", User: "1001", Host: "10.0.0.1", Port: 5060,
	})
	d := &dialogBase{invite: inv}

	target := d.remoteTarget()
	assert.Equal(t, "10.0.0.1", target.Host)
	assert.Equal(t, "1001", target.User)
}

func TestSipgoDialogUAC_Respond_ReturnsError(t *testing.T) {
	sess := &mockClientSession{}
	d := testDialogUAC(sess)

	err := d.Respond(200, "OK", nil)
	assert.ErrorIs(t, err, ErrInvalidState)
}

func TestSipgoDialogUAC_CallID(t *testing.T) {
	sess := &mockClientSession{}
	d := testDialogUAC(sess)

	assert.Equal(t, "test-call-id-123", d.CallID())
}

func TestSipgoDialogUAC_Header(t *testing.T) {
	sess := &mockClientSession{}
	d := testDialogUAC(sess)

	// X-Custom is on the INVITE request
	vals := d.Header("X-Custom")
	require.Len(t, vals, 1)
	assert.Equal(t, "custom-value", vals[0])

	// Session-Expires is on the response
	vals = d.Header("Session-Expires")
	require.Len(t, vals, 1)
	assert.Equal(t, "1800", vals[0])
}

func TestSipgoDialogUAC_Header_CaseInsensitive(t *testing.T) {
	sess := &mockClientSession{}
	d := testDialogUAC(sess)

	vals := d.Header("x-custom")
	require.Len(t, vals, 1)
	assert.Equal(t, "custom-value", vals[0])

	vals = d.Header("session-expires")
	require.Len(t, vals, 1)
	assert.Equal(t, "1800", vals[0])
}

func TestSipgoDialogUAC_Headers_DeepCopy(t *testing.T) {
	sess := &mockClientSession{}
	d := testDialogUAC(sess)

	h1 := d.Headers()
	require.NotNil(t, h1, "Headers() should return non-nil map")

	// Verify Call-ID is present
	callIDs, ok := h1["Call-ID"]
	require.True(t, ok, "Call-ID should be in headers")
	require.Len(t, callIDs, 1)
	assert.Equal(t, "test-call-id-123", callIDs[0])

	// Mutate and verify independent copy
	h1["Call-ID"] = []string{"mutated"}
	h2 := d.Headers()
	assert.Equal(t, "test-call-id-123", h2["Call-ID"][0], "mutation should not affect internal state")
}

func TestSipgoDialogUAC_OnNotify(t *testing.T) {
	sess := &mockClientSession{}
	d := testDialogUAC(sess)

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
