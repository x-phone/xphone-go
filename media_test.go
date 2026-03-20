package xphone

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/internal/media"
	"github.com/x-phone/xphone-go/internal/rtcp"
	"github.com/x-phone/xphone-go/testutil"
)

// activeCall creates an inbound call in StateActive with media pipeline ready.
// sentRTP is initialized so tests can observe outbound packets.
func activeCall(t *testing.T) *call {
	t.Helper()
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.sentRTP = make(chan *rtp.Packet, 256)
	c.Accept()
	c.startMedia()
	return c
}

// activeCallWithCodec creates an active call configured for a specific codec.
func activeCallWithCodec(t *testing.T, codec Codec) *call {
	t.Helper()
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.sentRTP = make(chan *rtp.Packet, 256)
	c.codec = codec
	c.Accept()
	c.startMedia()
	return c
}

// --- RTP pipeline tests ---

func TestMediaPipeline_RTPRawReaderPreJitter(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// Inject packets in wire order: seq 1, 3, 2.
	c.injectRTP(&rtp.Packet{Header: rtp.Header{SequenceNumber: 1}})
	c.injectRTP(&rtp.Packet{Header: rtp.Header{SequenceNumber: 3}})
	c.injectRTP(&rtp.Packet{Header: rtp.Header{SequenceNumber: 2}})

	// RTPRawReader must deliver in wire order (pre-jitter), not reordered.
	ch := c.RTPRawReader()
	p1 := readPacket(t, ch, 100*time.Millisecond)
	p2 := readPacket(t, ch, 100*time.Millisecond)
	p3 := readPacket(t, ch, 100*time.Millisecond)

	assert.Equal(t, uint16(1), p1.SequenceNumber)
	assert.Equal(t, uint16(3), p2.SequenceNumber)
	assert.Equal(t, uint16(2), p3.SequenceNumber)
}

func TestMediaPipeline_RTPReaderPostJitter(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// Inject out-of-order packets.
	c.injectRTP(&rtp.Packet{Header: rtp.Header{SequenceNumber: 3}})
	c.injectRTP(&rtp.Packet{Header: rtp.Header{SequenceNumber: 1}})
	c.injectRTP(&rtp.Packet{Header: rtp.Header{SequenceNumber: 2}})

	// RTPReader should deliver reordered (post-jitter).
	ch := c.RTPReader()
	p1 := readPacket(t, ch, 200*time.Millisecond)
	p2 := readPacket(t, ch, 200*time.Millisecond)
	p3 := readPacket(t, ch, 200*time.Millisecond)

	assert.Equal(t, uint16(1), p1.SequenceNumber)
	assert.Equal(t, uint16(2), p2.SequenceNumber)
	assert.Equal(t, uint16(3), p3.SequenceNumber)
}

func TestMediaPipeline_TapIndependence(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// Inject a single packet (PCMU payload so PCMReader also gets decoded audio).
	c.injectRTP(&rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 42, PayloadType: 0},
		Payload: make([]byte, 160), // 160 bytes PCMU = 160 samples
	})

	// All three reader taps should independently receive data.
	raw := readPacket(t, c.RTPRawReader(), 200*time.Millisecond)
	ordered := readPacket(t, c.RTPReader(), 200*time.Millisecond)

	require.NotNil(t, raw)
	require.NotNil(t, ordered)
	assert.Equal(t, uint16(42), raw.SequenceNumber)
	assert.Equal(t, uint16(42), ordered.SequenceNumber)

	// Third tap: PCMReader should receive decoded audio.
	select {
	case pcm := <-c.PCMReader():
		require.NotEmpty(t, pcm, "PCMReader should receive decoded samples")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("PCMReader never received decoded audio")
	}

	// Verify taps are independent: mutating one must not affect another.
	raw.SequenceNumber = 999
	assert.Equal(t, uint16(42), ordered.SequenceNumber, "taps must be independent")
}

