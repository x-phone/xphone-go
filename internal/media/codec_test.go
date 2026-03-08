package media

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- PCMU tests ---

func TestPCMU_DecodeKnownValues(t *testing.T) {
	cp := &pcmuProcessor{}
	// 0xFF is mu-law silence → 0
	assert.Equal(t, int16(0), cp.Decode([]byte{0xFF})[0])
	// 0x80 → large positive
	assert.Greater(t, cp.Decode([]byte{0x80})[0], int16(16000))
	// 0x00 → large negative
	assert.Less(t, cp.Decode([]byte{0x00})[0], int16(-16000))
}

func TestPCMU_EncodeKnownValues(t *testing.T) {
	cp := &pcmuProcessor{}
	// 0 → 0xFF (silence)
	assert.Equal(t, byte(0xFF), cp.Encode([]int16{0})[0])
	// Positive and negative samples should differ in sign bit only (bit 7).
	posEnc := cp.Encode([]int16{1000})[0]
	negEnc := cp.Encode([]int16{-1000})[0]
	assert.NotEqual(t, posEnc, negEnc)
}

func TestPCMU_RoundTrip(t *testing.T) {
	cp := &pcmuProcessor{}
	// Round-trip: decode(encode(sample)) should be within mu-law quantization error.
	testSamples := []int16{0, 100, -100, 1000, -1000, 8000, -8000, 16000, -16000}
	for _, sample := range testSamples {
		encoded := cp.Encode([]int16{sample})
		decoded := cp.Decode(encoded)
		// Quantization error depends on segment; allow 2% or ±8 whichever is larger.
		tolerance := math.Abs(float64(sample)) * 0.02
		if tolerance < 8 {
			tolerance = 8
		}
		diff := math.Abs(float64(decoded[0]) - float64(sample))
		assert.LessOrEqual(t, diff, tolerance+1,
			"sample %d: encoded=0x%02X decoded=%d", sample, encoded[0], decoded[0])
	}
}

func TestPCMU_FrameSize(t *testing.T) {
	cp := &pcmuProcessor{}
	// 160 bytes PCMU ↔ 160 samples
	payload := make([]byte, 160)
	samples := cp.Decode(payload)
	assert.Len(t, samples, 160)

	pcm := make([]int16, 160)
	encoded := cp.Encode(pcm)
	assert.Len(t, encoded, 160)
}

func TestPCMU_Silence(t *testing.T) {
	cp := &pcmuProcessor{}
	// 160 zeros encode → decode back to approximately zero.
	silence := make([]int16, 160)
	encoded := cp.Encode(silence)
	decoded := cp.Decode(encoded)
	for i, s := range decoded {
		assert.InDelta(t, 0, int(s), 8, "sample %d not near zero: %d", i, s)
	}
}

// --- PCMA tests ---

func TestPCMA_DecodeKnownValues(t *testing.T) {
	cp := &pcmaProcessor{}
	// 0xD5 is A-law silence → small value near 0 (A-law has no exact zero; min positive is 8).
	decoded := cp.Decode([]byte{0xD5})[0]
	assert.InDelta(t, 0, int(decoded), 8, "A-law silence should be near zero, got %d", decoded)
	// Verify positive and negative sides exist.
	assert.Greater(t, cp.Decode([]byte{0x80})[0], int16(0), "0x80 should be positive")
	assert.Less(t, cp.Decode([]byte{0x00})[0], int16(0), "0x00 should be negative")
}

func TestPCMA_EncodeKnownValues(t *testing.T) {
	cp := &pcmaProcessor{}
	// 0 encodes to 0xD5 (A-law silence).
	assert.Equal(t, byte(0xD5), cp.Encode([]int16{0})[0])
	// Positive and negative should produce different bytes.
	posEnc := cp.Encode([]int16{1000})[0]
	negEnc := cp.Encode([]int16{-1000})[0]
	assert.NotEqual(t, posEnc, negEnc)
}

