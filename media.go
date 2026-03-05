package xphone

import "time"

// Default media configuration values.
const (
	defaultMediaTimeout = 30 * time.Second
	defaultJitterDepth  = 50 * time.Millisecond
)

// startMedia initializes the media pipeline (jitter buffer, RTP demux,
// media timeout timer). Stub — Phase 2 implementation.
func (c *call) startMedia() {
}

// stopMedia tears down the media pipeline and releases RTP ports.
// Stub — Phase 2 implementation.
func (c *call) stopMedia() {
}

// resetMediaTimer resets the media timeout timer. Called on each received
// RTP packet. Stub — Phase 2 implementation.
func (c *call) resetMediaTimer() {
}
