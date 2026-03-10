package xphone

import (
	"net"
	"sync"
	"time"

	"github.com/x-phone/xphone-go/internal/sdp"
	"github.com/x-phone/xphone-go/internal/srtp"
)

// videoUpgradeTimeout is how long the app has to accept or reject a video
// upgrade request before it is automatically rejected.
const videoUpgradeTimeout = 30 * time.Second

// reInviteResponder allows responding to a specific re-INVITE transaction.
type reInviteResponder interface {
	Respond(code int, reason string, body []byte) error
}

// VideoUpgradeRequest is created when the remote sends a re-INVITE adding video.
// The app must call Accept() or Reject(). If neither is called within 30 seconds,
// the request is automatically rejected. Calling Accept or Reject more than once
// is a no-op.
type VideoUpgradeRequest struct {
	call      *call
	responder reInviteResponder
	remoteSDP string
	sess      *sdp.Session
	videoConn net.PacketConn // pre-allocated video socket (closed on reject)
	videoPort int
	responded sync.Once
	timer     *time.Timer
}

func newVideoUpgradeRequest(c *call, resp reInviteResponder, rawSDP string, sess *sdp.Session, videoConn net.PacketConn, videoPort int) *VideoUpgradeRequest {
	req := &VideoUpgradeRequest{
		call:      c,
		responder: resp,
		remoteSDP: rawSDP,
		sess:      sess,
		videoConn: videoConn,
		videoPort: videoPort,
	}
	req.timer = time.AfterFunc(videoUpgradeTimeout, func() {
		req.Reject()
	})
	return req
}

// Accept accepts the video upgrade, starts the video pipeline, and responds
// 200 OK with a video SDP answer to the remote party.
func (r *VideoUpgradeRequest) Accept() {
	r.responded.Do(func() {
		r.timer.Stop()
		r.call.acceptVideoUpgrade(r)
	})
}

// Reject rejects the video upgrade and responds 200 OK with m=video 0
// (RFC 3264 media rejection). Audio is unchanged.
func (r *VideoUpgradeRequest) Reject() {
	r.responded.Do(func() {
		r.timer.Stop()
		r.call.rejectVideoUpgrade(r)
	})
}

// RemoteCodec returns the video codec offered by the remote party.
func (r *VideoUpgradeRequest) RemoteCodec() VideoCodec {
	if vm := r.sess.VideoMedia(); vm != nil && len(vm.Codecs) > 0 {
		return VideoCodec(vm.Codecs[0])
	}
	return 0
}

// --- call methods for accept/reject ---

func (c *call) acceptVideoUpgrade(r *VideoUpgradeRequest) {
	c.mu.Lock()
	if c.state == StateEnded {
		c.mu.Unlock()
		if r.videoConn != nil {
			r.videoConn.Close()
		}
		return
	}

	// Install video socket.
	c.videoRTPConn = r.videoConn
	c.videoRTPPort = r.videoPort

	// Update remote SDP and negotiate video codec.
	c.remoteSDP = r.remoteSDP
	c.setRemoteEndpoint(r.sess)
	c.negotiateVideoCodec(r.sess)
	c.setVideoRemoteEndpoint(r.sess)

	// Populate video codec prefs so buildAnswerSDP includes video m= line.
	c.opts.VideoCodecs = []VideoCodec{c.videoCodecType}
	c.opts.Video = true

	// Initialize video pipeline channels.
	if c.videoRTPReader == nil {
		c.initVideoChannels()
	}

	// Build SDP answer with video.
	answerSDP := c.buildAnswerSDP(r.sess, sdp.DirSendRecv)
	c.localSDP = answerSDP

	// Capture SRTP state for setup outside lock.
	srtpLocalKey := c.srtpLocalKey
	isSRTP := srtpLocalKey != "" && r.sess.IsSRTP()
	var remoteKey string
	if isSRTP {
		if crypto := r.sess.FirstCrypto(); crypto != nil {
			remoteKey = crypto.InlineKey()
		}
	}

	videoFn := c.onVideoFn
	c.pendingVideoUpgrade = nil
	c.mu.Unlock()

	// Respond 200 OK with video SDP answer.
	if r.responder != nil {
		r.responder.Respond(200, "OK", []byte(answerSDP))
	}

	// Set up video SRTP contexts if SRTP is active.
	if isSRTP && remoteKey != "" {
		videoOutCtx, err := srtp.FromSDESInline(srtpLocalKey)
		if err != nil {
			c.logger.Error("video SRTP out context failed", "id", c.id, "error", err)
		} else {
			videoInCtx, err := srtp.FromSDESInline(remoteKey)
			if err != nil {
				c.logger.Error("video SRTP in context failed", "id", c.id, "error", err)
			} else {
				c.mu.Lock()
				c.videoSrtpOut = videoOutCtx
				c.videoSrtpIn = videoInCtx
				c.mu.Unlock()
			}
		}
	}

	// Start video pipeline.
	c.startVideoMedia()
	c.startVideoRTPReader()

	// Fire OnVideo callback.
	if videoFn != nil {
		c.dispatch(videoFn)
	}
	c.logger.Info("video upgrade accepted", "id", c.id)
}

