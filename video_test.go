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

// activeVideoCallWithCodec creates an inbound call in StateActive with both audio and
// video media pipelines ready. sentRTP and videoSentRTP are initialized so
// tests can observe outbound packets.
func activeVideoCallWithCodec(t *testing.T, codec VideoCodec) *call {
	t.Helper()
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.sentRTP = make(chan *rtp.Packet, 256)

	// Set up video state before Accept.
	c.hasVideo = true
	c.videoCodecType = codec
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

func activeVideoCall(t *testing.T) *call {
	t.Helper()
	return activeVideoCallWithCodec(t, VideoCodecH264)
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

// --- Depacketizer integration tests ---

func TestVideoMedia_H264Depacketize(t *testing.T) {
	c := activeVideoCall(t)
	defer c.stopMedia()
	defer c.videoRTPConn.Close()

	// Inject an H.264 IDR NAL as a single RTP packet (marker set).
	nal := []byte{0x65, 0xAA, 0xBB, 0xCC} // NAL type 5 = IDR
	pkt := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, PayloadType: 96, Timestamp: 90000, SSRC: 0x1234, Marker: true},
		Payload: nal,
	}
	c.videoRTPInbound <- pkt

	// Should appear as an assembled VideoFrame on videoReader.
	select {
	case frame := <-c.VideoReader():
		assert.True(t, frame.IsKeyframe, "IDR should be detected as keyframe")
		assert.Equal(t, uint32(90000), frame.Timestamp)
		assert.Equal(t, VideoCodecH264, frame.Codec)
		// Frame data should contain Annex-B start code + NAL.
		assert.True(t, len(frame.Data) > 4, "frame should have start code + NAL")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("VideoReader did not receive assembled frame")
	}
}

func TestVideoMedia_H264FUADepacketize(t *testing.T) {
	c := activeVideoCall(t)
	defer c.stopMedia()
	defer c.videoRTPConn.Close()

	// Simulate FU-A fragmentation of an IDR NAL.
	nalHeader := byte(0x65) // NRI=3, type=5 (IDR)
	fuIndicator := (nalHeader & 0xE0) | 28

	// Fragment 1: S=1, E=0.
	frag1 := append([]byte{fuIndicator, 0x80 | 0x05}, make([]byte, 100)...)
	pkt1 := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, Timestamp: 90000, PayloadType: 96},
		Payload: frag1,
	}
	c.videoRTPInbound <- pkt1

	// Fragment 2: S=0, E=1, marker set.
	frag2 := append([]byte{fuIndicator, 0x40 | 0x05}, make([]byte, 50)...)
	pkt2 := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 2, Timestamp: 90000, PayloadType: 96, Marker: true},
		Payload: frag2,
	}
	c.videoRTPInbound <- pkt2

	// Should produce a single assembled keyframe.
	select {
	case frame := <-c.VideoReader():
		assert.True(t, frame.IsKeyframe)
		assert.Equal(t, uint32(90000), frame.Timestamp)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("VideoReader did not receive FU-A reassembled frame")
	}
}

func TestVideoMedia_OutboundVideoFrame(t *testing.T) {
	c := activeVideoCall(t)
	defer c.stopMedia()
	defer c.videoRTPConn.Close()

	// Build an Annex-B encoded frame (SPS + IDR).
	var frameData []byte
	frameData = append(frameData, 0x00, 0x00, 0x00, 0x01) // start code
	frameData = append(frameData, 0x67, 0x42, 0xE0, 0x1F) // SPS
	frameData = append(frameData, 0x00, 0x00, 0x00, 0x01) // start code
	frameData = append(frameData, 0x65)                   // IDR header
	frameData = append(frameData, make([]byte, 50)...)    // IDR payload

	frame := VideoFrame{
		Codec:      VideoCodecH264,
		Timestamp:  180000,
		IsKeyframe: true,
		Data:       frameData,
	}

	// Send via VideoWriter.
	select {
	case c.VideoWriter() <- frame:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("VideoWriter blocked")
	}

	// Should produce RTP packets on videoSentRTP.
	time.Sleep(50 * time.Millisecond)
	pkts := drainPackets(c.videoSentRTP)
	require.True(t, len(pkts) >= 2, "expected at least 2 RTP packets (SPS + IDR), got %d", len(pkts))

	// Last packet should have marker bit set.
	assert.True(t, pkts[len(pkts)-1].Marker, "last packet should have marker bit")
	// All packets should have the correct timestamp.
	for _, p := range pkts {
		assert.Equal(t, uint32(180000), p.Timestamp)
		assert.Equal(t, uint8(96), p.PayloadType)
	}
}

// --- VP8 pipeline tests ---

func activeVP8VideoCall(t *testing.T) *call {
	t.Helper()
	return activeVideoCallWithCodec(t, VideoCodecVP8)
}

func TestVideoMedia_VP8Depacketize(t *testing.T) {
	c := activeVP8VideoCall(t)
	defer c.stopMedia()
	defer c.videoRTPConn.Close()

	// VP8 keyframe: descriptor S=1 (0x10), frame byte bit0=0 (keyframe).
	pkt := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, PayloadType: 97, Timestamp: 90000, Marker: true},
		Payload: []byte{0x10, 0x10, 0x00, 0x00, 0xAA, 0xBB}, // S=1, keyframe
	}
	c.videoRTPInbound <- pkt

	select {
	case frame := <-c.VideoReader():
		assert.True(t, frame.IsKeyframe)
		assert.Equal(t, VideoCodecVP8, frame.Codec)
		assert.Equal(t, uint32(90000), frame.Timestamp)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("VP8 VideoReader did not receive frame")
	}
}

func TestVideoMedia_VP8OutboundFrame(t *testing.T) {
	c := activeVP8VideoCall(t)
	defer c.stopMedia()
	defer c.videoRTPConn.Close()

	frameData := make([]byte, 200)
	frameData[0] = 0x10 // keyframe (bit 0 = 0)
	for i := 1; i < len(frameData); i++ {
		frameData[i] = byte(i)
	}

	frame := VideoFrame{
		Codec:      VideoCodecVP8,
		Timestamp:  270000,
		IsKeyframe: true,
		Data:       frameData,
	}

	select {
	case c.VideoWriter() <- frame:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("VP8 VideoWriter blocked")
	}

	time.Sleep(50 * time.Millisecond)
	pkts := drainPackets(c.videoSentRTP)
	require.True(t, len(pkts) >= 1, "expected at least 1 RTP packet")
	assert.True(t, pkts[len(pkts)-1].Marker, "last packet should have marker bit")
	for _, p := range pkts {
		assert.Equal(t, uint32(270000), p.Timestamp)
		assert.Equal(t, uint8(97), p.PayloadType)
	}
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
