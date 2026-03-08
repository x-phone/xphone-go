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

// startRTPReader launches a goroutine that reads RTP packets from the
// network socket (rtpConn) and feeds them into the media pipeline's
// rtpInbound channel. The goroutine exits when rtpConn is closed.
func (c *call) startRTPReader() {
	c.mu.Lock()
	conn := c.rtpConn
	c.mu.Unlock()
	if conn == nil {
		return
	}

	c.mu.Lock()
	srtpIn := c.srtpIn
	c.mu.Unlock()

	go func() {
		buf := make([]byte, 1500)
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				return // socket closed
			}
			// Copy before unmarshal since we reuse the read buffer.
			cp := make([]byte, n)
			copy(cp, buf[:n])

			// SRTP decrypt before unmarshal.
			if srtpIn != nil {
				cp, err = srtpIn.Unprotect(cp)
				if err != nil {
					continue // auth failed or malformed — drop
				}
			}

			pkt := &rtp.Packet{}
			if err := pkt.Unmarshal(cp); err != nil {
				continue
			}
			sendDropOldest(c.rtpInbound, pkt)
		}
	}()
}

// startMedia initializes the media pipeline (jitter buffer, RTP demux,
// media timeout timer, codec dispatch, outbound encoding).
func (c *call) startMedia() {
	c.mu.Lock()
	if c.mediaDone != nil {
		c.mu.Unlock()
		return // already running
	}
	timeout := c.mediaTimeout
	if timeout == 0 {
		timeout = defaultMediaTimeout
	}
	jitterDepth := c.jitterDepth
	if jitterDepth == 0 {
		jitterDepth = defaultJitterDepth
	}
	pcmRate := c.pcmRate
	if pcmRate == 0 {
		pcmRate = defaultPCMRate
	}
	codec := c.codec
	srtpOut := c.srtpOut
	c.mediaDone = make(chan struct{})
	c.mediaActive = true
	done := c.mediaDone
	conn := c.rtpConn
	c.mu.Unlock()

	jb := media.NewJitterBuffer(jitterDepth)
	cp := media.NewCodecProcessor(int(codec), pcmRate)

	c.logger.Debug("media pipeline started",
		"id", c.id, "codec", int(codec), "pcm_rate", pcmRate,
		"jitter_depth", jitterDepth, "media_timeout", timeout,
		"local_addr", conn.LocalAddr().String())

	// Outbound RTP state for PCMWriter encode path.
	var outSeq uint16
	var outTimestamp uint32
	outSSRC := randUint32()
	var rtpWriterUsed bool
	var inboundCount int
	var lastDTMFTimestamp uint32 // dedup RFC 4733 redundant end events
	var lastDTMFSeen bool

	go func() {
		mediaTimer := time.NewTimer(timeout)
		defer mediaTimer.Stop()
		jitterTick := time.NewTicker(5 * time.Millisecond)
		defer jitterTick.Stop()
		// Close output channels so consumers unblock on call end.
		defer c.closeOutputChannels()

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
				inboundCount++
				if inboundCount == 1 {
					c.logger.Debug("first RTP packet received",
						"id", c.id, "pt", pkt.PayloadType, "ssrc", pkt.SSRC,
						"seq", pkt.SequenceNumber, "payload_len", len(pkt.Payload))
				}
				sendDropOldest(c.rtpRawReader, clonePacket(pkt))

				// DTMF dispatch: intercept PT=101 before jitter buffer.
				if pkt.PayloadType == DTMFPayloadType {
					if ev := DecodeDTMF(pkt.Payload); ev != nil && ev.End && !(lastDTMFSeen && pkt.Timestamp == lastDTMFTimestamp) {
						lastDTMFTimestamp = pkt.Timestamp
						lastDTMFSeen = true
						c.mu.Lock()
						fn := c.onDTMFFn
						fnPhone := c.onDTMFPhone
						c.mu.Unlock()
						if fnPhone != nil {
							go fnPhone(ev.Digit)
						}
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
				if !rtpWriterUsed {
					c.logger.Debug("first outbound RTP (raw writer)", "id", c.id, "pt", pkt.PayloadType)
				}
				rtpWriterUsed = true
				c.mu.Lock()
				muted := c.muted
				dst := c.remoteAddr
				c.mu.Unlock()
				if muted {
					continue
				}
				if c.sentRTP != nil {
					sendDropOldest(c.sentRTP, pkt)
				}
				if conn != nil && dst != nil {
					if data, err := pkt.Marshal(); err == nil {
						if srtpOut != nil {
							data, err = srtpOut.Protect(data)
							if err != nil {
								continue
							}
						}
						conn.WriteTo(data, dst)
					}
				}

			case pcmFrame := <-c.pcmWriter:
				if rtpWriterUsed || cp == nil {
					continue
				}
				c.mu.Lock()
				muted := c.muted
				dst := c.remoteAddr
				c.mu.Unlock()
				if muted {
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
				if conn != nil && dst != nil {
					if data, err := outPkt.Marshal(); err == nil {
						if srtpOut != nil {
							data, err = srtpOut.Protect(data)
							if err != nil {
								continue
							}
						}
						conn.WriteTo(data, dst)
					}
				}

			case <-mediaTimer.C:
				c.mu.Lock()
				if c.state == StateOnHold {
					c.mu.Unlock()
					mediaTimer.Reset(timeout)
					continue
				}
				c.state = StateEnded
				c.fireOnState(StateEnded)
				c.logger.Warn("media timeout", "id", c.id)
				c.fireOnEnded(EndedByTimeout)
				c.mu.Unlock()
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
