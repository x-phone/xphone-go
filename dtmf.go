package xphone

import (
	"encoding/binary"

	"github.com/pion/rtp"
)

// DTMFPayloadType is the RTP payload type for DTMF events (RFC 4733).
const DTMFPayloadType = 101

// DTMFEvent represents a decoded DTMF event from an RTP packet.
type DTMFEvent struct {
	Digit    string
	Duration uint16
	End      bool
	Volume   uint8
}

var dtmfDigits = map[string]int{
	"0": 0, "1": 1, "2": 2, "3": 3, "4": 4,
	"5": 5, "6": 6, "7": 7, "8": 8, "9": 9,
	"*": 10, "#": 11,
	"A": 12, "B": 13, "C": 14, "D": 15,
}

var dtmfCodes = map[int]string{
	0: "0", 1: "1", 2: "2", 3: "3", 4: "4",
	5: "5", 6: "6", 7: "7", 8: "8", 9: "9",
	10: "*", 11: "#",
	12: "A", 13: "B", 14: "C", 15: "D",
}

// DTMFDigitCode returns the RFC 4733 event code for a digit string.
// Returns -1 if the digit is invalid.
func DTMFDigitCode(digit string) int {
	code, ok := dtmfDigits[digit]
	if !ok {
		return -1
	}
	return code
}

// DTMFCodeDigit returns the digit string for an RFC 4733 event code.
// Returns "" if the code is unknown.
func DTMFCodeDigit(code int) string {
	return dtmfCodes[code]
}

// EncodeDTMF encodes a DTMF digit into a sequence of RTP packets (RFC 4733).
func EncodeDTMF(digit string, ts uint32, seq uint16, ssrc uint32) ([]*rtp.Packet, error) {
	code := DTMFDigitCode(digit)
	if code < 0 {
		return nil, ErrInvalidDTMFDigit
	}

	const volume = 10
	durations := []uint16{160, 320, 320}
	pkts := make([]*rtp.Packet, 3)

	for i := range pkts {
		endBit := byte(0)
		if i == 2 {
			endBit = 0x80
		}
		payload := make([]byte, 4)
		payload[0] = byte(code)
		payload[1] = endBit | volume
		binary.BigEndian.PutUint16(payload[2:4], durations[i])

		pkts[i] = &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         i == 0,
				PayloadType:    DTMFPayloadType,
				SequenceNumber: seq + uint16(i),
				Timestamp:      ts,
				SSRC:           ssrc,
			},
			Payload: payload,
		}
	}
	return pkts, nil
}

// DecodeDTMF decodes a DTMF event from an RTP payload.
// Returns nil if the payload is less than 4 bytes.
func DecodeDTMF(payload []byte) *DTMFEvent {
	if len(payload) < 4 {
		return nil
	}
	code := int(payload[0])
	digit := DTMFCodeDigit(code)
	if digit == "" {
		return nil
	}
	return &DTMFEvent{
		Digit:    digit,
		End:      payload[1]&0x80 != 0,
		Volume:   payload[1] & 0x3F,
		Duration: binary.BigEndian.Uint16(payload[2:4]),
	}
}