func (c *call) rejectVideoUpgrade(r *VideoUpgradeRequest) {
	// Close pre-allocated video socket.
	if r.videoConn != nil {
		r.videoConn.Close()
	}

	c.mu.Lock()
	if c.state == StateEnded {
		c.mu.Unlock()
		return
	}

	// Update remote SDP for audio changes only (skip video negotiation).
	c.remoteSDP = r.remoteSDP
	c.setRemoteEndpoint(r.sess)
	c.negotiateAudioCodec(r.sess)

	// Build audio-only answer with m=video 0 (RFC 3264 rejection).
	answerSDP := c.buildAnswerRejectVideoSDP(r.sess)
	c.localSDP = answerSDP
	c.pendingVideoUpgrade = nil
	c.mu.Unlock()

	if r.responder != nil {
		r.responder.Respond(200, "OK", []byte(answerSDP))
	}
	c.logger.Info("video upgrade rejected", "id", c.id)
}

// handleReInvite processes an inbound re-INVITE for an existing call.
// It detects video upgrade, video downgrade, and hold/resume transitions.
func (c *call) handleReInvite(responder reInviteResponder, rawSDP string) {
	sess, err := sdp.Parse(rawSDP)
	if err != nil {
		c.logger.Warn("re-INVITE SDP parse failed", "id", c.id, "error", err)
		if responder != nil {
			responder.Respond(400, "Bad Request", nil)
		}
		return
	}

	c.mu.Lock()
	if c.state == StateEnded {
		c.mu.Unlock()
		return
	}

	vm := sess.VideoMedia()
	currentlyHasVideo := c.hasVideo

	// Video downgrade: remote sends m=video 0 (or no video) when we have video.
	if currentlyHasVideo && (vm == nil || vm.Port == 0 || len(vm.Codecs) == 0) {
		c.mu.Unlock()
		c.stopVideoPipeline()
		// Respond with audio-only answer.
		c.mu.Lock()
		c.remoteSDP = rawSDP
		c.setRemoteEndpoint(sess)
		c.negotiateAudioCodec(sess) // audio only — video is being removed
		dir := sess.Dir()
		if dir == "" {
			dir = sdp.DirSendRecv
		}
		answerSDP := c.buildAnswerSDP(sess, dir)
		c.localSDP = answerSDP
		c.mu.Unlock()
		if responder != nil {
			responder.Respond(200, "OK", []byte(answerSDP))
		}
		c.logger.Info("video downgrade processed", "id", c.id)
		return
	}

	// Video upgrade: remote adds m=video with codecs, we don't have video yet.
	if !currentlyHasVideo && vm != nil && vm.Port > 0 && len(vm.Codecs) > 0 {
		requestFn := c.onVideoRequestFn
		c.mu.Unlock()

		// Pre-allocate video socket outside the lock.
		videoConn, err := listenRTPPort(c.rtpPortMin, c.rtpPortMax)
		var videoPort int
		if err == nil {
			videoPort = videoConn.LocalAddr().(*net.UDPAddr).Port
		} else {
			c.logger.Error("video upgrade: failed to allocate video RTP port", "id", c.id, "error", err)
			if responder != nil {
				responder.Respond(500, "Internal Server Error", nil)
			}
			return
		}

		req := newVideoUpgradeRequest(c, responder, rawSDP, sess, videoConn, videoPort)
		c.mu.Lock()
		c.pendingVideoUpgrade = req
		c.mu.Unlock()

		if requestFn != nil {
			c.dispatch(func() { requestFn(req) })
		} else {
			// No handler — auto-reject (safe default, no video without consent).
			req.Reject()
		}
		return
	}

	// Audio-only re-INVITE (hold/resume/codec change).
	// Update remote SDP and handle direction changes.
	// ICE credentials are updated by buildAnswerSDP below.
	c.remoteSDP = rawSDP
	c.setRemoteEndpoint(sess)

	dir := sess.Dir()
	var holdFn, resumeFn func()
	switch {
	case dir == sdp.DirSendOnly && c.state == StateActive:
		c.state = StateOnHold
		holdFn = c.onHoldFn
		c.fireOnState(StateOnHold)
		c.signalMediaTimerReset(defaultHoldMediaTimeout)
	case dir == sdp.DirSendRecv && c.state == StateOnHold:
		c.state = StateActive
		resumeFn = c.onResumeFn
		c.fireOnState(StateActive)
		c.signalMediaTimerReset(c.effectiveMediaTimeout())
	}

	c.negotiateCodec(sess)

	// Build SDP answer preserving direction semantics.
	answerDir := sdp.DirSendRecv
	if dir == sdp.DirSendOnly {
		answerDir = sdp.DirRecvOnly
	}
	answerSDP := c.buildAnswerSDP(sess, answerDir)
	c.localSDP = answerSDP
	c.mu.Unlock()

	if responder != nil {
		responder.Respond(200, "OK", []byte(answerSDP))
	}

	if holdFn != nil {
		c.dispatch(holdFn)
	}
	if resumeFn != nil {
		c.dispatch(resumeFn)
	}
}

