//go:build opus

package media

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpus_Interface(t *testing.T) {
	cp := NewCodecProcessor(111, 8000)
	require.NotNil(t, cp, "PT=111 should return OpusProcessor with opus tag")
	assert.Equal(t, uint8(111), cp.PayloadType())
	assert.Equal(t, uint32(48000), cp.ClockRate())
	assert.Equal(t, uint32(960), cp.SamplesPerFrame())
}

func TestOpus_RoundTripSilence(t *testing.T) {
	cp := newOpusProcessor()
	require.NotNil(t, cp)

	silence := make([]int16, 160)
	encoded := cp.Encode(silence)
	require.NotEmpty(t, encoded)

	decoded := cp.Decode(encoded)
	require.Len(t, decoded, 160)
	for i, s := range decoded {
		assert.InDelta(t, 0, int(s), 50, "sample %d not near zero: %d", i, s)
	}
}

func TestOpus_RoundTripTone(t *testing.T) {
	cp := newOpusProcessor()
	require.NotNil(t, cp)

	// Generate 400Hz tone at 8kHz (20 samples per cycle).
	input := make([]int16, 160)
	for i := range input {
		input[i] = int16(8000 * math.Sin(2*math.Pi*400*float64(i)/8000))
	}

	// Opus is stateful; feed a few frames to let the encoder settle.
	for range 3 {
		cp.Encode(input)
	}

	encoded := cp.Encode(input)
	require.NotEmpty(t, encoded)

	decoded := cp.Decode(encoded)
	require.Len(t, decoded, 160)

	// Check that decoded signal has non-trivial amplitude (not silence).
	var maxAbs int16
	for _, s := range decoded {
		if s < 0 {
			s = -s
		}
		if s > maxAbs {
			maxAbs = s
		}
	}
	assert.Greater(t, maxAbs, int16(1000), "decoded tone should have significant amplitude")
}

func TestOpus_Compression(t *testing.T) {
	cp := newOpusProcessor()
	require.NotNil(t, cp)

	// 160 samples × 2 bytes = 320 bytes PCM. Opus should compress.
	silence := make([]int16, 160)
	encoded := cp.Encode(silence)
	require.NotEmpty(t, encoded)
	assert.Less(t, len(encoded), 320, "Opus should compress 160 samples below 320 bytes")
}

func TestOpus_OutputSize(t *testing.T) {
	cp := newOpusProcessor()
	require.NotNil(t, cp)

	// Encode a tone and verify output is within typical Opus frame sizes.
	input := make([]int16, 160)
	for i := range input {
		input[i] = int16(4000 * math.Sin(2*math.Pi*float64(i)/20.0))
	}
	encoded := cp.Encode(input)
	require.NotEmpty(t, encoded)
	// Typical Opus VoIP frame: 10-80 bytes for 20ms at 8kHz.
	assert.Less(t, len(encoded), 200, "Opus output should be well under 200 bytes")
}

func TestOpus_Stateful(t *testing.T) {
	cp := newOpusProcessor()
	require.NotNil(t, cp)

	// Successive encodes of the same input may produce different output
	// (encoder state adaptation).
	input := make([]int16, 160)
	for i := range input {
		input[i] = int16(2000 * math.Sin(2*math.Pi*float64(i)/20.0))
	}
	enc1 := cp.Encode(input)
	enc2 := cp.Encode(input)

	// At minimum both should succeed.
	require.NotEmpty(t, enc1)
	require.NotEmpty(t, enc2)
}

func TestOpus_MultiFrame(t *testing.T) {
	cp := newOpusProcessor()
	require.NotNil(t, cp)

	// Encode and decode multiple consecutive frames.
	for frame := 0; frame < 5; frame++ {
		input := make([]int16, 160)
		for i := range input {
			input[i] = int16(3000 * math.Sin(2*math.Pi*float64(i+frame*160)/20.0))
		}
		encoded := cp.Encode(input)
		require.NotEmpty(t, encoded, "frame %d: encode failed", frame)

		decoded := cp.Decode(encoded)
		require.Len(t, decoded, 160, "frame %d: decode should produce 160 samples", frame)
	}
}