func TestMediaPipeline_OutboundMutex(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// Write an RTP packet and wait for it to be forwarded, ensuring
	// rtpWriterUsed is set before we send the PCM frame.
	rtpPkt := &rtp.Packet{Header: rtp.Header{SequenceNumber: 1, PayloadType: 0}}
	select {
	case c.RTPWriter() <- rtpPkt:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RTPWriter blocked")
	}

	sent := readPacket(t, c.sentRTP, 200*time.Millisecond)
	assert.Equal(t, uint16(1), sent.SequenceNumber, "RTP packet must be forwarded")

	// Now write a PCM frame — should be dropped since RTPWriter was used.
	frame := make([]int16, 160)
	frame[0] = 9999
	select {
	case c.PCMWriter() <- frame:
	case <-time.After(100 * time.Millisecond):
	}

	// PCM must NOT produce an outbound packet (mutex: RTPWriter wins).
	select {
	case extra := <-c.sentRTP:
		t.Fatalf("PCMWriter should be suppressed when RTPWriter is active, got seq %d", extra.SequenceNumber)
	case <-time.After(100 * time.Millisecond):
		// correct: no PCM-encoded packet appeared
	}
}

func TestMediaPipeline_RTPWriterPassthrough(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// Write a packet with intentionally wrong timestamps — library must
	// send it as-is without validation or modification.
	pkt := &rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: 999,
			Timestamp:      12345, // arbitrary
			PayloadType:    111,   // not matching call codec
		},
		Payload: []byte{0xDE, 0xAD},
	}

	select {
	case c.RTPWriter() <- pkt:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RTPWriter blocked")
	}

	// Read what the pipeline actually sent and verify it matches exactly.
	sent := readPacket(t, c.sentRTP, 200*time.Millisecond)
	assert.Equal(t, uint16(999), sent.SequenceNumber)
	assert.Equal(t, uint32(12345), sent.Timestamp)
	assert.Equal(t, uint8(111), sent.PayloadType)
	assert.Equal(t, []byte{0xDE, 0xAD}, sent.Payload)
}

func TestMediaPipeline_PCMWriterOverflow(t *testing.T) {
	// Test raw channel overflow behavior without the media goroutine racing.
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	// Don't start media — test channel overflow in isolation.

	// Write 300 distinguishable frames into PCMWriter (buffered 256).
	// Overflow policy: newest dropped, oldest kept.
	for i := 0; i < 300; i++ {
		frame := make([]int16, 160)
		frame[0] = int16(i) // sequence marker
		select {
		case c.PCMWriter() <- frame:
		default:
			// channel full — newest frame dropped (expected)
		}
	}

	// Drain the internal pcmWriter channel and collect sequence markers.
	var seqs []int16
	draining := true
	for draining {
		select {
		case f := <-c.pcmWriter:
			seqs = append(seqs, f[0])
		default:
			draining = false
		}
	}
	require.NotEmpty(t, seqs, "should have received some frames")
	// With drop-newest policy, the oldest frames (0..255) must survive.
	assert.Equal(t, int16(0), seqs[0], "oldest frame must be preserved")
	assert.LessOrEqual(t, seqs[len(seqs)-1], int16(255),
		"newest frames beyond buffer capacity must be dropped")
}

func TestMediaPipeline_ChannelOverflow(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// Saturate RTPRawReader channel (buffered 256).
	// Overflow policy: drop oldest to make room for new.
	for i := 0; i < 300; i++ {
		c.injectRTP(&rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(i)}})
	}

	// Brief settle time for pipeline goroutines to finish processing.
	time.Sleep(50 * time.Millisecond)

	// Drain and verify the newest packets survived.
	pkts := drainPackets(c.RTPRawReader())
	require.NotEmpty(t, pkts, "should have received some packets")

	var seqs []uint16
	for _, pkt := range pkts {
		seqs = append(seqs, pkt.SequenceNumber)
	}

	// The last packet in the channel should be seq 299 (newest).
	assert.Equal(t, uint16(299), seqs[len(seqs)-1], "newest packet must survive overflow")
	// Oldest surviving packet should be > 0 (some were dropped).
	assert.Greater(t, seqs[0], uint16(0), "oldest packets should have been dropped")
}

func TestMediaPipeline_MediaTimeout(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.mediaTimeout = 50 * time.Millisecond // short timeout for test
	ended := make(chan EndReason, 1)
	c.OnEnded(func(r EndReason) { ended <- r })
	c.Accept()
	c.startMedia()
	defer c.stopMedia()

	// Don't send any RTP — timeout should fire.
	select {
	case reason := <-ended:
		assert.Equal(t, EndedByTimeout, reason)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("media timeout never fired")
	}
}

