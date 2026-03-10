package xphone

import (
	"net"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/testutil"
)

// activeVideoCall creates an inbound call in StateActive with both audio and
// video media pipelines ready. sentRTP and videoSentRTP are initialized so
// tests can observe outbound packets.
func activeVideoCall(t *testing.T) *call {
	t.Helper()
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.sentRTP = make(chan *rtp.Packet, 256)

	// Set up video state before Accept.
	c.hasVideo = true
	c.videoCodecType = VideoCodecH264
	c.initVideoChannels()
	c.videoSentRTP = make(chan *rtp.Packet, 256)

	// Allocate a real UDP socket for video.
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	c.videoRTPConn = conn
	c.videoRTPPort = conn.LocalAddr().(*net.UDPAddr).Port

	// Set a dummy remote address for video.
	c.videoRemoteAddr, _ = net.ResolveUDPAddr("udp", "127.0.0.1:19000")

	c.Accept()
	c.startMedia()
	c.startVideoMedia()
	return c
}

// --- Video negotiation tests ---

func TestCall_HasVideo_AudioOnly(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	assert.False(t, c.HasVideo(), "audio-only call should have HasVideo=false")
}

func TestCall_HasVideo_WithVideo(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.mu.Lock()
	c.hasVideo = true
	c.videoCodecType = VideoCodecH264
	c.mu.Unlock()
	assert.True(t, c.HasVideo())
	assert.Equal(t, VideoCodecH264, c.VideoCodec())
}

func TestCall_VideoChannelAccessors_Nil_AudioOnly(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	assert.Nil(t, c.VideoReader())
	assert.Nil(t, c.VideoWriter())
	assert.Nil(t, c.VideoRTPReader())
	assert.Nil(t, c.VideoRTPWriter())
}

func TestCall_VideoChannelAccessors_NotNil_WithVideo(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.mu.Lock()
	c.hasVideo = true
	c.initVideoChannels()
	c.mu.Unlock()
	assert.NotNil(t, c.VideoReader())
	assert.NotNil(t, c.VideoWriter())
	assert.NotNil(t, c.VideoRTPReader())
	assert.NotNil(t, c.VideoRTPWriter())
}

// --- MuteVideo / UnmuteVideo tests ---

func TestCall_MuteVideo_NoVideo(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.Accept()
	assert.Equal(t, ErrNoVideo, c.MuteVideo())
	assert.Equal(t, ErrNoVideo, c.UnmuteVideo())
}

func TestCall_MuteVideo_NotActive(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	// Still in StateRinging
	assert.Equal(t, ErrInvalidState, c.MuteVideo())
}

func TestCall_MuteVideo_MuteUnmute(t *testing.T) {
	c := activeVideoCall(t)
	defer c.stopMedia()
	defer c.videoRTPConn.Close()

	require.NoError(t, c.MuteVideo())

	// Second mute should fail.
	assert.Equal(t, ErrAlreadyMuted, c.MuteVideo())

	// Unmute.
	require.NoError(t, c.UnmuteVideo())

	// Second unmute should fail.
	assert.Equal(t, ErrNotMuted, c.UnmuteVideo())
}

func TestCall_MuteVideo_SuppressesOutboundVideo(t *testing.T) {
	c := activeVideoCall(t)
	defer c.stopMedia()
	defer c.videoRTPConn.Close()

	require.NoError(t, c.MuteVideo())

	// Write a video RTP packet — should be silently dropped (muted).
	pkt := &rtp.Packet{Header: rtp.Header{SequenceNumber: 1, PayloadType: 96}}
	select {
	case c.VideoRTPWriter() <- pkt:
	case <-time.After(100 * time.Millisecond):
	}

	// No packet should appear on videoSentRTP.
	time.Sleep(50 * time.Millisecond)
	pkts := drainPackets(c.videoSentRTP)
	assert.Empty(t, pkts, "VideoRTPWriter output must be suppressed while muted")
}

// --- Video pipeline tests ---

func TestVideoMedia_RTPPassthrough(t *testing.T) {
	c := activeVideoCall(t)
	defer c.stopMedia()
	defer c.videoRTPConn.Close()

	// Inject a video RTP packet into the pipeline.
	pkt := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 42, PayloadType: 96, Timestamp: 12345, SSRC: 0xABCD},
		Payload: []byte{0x00, 0x01, 0x02},
	}
	c.videoRTPInbound <- pkt

	// Should appear on both videoRTPRawReader and videoRTPReader.
	raw := readPacket(t, c.videoRTPRawReader, 200*time.Millisecond)
	reader := readPacket(t, c.videoRTPReader, 200*time.Millisecond)
	assert.Equal(t, uint16(42), raw.SequenceNumber)
	assert.Equal(t, uint16(42), reader.SequenceNumber)
}

