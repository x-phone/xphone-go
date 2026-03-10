package media

import "github.com/pion/rtp"

// VP8Depacketizer reassembles VP8 video frames from RTP packets (RFC 7741).
type VP8Depacketizer struct {
	currentTS  uint32
	frameBuf   []byte
	hasData    bool
	isKeyframe bool
}

// Push processes an RTP packet and returns any completed frames.
func (d *VP8Depacketizer) Push(pkt *rtp.Packet) []VideoFrame {
	if len(pkt.Payload) == 0 {
		return nil
	}

	var frames []VideoFrame

	// Timestamp change means the previous frame is complete.
	if d.hasData && pkt.Timestamp != d.currentTS {
		frames = append(frames, d.flushFrame())
	}

	d.currentTS = pkt.Timestamp

	// Parse VP8 payload descriptor (RFC 7741 §4.2).
	off := 0
	if off >= len(pkt.Payload) {
		return frames
	}
	desc0 := pkt.Payload[off]
	xBit := (desc0 >> 7) & 1
	sBit := (desc0 >> 4) & 1
	off++

	if xBit == 1 && off < len(pkt.Payload) {
		ext := pkt.Payload[off]
		iBit := (ext >> 7) & 1
		lBit := (ext >> 6) & 1
		tBit := (ext >> 5) & 1
		kBit := (ext >> 4) & 1
		off++

		// PictureID (I=1)
		if iBit == 1 && off < len(pkt.Payload) {
			if pkt.Payload[off]&0x80 != 0 && off+1 < len(pkt.Payload) {
				off += 2 // 16-bit PictureID (M=1)
			} else {
				off++ // 8-bit PictureID (or truncated 16-bit)
			}
		}
		// TL0PICIDX (L=1)
		if lBit == 1 {
			off++
		}
		// TID/KEYIDX (T=1 or K=1)
		if tBit == 1 || kBit == 1 {
			off++
		}
	}

	if off > len(pkt.Payload) {
		return frames
	}

	vpPayload := pkt.Payload[off:]

	if sBit == 1 {
		// Start of a new partition — detect keyframe from VP8 frame header.
		if len(vpPayload) > 0 {
			// VP8 spec §9.1: frame_tag bits 0 = frame_type (0 = key, 1 = inter).
			d.isKeyframe = (vpPayload[0] & 0x01) == 0
		}
		d.hasData = true
	}

	if d.hasData {
		d.frameBuf = append(d.frameBuf, vpPayload...)
	}

	// Marker bit signals end of frame.
	if pkt.Marker && d.hasData {
		frames = append(frames, d.flushFrame())
	}

	return frames
}

// flushFrame returns the assembled frame and resets state.
// The backing array of frameBuf is reused across frames to reduce allocations.
func (d *VP8Depacketizer) flushFrame() VideoFrame {
	data := make([]byte, len(d.frameBuf))
	copy(data, d.frameBuf)
	frame := VideoFrame{
		Timestamp:  d.currentTS,
		IsKeyframe: d.isKeyframe,
		Data:       data,
	}
	d.frameBuf = d.frameBuf[:0] // reuse capacity
	d.hasData = false
	d.isKeyframe = false
	return frame
}

// VP8Packetizer fragments VP8 video frames into RTP payloads (RFC 7741).
type VP8Packetizer struct {
	MTU int // max RTP payload size (0 = DefaultMTU)
}

// Packetize splits a VP8 frame into RTP payloads with VP8 payload descriptors.
func (p *VP8Packetizer) Packetize(data []byte) [][]byte {
	mtu := p.MTU
	if mtu <= 0 {
		mtu = DefaultMTU
	}
	// Need at least 2 bytes: descriptor + 1 byte payload.
	if mtu < 2 {
		mtu = 2
	}

	if len(data) == 0 {
		return nil
	}

	// VP8 payload descriptor: 1 byte (minimal).
	// First packet: X=0, R=0, N=0, S=1, R=0, PID=0 → 0x10
	// Continuation: X=0, R=0, N=0, S=0, R=0, PID=0 → 0x00
	maxPayload := mtu - 1 // 1 byte for payload descriptor

	var payloads [][]byte
	offset := 0
	first := true
	for offset < len(data) {
		end := offset + maxPayload
		if end > len(data) {
			end = len(data)
		}

		var desc byte
		if first {
			desc = 0x10 // S bit set
			first = false
		}

		payload := make([]byte, 1+end-offset)
		payload[0] = desc
		copy(payload[1:], data[offset:end])
		payloads = append(payloads, payload)
		offset = end
	}
	return payloads
}