// stopVideoPipeline stops the video media pipeline and cleans up video state.
// Safe to call when video is not active (no-op).
func (c *call) stopVideoPipeline() {
	c.mu.Lock()
	// Signal video goroutine to stop.
	if c.videoDone != nil {
		select {
		case <-c.videoDone:
		default:
			close(c.videoDone)
		}
		c.videoDone = nil
	}
	c.mu.Unlock()

	// Wait for video goroutine to exit before modifying shared state.
	c.videoWg.Wait()

	c.mu.Lock()
	// Close video sockets.
	if c.videoRTCPConn != nil {
		c.videoRTCPConn.Close()
		c.videoRTCPConn = nil
	}
	if c.videoRTPConn != nil {
		c.videoRTPConn.Close()
		c.videoRTPConn = nil
	}
	// Zeroize video SRTP contexts.
	if c.videoSrtpIn != nil {
		c.videoSrtpIn.Zeroize()
		c.videoSrtpIn = nil
	}
	if c.videoSrtpOut != nil {
		c.videoSrtpOut.Zeroize()
		c.videoSrtpOut = nil
	}
	// Reset video state.
	c.hasVideo = false
	c.videoCodecType = 0
	c.videoRTPPort = 0
	c.videoRemoteAddr = nil
	c.videoStream = nil
	// Close video output channels if pipeline was running.
	if c.videoRTPReader != nil {
		c.closeVideoOutputChannels()
	}
	// Nil out channels to allow re-initialization on future upgrade.
	c.videoRTPInbound = nil
	c.videoRTPReader = nil
	c.videoRTPRawReader = nil
	c.videoRTPWriter = nil
	c.videoReader = nil
	c.videoWriter = nil
	c.videoSentRTP = nil
	c.mu.Unlock()
	c.logger.Debug("video pipeline stopped", "id", c.id)
}

// buildAnswerRejectVideoSDP builds an SDP answer that accepts audio but
// rejects video with m=video 0 RTP/AVP 0 (RFC 3264 §6).
// Delegates to buildAnswerSDP (audio-only since video opts are not set)
// and appends the rejection m= line.
func (c *call) buildAnswerRejectVideoSDP(remote *sdp.Session) string {
	return c.buildAnswerSDP(remote, sdp.DirSendRecv) + "m=video 0 RTP/AVP 0\r\n"
}

// AddVideo initiates an outbound video upgrade by sending a re-INVITE with
// audio + video SDP. The remote peer's response determines if video is activated.
func (c *call) AddVideo(codecs ...VideoCodec) error {
	if len(codecs) == 0 {
		codecs = []VideoCodec{VideoCodecH264, VideoCodecVP8}
	}

	c.mu.Lock()
	if c.state != StateActive {
		c.mu.Unlock()
		return ErrInvalidState
	}
	if c.hasVideo {
		c.mu.Unlock()
		return ErrVideoAlreadyActive
	}

	// Store video codec preferences and allocate video port.
	c.opts.VideoCodecs = codecs
	c.opts.Video = true
	c.ensureVideoRTPPort()

	// Build SDP offer with audio + video.
	sdpOffer := c.buildLocalSDP(sdp.DirSendRecv)
	c.localSDP = sdpOffer
	c.mu.Unlock()

	return c.dlg.SendReInvite([]byte(sdpOffer))
}

// OnVideoRequest registers a callback for inbound video upgrade requests.
// The callback receives a *VideoUpgradeRequest that the app must Accept() or Reject().
// If no callback is registered, video upgrades are automatically rejected.
func (c *call) OnVideoRequest(fn func(*VideoUpgradeRequest)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onVideoRequestFn = fn
}

// OnVideo registers a callback that fires when video becomes active on the call
// (after a video upgrade is accepted).
func (c *call) OnVideo(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onVideoFn = fn
}
