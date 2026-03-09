//go:build g729

package media

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestG729_Interface(t *testing.T) {
	cp := NewCodecProcessor(18, 8000)
	require.NotNil(t, cp, "PT=18 should return g729Processor with g729 tag")
	assert.Equal(t, uint8(18), cp.PayloadType())
	assert.Equal(t, uint32(8000), cp.ClockRate())
	assert.Equal(t, uint32(160), cp.SamplesPerFrame())
}

func TestG729_RoundTripSilence(t *testing.T) {
	cp := newG729Processor()
	require.NotNil(t, cp)

	silence := make([]int16, 160)
	encoded := cp.Encode(silence)
	require.Len(t, encoded, 20, "160 samples → 2 G.729 frames → 20 bytes")

	decoded := cp.Decode(encoded)
	require.Len(t, decoded, 160)
	for i, s := range decoded {
		assert.InDelta(t, 0, int(s), 200, "sample %d not near zero: %d", i, s)
	}
}

func TestG729_RoundTripTone(t *testing.T) {
	cp := newG729Processor()
	require.NotNil(t, cp)

	// Generate 400Hz tone at 8kHz.
	input := make([]int16, 160)
	for i := range input {
		input[i] = int16(8000 * math.Sin(2*math.Pi*400*float64(i)/8000))
	}

	// Feed a few frames to let the codec settle.
	for range 3 {
		encoded := cp.Encode(input)
		cp.Decode(encoded)
	}

	encoded := cp.Encode(input)
	require.NotEmpty(t, encoded)

	decoded := cp.Decode(encoded)
	require.Len(t, decoded, 160)

	// Check that decoded signal has non-trivial amplitude.
	var maxAbs int16
	for _, s := range decoded {
		if s < 0 {
			s = -s
		}
		if s > maxAbs {
			maxAbs = s
		}
	}
	assert.Greater(t, maxAbs, int16(500), "decoded tone should have significant amplitude")
}

func TestG729_Compression(t *testing.T) {
	cp := newG729Processor()
	require.NotNil(t, cp)

	// 160 samples × 2 bytes = 320 bytes PCM → 20 bytes G.729.
	silence := make([]int16, 160)
	encoded := cp.Encode(silence)
	assert.Len(t, encoded, 20, "G.729 always produces 10 bytes per 10ms frame")
}

func TestG729_SingleFrame(t *testing.T) {
	cp := newG729Processor()
	require.NotNil(t, cp)

	// 80 samples = one 10ms frame → 10 bytes.
	samples := make([]int16, 80)
	encoded := cp.Encode(samples)
	assert.Len(t, encoded, 10)

	decoded := cp.Decode(encoded)
	assert.Len(t, decoded, 80)
}

func TestG729_MultiFrame(t *testing.T) {
	cp := newG729Processor()
	require.NotNil(t, cp)

	for frame := 0; frame < 5; frame++ {
		input := make([]int16, 160)
		for i := range input {
			input[i] = int16(3000 * math.Sin(2*math.Pi*float64(i+frame*160)/20.0))
		}
		encoded := cp.Encode(input)
		require.Len(t, encoded, 20, "frame %d: encode failed", frame)

		decoded := cp.Decode(encoded)
		require.Len(t, decoded, 160, "frame %d: decode should produce 160 samples", frame)
	}
}

func TestG729_DecodeShortPayload(t *testing.T) {
	cp := newG729Processor()
	require.NotNil(t, cp)

	// Payload shorter than one frame → silence.
	decoded := cp.Decode([]byte{1, 2, 3})
	require.Len(t, decoded, 160)
	for i, s := range decoded {
		assert.Equal(t, int16(0), s, "sample %d should be zero silence", i)
	}
}

func TestG729_DecodeEmpty(t *testing.T) {
	cp := newG729Processor()
	require.NotNil(t, cp)

	decoded := cp.Decode(nil)
	assert.Len(t, decoded, 160)

	decoded = cp.Decode([]byte{})
	assert.Len(t, decoded, 160)
}

func TestG729_EncodeEmpty(t *testing.T) {
	cp := newG729Processor()
	require.NotNil(t, cp)

	encoded := cp.Encode(nil)
	assert.Empty(t, encoded)

	encoded = cp.Encode([]int16{})
	assert.Empty(t, encoded)

	// Sub-frame input (< 80 samples) also returns empty.
	encoded = cp.Encode(make([]int16, 40))
	assert.Empty(t, encoded)
}