func TestMediaPipeline_MediaTimeoutSuspendedOnHold(t *testing.T) {
	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.mediaTimeout = 50 * time.Millisecond
	ended := make(chan EndReason, 1)
	c.OnEnded(func(r EndReason) { ended <- r })
	c.Accept()
	c.startMedia()
	defer c.stopMedia()

	// Put call on hold — media timeout must be suspended.
	c.Hold()

	// Wait longer than the timeout — should NOT fire while on hold.
	time.Sleep(100 * time.Millisecond)

	select {
	case reason := <-ended:
		t.Fatalf("media timeout must not fire while on hold, got %v", reason)
	default:
		// correct: no timeout while on hold
	}

	// Resume — timeout should restart. With no RTP, it should fire.
	c.Resume()
	select {
	case reason := <-ended:
		assert.Equal(t, EndedByTimeout, reason)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("media timeout should fire after resume with no RTP")
	}
}

// --- Codec dispatch integration tests ---

func TestMediaPipeline_CodecDispatch_PCMU(t *testing.T) {
	c := activeCall(t) // default codec is PCMU
	defer c.stopMedia()

	// Inject mu-law payload (0xFF = silence).
	payload := make([]byte, 160)
	for i := range payload {
		payload[i] = 0xFF
	}
	c.injectRTP(&rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, PayloadType: 0},
		Payload: payload,
	})

	// PCMReader should get real decoded PCM (all ~0 for silence).
	select {
	case pcm := <-c.PCMReader():
		require.Len(t, pcm, 160)
		// mu-law 0xFF decodes to 0
		for i, s := range pcm {
			assert.Equal(t, int16(0), s, "sample %d: expected 0 for mu-law silence", i)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("PCMReader never received decoded audio")
	}
}

func TestMediaPipeline_CodecDispatch_PCMA(t *testing.T) {
	c := activeCallWithCodec(t, CodecPCMA)
	defer c.stopMedia()

	// Inject A-law payload (0xD5 = A-law silence).
	payload := make([]byte, 160)
	for i := range payload {
		payload[i] = 0xD5
	}
	c.injectRTP(&rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, PayloadType: 8},
		Payload: payload,
	})

	// PCMReader should get decoded PCM near zero.
	select {
	case pcm := <-c.PCMReader():
		require.Len(t, pcm, 160)
		for i, s := range pcm {
			// A-law silence decodes to 8 (min positive magnitude).
			assert.InDelta(t, 0, int(s), 8,
				"sample %d: expected near-zero for A-law silence, got %d", i, s)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("PCMReader never received decoded audio")
	}
}

func TestMediaPipeline_PCMWriterEncode(t *testing.T) {
	c := activeCall(t) // PCMU
	defer c.stopMedia()

	// Write a PCM frame → should appear on sentRTP as mu-law encoded.
	frame := make([]int16, 160)
	select {
	case c.PCMWriter() <- frame:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("PCMWriter blocked")
	}

	sent := readPacket(t, c.sentRTP, 200*time.Millisecond)
	assert.Equal(t, uint8(0), sent.PayloadType, "should be PCMU PT=0")
	assert.Len(t, sent.Payload, 160, "mu-law frame should be 160 bytes")
	assert.Equal(t, uint8(2), sent.Version)

	// Verify silence: all samples were 0, should encode to 0xFF.
	cp := media.NewCodecProcessor(0, 8000)
	for i, b := range sent.Payload {
		decoded := cp.Decode([]byte{b})
		assert.InDelta(t, 0, int(decoded[0]), 8,
			"byte %d: encoded silence should decode near zero", i)
	}
}

func TestMediaPipeline_PCMWriterSeqAndTimestamp(t *testing.T) {
	c := activeCall(t) // PCMU
	defer c.stopMedia()

	// Write two frames.
	for i := 0; i < 2; i++ {
		frame := make([]int16, 160)
		select {
		case c.PCMWriter() <- frame:
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("PCMWriter blocked on frame %d", i)
		}
	}

	p0 := readPacket(t, c.sentRTP, 200*time.Millisecond)
	p1 := readPacket(t, c.sentRTP, 200*time.Millisecond)

	// Sequence numbers: 0, 1
	assert.Equal(t, uint16(0), p0.SequenceNumber)
	assert.Equal(t, uint16(1), p1.SequenceNumber)

	// Timestamps: 0, 160
	assert.Equal(t, uint32(0), p0.Timestamp)
	assert.Equal(t, uint32(160), p1.Timestamp)

	// Same SSRC
	assert.Equal(t, p0.SSRC, p1.SSRC, "both packets must share the same SSRC")
	assert.NotEqual(t, uint32(0), p0.SSRC, "SSRC should be non-zero (random)")
}

