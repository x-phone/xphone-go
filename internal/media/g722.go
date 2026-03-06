package media

import "github.com/gotranspile/g722"

// G.722 codec wrapper (ITU-T G.722, payload type 9).
// Wraps gotranspile/g722 encoder/decoder (stateful ADPCM).
//
// G.722 operates at 16kHz internally. At pcmRate=8000, trivial 2:1
// decimation/upsampling is used. Full sinc resampling deferred to Phase 3.5.

type g722Processor struct {
	enc     *g722.Encoder
	dec     *g722.Decoder
	pcmRate int
}

func newG722Processor(pcmRate int) *g722Processor {
	return &g722Processor{
		enc:     g722.NewEncoder(64000, 0), // 64kbps, 16kHz native
		dec:     g722.NewDecoder(64000, 0),
		pcmRate: pcmRate,
	}
}

func (p *g722Processor) Decode(payload []byte) []int16 {
	// G.722 at 64kbps: 160 bytes → 320 samples at 16kHz.
	samples := make([]int16, len(payload)*2)
	n := p.dec.Decode(samples, payload)
	samples = samples[:n]

	if p.pcmRate == 8000 {
		// Decimate 2:1: take every other sample (16kHz → 8kHz).
		out := make([]int16, n/2)
		for i := range out {
			out[i] = samples[i*2]
		}
		return out
	}
	return samples
}

func (p *g722Processor) Encode(samples []int16) []byte {
	input := samples
	if p.pcmRate == 8000 {
		// Upsample 2:1: duplicate each sample (8kHz → 16kHz).
		input = make([]int16, len(samples)*2)
		for i, s := range samples {
			input[i*2] = s
			input[i*2+1] = s
		}
	}
	// Each pair of 16kHz samples encodes to 1 byte.
	dst := make([]byte, len(input)/2)
	n := p.enc.Encode(dst, input)
	return dst[:n]
}

func (p *g722Processor) PayloadType() uint8      { return 9 }
func (p *g722Processor) ClockRate() uint32       { return 8000 }
func (p *g722Processor) SamplesPerFrame() uint32 { return 160 }
