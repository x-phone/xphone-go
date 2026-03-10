package media

import "github.com/pion/rtp"

// H.264 NAL unit types (RFC 6184 §5.2).
const (
	h264NalSingleMin = 1
	h264NalSingleMax = 23
	h264NalSTAPA     = 24
	h264NalFUA       = 28

	h264NalIDR = 5 // Instantaneous Decoding Refresh (keyframe)
	h264NalSPS = 7 // Sequence Parameter Set
	h264NalPPS = 8 // Picture Parameter Set
)

// annexBStartCode is the 4-byte Annex-B start code.
var annexBStartCode = []byte{0x00, 0x00, 0x00, 0x01}

// H264Depacketizer reassembles H.264 video frames from RTP packets (RFC 6184).
// Supports Single NAL, STAP-A, and FU-A packet types.
type H264Depacketizer struct {
	currentTS  uint32
	nals       [][]byte // accumulated NAL units for current frame
	isKeyframe bool
	hasData    bool

	// FU-A reassembly state.
	fuaBuf    []byte
	fuaActive bool
}

// Push processes an RTP packet and returns any completed frames.
func (d *H264Depacketizer) Push(pkt *rtp.Packet) []VideoFrame {
	if len(pkt.Payload) == 0 {
		return nil
	}

	var frames []VideoFrame

	// Timestamp change means the previous frame is complete.
	if d.hasData && pkt.Timestamp != d.currentTS {
		frames = append(frames, d.flushFrame())
	}

	d.currentTS = pkt.Timestamp
	d.hasData = true

	nalType := pkt.Payload[0] & 0x1F

	switch {
	case nalType >= h264NalSingleMin && nalType <= h264NalSingleMax:
		d.pushSingleNAL(pkt.Payload)
	case nalType == h264NalSTAPA:
		d.pushSTAPA(pkt.Payload)
	case nalType == h264NalFUA:
		d.pushFUA(pkt.Payload)
	}

	// Marker bit signals end of frame.
	if pkt.Marker && d.hasData {
		frames = append(frames, d.flushFrame())
	}

	return frames
}

// pushSingleNAL handles a single NAL unit packet (types 1-23).
func (d *H264Depacketizer) pushSingleNAL(payload []byte) {
	nal := make([]byte, len(payload))
	copy(nal, payload)
	d.nals = append(d.nals, nal)
	d.checkKeyframe(payload[0] & 0x1F)
}

// pushSTAPA handles STAP-A aggregation packets (type 24).
func (d *H264Depacketizer) pushSTAPA(payload []byte) {
	// Skip the STAP-A header byte.
	off := 1
	for off+2 <= len(payload) {
		nalSize := int(payload[off])<<8 | int(payload[off+1])
		off += 2
		if off+nalSize > len(payload) {
			break // malformed
		}
		nal := make([]byte, nalSize)
		copy(nal, payload[off:off+nalSize])
		d.nals = append(d.nals, nal)
		if nalSize > 0 {
			d.checkKeyframe(nal[0] & 0x1F)
		}
		off += nalSize
	}
}

// pushFUA handles FU-A fragmentation packets (type 28).
func (d *H264Depacketizer) pushFUA(payload []byte) {
	if len(payload) < 2 {
		return
	}
	fuIndicator := payload[0]
	fuHeader := payload[1]
	startBit := (fuHeader >> 7) & 1
	endBit := (fuHeader >> 6) & 1
	nalType := fuHeader & 0x1F

	if startBit == 1 {
		// Reconstruct the NAL header: NRI from FU indicator + type from FU header.
		nalHeader := (fuIndicator & 0xE0) | nalType
		d.fuaBuf = make([]byte, 1, len(payload)+1024)
		d.fuaBuf[0] = nalHeader
		d.fuaBuf = append(d.fuaBuf, payload[2:]...)
		d.fuaActive = true
		d.checkKeyframe(nalType)
	} else if d.fuaActive {
		d.fuaBuf = append(d.fuaBuf, payload[2:]...)
	}

	if endBit == 1 && d.fuaActive {
		d.nals = append(d.nals, d.fuaBuf)
		d.fuaBuf = nil
		d.fuaActive = false
	}
}

// checkKeyframe marks the current frame as a keyframe if the NAL type indicates it.
func (d *H264Depacketizer) checkKeyframe(nalType uint8) {
	if nalType == h264NalIDR || nalType == h264NalSPS || nalType == h264NalPPS {
		d.isKeyframe = true
	}
}

