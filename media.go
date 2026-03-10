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
	"github.com/x-phone/xphone-go/internal/stun"
)

// Default media configuration values.
const (
	defaultMediaTimeout     = 30 * time.Second
	defaultHoldMediaTimeout = 5 * time.Minute
	defaultJitterDepth      = 50 * time.Millisecond
	defaultPCMRate          = 8000
)

// mediaStream manages the RTP pipeline for a single media stream (audio or video).
// The call struct owns the channels and sockets; mediaStream reads/writes them via
// its back-pointer during run(). Per-stream mutable state (muted, RTP counters) lives here.
type mediaStream struct {
	call *call // back-pointer for shared state (channels, sockets, SRTP, callbacks)

	// Per-stream mute flag (protected by call.mu).
	muted bool

	// Pipeline config (set by startMedia before launching run goroutine).
	timeout        time.Duration
	conn           net.PacketConn
	rtcpConn       net.PacketConn
	rtcpRemoteAddr net.Addr
	done           chan struct{}

	// Goroutine-local pipeline state (only accessed by run goroutine — no lock needed).
	outSeq            uint16
	outTimestamp      uint32
	outSSRC           uint32
	rtpWriterUsed     bool
	inboundCount      int
	lastDTMFTimestamp uint32
	lastDTMFSeen      bool
}

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
// them out to rtpReader and pcmReader on the owning call.
func (s *mediaStream) drainJB(jb *media.JitterBuffer, cp media.CodecProcessor) {
	c := s.call
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

// run is the media pipeline goroutine for a single stream.
// It handles: inbound RTP demux, jitter buffer, DTMF interception, codec encode/decode,
// outbound RTP forwarding, RTCP, and media timeout.
// Pipeline config must be set on the struct before calling run.
func (s *mediaStream) run(jb *media.JitterBuffer, cp media.CodecProcessor, rtpClockRate uint32) {
	c := s.call
	timeout := s.timeout
	conn := s.conn
	rtcpConn := s.rtcpConn
	rtcpRemoteAddr := s.rtcpRemoteAddr
	done := s.done

	mediaTimer := time.NewTimer(timeout)
	defer mediaTimer.Stop()
	jitterTick := time.NewTicker(5 * time.Millisecond)
	defer jitterTick.Stop()
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

	// RTCP reader goroutine.
	rtcpInbound := make(chan []byte, 16)
	if rtcpConn != nil {
		go func() {
			buf := make([]byte, 1500)
			for {
				n, _, err := rtcpConn.ReadFrom(buf)
				if err != nil {
					return
				}
				if n < 8 {
					continue
				}
				cp := make([]byte, n)
				copy(cp, buf[:n])
				select {
				case rtcpInbound <- cp:
				default:
				}
			}
		}()
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
			s.inboundCount++
			if s.inboundCount == 1 {
				c.logger.Debug("first RTP packet received",
					"id", c.id, "pt", pkt.PayloadType, "ssrc", pkt.SSRC,
					"seq", pkt.SequenceNumber, "payload_len", len(pkt.Payload))
			}
			sendDropOldest(c.rtpRawReader, clonePacket(pkt))

			// DTMF dispatch: intercept PT=101 before jitter buffer.
			if pkt.PayloadType == DTMFPayloadType {
				if ev := DecodeDTMF(pkt.Payload); ev != nil && ev.End && !(s.lastDTMFSeen && pkt.Timestamp == s.lastDTMFTimestamp) {
					s.lastDTMFTimestamp = pkt.Timestamp
					s.lastDTMFSeen = true
					c.mu.Lock()
					mode := c.dtmfMode
					c.mu.Unlock()
					if mode != DtmfSipInfo {
						c.fireOnDTMF(ev.Digit)
					}
				}
				resetTimer(timeout)
				continue
			}

			rtcpStats.RecordRTPReceived(pkt.SequenceNumber, pkt.Timestamp, pkt.SSRC, rtpClockRate)
			jb.Push(pkt)
			resetTimer(timeout)
			s.drainJB(jb, cp)

		case <-jitterTick.C:
			s.drainJB(jb, cp)

		case pkt := <-c.rtpWriter:
			if !s.rtpWriterUsed {
				c.logger.Debug("first outbound RTP (raw writer)", "id", c.id, "pt", pkt.PayloadType)
			}
			s.rtpWriterUsed = true
			c.mu.Lock()
			muted := s.muted
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
			if s.rtpWriterUsed || cp == nil {
				continue
			}
			c.mu.Lock()
			muted := s.muted
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
					SequenceNumber: s.outSeq,
					Timestamp:      s.outTimestamp,
					SSRC:           s.outSSRC,
				},
				Payload: encoded,
			}
			s.outSeq++
			s.outTimestamp += cp.SamplesPerFrame()
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
				c.mu.Lock()
				srtcpOut := c.srtpOut
				c.mu.Unlock()
				sr := rtcp.BuildSR(s.outSSRC, rtcpStats)
				if srtcpOut != nil {
					var err error
					sr, err = srtcpOut.ProtectRTCP(sr)
					if err != nil {
						continue
					}
				}
				rtcpConn.WriteTo(sr, rtcpRemoteAddr)
			}

		case data := <-rtcpInbound:
			c.mu.Lock()
			srtcpIn := c.srtpIn
			c.mu.Unlock()
			if srtcpIn != nil {
				var err error
				data, err = srtcpIn.UnprotectRTCP(data)
				if err != nil {
					continue
				}
			}
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
}

// startRTPReader launches a goroutine that reads RTP packets from the
// network socket (rtpConn) and feeds them into the media pipeline's
// rtpInbound channel. The goroutine exits when rtpConn is closed.
func (c *call) startRTPReader() {
	c.mu.Lock()
	conn := c.rtpConn
	iceAgent := c.iceAgent // immutable after call setup
	c.mu.Unlock()
	if conn == nil {
		return
	}

	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return // socket closed
			}
			// Copy before unmarshal since we reuse the read buffer.
			cp := make([]byte, n)
			copy(cp, buf[:n])

			// ICE STUN demux: intercept Binding Requests before RTP processing.
			if iceAgent != nil && stun.IsMessage(cp) {
				fromUDP, ok := from.(*net.UDPAddr)
				if ok {
					if resp := iceAgent.HandleBindingRequest(cp, fromUDP); resp != nil {
						conn.WriteTo(resp, from)
					}
				}
				continue
			}

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

	s := c.audioStream
	// Set pipeline config on stream and reset goroutine-local state.
	s.timeout = timeout
	s.conn = conn
	s.rtcpConn = rtcpConn
	s.rtcpRemoteAddr = rtcpRemoteAddr
	s.done = done
	s.outSeq = 0
	s.outTimestamp = 0
	s.rtpWriterUsed = false
	s.inboundCount = 0
	s.lastDTMFTimestamp = 0
	s.lastDTMFSeen = false
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

	go s.run(jb, cp, rtpClockRate)
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
