package xphone

import (
	"crypto/rand"
	"encoding/binary"
	"net"
	"strconv"
	"time"

	"github.com/pion/rtp"
	"github.com/x-phone/xphone-go/internal/media"
	"github.com/x-phone/xphone-go/internal/rtcp"
)

// Default media configuration values.
const (
	defaultMediaTimeout     = 30 * time.Second
	defaultHoldMediaTimeout = 5 * time.Minute
	defaultJitterDepth      = 50 * time.Millisecond
	defaultPCMRate          = 8000
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

			// Re-read srtpIn each iteration so a re-INVITE key change takes effect.
			c.mu.Lock()
			srtpIn := c.srtpIn
			c.mu.Unlock()
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
	timeout := c.effectiveMediaTimeout()
	jitterDepth := c.jitterDepth
	if jitterDepth == 0 {
		jitterDepth = defaultJitterDepth
	}
	pcmRate := c.pcmRate
	if pcmRate == 0 {
		pcmRate = defaultPCMRate
	}
	codec := c.codec
	c.mediaDone = make(chan struct{})
	c.mediaTimerReset = make(chan time.Duration, 1)
	c.mediaActive = true
	done := c.mediaDone
	conn := c.rtpConn

	// Bind RTCP socket (RTP port + 1). Non-fatal if it fails.
	if conn != nil && c.rtcpConn == nil {
		rtpLocal := conn.LocalAddr().(*net.UDPAddr)
		rtcpAddr := net.JoinHostPort(rtpLocal.IP.String(), strconv.Itoa(rtpLocal.Port+1))
		if rc, err := net.ListenPacket("udp", rtcpAddr); err == nil {
			c.rtcpConn = rc
		} else {
			c.logger.Warn("RTCP port bind failed, RTCP disabled", "rtcp_addr", rtcpAddr, "error", err)
		}
	}
	rtcpConn := c.rtcpConn

	// Compute remote RTCP address (remote RTP port + 1).
	var rtcpRemoteAddr net.Addr
	if c.remotePort > 0 && c.remoteIP != "" {
		rtcpRemoteAddr, _ = net.ResolveUDPAddr("udp", net.JoinHostPort(c.remoteIP, strconv.Itoa(c.remotePort+1)))
	}
	c.mu.Unlock()

	jb := media.NewJitterBuffer(jitterDepth)
	cp := media.NewCodecProcessor(int(codec), pcmRate)

	// Use codec RTP clock rate for RTCP jitter calculation (e.g. 48kHz for Opus).
	rtpClockRate := uint32(pcmRate)
	if cp != nil {
		rtpClockRate = cp.ClockRate()
	}

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

	// Start RTCP reader goroutine if we have an RTCP socket.
	rtcpInbound := make(chan []byte, 16)
	if rtcpConn != nil {
		go func() {
			buf := make([]byte, 1500)
			for {
				n, _, err := rtcpConn.ReadFrom(buf)
				if err != nil {
					return // socket closed
				}
				if n < 8 {
					continue
				}
				cp := make([]byte, n)
				copy(cp, buf[:n])
				select {
				case rtcpInbound <- cp:
				default: // drop if full
				}
			}
		}()
	}

	go func() {
		mediaTimer := time.NewTimer(timeout)
		defer mediaTimer.Stop()
		jitterTick := time.NewTicker(5 * time.Millisecond)
		defer jitterTick.Stop()
		// Close output channels so consumers unblock on call end.
		defer c.closeOutputChannels()

		// RTCP state.
		rtcpStats := rtcp.NewStats()
		var rtcpTick <-chan time.Time
		var rtcpTicker *time.Ticker
		if rtcpConn != nil {
			rtcpTicker = time.NewTicker(time.Duration(rtcp.IntervalSecs) * time.Second)
			rtcpTick = rtcpTicker.C
			defer rtcpTicker.Stop()
		}

		resetTimer := func(d time.Duration) {
			if !mediaTimer.Stop() {
				select {
				case <-mediaTimer.C:
				default:
				}
			}
			mediaTimer.Reset(d)
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
					resetTimer(timeout)
					continue
				}

				rtcpStats.RecordRTPReceived(pkt.SequenceNumber, pkt.Timestamp, pkt.SSRC, rtpClockRate)
				jb.Push(pkt)
				resetTimer(timeout)
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
				srtpOut := c.srtpOut
				c.mu.Unlock()
				if muted {
					continue
				}
				rtcpStats.RecordRTPSent(len(pkt.Payload), pkt.Timestamp)
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
				srtpOut := c.srtpOut
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
				rtcpStats.RecordRTPSent(len(outPkt.Payload), outPkt.Timestamp)
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

			case <-rtcpTick:
				if rtcpConn != nil && rtcpRemoteAddr != nil {
					sr := rtcp.BuildSR(outSSRC, rtcpStats)
					rtcpConn.WriteTo(sr, rtcpRemoteAddr)
				}

			case data := <-rtcpInbound:
				if pkt := rtcp.Parse(data); pkt != nil && pkt.IsSenderReport() {
					rtcpStats.ProcessIncomingSR(pkt.NTPSec, pkt.NTPFrac)
				}

			case d := <-c.mediaTimerReset:
				resetTimer(d)

			case <-mediaTimer.C:
				c.mu.Lock()
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

// effectiveMediaTimeout returns the configured media timeout or the default.
func (c *call) effectiveMediaTimeout() time.Duration {
	if c.mediaTimeout > 0 {
		return c.mediaTimeout
	}
	return defaultMediaTimeout
}

// signalMediaTimerReset sends a timer reset request to the media goroutine.
// Non-blocking: drops the signal if the channel is full (media goroutine will
// pick it up on the next iteration).
func (c *call) signalMediaTimerReset(d time.Duration) {
	if c.mediaTimerReset == nil {
		return
	}
	// Drain any pending signal so the latest value always wins.
	select {
	case <-c.mediaTimerReset:
	default:
	}
	c.mediaTimerReset <- d
}
