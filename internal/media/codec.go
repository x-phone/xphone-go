package media

// CodecProcessor handles encoding and decoding for a specific audio codec.
type CodecProcessor interface {
	Decode(payload []byte) []int16
	Encode(samples []int16) []byte
	PayloadType() uint8
	ClockRate() uint32
	SamplesPerFrame() uint32
}

// NewCodecProcessor returns a CodecProcessor for the given RTP payload type
// and PCM sample rate. Returns nil for unsupported payload types.
func NewCodecProcessor(payloadType int, pcmRate int) CodecProcessor {
	switch payloadType {
	case 0:
		return &pcmuProcessor{}
	case 8:
		return &pcmaProcessor{}
	case 9:
		return newG722Processor(pcmRate)
	case 111:
		return newOpusProcessor()
	default:
		return nil
	}
}
