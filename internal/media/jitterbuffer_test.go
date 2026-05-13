package media

import (
	"math/rand"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJitterBuffer_InOrder(t *testing.T) {
	jb := NewJitterBuffer(50 * time.Millisecond)

	for seq := uint16(1); seq <= 3; seq++ {
		jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: seq}})
	}

	pkts := jb.Flush()
	require.Len(t, pkts, 3)
	for i, pkt := range pkts {
		assert.Equal(t, uint16(i+1), pkt.SequenceNumber)
	}
}

func TestJitterBuffer_Reorder(t *testing.T) {
	jb := NewJitterBuffer(50 * time.Millisecond)

	// Push out of order: 3, 1, 2.
	jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 3}})
	jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 1}})
	jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 2}})

	pkts := jb.Flush()
	require.Len(t, pkts, 3)
	assert.Equal(t, uint16(1), pkts[0].SequenceNumber)
	assert.Equal(t, uint16(2), pkts[1].SequenceNumber)
	assert.Equal(t, uint16(3), pkts[2].SequenceNumber)
}

func TestJitterBuffer_Dedup(t *testing.T) {
	jb := NewJitterBuffer(50 * time.Millisecond)

	jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 1}, Payload: []byte{0xAA}})
	jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 1}, Payload: []byte{0xBB}}) // duplicate
	jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 2}, Payload: []byte{0xCC}})

	pkts := jb.Flush()
	require.Len(t, pkts, 2, "duplicate seq 1 should be suppressed")
	assert.Equal(t, uint16(1), pkts[0].SequenceNumber)
	assert.Equal(t, []byte{0xAA}, pkts[0].Payload, "first copy must be preserved")
	assert.Equal(t, uint16(2), pkts[1].SequenceNumber)
}

func TestJitterBuffer_ConfigurableDepth(t *testing.T) {
	// With a very short depth (10ms), Pop should release packets quickly —
	// even a slightly out-of-order packet may not be reorderable.
	short := NewJitterBuffer(10 * time.Millisecond)
	short.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 2}})
	// With only 10ms depth, the buffer should not hold seq 2 for long
	// while waiting for seq 1. After depth elapses, Pop should yield it.
	time.Sleep(15 * time.Millisecond)
	pkt := short.Pop()
	require.NotNil(t, pkt, "short depth should release packet after delay")
	assert.Equal(t, uint16(2), pkt.SequenceNumber)

	// With a longer depth (200ms), the buffer should hold packets longer,
	// giving more time for reordering.
	long := NewJitterBuffer(200 * time.Millisecond)
	long.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 3}})
	// Immediately after push, Pop should return nil (within depth window).
	pkt = long.Pop()
	assert.Nil(t, pkt, "long depth should hold packet within depth window")
	// After depth elapses, it should be available.
	time.Sleep(210 * time.Millisecond)
	pkt = long.Pop()
	require.NotNil(t, pkt, "long depth should release packet after delay")
	assert.Equal(t, uint16(3), pkt.SequenceNumber)
}

func TestJitterBuffer_SequenceWrapAround(t *testing.T) {
	jb := NewJitterBuffer(50 * time.Millisecond)

	// Simulate 16-bit sequence wrap: 65534, 65535, 0, 1 (out of order).
	jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 0}})
	jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 65535}})
	jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 65534}})
	jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 1}})

	pkts := jb.Flush()
	require.Len(t, pkts, 4)
	assert.Equal(t, uint16(65534), pkts[0].SequenceNumber)
	assert.Equal(t, uint16(65535), pkts[1].SequenceNumber)
	assert.Equal(t, uint16(0), pkts[2].SequenceNumber)
	assert.Equal(t, uint16(1), pkts[3].SequenceNumber)
}

func TestJitterBuffer_Empty(t *testing.T) {
	jb := NewJitterBuffer(50 * time.Millisecond)

	// Flush with nothing pushed.
	pkts := jb.Flush()
	assert.Empty(t, pkts)

	// Pop with nothing pushed.
	pkt := jb.Pop()
	assert.Nil(t, pkt)
}

// TestJitterBuffer_PopOrderAfterShuffledPush verifies that Pop returns
// packets in sequence order regardless of arrival order — i.e., that
// Push maintains the sorted invariant rather than relying on a Pop-time
// sort.
func TestJitterBuffer_PopOrderAfterShuffledPush(t *testing.T) {
	// 50ms depth + 100ms sleep gives a 50ms margin so the test stays
	// reliable on loaded CI runners.
	jb := NewJitterBuffer(50 * time.Millisecond)

	rng := rand.New(rand.NewSource(42))
	const n = 64
	seqs := make([]uint16, n)
	for i := range seqs {
		seqs[i] = uint16(1000 + i)
	}
	rng.Shuffle(n, func(i, j int) { seqs[i], seqs[j] = seqs[j], seqs[i] })

	for _, s := range seqs {
		jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: s}})
	}

	time.Sleep(100 * time.Millisecond)
	for i := uint16(0); i < n; i++ {
		pkt := jb.Pop()
		require.NotNil(t, pkt, "expected packet at index %d", i)
		assert.Equal(t, 1000+i, pkt.SequenceNumber)
	}
	assert.Nil(t, jb.Pop(), "buffer should be empty after draining")
}

// TestJitterBuffer_OutOfOrderInsertNearWrap covers inserting late-arriving
// packets whose sequence numbers straddle the 16-bit wrap boundary —
// the regime where a naive total-order comparator would misorder them.
func TestJitterBuffer_OutOfOrderInsertNearWrap(t *testing.T) {
	jb := NewJitterBuffer(10 * time.Millisecond)

	// Arrive: 65535, 65533, 0, 65534, 1 (across the wrap, shuffled).
	for _, s := range []uint16{65535, 65533, 0, 65534, 1} {
		jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: s}})
	}

	pkts := jb.Flush()
	require.Len(t, pkts, 5)
	assert.Equal(t, uint16(65533), pkts[0].SequenceNumber)
	assert.Equal(t, uint16(65534), pkts[1].SequenceNumber)
	assert.Equal(t, uint16(65535), pkts[2].SequenceNumber)
	assert.Equal(t, uint16(0), pkts[3].SequenceNumber)
	assert.Equal(t, uint16(1), pkts[4].SequenceNumber)
}

// BenchmarkJitterBuffer_PushPopEmpty measures the per-call cost when the
// buffer never holds more than one entry (depth=0 drains immediately).
// Lower bound on Pop cost; the production hot path is
// BenchmarkJitterBuffer_PopWithBacklog.
func BenchmarkJitterBuffer_PushPopEmpty(b *testing.B) {
	jb := NewJitterBuffer(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(i)}})
		_ = jb.Pop()
	}
}

// BenchmarkJitterBuffer_PopWithBacklog models the dominant production
// pattern: the 5ms drainJB ticker fires while the buffer holds a handful
// of packets that are not yet depth-aged. Previously this paid a full
// sort.Slice on every tick; now Pop is O(1) and returns immediately.
func BenchmarkJitterBuffer_PopWithBacklog(b *testing.B) {
	jb := NewJitterBuffer(time.Hour) // depth large enough that Pop never drains
	for i := 0; i < 8; i++ {
		jb.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(i)}})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = jb.Pop()
	}
}
