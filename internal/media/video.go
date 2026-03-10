package media

import "github.com/pion/rtp"

// VideoFrame is an assembled video frame from RTP depacketization.
type VideoFrame struct {
	Timestamp  uint32
	IsKeyframe bool
	Data       []byte
}

// VideoDepacketizer reassembles video frames from RTP packets.
type VideoDepacketizer interface {
	// Push feeds an RTP packet to the depacketizer.
	// Returns assembled frames (usually 0 or 1 per call).
	Push(pkt *rtp.Packet) []VideoFrame
}

// VideoPacketizer fragments video frames into RTP payloads.
type VideoPacketizer interface {
	// Packetize splits a video frame into RTP-sized payloads.
	// The caller sets RTP headers (seq, timestamp, SSRC, marker on last).
	Packetize(data []byte) [][]byte
}

// DefaultMTU is the default maximum RTP payload size.
// Conservative: leaves room for RTP (12) + UDP (8) + IP (20) + SRTP (10) headers.
const DefaultMTU = 1200
