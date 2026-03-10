package media

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVP8Depacketizer_SinglePacket(t *testing.T) {
	d := &VP8Depacketizer{}

	// Keyframe: S=1 (0x10), VP8 frame tag byte 0 bit 0 = 0 (keyframe).
	// 0x10 = show_frame=1, version=0, frame_type=0 (key).
	frameData := []byte{0x10, 0x10, 0x00, 0x00, 0x9D, 0x01, 0x2A} // desc + VP8 keyframe
	pkt := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, Timestamp: 1000, Marker: true},
		Payload: frameData,
	}
	frames := d.Push(pkt)
	require.Len(t, frames, 1)
	assert.True(t, frames[0].IsKeyframe)
	assert.Equal(t, uint32(1000), frames[0].Timestamp)
	assert.Equal(t, frameData[1:], frames[0].Data) // VP8 data without descriptor
}

func TestVP8Depacketizer_Interframe(t *testing.T) {
	d := &VP8Depacketizer{}

	// Interframe: S=1, VP8 frame byte 0 bit 0 = 1.
	pkt := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, Timestamp: 2000, Marker: true},
		Payload: []byte{0x10, 0x01, 0xAA}, // desc=0x10 (S=1), VP8 header bit0=1 (inter)
	}
	frames := d.Push(pkt)
	require.Len(t, frames, 1)
	assert.False(t, frames[0].IsKeyframe)
}

func TestVP8Depacketizer_MultiPacket(t *testing.T) {
	d := &VP8Depacketizer{}

	// First packet: S=1. VP8 keyframe (byte 0 bit 0 = 0).
	pkt1 := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, Timestamp: 3000},
		Payload: []byte{0x10, 0x10, 0x00, 0x2A}, // S=1, keyframe (0x10 bit0=0)
	}
	frames := d.Push(pkt1)
	assert.Empty(t, frames)

	// Continuation packet: S=0.
	pkt2 := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 2, Timestamp: 3000},
		Payload: []byte{0x00, 0xBB, 0xCC}, // S=0
	}
	frames = d.Push(pkt2)
	assert.Empty(t, frames)

	// Last packet: S=0, marker set.
	pkt3 := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 3, Timestamp: 3000, Marker: true},
		Payload: []byte{0x00, 0xDD}, // S=0
	}
	frames = d.Push(pkt3)
	require.Len(t, frames, 1)
	assert.True(t, frames[0].IsKeyframe)
	// Data should be: 0x10 0x00 0x2A 0xBB 0xCC 0xDD
	assert.Equal(t, []byte{0x10, 0x00, 0x2A, 0xBB, 0xCC, 0xDD}, frames[0].Data)
}

func TestVP8Depacketizer_TimestampChange(t *testing.T) {
	d := &VP8Depacketizer{}

	// Frame 1, no marker.
	pkt1 := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, Timestamp: 1000},
		Payload: []byte{0x10, 0x01, 0xAA}, // S=1, inter
	}
	d.Push(pkt1)

	// Frame 2 with different timestamp — should flush frame 1.
	pkt2 := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 2, Timestamp: 4000, Marker: true},
		Payload: []byte{0x10, 0x9D, 0xBB}, // S=1, keyframe
	}
	frames := d.Push(pkt2)
	require.Len(t, frames, 2)
	assert.Equal(t, uint32(1000), frames[0].Timestamp)
	assert.Equal(t, uint32(4000), frames[1].Timestamp)
}

func TestVP8Depacketizer_ExtendedDescriptor(t *testing.T) {
	d := &VP8Depacketizer{}

	// Extended descriptor: X=1, I=1 (8-bit PictureID).
	// VP8 frame byte: 0x10 = keyframe (bit 0 = 0).
	payload := []byte{
		0x90,       // X=1, S=1
		0x80,       // I=1, L=0, T=0, K=0
		42,         // PictureID (8-bit, M=0)
		0x10, 0x01, // VP8 frame data (keyframe: bit0=0)
	}
	pkt := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, Timestamp: 5000, Marker: true},
		Payload: payload,
	}
	frames := d.Push(pkt)
	require.Len(t, frames, 1)
	assert.True(t, frames[0].IsKeyframe)
	assert.Equal(t, []byte{0x10, 0x01}, frames[0].Data)
}

func TestVP8Depacketizer_ExtendedDescriptor16bitPictureID(t *testing.T) {
	d := &VP8Depacketizer{}

	// Extended descriptor: X=1, I=1 (16-bit PictureID, M=1).
	payload := []byte{
		0x90,       // X=1, S=1
		0x80,       // I=1
		0x80, 0x2A, // 16-bit PictureID (M=1)
		0x9D, 0x01, // VP8 frame data
	}
	pkt := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 1, Timestamp: 6000, Marker: true},
		Payload: payload,
	}
	frames := d.Push(pkt)
	require.Len(t, frames, 1)
	assert.Equal(t, []byte{0x9D, 0x01}, frames[0].Data)
}

func TestVP8Packetizer_SmallFrame(t *testing.T) {
	p := &VP8Packetizer{MTU: 1200}

	data := []byte{0x9D, 0x01, 0x2A, 0xAA, 0xBB}
	payloads := p.Packetize(data)
	require.Len(t, payloads, 1)
	assert.Equal(t, byte(0x10), payloads[0][0]) // S=1
	assert.Equal(t, data, payloads[0][1:])
}

func TestVP8Packetizer_LargeFrame(t *testing.T) {
	p := &VP8Packetizer{MTU: 100}

	data := bytes.Repeat([]byte{0xAA}, 250)
	payloads := p.Packetize(data)
	require.True(t, len(payloads) > 1)

	// First packet: S=1.
	assert.Equal(t, byte(0x10), payloads[0][0])
	// Continuation packets: S=0.
	for _, payload := range payloads[1:] {
		assert.Equal(t, byte(0x00), payload[0])
	}

	// Reassemble and verify.
	var assembled []byte
	for _, payload := range payloads {
		assembled = append(assembled, payload[1:]...) // skip descriptor
	}
	assert.Equal(t, data, assembled)
}

func TestVP8RoundTrip(t *testing.T) {
	original := make([]byte, 500)
	original[0] = 0x10 // keyframe (bit 0 = 0)
	original[1] = 0x00
	original[2] = 0x00
	for i := 3; i < len(original); i++ {
		original[i] = byte(i)
	}

	// Packetize with small MTU.
	p := &VP8Packetizer{MTU: 150}
	payloads := p.Packetize(original)
	require.True(t, len(payloads) > 1)

	// Depacketize.
	d := &VP8Depacketizer{}
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
	assert.Equal(t, original, frames[0].Data)
}

func TestVP8Packetizer_Empty(t *testing.T) {
	p := &VP8Packetizer{}
	assert.Nil(t, p.Packetize(nil))
	assert.Nil(t, p.Packetize([]byte{}))
}
