package media

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitAnnexB(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want int
	}{
		{
			name: "4-byte start codes",
			data: []byte{0, 0, 0, 1, 0x65, 0xAA, 0xBB, 0, 0, 0, 1, 0x67, 0xCC},
			want: 2,
		},
		{
			name: "3-byte start codes",
			data: []byte{0, 0, 1, 0x65, 0xAA, 0, 0, 1, 0x67, 0xCC},
			want: 2,
		},
		{
			name: "mixed start codes",
			data: []byte{0, 0, 0, 1, 0x67, 0xAA, 0, 0, 1, 0x68, 0xBB, 0, 0, 0, 1, 0x65, 0xCC},
			want: 3,
		},
		{
			name: "empty",
			data: nil,
			want: 0,
		},
		{
			name: "single NAL",
			data: []byte{0, 0, 0, 1, 0x65, 0xAA, 0xBB, 0xCC},
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nals := splitAnnexB(tt.data)
			assert.Equal(t, tt.want, len(nals))
		})
	}
}

func TestH264Depacketizer_SingleNAL(t *testing.T) {
	d := &H264Depacketizer{}

	// Single IDR NAL (type 5 = keyframe), marker set.
	pkt := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, Timestamp: 1000, Marker: true, PayloadType: 96},
		Payload: []byte{0x65, 0xAA, 0xBB, 0xCC}, // NAL type 5 (IDR)
	}
	frames := d.Push(pkt)
	require.Len(t, frames, 1)
	assert.True(t, frames[0].IsKeyframe)
	assert.Equal(t, uint32(1000), frames[0].Timestamp)
	// Should have Annex-B start code prefix.
	assert.True(t, bytes.HasPrefix(frames[0].Data, annexBStartCode))
}

func TestH264Depacketizer_STAPA(t *testing.T) {
	d := &H264Depacketizer{}

	// STAP-A with SPS (type 7) + PPS (type 8).
	sps := []byte{0x67, 0x42, 0xE0, 0x1F} // NAL type 7
	pps := []byte{0x68, 0xCE, 0x01}       // NAL type 8

	payload := []byte{h264NalSTAPA}
	// SPS: length (2 bytes) + data
	payload = append(payload, byte(len(sps)>>8), byte(len(sps)))
	payload = append(payload, sps...)
	// PPS: length (2 bytes) + data
	payload = append(payload, byte(len(pps)>>8), byte(len(pps)))
	payload = append(payload, pps...)

	pkt := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, Timestamp: 2000, Marker: true},
		Payload: payload,
	}
	frames := d.Push(pkt)
	require.Len(t, frames, 1)
	assert.True(t, frames[0].IsKeyframe) // SPS/PPS = keyframe
	// Should contain 2 NALs with Annex-B start codes.
	assert.Equal(t, 4+len(sps)+4+len(pps), len(frames[0].Data))
}

func TestH264Depacketizer_FUA(t *testing.T) {
	d := &H264Depacketizer{}

	// Simulate FU-A fragmentation of a large IDR NAL.
	// Original NAL: type 5 (IDR), NRI=3 (0x60).
	nalHeader := byte(0x65) // NRI=3, type=5
	fuIndicator := (nalHeader & 0xE0) | h264NalFUA

	// Fragment 1: S=1, E=0.
	frag1 := append([]byte{fuIndicator, 0x80 | 0x05}, bytes.Repeat([]byte{0xAA}, 100)...)
	pkt1 := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, Timestamp: 3000},
		Payload: frag1,
	}
	frames := d.Push(pkt1)
	assert.Empty(t, frames, "FU-A start should not produce a frame")

	// Fragment 2: S=0, E=0 (middle).
	frag2 := append([]byte{fuIndicator, 0x05}, bytes.Repeat([]byte{0xBB}, 100)...)
	pkt2 := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 2, Timestamp: 3000},
		Payload: frag2,
	}
	frames = d.Push(pkt2)
	assert.Empty(t, frames, "FU-A middle should not produce a frame")

	// Fragment 3: S=0, E=1 (end), marker set.
	frag3 := append([]byte{fuIndicator, 0x40 | 0x05}, bytes.Repeat([]byte{0xCC}, 50)...)
	pkt3 := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 3, Timestamp: 3000, Marker: true},
		Payload: frag3,
	}
	frames = d.Push(pkt3)
	require.Len(t, frames, 1)
	assert.True(t, frames[0].IsKeyframe)
	assert.Equal(t, uint32(3000), frames[0].Timestamp)

	// Verify the reassembled NAL: start code + original header + payload.
	assert.True(t, bytes.HasPrefix(frames[0].Data, annexBStartCode))
	// After start code: reconstructed NAL header (0x65) + payload.
	assert.Equal(t, nalHeader, frames[0].Data[4])
	assert.Equal(t, 4+1+100+100+50, len(frames[0].Data)) // start code + header + 3 fragments
}

