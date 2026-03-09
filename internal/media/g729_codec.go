//go:build g729

package media

/*
#cgo pkg-config: libbcg729
#include <bcg729/decoder.h>
#include <bcg729/encoder.h>
#include <stdlib.h>
*/
import "C"
import (
	"runtime"
	"unsafe"
)

// g729Processor wraps bcg729 for G.729 Annex A encoding/decoding.
// G.729 operates on 10ms frames (80 samples → 10 bytes). Since the
// media pipeline uses 20ms frames (160 samples), each Encode/Decode
// call processes two consecutive G.729 frames.
type g729Processor struct {
	enc *C.bcg729EncoderChannelContextStruct
	dec *C.bcg729DecoderChannelContextStruct
}

const (
	g729FrameSamples = 80 // 10ms at 8kHz
	g729FrameBytes   = 10 // compressed frame size
)

func newG729Processor() CodecProcessor {
	enc := C.initBcg729EncoderChannel(0) // VAD disabled
	if enc == nil {
		return nil
	}
	dec := C.initBcg729DecoderChannel()
	if dec == nil {
		C.closeBcg729EncoderChannel(enc)
		return nil
	}
	p := &g729Processor{enc: enc, dec: dec}
	runtime.SetFinalizer(p, func(p *g729Processor) {
		C.closeBcg729DecoderChannel(p.dec)
		C.closeBcg729EncoderChannel(p.enc)
	})
	return p
}

func (p *g729Processor) Decode(payload []byte) []int16 {
	nFrames := len(payload) / g729FrameBytes
	if nFrames == 0 {
		return make([]int16, g729FrameSamples*2) // silence
	}
	out := make([]int16, nFrames*g729FrameSamples)
	for i := 0; i < nFrames; i++ {
		C.bcg729Decoder(
			p.dec,
			(*C.uint8_t)(unsafe.Pointer(&payload[i*g729FrameBytes])),
			C.uint8_t(g729FrameBytes), // bitStreamLength
			0,                         // frameErasureFlag
			0,                         // SIDFrameFlag
			0,                         // rfc3389PayloadFlag
			(*C.int16_t)(unsafe.Pointer(&out[i*g729FrameSamples])),
		)
	}
	return out
}

func (p *g729Processor) Encode(samples []int16) []byte {
	nFrames := len(samples) / g729FrameSamples
	if nFrames == 0 {
		return []byte{}
	}
	out := make([]byte, nFrames*g729FrameBytes)
	for i := 0; i < nFrames; i++ {
		var bsLen C.uint8_t
		C.bcg729Encoder(
			p.enc,
			(*C.int16_t)(unsafe.Pointer(&samples[i*g729FrameSamples])),
			(*C.uint8_t)(unsafe.Pointer(&out[i*g729FrameBytes])),
			&bsLen,
		)
	}
	return out
}

func (p *g729Processor) PayloadType() uint8      { return 18 }
func (p *g729Processor) ClockRate() uint32       { return 8000 }
func (p *g729Processor) SamplesPerFrame() uint32 { return 160 }
