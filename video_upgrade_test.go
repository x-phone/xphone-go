package xphone

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/internal/sdp"
	"github.com/x-phone/xphone-go/testutil"
)

// mockResponder records the SIP response sent for a re-INVITE.
type mockResponder struct {
	code   int
	reason string
	body   []byte
}

func (r *mockResponder) Respond(code int, reason string, body []byte) error {
	r.code = code
	r.reason = reason
	r.body = body
	return nil
}

// testVideoOfferSDP generates a remote SDP offer with audio + video.
func testVideoOfferSDP(ip string, audioPort, videoPort int, videoCodecs ...int) string {
	return sdp.BuildOfferVideo(ip, audioPort, []int{0}, videoPort, videoCodecs, "sendrecv")
}

// --- Video upgrade accept ---

func TestVideoUpgrade_Accept(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	var reqReceived *VideoUpgradeRequest
	requestCh := make(chan *VideoUpgradeRequest, 1)
	c.OnVideoRequest(func(req *VideoUpgradeRequest) {
		requestCh <- req
	})

	videoCh := make(chan struct{}, 1)
	c.OnVideo(func() { videoCh <- struct{}{} })

	resp := &mockResponder{}
	offer := testVideoOfferSDP("10.0.0.1", 5000, 5002, 96)
	c.handleReInvite(resp, offer)

	// Should receive the upgrade request via callback.
	select {
	case reqReceived = <-requestCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnVideoRequest callback never fired")
	}

	require.NotNil(t, reqReceived)
	assert.Equal(t, VideoCodecH264, reqReceived.RemoteCodec())

	// Accept the upgrade.
	reqReceived.Accept()

	// Should respond 200 OK with video SDP.
	assert.Equal(t, 200, resp.code)
	assert.Equal(t, "OK", resp.reason)
	assert.Contains(t, string(resp.body), "m=video")
	assert.NotContains(t, string(resp.body), "m=video 0")

	// OnVideo callback should fire.
	select {
	case <-videoCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnVideo callback never fired after accept")
	}

	// Call should have video active.
	assert.True(t, c.HasVideo())
}

// --- Video upgrade reject ---

func TestVideoUpgrade_Reject(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	requestCh := make(chan *VideoUpgradeRequest, 1)
	c.OnVideoRequest(func(req *VideoUpgradeRequest) {
		requestCh <- req
	})

	resp := &mockResponder{}
	offer := testVideoOfferSDP("10.0.0.1", 5000, 5002, 96)
	c.handleReInvite(resp, offer)

	var req *VideoUpgradeRequest
	select {
	case req = <-requestCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnVideoRequest callback never fired")
	}

	req.Reject()

	// Should respond 200 OK with m=video 0 (rejection).
	assert.Equal(t, 200, resp.code)
	assert.Contains(t, string(resp.body), "m=video 0")

	// Call should NOT have video.
	assert.False(t, c.HasVideo())
}

// --- Auto-reject when no handler ---

func TestVideoUpgrade_AutoRejectNoHandler(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	// No OnVideoRequest handler registered.

	resp := &mockResponder{}
	offer := testVideoOfferSDP("10.0.0.1", 5000, 5002, 96)
	c.handleReInvite(resp, offer)

	// Give time for auto-reject to process.
	time.Sleep(100 * time.Millisecond)

	// Should respond 200 OK with m=video 0 (auto-rejected).
	assert.Equal(t, 200, resp.code)
	assert.Contains(t, string(resp.body), "m=video 0")
	assert.False(t, c.HasVideo())
}

// --- Timeout auto-reject ---

func TestVideoUpgrade_TimeoutAutoReject(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	requestCh := make(chan *VideoUpgradeRequest, 1)
	c.OnVideoRequest(func(req *VideoUpgradeRequest) {
		requestCh <- req
	})

	resp := &mockResponder{}
	offer := testVideoOfferSDP("10.0.0.1", 5000, 5002, 96)
	c.handleReInvite(resp, offer)

	var req *VideoUpgradeRequest
	select {
	case req = <-requestCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnVideoRequest callback never fired")
	}

	// Manually trigger the timer callback (simulating timeout).
	req.timer.Stop()
	req.Reject() // simulate what the timer would do

	assert.Equal(t, 200, resp.code)
	assert.Contains(t, string(resp.body), "m=video 0")
	assert.False(t, c.HasVideo())
}