func TestMediaPipeline_PCMWriterPayloadType_PCMA(t *testing.T) {
	c := activeCallWithCodec(t, CodecPCMA)
	defer c.stopMedia()

	frame := make([]int16, 160)
	select {
	case c.PCMWriter() <- frame:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("PCMWriter blocked")
	}

	sent := readPacket(t, c.sentRTP, 200*time.Millisecond)
	assert.Equal(t, uint8(8), sent.PayloadType, "should be PCMA PT=8")
	assert.Len(t, sent.Payload, 160, "A-law frame should be 160 bytes")
}

// readPacket reads a single packet from a channel with a timeout.
func readPacket(t *testing.T, ch <-chan *rtp.Packet, timeout time.Duration) *rtp.Packet {
	t.Helper()
	select {
	case pkt := <-ch:
		return pkt
	case <-time.After(timeout):
		t.Fatal("timed out waiting for RTP packet")
		return nil
	}
}

// drainPackets reads all immediately available packets from a channel.
func drainPackets(ch <-chan *rtp.Packet) []*rtp.Packet {
	var pkts []*rtp.Packet
	for {
		select {
		case pkt := <-ch:
			pkts = append(pkts, pkt)
		default:
			return pkts
		}
	}
}

// --- Mute / Unmute media tests ---

func TestCall_Mute_SuppressesOutboundPCM(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	require.NoError(t, c.Mute())

	// Write a PCM frame — should be silently dropped (muted).
	frame := make([]int16, 160)
	frame[0] = 9999
	select {
	case c.PCMWriter() <- frame:
	case <-time.After(100 * time.Millisecond):
	}

	// No packet should appear on sentRTP.
	time.Sleep(50 * time.Millisecond)
	pkts := drainPackets(c.sentRTP)
	assert.Empty(t, pkts, "PCMWriter output must be suppressed while muted")
}

func TestCall_Mute_SuppressesOutboundRTPWriter(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	require.NoError(t, c.Mute())

	pkt := &rtp.Packet{Header: rtp.Header{SequenceNumber: 42, PayloadType: 0}}
	select {
	case c.RTPWriter() <- pkt:
	case <-time.After(100 * time.Millisecond):
	}

	time.Sleep(50 * time.Millisecond)
	pkts := drainPackets(c.sentRTP)
	assert.Empty(t, pkts, "RTPWriter output must be suppressed while muted")
}

func TestCall_Unmute_RestoresOutboundPCM(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	require.NoError(t, c.Mute())
	require.NoError(t, c.Unmute())

	// PCM frame should now produce outbound RTP again.
	frame := make([]int16, 160)
	select {
	case c.PCMWriter() <- frame:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("PCMWriter blocked")
	}

	sent := readPacket(t, c.sentRTP, 200*time.Millisecond)
	assert.NotNil(t, sent, "PCMWriter should produce packets after Unmute")
}

func TestCall_Unmute_RestoresOutboundRTPWriter(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	require.NoError(t, c.Mute())
	require.NoError(t, c.Unmute())

	pkt := &rtp.Packet{Header: rtp.Header{SequenceNumber: 77, PayloadType: 0}}
	select {
	case c.RTPWriter() <- pkt:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RTPWriter blocked")
	}

	sent := readPacket(t, c.sentRTP, 200*time.Millisecond)
	assert.Equal(t, uint16(77), sent.SequenceNumber, "RTPWriter should forward packets after Unmute")
}

func TestCall_Mute_InboundStillFlows(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	require.NoError(t, c.Mute())

	// Inject inbound RTP — should still arrive on readers.
	c.injectRTP(&rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, PayloadType: 0},
		Payload: make([]byte, 160),
	})

	raw := readPacket(t, c.RTPRawReader(), 200*time.Millisecond)
	assert.NotNil(t, raw, "inbound RTP must still flow while muted")
}

