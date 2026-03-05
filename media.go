package xphone

import (
	"time"

	"github.com/pion/rtp"
	"github.com/x-phone/xphone-go/internal/media"
)

// Default media configuration values.
const (
	defaultMediaTimeout = 30 * time.Second
	defaultJitterDepth  = 50 * time.Millisecond
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

// decodePCMU trivially decodes PCMU payload to int16 samples.
// Real mu-law expansion is Phase 3; here each byte maps to int16.
func decodePCMU(payload []byte) []int16 {
	samples := make([]int16, len(payload))
	for i, b := range payload {
		samples[i] = int16(b)
	}
	return samples
}

// drainJB pops all depth-expired packets from the jitter buffer and fans
// them out to rtpReader and pcmReader.
func (c *call) drainJB(jb *media.JitterBuffer) {
	for {
		pkt := jb.Pop()
		if pkt == nil {
			return
		}
		sendDropOldest(c.rtpReader, clonePacket(pkt))
		if len(pkt.Payload) > 0 {
			sendDropOldestPCM(c.pcmReader, decodePCMU(pkt.Payload))
		}
	}
}

// startMedia initializes the media pipeline (jitter buffer, RTP demux,
// media timeout timer).
func (c *call) startMedia() {
	c.mu.Lock()
	timeout := c.mediaTimeout
	if timeout == 0 {
		timeout = defaultMediaTimeout
	}
	c.mediaDone = make(chan struct{})
	c.mediaActive = true
	done := c.mediaDone
	c.mu.Unlock()

	jb := media.NewJitterBuffer(defaultJitterDepth)

	go func() {
		mediaTimer := time.NewTimer(timeout)
		defer mediaTimer.Stop()
		jitterTick := time.NewTicker(5 * time.Millisecond)
		defer jitterTick.Stop()

		for {
			select {
			case <-done:
				return

			case pkt := <-c.rtpInbound:
				sendDropOldest(c.rtpRawReader, clonePacket(pkt))
				jb.Push(pkt)
				// Reset media timer on inbound RTP.
				if !mediaTimer.Stop() {
					select {
					case <-mediaTimer.C:
					default:
					}
				}
				mediaTimer.Reset(timeout)
				c.drainJB(jb)

			case <-jitterTick.C:
				c.drainJB(jb)

			case pkt := <-c.rtpWriter:
				if c.sentRTP != nil {
					sendDropOldest(c.sentRTP, pkt)
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
