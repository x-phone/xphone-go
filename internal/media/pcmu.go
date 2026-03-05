package media

// G.711 mu-law codec (ITU-T G.711, payload type 0).

const (
	muLawBias = 0x84  // 132
	muLawClip = 32635 // max encodable magnitude
)

// muLawDecodeTable maps each mu-law byte to its 16-bit linear PCM value.
var muLawDecodeTable [256]int16

// muLawExpLut maps (biased_sample >> 7) to the exponent (0–7) for encoding.
var muLawExpLut [256]int

func init() {
	// Build decode table from ITU G.711 mu-law specification.
	for i := 0; i < 256; i++ {
		b := byte(i) ^ 0xFF // complement
		t := int(b&0x0F)<<3 + muLawBias
		t <<= uint(b>>4) & 7
		if b&0x80 != 0 {
			muLawDecodeTable[i] = int16(muLawBias - t)
		} else {
			muLawDecodeTable[i] = int16(t - muLawBias)
		}
	}

	// Build exponent lookup table (floor of log2).
	for i := 1; i < 256; i++ {
		val := i
		exp := 0
		for val > 1 {
			val >>= 1
			exp++
		}
		if exp > 7 {
			exp = 7
		}
		muLawExpLut[i] = exp
	}
}

type pcmuProcessor struct{}

func (p *pcmuProcessor) Decode(payload []byte) []int16 {
	samples := make([]int16, len(payload))
	for i, b := range payload {
		samples[i] = muLawDecodeTable[b]
	}
	return samples
}

func (p *pcmuProcessor) Encode(samples []int16) []byte {
	out := make([]byte, len(samples))
	for i, s := range samples {
		out[i] = encodeMuLaw(s)
	}
	return out
}

func (p *pcmuProcessor) PayloadType() uint8     { return 0 }
func (p *pcmuProcessor) ClockRate() uint32      { return 8000 }
func (p *pcmuProcessor) SamplesPerFrame() uint32 { return 160 }

// encodeMuLaw converts a 16-bit linear PCM sample to mu-law.
func encodeMuLaw(sample int16) byte {
	s := int(sample)
	sign := (s >> 8) & 0x80
	if sign != 0 {
		s = -s
	}
	if s > muLawClip {
		s = muLawClip
	}
	s += muLawBias

	exp := muLawExpLut[(s>>7)&0xFF]
	mantissa := (s >> (exp + 3)) & 0x0F

	return byte(^(sign | (exp << 4) | mantissa))
}