// --- Idempotent Accept/Reject ---

func TestVideoUpgrade_IdempotentAcceptReject(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	requestCh := make(chan *VideoUpgradeRequest, 1)
	c.OnVideoRequest(func(req *VideoUpgradeRequest) {
		requestCh <- req
	})

	resp := &mockResponder{}
	offer := testVideoOfferSDP("10.0.0.1", 5000, 5002, 96)
	c.handleReInvite(resp, offer)

	var req *VideoUpgradeRequest
	select {
	case req = <-requestCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnVideoRequest callback never fired")
	}

	// Accept first — should succeed.
	req.Accept()
	assert.Equal(t, 200, resp.code)

	// Second Accept is a no-op (sync.Once).
	req.Accept()
	// Second Reject is a no-op.
	req.Reject()

	// State should still be video-active from first Accept.
	assert.True(t, c.HasVideo())
}

// --- Ended call ignores upgrade ---

func TestVideoUpgrade_EndedCallIgnoresAccept(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	requestCh := make(chan *VideoUpgradeRequest, 1)
	c.OnVideoRequest(func(req *VideoUpgradeRequest) {
		requestCh <- req
	})

	resp := &mockResponder{}
	offer := testVideoOfferSDP("10.0.0.1", 5000, 5002, 96)
	c.handleReInvite(resp, offer)

	var req *VideoUpgradeRequest
	select {
	case req = <-requestCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnVideoRequest callback never fired")
	}

	// End the call first.
	c.End()

	// Accept on ended call is a no-op (no panic, video socket cleaned up).
	req.Accept()
	assert.False(t, c.HasVideo())
}

func TestVideoUpgrade_EndedCallIgnoresReInvite(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	c.End()

	resp := &mockResponder{}
	offer := testVideoOfferSDP("10.0.0.1", 5000, 5002, 96)
	c.handleReInvite(resp, offer)

	// Responder should not have been called (early return for ended call).
	assert.Equal(t, 0, resp.code)
}

// --- Video downgrade ---

func TestVideoDowngrade(t *testing.T) {
	c := activeVideoCall(t)
	defer c.stopMedia()
	defer c.videoRTPConn.Close()

	assert.True(t, c.HasVideo())

	// Send re-INVITE with m=video 0 (downgrade).
	resp := &mockResponder{}
	audioOnlySDP := testSDP("10.0.0.1", 5000, "sendrecv", 0)
	c.handleReInvite(resp, audioOnlySDP)

	// Should respond 200 OK, audio-only.
	assert.Equal(t, 200, resp.code)
	assert.False(t, c.HasVideo())
}

func TestVideoDowngrade_VideoPort0(t *testing.T) {
	c := activeVideoCall(t)
	defer c.stopMedia()
	defer c.videoRTPConn.Close()

	assert.True(t, c.HasVideo())

	// Send re-INVITE with explicit m=video 0 (port 0 rejection).
	resp := &mockResponder{}
	offer := testVideoOfferSDP("10.0.0.1", 5000, 0, 96)
	c.handleReInvite(resp, offer)

	assert.Equal(t, 200, resp.code)
	assert.False(t, c.HasVideo())
}

// --- Hold delegation (audio-only re-INVITE while no video) ---

func TestReInvite_HoldResumeWithoutVideo(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	holdResp := &mockResponder{}
	holdSDP := testSDP("10.0.0.1", 5000, "sendonly", 0)
	c.handleReInvite(holdResp, holdSDP)

	assert.Equal(t, 200, holdResp.code)
	assert.Equal(t, StateOnHold, c.State())

	resumeResp := &mockResponder{}
	resumeSDP := testSDP("10.0.0.1", 5000, "sendrecv", 0)
	c.handleReInvite(resumeResp, resumeSDP)

	assert.Equal(t, 200, resumeResp.code)
	assert.Equal(t, StateActive, c.State())
}

// --- AddVideo sends re-INVITE ---

func TestAddVideo_SendsReInvite(t *testing.T) {
	dlg := testutil.NewMockDialog()
	c := testInboundCall(t, dlg)
	c.Accept()

	err := c.AddVideo(VideoCodecH264, VideoCodecVP8)
	require.NoError(t, err)

	reInviteSDP := dlg.LastReInviteSDP()
	assert.NotEmpty(t, reInviteSDP)
	assert.Contains(t, reInviteSDP, "m=video")
}