// --- MuteAudio / UnmuteAudio tests ---

func TestCall_MuteAudio_EquivalentToMute(t *testing.T) {
	// MuteAudio + Unmute should work (they share the same flag).
	c := activeCall(t)
	defer c.stopMedia()

	require.NoError(t, c.MuteAudio())
	require.NoError(t, c.Unmute())

	// And vice versa.
	require.NoError(t, c.Mute())
	require.NoError(t, c.UnmuteAudio())
}

func TestCall_MuteAudio_AlreadyMutedReturnsError(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	require.NoError(t, c.MuteAudio())
	assert.Equal(t, ErrAlreadyMuted, c.MuteAudio())
}

func TestCall_UnmuteAudio_WhenNotMutedReturnsError(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	assert.Equal(t, ErrNotMuted, c.UnmuteAudio())
}

// --- PacedPCMWriter tests ---

func TestPacedPCMWriter_SingleFrame(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// Send exactly one frame (160 samples) via PacedPCMWriter.
	frame := make([]int16, 160)
	select {
	case c.PacedPCMWriter() <- frame:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("PacedPCMWriter blocked")
	}

	p := readPacket(t, c.sentRTP, 500*time.Millisecond)
	assert.Equal(t, uint8(0), p.PayloadType, "should be PCMU PT=0")
	assert.Len(t, p.Payload, 160)
}

func TestPacedPCMWriter_BurstSplitting(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// Send 800 samples (5 frames × 160) in a single burst.
	buf := make([]int16, 800)
	select {
	case c.PacedPCMWriter() <- buf:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("PacedPCMWriter blocked")
	}

	// Should produce 5 paced packets.
	var arrivals []time.Time
	for i := 0; i < 5; i++ {
		select {
		case <-c.sentRTP:
			arrivals = append(arrivals, time.Now())
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timed out waiting for paced packet %d (got %d)", i, len(arrivals))
		}
	}

	// Verify pacing: 5 packets should span at least 50ms (4 gaps × ~20ms).
	totalElapsed := arrivals[len(arrivals)-1].Sub(arrivals[0])
	assert.GreaterOrEqual(t, totalElapsed.Milliseconds(), int64(50),
		"5 paced packets should take at least 50ms total, got %v", totalElapsed)
}

func TestPacedPCMWriter_PartialFrame(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// Send 100 samples (less than one frame). No packet yet.
	buf1 := make([]int16, 100)
	select {
	case c.PacedPCMWriter() <- buf1:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("PacedPCMWriter blocked")
	}

	// Brief pause — should NOT have produced a packet.
	time.Sleep(50 * time.Millisecond)
	pkts := drainPackets(c.sentRTP)
	assert.Empty(t, pkts, "partial frame should not produce a packet yet")

	// Send 60 more samples (total 160 = one frame).
	buf2 := make([]int16, 60)
	select {
	case c.PacedPCMWriter() <- buf2:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("PacedPCMWriter blocked on second write")
	}

	// Now one packet should arrive.
	p := readPacket(t, c.sentRTP, 500*time.Millisecond)
	assert.NotNil(t, p)
	assert.Len(t, p.Payload, 160)
}

func TestPacedPCMWriter_SequenceAndTimestamp(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// Send 480 samples (3 frames) in one burst.
	buf := make([]int16, 480)
	select {
	case c.PacedPCMWriter() <- buf:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("PacedPCMWriter blocked")
	}

	p0 := readPacket(t, c.sentRTP, 500*time.Millisecond)
	p1 := readPacket(t, c.sentRTP, 500*time.Millisecond)
	p2 := readPacket(t, c.sentRTP, 500*time.Millisecond)

	// Sequence numbers must be consecutive.
	assert.Equal(t, p0.SequenceNumber+1, p1.SequenceNumber)
	assert.Equal(t, p1.SequenceNumber+1, p2.SequenceNumber)

	// Timestamps must increment by 160 (PCMU samples per frame).
	assert.Equal(t, p0.Timestamp+160, p1.Timestamp)
	assert.Equal(t, p1.Timestamp+160, p2.Timestamp)
}

