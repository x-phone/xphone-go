package xphone

import (
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/testutil"
)

// activeCall creates an inbound call in StateActive with media pipeline ready.
// sentRTP is initialized so tests can observe outbound packets.
func activeCall() *call {
	c := newInboundCall(testutil.NewMockDialog())
	c.sentRTP = make(chan *rtp.Packet, 256)
	c.Accept()
	c.startMedia()
	return c
}

// --- RTP pipeline tests ---

func TestMediaPipeline_RTPRawReaderPreJitter(t *testing.T) {
	c := activeCall()
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
	c := activeCall()
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
	c := activeCall()
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
	c := activeCall()
	defer c.stopMedia()

	// Write an RTP packet (takes priority over PCM).
	rtpPkt := &rtp.Packet{Header: rtp.Header{SequenceNumber: 1, PayloadType: 0}}
	select {
	case c.RTPWriter() <- rtpPkt:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RTPWriter blocked")
	}

	// Also write a PCM frame — should be dropped since RTPWriter is active.
	frame := make([]int16, 160)
	frame[0] = 9999
	select {
	case c.PCMWriter() <- frame:
	case <-time.After(100 * time.Millisecond):
	}

	// The pipeline should forward the RTP packet on sentRTP.
	sent := readPacket(t, c.sentRTP, 200*time.Millisecond)
	assert.Equal(t, uint16(1), sent.SequenceNumber, "RTP packet must be forwarded")

	// PCM must NOT produce a second outbound packet (mutex: RTPWriter wins).
	select {
	case extra := <-c.sentRTP:
		t.Fatalf("PCMWriter should be suppressed when RTPWriter is active, got seq %d", extra.SequenceNumber)
	case <-time.After(100 * time.Millisecond):
		// correct: no PCM-encoded packet appeared
	}
}

func TestMediaPipeline_RTPWriterPassthrough(t *testing.T) {
	c := activeCall()
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
	c := activeCall()
	defer c.stopMedia()

	// Write 300 distinguishable frames into PCMWriter (buffered 256).
	// Overflow policy: newest dropped, oldest kept.
	for i := 0; i < 300; i++ {
		frame := make([]int16, 160)
		frame[0] = int16(i) // sequence marker
		select {
		case c.PCMWriter() <- frame:
		default:
			// pipeline should handle overflow internally; if the raw
			// channel rejects, implementation is responsible for the
			// drop-newest policy via a goroutine consumer.
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
	c := activeCall()
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
	c.mediaTimeout = 50 * time.Millisecond // short timeout for test
	c.Accept()
	c.startMedia()
	defer c.stopMedia()

	ended := make(chan EndReason, 1)
	c.OnEnded(func(r EndReason) { ended <- r })

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
	c.mediaTimeout = 50 * time.Millisecond
	c.Accept()
	c.startMedia()
	defer c.stopMedia()

	ended := make(chan EndReason, 1)
	c.OnEnded(func(r EndReason) { ended <- r })

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