// flushFrame assembles accumulated NALs into an Annex-B frame and resets state.
func (d *H264Depacketizer) flushFrame() VideoFrame {
	// Calculate total size: for each NAL, 4-byte start code + NAL data.
	totalSize := 0
	for _, nal := range d.nals {
		totalSize += 4 + len(nal)
	}
	data := make([]byte, 0, totalSize)
	for _, nal := range d.nals {
		data = append(data, annexBStartCode...)
		data = append(data, nal...)
	}

	frame := VideoFrame{
		Timestamp:  d.currentTS,
		IsKeyframe: d.isKeyframe,
		Data:       data,
	}

	// Nil out stale references to allow GC of old NAL buffers.
	for i := range d.nals {
		d.nals[i] = nil
	}
	d.nals = d.nals[:0]
	d.isKeyframe = false
	d.hasData = false
	d.fuaBuf = nil
	d.fuaActive = false
	return frame
}

// H264Packetizer fragments H.264 Annex-B frames into RTP payloads (RFC 6184).
type H264Packetizer struct {
	MTU int // max RTP payload size (0 = DefaultMTU)
}

// Packetize splits an Annex-B encoded frame into RTP payloads.
func (p *H264Packetizer) Packetize(data []byte) [][]byte {
	mtu := p.MTU
	if mtu <= 0 {
		mtu = DefaultMTU
	}
	// FU-A requires at least 3 bytes: indicator + header + 1 byte payload.
	if mtu < 3 {
		mtu = 3
	}

	nals := splitAnnexB(data)
	if len(nals) == 0 {
		return nil
	}

	var payloads [][]byte
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		if len(nal) <= mtu {
			// Single NAL unit packet.
			payload := make([]byte, len(nal))
			copy(payload, nal)
			payloads = append(payloads, payload)
		} else {
			// FU-A fragmentation.
			payloads = append(payloads, fragmentFUA(nal, mtu)...)
		}
	}
	return payloads
}

// fragmentFUA splits a single NAL unit into FU-A fragments.
func fragmentFUA(nal []byte, mtu int) [][]byte {
	if len(nal) < 1 {
		return nil
	}

	nalHeader := nal[0]
	nalType := nalHeader & 0x1F
	nri := nalHeader & 0xE0

	// FU indicator: NRI from original + type 28 (FU-A).
	fuIndicator := nri | h264NalFUA

	// Payload data starts after the NAL header byte.
	data := nal[1:]
	maxPayload := mtu - 2 // 2 bytes for FU indicator + FU header

	numFragments := (len(data) + maxPayload - 1) / maxPayload
	payloads := make([][]byte, 0, numFragments)
	offset := 0
	for offset < len(data) {
		end := offset + maxPayload
		if end > len(data) {
			end = len(data)
		}

		fuHeader := nalType
		if offset == 0 {
			fuHeader |= 0x80 // S bit
		}
		if end == len(data) {
			fuHeader |= 0x40 // E bit
		}

		payload := make([]byte, 2+end-offset)
		payload[0] = fuIndicator
		payload[1] = fuHeader
		copy(payload[2:], data[offset:end])
		payloads = append(payloads, payload)
		offset = end
	}
	return payloads
}

// splitAnnexB parses Annex-B encoded data into individual NAL units.
// Handles both 3-byte (0x000001) and 4-byte (0x00000001) start codes.
func splitAnnexB(data []byte) [][]byte {
	var nals [][]byte
	i := 0
	n := len(data)

	// Skip to first start code.
	for i < n {
		if i+3 <= n && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			i += 3
			break
		}
		if i+4 <= n && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			i += 4
			break
		}
		i++
	}

	for i < n {
		nalStart := i
		// Scan for next start code.
		for i < n {
			if i+3 <= n && data[i] == 0 && data[i+1] == 0 {
				if data[i+2] == 1 {
					break
				}
				if i+4 <= n && data[i+2] == 0 && data[i+3] == 1 {
					break
				}
			}
			i++
		}

		nal := data[nalStart:i]
		if len(nal) > 0 {
			nals = append(nals, nal)
		}

		// Skip the start code.
		if i+4 <= n && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			i += 4
		} else if i+3 <= n && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			i += 3
		} else {
			break
		}
	}

	return nals
}
