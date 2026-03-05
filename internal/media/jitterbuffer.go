package media

import (
	"time"

	"github.com/pion/rtp"
)

// JitterBuffer reorders and deduplicates incoming RTP packets.
type JitterBuffer struct {
	depth time.Duration
}

// NewJitterBuffer creates a JitterBuffer with the given playout depth.
func NewJitterBuffer(depth time.Duration) *JitterBuffer {
	return &JitterBuffer{depth: depth}
}

// Push adds an RTP packet to the buffer.
func (jb *JitterBuffer) Push(pkt *rtp.Packet) {
}

// Pop returns the next packet in sequence order, or nil if unavailable.
func (jb *JitterBuffer) Pop() *rtp.Packet {
	return nil
}

// Flush returns all buffered packets in sequence order and clears the buffer.
func (jb *JitterBuffer) Flush() []*rtp.Packet {
	return nil
}