func TestPCMA_RoundTrip(t *testing.T) {
	cp := &pcmaProcessor{}
	testSamples := []int16{0, 100, -100, 1000, -1000, 8000, -8000, 16000, -16000}
	for _, sample := range testSamples {
		encoded := cp.Encode([]int16{sample})
		decoded := cp.Decode(encoded)
		tolerance := math.Abs(float64(sample)) * 0.02
		if tolerance < 16 {
			tolerance = 16
		}
		diff := math.Abs(float64(decoded[0]) - float64(sample))
		assert.LessOrEqual(t, diff, tolerance+1,
			"sample %d: encoded=0x%02X decoded=%d", sample, encoded[0], decoded[0])
	}
}

func TestPCMA_FrameSize(t *testing.T) {
	cp := &pcmaProcessor{}
	payload := make([]byte, 160)
	samples := cp.Decode(payload)
	assert.Len(t, samples, 160)

	pcm := make([]int16, 160)
	encoded := cp.Encode(pcm)
	assert.Len(t, encoded, 160)
}

func TestPCMA_Silence(t *testing.T) {
	cp := &pcmaProcessor{}
	silence := make([]int16, 160)
	encoded := cp.Encode(silence)
	decoded := cp.Decode(encoded)
	for i, s := range decoded {
		assert.InDelta(t, 0, int(s), 16, "sample %d not near zero: %d", i, s)
	}
}

// --- G.722 tests ---

func TestG722_Decode(t *testing.T) {
	// Encode a known signal, then decode and verify round-trip at pcmRate=8000.
	cp := newG722Processor(8000)
	// Generate a 160-sample 8kHz sine wave.
	input := make([]int16, 160)
	for i := range input {
		input[i] = int16(4000 * math.Sin(2*math.Pi*float64(i)/160.0))
	}
	encoded := cp.Encode(input)
	decoded := cp.Decode(encoded)
	assert.Len(t, decoded, 160, "decoded frame should be 160 samples at 8kHz")
}

func TestG722_RoundTrip(t *testing.T) {
	// Multi-frame encode → decode within tolerance.
	cp := newG722Processor(8000)
	for frame := 0; frame < 3; frame++ {
		input := make([]int16, 160)
		for i := range input {
			input[i] = int16(2000 * math.Sin(2*math.Pi*float64(i+frame*160)/160.0))
		}
		encoded := cp.Encode(input)
		require.NotEmpty(t, encoded, "frame %d: encoded should not be empty", frame)
		decoded := cp.Decode(encoded)
		require.Len(t, decoded, 160, "frame %d: decoded should be 160 samples", frame)
		// ADPCM has settling time; check mid-frame samples after first frame.
		if frame > 0 {
			for i := 40; i < 120; i++ {
				diff := math.Abs(float64(decoded[i]) - float64(input[i]))
				assert.LessOrEqual(t, diff, float64(2000),
					"frame %d sample %d: input=%d decoded=%d", frame, i, input[i], decoded[i])
			}
		}
	}
}

func TestG722_PayloadType(t *testing.T) {
	cp := newG722Processor(8000)
	assert.Equal(t, uint8(9), cp.PayloadType())
	assert.Equal(t, uint32(8000), cp.ClockRate())
	assert.Equal(t, uint32(160), cp.SamplesPerFrame())
}

// --- Factory tests ---

func TestCodecProcessor_PCMU_Interface(t *testing.T) {
	cp := NewCodecProcessor(0, 8000)
	require.NotNil(t, cp)
	assert.Equal(t, uint8(0), cp.PayloadType())
	assert.Equal(t, uint32(8000), cp.ClockRate())
	assert.Equal(t, uint32(160), cp.SamplesPerFrame())
}

func TestCodecProcessor_PCMA_Interface(t *testing.T) {
	cp := NewCodecProcessor(8, 8000)
	require.NotNil(t, cp)
	assert.Equal(t, uint8(8), cp.PayloadType())
	assert.Equal(t, uint32(8000), cp.ClockRate())
	assert.Equal(t, uint32(160), cp.SamplesPerFrame())
}

func TestCodecProcessor_Unknown_ReturnsNil(t *testing.T) {
	assert.Nil(t, NewCodecProcessor(99, 8000), "PT=99 should return nil")
}