func TestAddVideo_RequiresActiveState(t *testing.T) {
	c := testInboundCall(t)
	// Still ringing — not accepted yet.
	err := c.AddVideo()
	assert.Equal(t, ErrInvalidState, err)
}

func TestAddVideo_AlreadyActive(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	c.mu.Lock()
	c.hasVideo = true
	c.mu.Unlock()

	err := c.AddVideo()
	assert.Equal(t, ErrVideoAlreadyActive, err)
}

func TestAddVideo_DefaultCodecs(t *testing.T) {
	dlg := testutil.NewMockDialog()
	c := testInboundCall(t, dlg)
	c.Accept()

	// No codecs specified — should default to H264 + VP8.
	err := c.AddVideo()
	require.NoError(t, err)

	reInviteSDP := dlg.LastReInviteSDP()
	assert.Contains(t, reInviteSDP, "m=video")
}

// --- RemoteCodec ---

func TestVideoUpgrade_RemoteCodecVP8(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	requestCh := make(chan *VideoUpgradeRequest, 1)
	c.OnVideoRequest(func(req *VideoUpgradeRequest) {
		requestCh <- req
	})

	resp := &mockResponder{}
	offer := testVideoOfferSDP("10.0.0.1", 5000, 5002, 97) // VP8
	c.handleReInvite(resp, offer)

	var req *VideoUpgradeRequest
	select {
	case req = <-requestCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnVideoRequest callback never fired")
	}

	assert.Equal(t, VideoCodecVP8, req.RemoteCodec())
	req.Reject() // clean up
}

// --- Bad SDP ---

func TestReInvite_BadSDP(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	resp := &mockResponder{}
	c.handleReInvite(resp, "not valid sdp")

	assert.Equal(t, 400, resp.code)
}

// --- Pending upgrade cleaned up on End ---

func TestVideoUpgrade_PendingCleanedUpOnEnd(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	requestCh := make(chan *VideoUpgradeRequest, 1)
	c.OnVideoRequest(func(req *VideoUpgradeRequest) {
		requestCh <- req
	})

	resp := &mockResponder{}
	offer := testVideoOfferSDP("10.0.0.1", 5000, 5002, 96)
	c.handleReInvite(resp, offer)

	select {
	case <-requestCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnVideoRequest callback never fired")
	}

	// pendingVideoUpgrade should be set.
	c.mu.Lock()
	hasPending := c.pendingVideoUpgrade != nil
	c.mu.Unlock()
	assert.True(t, hasPending)

	// End the call — should clean up the pending upgrade.
	c.End()

	time.Sleep(100 * time.Millisecond)
	c.mu.Lock()
	hasPending = c.pendingVideoUpgrade != nil
	c.mu.Unlock()
	assert.False(t, hasPending)
}

// --- Reject SDP contains m=video 0 ---

func TestVideoUpgrade_RejectSDPContainsVideoZero(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()

	requestCh := make(chan *VideoUpgradeRequest, 1)
	c.OnVideoRequest(func(req *VideoUpgradeRequest) {
		requestCh <- req
	})

	resp := &mockResponder{}
	offer := testVideoOfferSDP("10.0.0.1", 5000, 5002, 96)
	c.handleReInvite(resp, offer)

	var req *VideoUpgradeRequest
	select {
	case req = <-requestCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OnVideoRequest callback never fired")
	}

	req.Reject()

	body := string(resp.body)
	// Must contain audio m= line.
	assert.True(t, strings.Contains(body, "m=audio"), "reject SDP must contain audio")
	// Must contain m=video 0 rejection line (RFC 3264).
	assert.True(t, strings.Contains(body, "m=video 0"), "reject SDP must contain m=video 0")
}

// --- stopVideoPipeline ---

func TestStopVideoPipeline_NoOpWhenNoVideo(t *testing.T) {
	c := testInboundCall(t)
	c.Accept()
	// Should not panic when called without video.
	c.stopVideoPipeline()
}

// --- MockCall video upgrade methods ---

func TestMockCall_AddVideo(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)

	err := c.AddVideo()
	assert.NoError(t, err)
}

func TestMockCall_OnVideoRequest(t *testing.T) {
	c := NewMockCall()
	// Should not panic.
	c.OnVideoRequest(func(req *VideoUpgradeRequest) {})
	c.OnVideo(func() {})
}
