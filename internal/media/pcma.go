package media

// G.711 A-law codec (ITU-T G.711, payload type 8).

// aLawDecodeTable maps each A-law byte to its 16-bit linear PCM value.
var aLawDecodeTable [256]int16

// aLawSegEnd defines segment boundaries for A-law encoding (13-bit domain).
var aLawSegEnd = [8]int{0x1F, 0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF}

func init() {
	// Build decode table from ITU G.711 A-law specification.
	for i := 0; i < 256; i++ {
		a := byte(i) ^ 0x55
		t := int(a&0x0F) << 4
		seg := int(a&0x70) >> 4
		switch seg {
		case 0:
			t += 8
		case 1:
			t += 0x108
		default:
			t += 0x108
			t <<= uint(seg - 1)
		}
		if a&0x80 != 0 {
			aLawDecodeTable[i] = int16(t)
		} else {
			aLawDecodeTable[i] = int16(-t)
		}
	}
}

type pcmaProcessor struct{}

func (p *pcmaProcessor) Decode(payload []byte) []int16 {
	samples := make([]int16, len(payload))
	for i, b := range payload {
		samples[i] = aLawDecodeTable[b]
	}
	return samples
}

func (p *pcmaProcessor) Encode(samples []int16) []byte {
	out := make([]byte, len(samples))
	for i, s := range samples {
		out[i] = encodeALaw(s)
	}
	return out
}

func (p *pcmaProcessor) PayloadType() uint8      { return 8 }
func (p *pcmaProcessor) ClockRate() uint32       { return 8000 }
func (p *pcmaProcessor) SamplesPerFrame() uint32 { return 160 }

// encodeALaw converts a 16-bit linear PCM sample to A-law.
func encodeALaw(sample int16) byte {
	pcmVal := int(sample) >> 3 // scale 16-bit to 13-bit
	var mask byte
	if pcmVal >= 0 {
		mask = 0xD5
	} else {
		mask = 0x55
		pcmVal = -pcmVal - 1
	}

	// Find segment.
	seg := 0
	for seg < 8 {
		if pcmVal <= aLawSegEnd[seg] {
			break
		}
		seg++
	}

	if seg >= 8 {
		return 0x7F ^ mask
	}

	var aval byte
	if seg < 2 {
		aval = byte((pcmVal >> 1) & 0x0F)
	} else {
		aval = byte((pcmVal >> uint(seg)) & 0x0F)
	}

	return (byte(seg<<4) | aval) ^ mask
}