func TestVideoMedia_OutboundRTPWriter(t *testing.T) {
	c := activeVideoCall(t)
	defer c.stopMedia()
	defer c.videoRTPConn.Close()

	pkt := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 99, PayloadType: 96, Timestamp: 90000},
		Payload: []byte{0xDE, 0xAD},
	}
	select {
	case c.VideoRTPWriter() <- pkt:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("VideoRTPWriter blocked")
	}

	sent := readPacket(t, c.videoSentRTP, 200*time.Millisecond)
	assert.Equal(t, uint16(99), sent.SequenceNumber)
	assert.Equal(t, uint8(96), sent.PayloadType)
	assert.Equal(t, []byte{0xDE, 0xAD}, sent.Payload)
}

// --- RequestKeyframe tests ---

func TestCall_RequestKeyframe_NoVideo(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.Accept()
	assert.Equal(t, ErrNoVideo, c.RequestKeyframe())
}

func TestCall_RequestKeyframe_NotActive(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	// Still in StateRinging
	assert.Equal(t, ErrInvalidState, c.RequestKeyframe())
}

func TestCall_RequestKeyframe_SendsPLI(t *testing.T) {
	// Build a call with video RTCP pre-configured before pipeline starts.
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.sentRTP = make(chan *rtp.Packet, 256)
	c.hasVideo = true
	c.videoCodecType = VideoCodecH264
	c.initVideoChannels()
	c.videoSentRTP = make(chan *rtp.Packet, 256)

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	c.videoRTPConn = conn
	c.videoRTPPort = conn.LocalAddr().(*net.UDPAddr).Port
	c.videoRemoteAddr, _ = net.ResolveUDPAddr("udp", "127.0.0.1:19000")

	// Pre-bind video RTCP socket so startVideoMedia picks it up.
	rtcpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	c.videoRTCPConn = rtcpConn

	c.Accept()
	c.startMedia()
	c.startVideoMedia()
	defer c.stopMedia()
	defer conn.Close()
	defer rtcpConn.Close()

	err = c.RequestKeyframe()
	require.NoError(t, err)
}

// --- Mute/Unmute also mutes video ---

func TestCall_Mute_AlsoMutesVideo(t *testing.T) {
	c := activeVideoCall(t)
	defer c.stopMedia()
	defer c.videoRTPConn.Close()

	require.NoError(t, c.Mute())

	c.mu.Lock()
	videoMuted := c.videoStream.muted
	c.mu.Unlock()
	assert.True(t, videoMuted, "Mute() should also mute the video stream")

	require.NoError(t, c.Unmute())

	c.mu.Lock()
	videoMuted = c.videoStream.muted
	c.mu.Unlock()
	assert.False(t, videoMuted, "Unmute() should also unmute the video stream")
}

// --- MockCall video tests ---

func TestMockCall_VideoMethods(t *testing.T) {
	c := NewMockCall()
	c.SetState(StateActive)

	// Without video enabled.
	assert.False(t, c.HasVideo())
	assert.Equal(t, ErrNoVideo, c.MuteVideo())
	assert.Equal(t, ErrNoVideo, c.UnmuteVideo())
	assert.Equal(t, ErrNoVideo, c.RequestKeyframe())

	// Enable video.
	c.SetHasVideo(true)
	c.SetVideoCodec(VideoCodecVP8)
	assert.True(t, c.HasVideo())
	assert.Equal(t, VideoCodecVP8, c.VideoCodec())

	// Mute/Unmute video.
	require.NoError(t, c.MuteVideo())
	assert.Equal(t, ErrAlreadyMuted, c.MuteVideo())
	require.NoError(t, c.UnmuteVideo())
	assert.Equal(t, ErrNotMuted, c.UnmuteVideo())

	// Channels should be non-nil.
	assert.NotNil(t, c.VideoReader())
	assert.NotNil(t, c.VideoWriter())
	assert.NotNil(t, c.VideoRTPReader())
	assert.NotNil(t, c.VideoRTPWriter())

	// RequestKeyframe with video.
	require.NoError(t, c.RequestKeyframe())
}

// --- Close output channels includes video ---

func TestCall_CloseVideoOutputChannels(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.mu.Lock()
	c.hasVideo = true
	c.initVideoChannels()
	c.mu.Unlock()

	c.closeVideoOutputChannels()

	// Verify video reader channels are closed.
	_, ok := <-c.videoRTPReader
	assert.False(t, ok, "videoRTPReader should be closed")
	_, ok = <-c.videoRTPRawReader
	assert.False(t, ok, "videoRTPRawReader should be closed")
	_, ok = <-c.videoReader
	assert.False(t, ok, "videoReader should be closed")

	// Verify audio channels are NOT closed (separate lifecycle).
	select {
	case c.rtpReader <- nil:
		// can still send — not closed
	default:
		// channel full, but not closed
	}
}
