package media

import (
	"sort"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// seqLess compares two 16-bit RTP sequence numbers with wraparound
// per RFC 3550. Returns true if a comes before b.
func seqLess(a, b uint16) bool {
	diff := b - a
	return diff > 0 && diff < 0x8000
}

type jitterEntry struct {
	pkt     *rtp.Packet
	arrival time.Time
}

// JitterBuffer reorders and deduplicates incoming RTP packets.
type JitterBuffer struct {
	mu      sync.Mutex
	depth   time.Duration
	entries []jitterEntry
	seen    map[uint16]bool
}

// NewJitterBuffer creates a JitterBuffer with the given playout depth.
func NewJitterBuffer(depth time.Duration) *JitterBuffer {
	return &JitterBuffer{
		depth: depth,
		seen:  make(map[uint16]bool),
	}
}

// Push adds an RTP packet to the buffer. Duplicates are dropped.
func (jb *JitterBuffer) Push(pkt *rtp.Packet) {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	seq := pkt.Header.SequenceNumber
	if jb.seen[seq] {
		return
	}
	jb.seen[seq] = true
	jb.entries = append(jb.entries, jitterEntry{pkt: pkt, arrival: time.Now()})
}

// Pop returns the next packet in sequence order if its arrival time exceeds
// the jitter depth, or nil if no packet is ready.
func (jb *JitterBuffer) Pop() *rtp.Packet {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	if len(jb.entries) == 0 {
		return nil
	}

	sort.Slice(jb.entries, func(i, j int) bool {
		return seqLess(jb.entries[i].pkt.Header.SequenceNumber, jb.entries[j].pkt.Header.SequenceNumber)
	})

	now := time.Now()
	if now.Sub(jb.entries[0].arrival) >= jb.depth {
		pkt := jb.entries[0].pkt
		delete(jb.seen, pkt.Header.SequenceNumber)
		jb.entries = jb.entries[1:]
		return pkt
	}
	return nil
}

// Flush returns all buffered packets in sequence order and clears the buffer.
func (jb *JitterBuffer) Flush() []*rtp.Packet {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	if len(jb.entries) == 0 {
		return nil
	}

	sort.Slice(jb.entries, func(i, j int) bool {
		return seqLess(jb.entries[i].pkt.Header.SequenceNumber, jb.entries[j].pkt.Header.SequenceNumber)
	})

	pkts := make([]*rtp.Packet, len(jb.entries))
	for i, e := range jb.entries {
		pkts[i] = e.pkt
	}
	jb.entries = nil
	jb.seen = make(map[uint16]bool)
	return pkts
}
