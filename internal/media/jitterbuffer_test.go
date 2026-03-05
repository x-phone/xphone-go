package media

import (
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