func TestPacedPCMWriter_MuteSuppresses(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	require.NoError(t, c.Mute())

	buf := make([]int16, 320) // 2 frames
	select {
	case c.PacedPCMWriter() <- buf:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("PacedPCMWriter blocked")
	}

	// Wait for pacer ticks.
	time.Sleep(80 * time.Millisecond)
	pkts := drainPackets(c.sentRTP)
	assert.Empty(t, pkts, "paced packets must be suppressed while muted")
}

func TestPacedPCMWriter_QueueOverflow(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// Send a massive buffer that exceeds maxPacerQueue (1500 frames × 160 = 240000 samples).
	// Add extra to trigger overflow: 1600 frames = 256000 samples.
	buf := make([]int16, 256000)
	select {
	case c.PacedPCMWriter() <- buf:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PacedPCMWriter blocked")
	}

	// Pipeline should still work — read a few paced packets.
	p := readPacket(t, c.sentRTP, 500*time.Millisecond)
	assert.NotNil(t, p, "should still receive paced packets after overflow")
}

// --- IPv4 socket and WriteTo error logging tests ---

func TestListenRTPPort_BindsIPv4(t *testing.T) {
	conn, err := listenRTPPort(0, 0)
	require.NoError(t, err)
	defer conn.Close()

	addr := conn.LocalAddr().String()
	// Must be 0.0.0.0:<port>, not [::]:port.
	assert.Contains(t, addr, "0.0.0.0:", "RTP socket must bind IPv4, got %s", addr)
}

func TestListenRTPPort_Range_BindsIPv4(t *testing.T) {
	conn, err := listenRTPPort(30000, 30010)
	require.NoError(t, err)
	defer conn.Close()

	addr := conn.LocalAddr().String()
	assert.Contains(t, addr, "0.0.0.0:", "RTP socket must bind IPv4, got %s", addr)
}

func TestSendRTP_WriteTo_ErrorLoggedOnce(t *testing.T) {
	logger, buf := newTestLogger()

	c := newInboundCall(testutil.NewMockDialog())
	t.Cleanup(c.cleanup)
	c.logger = logger

	s := c.audioStream
	stats := &rtcp.Stats{}
	// Closed conn will return an error on WriteTo.
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	conn.Close() // close immediately so WriteTo fails

	dst, _ := net.ResolveUDPAddr("udp4", "127.0.0.1:9999")
	pkt := &rtp.Packet{Header: rtp.Header{PayloadType: 0, SequenceNumber: 1}, Payload: []byte{0x80}}

	// Send 3 times — should log only once.
	s.sendRTP(pkt, stats, nil, conn, dst, nil)
	s.sendRTP(pkt, stats, nil, conn, dst, nil)
	s.sendRTP(pkt, stats, nil, conn, dst, nil)

	assert.True(t, s.sendErrLogged, "sendErrLogged must be set after first failure")
	logged := buf.String()
	assert.Contains(t, logged, "RTP WriteTo failed")
	// Count occurrences — should be exactly 1.
	count := 0
	for i := 0; i < len(logged); i++ {
		idx := bytes.Index([]byte(logged[i:]), []byte("RTP WriteTo failed"))
		if idx < 0 {
			break
		}
		count++
		i += idx + len("RTP WriteTo failed")
	}
	assert.Equal(t, 1, count, "RTP WriteTo error should be logged exactly once")
}

func TestPacedPCMWriter_RTPWriterSuppresses(t *testing.T) {
	c := activeCall(t)
	defer c.stopMedia()

	// Set rtpWriterUsed by writing an RTP packet first.
	rtpPkt := &rtp.Packet{Header: rtp.Header{SequenceNumber: 1, PayloadType: 0}}
	select {
	case c.RTPWriter() <- rtpPkt:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RTPWriter blocked")
	}
	sent := readPacket(t, c.sentRTP, 200*time.Millisecond)
	assert.Equal(t, uint16(1), sent.SequenceNumber)

	// Now write via paced channel — should be silently ignored.
	buf := make([]int16, 320)
	select {
	case c.PacedPCMWriter() <- buf:
	case <-time.After(100 * time.Millisecond):
	}

	time.Sleep(80 * time.Millisecond)
	pkts := drainPackets(c.sentRTP)
	assert.Empty(t, pkts, "paced PCM must be suppressed when RTPWriter is active")
}
