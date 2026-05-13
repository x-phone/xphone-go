package media

import (
	"slices"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// seqLess compares two 16-bit RTP sequence numbers with wraparound
// per RFC 3550. Returns true if a comes before b. Not a total order
// across the full 16-bit range — at the 2^15 antipode (b-a == 0x8000)
// it returns false in both directions. The jitter buffer relies on
// concurrently-held sequences spanning less than 2^15, which holds
// in any realistic media session.
func seqLess(a, b uint16) bool {
	diff := b - a
	return diff > 0 && diff < 0x8000
}

type jitterEntry struct {
	pkt     *rtp.Packet
	arrival time.Time
}

// JitterBuffer reorders and deduplicates incoming RTP packets.
//
// Entries are kept in sequence order at all times: Push inserts at the right
// position (fast path on in-order arrivals; linear walk from the tail
// otherwise), so Pop and Flush are O(1) / O(N) without re-sorting on every
// call.
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

// Push adds an RTP packet to the buffer in sequence order. Duplicates are
// dropped.
func (jb *JitterBuffer) Push(pkt *rtp.Packet) {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	seq := pkt.Header.SequenceNumber
	if jb.seen[seq] {
		return
	}
	jb.seen[seq] = true

	entry := jitterEntry{pkt: pkt, arrival: time.Now()}
	n := len(jb.entries)
	if n == 0 {
		jb.entries = append(jb.entries, entry)
		return
	}

	// Fast path: in-order arrival (the common case).
	if seqLess(jb.entries[n-1].pkt.Header.SequenceNumber, seq) {
		jb.entries = append(jb.entries, entry)
		return
	}

	// Out-of-order: linear walk from the tail. Buffer depth is small in
	// steady state (a few dozen packets at most), and out-of-order packets
	// typically land within a few slots of the tail — cheaper and simpler
	// than binary search under RFC 3550 wraparound semantics, where the
	// comparator isn't a total order across the full 16-bit range.
	insertAt := n
	for insertAt > 0 && !seqLess(jb.entries[insertAt-1].pkt.Header.SequenceNumber, seq) {
		insertAt--
	}
	jb.entries = slices.Insert(jb.entries, insertAt, entry)
}

// Pop returns the next packet in sequence order if its arrival time exceeds
// the jitter depth, or nil if no packet is ready.
func (jb *JitterBuffer) Pop() *rtp.Packet {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	if len(jb.entries) == 0 {
		return nil
	}

	if time.Since(jb.entries[0].arrival) < jb.depth {
		return nil
	}

	pkt := jb.entries[0].pkt
	delete(jb.seen, pkt.Header.SequenceNumber)
	// Zero vacated slot to allow GC of the popped packet.
	jb.entries[0] = jitterEntry{}
	jb.entries = jb.entries[1:]
	// Compact when backing array is oversized to prevent unbounded growth.
	if cap(jb.entries) > 64 && cap(jb.entries) > 4*len(jb.entries) {
		compacted := make([]jitterEntry, len(jb.entries))
		copy(compacted, jb.entries)
		jb.entries = compacted
	}
	return pkt
}

// Flush returns all buffered packets in sequence order and clears the buffer.
func (jb *JitterBuffer) Flush() []*rtp.Packet {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	if len(jb.entries) == 0 {
		return nil
	}

	pkts := make([]*rtp.Packet, len(jb.entries))
	for i, e := range jb.entries {
		pkts[i] = e.pkt
	}
	jb.entries = nil
	clear(jb.seen)
	return pkts
}