func TestH264Depacketizer_TimestampChange(t *testing.T) {
	d := &H264Depacketizer{}

	// First frame (non-keyframe), no marker bit.
	pkt1 := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, Timestamp: 1000},
		Payload: []byte{0x41, 0xAA}, // NAL type 1 (non-IDR slice)
	}
	frames := d.Push(pkt1)
	assert.Empty(t, frames)

	// Second frame with different timestamp — should flush first frame.
	pkt2 := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 2, Timestamp: 4000, Marker: true},
		Payload: []byte{0x41, 0xBB},
	}
	frames = d.Push(pkt2)
	require.Len(t, frames, 2) // first flushed by timestamp change, second by marker
	assert.Equal(t, uint32(1000), frames[0].Timestamp)
	assert.Equal(t, uint32(4000), frames[1].Timestamp)
}

func TestH264Packetizer_SingleNAL(t *testing.T) {
	p := &H264Packetizer{MTU: 1200}

	// Small NAL that fits in a single packet.
	nal := bytes.Repeat([]byte{0xAA}, 100)
	data := append(annexBStartCode, 0x65) // IDR
	data = append(data, nal...)

	payloads := p.Packetize(data)
	require.Len(t, payloads, 1)
	assert.Equal(t, byte(0x65), payloads[0][0]) // NAL header preserved
}

func TestH264Packetizer_FUA(t *testing.T) {
	p := &H264Packetizer{MTU: 100}

	// Large NAL that requires FU-A fragmentation.
	nal := make([]byte, 300)
	nal[0] = 0x65 // NAL type 5 (IDR), NRI=3
	for i := 1; i < len(nal); i++ {
		nal[i] = byte(i)
	}
	data := append(annexBStartCode, nal...)

	payloads := p.Packetize(data)
	require.True(t, len(payloads) > 1, "should produce multiple FU-A fragments")

	// First fragment: S=1, E=0.
	assert.Equal(t, byte((0x65&0xE0)|h264NalFUA), payloads[0][0]) // FU indicator
	assert.Equal(t, byte(0x80|0x05), payloads[0][1])              // FU header: S=1, type=5

	// Last fragment: S=0, E=1.
	last := payloads[len(payloads)-1]
	assert.Equal(t, byte(0x40|0x05), last[1]) // FU header: E=1, type=5

	// Middle fragments: S=0, E=0.
	for _, mid := range payloads[1 : len(payloads)-1] {
		assert.Equal(t, byte(0x05), mid[1]) // FU header: type=5 only
	}
}

func TestH264Packetizer_MultipleNALs(t *testing.T) {
	p := &H264Packetizer{MTU: 1200}

	// SPS + PPS + IDR, each small enough for single NAL.
	var data []byte
	data = append(data, annexBStartCode...)
	data = append(data, 0x67, 0x42, 0xE0, 0x1F) // SPS
	data = append(data, annexBStartCode...)
	data = append(data, 0x68, 0xCE, 0x01) // PPS
	data = append(data, annexBStartCode...)
	data = append(data, 0x65)                              // IDR header
	data = append(data, bytes.Repeat([]byte{0xAA}, 50)...) // IDR payload

	payloads := p.Packetize(data)
	require.Len(t, payloads, 3)
	assert.Equal(t, byte(0x67), payloads[0][0]&0x1F|0x60) // SPS
	assert.Equal(t, byte(0x68), payloads[1][0])           // PPS
	assert.Equal(t, byte(0x65), payloads[2][0])           // IDR
}

func TestH264RoundTrip(t *testing.T) {
	// Build a frame with SPS + PPS + IDR.
	var original []byte
	original = append(original, annexBStartCode...)
	original = append(original, 0x67, 0x42, 0xE0, 0x1F) // SPS
	original = append(original, annexBStartCode...)
	original = append(original, 0x68, 0xCE, 0x01) // PPS
	original = append(original, annexBStartCode...)
	idr := make([]byte, 500)
	idr[0] = 0x65
	for i := 1; i < len(idr); i++ {
		idr[i] = byte(i)
	}
	original = append(original, idr...)

	// Packetize with small MTU to force FU-A.
	p := &H264Packetizer{MTU: 200}
	payloads := p.Packetize(original)
	require.True(t, len(payloads) > 0)

	// Depacketize.
	d := &H264Depacketizer{}
	var frames []VideoFrame
	for i, payload := range payloads {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				SequenceNumber: uint16(i),
				Timestamp:      90000,
				Marker:         i == len(payloads)-1,
			},
			Payload: payload,
		}
		frames = append(frames, d.Push(pkt)...)
	}

	require.Len(t, frames, 1)
	assert.True(t, frames[0].IsKeyframe)
	assert.Equal(t, uint32(90000), frames[0].Timestamp)

	// Verify all NALs survived the round trip.
	recoveredNALs := splitAnnexB(frames[0].Data)
	originalNALs := splitAnnexB(original)
	require.Equal(t, len(originalNALs), len(recoveredNALs))
	for i := range originalNALs {
		assert.Equal(t, originalNALs[i], recoveredNALs[i], "NAL %d mismatch", i)
	}
}
