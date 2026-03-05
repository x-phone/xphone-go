package xphone

import (
	"crypto/rand"
	"encoding/binary"
	"time"

	"github.com/pion/rtp"
	"github.com/x-phone/xphone-go/internal/media"
)

// Default media configuration values.
const (
	defaultMediaTimeout = 30 * time.Second
	defaultJitterDepth  = 50 * time.Millisecond
	defaultPCMRate      = 8000
)

// clonePacket returns a deep copy of an RTP packet so taps are independent.
func clonePacket(pkt *rtp.Packet) *rtp.Packet {
	clone := &rtp.Packet{Header: pkt.Header}
	if pkt.Payload != nil {
		clone.Payload = make([]byte, len(pkt.Payload))
		copy(clone.Payload, pkt.Payload)
	}
	return clone
}

// sendDropOldest sends pkt to ch; if full, drains one oldest entry first.
func sendDropOldest(ch chan *rtp.Packet, pkt *rtp.Packet) {
	select {
	case ch <- pkt:
	default:
		<-ch // drop oldest
		ch <- pkt
	}
}

// sendDropOldestPCM sends samples to ch; if full, drains one oldest entry first.
func sendDropOldestPCM(ch chan []int16, samples []int16) {
	select {
	case ch <- samples:
	default:
		<-ch // drop oldest
		ch <- samples
	}
}

// drainJB pops all depth-expired packets from the jitter buffer and fans
// them out to rtpReader and pcmReader.
func (c *call) drainJB(jb *media.JitterBuffer, cp media.CodecProcessor) {
	for {
		pkt := jb.Pop()
		if pkt == nil {
			return
		}
		sendDropOldest(c.rtpReader, clonePacket(pkt))
		if len(pkt.Payload) > 0 && cp != nil {
			sendDropOldestPCM(c.pcmReader, cp.Decode(pkt.Payload))
		}
	}
}

// randUint32 returns a cryptographically random uint32 for RTP SSRC.
func randUint32() uint32 {
	var b [4]byte
	rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}

// startMedia initializes the media pipeline (jitter buffer, RTP demux,
// media timeout timer, codec dispatch, outbound encoding).
func (c *call) startMedia() {
	c.mu.Lock()
	timeout := c.mediaTimeout
	if timeout == 0 {
		timeout = defaultMediaTimeout
	}
	codec := c.codec
	c.mediaDone = make(chan struct{})
	c.mediaActive = true
	done := c.mediaDone
	c.mu.Unlock()

	jb := media.NewJitterBuffer(defaultJitterDepth)
	cp := media.NewCodecProcessor(int(codec), defaultPCMRate)

	// Outbound RTP state for PCMWriter encode path.
	var outSeq uint16
	var outTimestamp uint32
	outSSRC := randUint32()
	var rtpWriterUsed bool

	go func() {
		mediaTimer := time.NewTimer(timeout)
		defer mediaTimer.Stop()
		jitterTick := time.NewTicker(5 * time.Millisecond)
		defer jitterTick.Stop()

		resetTimer := func() {
			if !mediaTimer.Stop() {
				select {
				case <-mediaTimer.C:
				default:
				}
			}
			mediaTimer.Reset(timeout)
		}

		for {
			select {
			case <-done:
				return

			case pkt := <-c.rtpInbound:
				sendDropOldest(c.rtpRawReader, clonePacket(pkt))

				// DTMF dispatch: intercept PT=101 before jitter buffer.
				if pkt.PayloadType == DTMFPayloadType {
					if ev := DecodeDTMF(pkt.Payload); ev != nil && ev.End {
						c.mu.Lock()
						fn := c.onDTMFFn
						c.mu.Unlock()
						if fn != nil {
							go fn(ev.Digit)
						}
					}
					resetTimer()
					continue
				}

				jb.Push(pkt)
				resetTimer()
				c.drainJB(jb, cp)

			case <-jitterTick.C:
				c.drainJB(jb, cp)

			case pkt := <-c.rtpWriter:
				rtpWriterUsed = true
				if c.sentRTP != nil {
					sendDropOldest(c.sentRTP, pkt)
				}

			case pcmFrame := <-c.pcmWriter:
				if rtpWriterUsed || cp == nil {
					continue
				}
				encoded := cp.Encode(pcmFrame)
				outPkt := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    cp.PayloadType(),
						SequenceNumber: outSeq,
						Timestamp:      outTimestamp,
						SSRC:           outSSRC,
					},
					Payload: encoded,
				}
				outSeq++
				outTimestamp += cp.SamplesPerFrame()
				if c.sentRTP != nil {
					sendDropOldest(c.sentRTP, outPkt)
				}

			case <-mediaTimer.C:
				c.mu.Lock()
				if c.state == StateOnHold {
					c.mu.Unlock()
					mediaTimer.Reset(timeout)
					continue
				}
				c.state = StateEnded
				c.mediaActive = false
				fn := c.onEndedFn
				c.mu.Unlock()
				if fn != nil {
					go fn(EndedByTimeout)
				}
				return
			}
		}
	}()
}

// stopMedia tears down the media pipeline and releases RTP ports.
func (c *call) stopMedia() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mediaDone != nil {
		select {
		case <-c.mediaDone:
			// already closed
		default:
			close(c.mediaDone)
		}
	}
	c.mediaActive = false
}

// resetMediaTimer resets the media timeout timer. Called on each received
// RTP packet. Timer is managed inside the media goroutine.
func (c *call) resetMediaTimer() {
}
