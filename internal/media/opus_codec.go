//go:build opus

package media

import "gopkg.in/hraban/opus.v2"

// opusProcessor wraps libopus for 8kHz mono VoIP encoding/decoding.
// Opus natively supports 8kHz — no resampling needed.
// Per RFC 7587, RTP timestamps use 48kHz clock regardless of sample rate.
type opusProcessor struct {
	enc *opus.Encoder
	dec *opus.Decoder
}

func newOpusProcessor() CodecProcessor {
	enc, err := opus.NewEncoder(8000, 1, opus.AppVoIP)
	if err != nil {
		return nil
	}
	dec, err := opus.NewDecoder(8000, 1)
	if err != nil {
		return nil
	}
	return &opusProcessor{enc: enc, dec: dec}
}

func (p *opusProcessor) Decode(payload []byte) []int16 {
	// 480 samples handles up to 60ms frames at 8kHz (max Opus frame duration).
	pcm := make([]int16, 480)
	n, err := p.dec.Decode(payload, pcm)
	if err != nil || n == 0 {
		return make([]int16, 160) // silence on error
	}
	return pcm[:n]
}

func (p *opusProcessor) Encode(samples []int16) []byte {
	buf := make([]byte, 256)
	n, err := p.enc.Encode(samples, buf)
	if err != nil {
		return []byte{}
	}
	return buf[:n]
}

func (p *opusProcessor) PayloadType() uint8 { return 111 }

// ClockRate returns 48000 per RFC 7587 — RTP timestamps always use 48kHz
// clock for Opus, regardless of the actual encoder sample rate.
func (p *opusProcessor) ClockRate() uint32 { return 48000 }

// SamplesPerFrame returns 960 (20ms at 48kHz clock rate) for RTP timestamp
// advancement. The actual PCM frame is 160 samples at 8kHz.
func (p *opusProcessor) SamplesPerFrame() uint32 { return 960 }
