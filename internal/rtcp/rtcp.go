// Package rtcp implements basic RTCP (RFC 3550) Sender/Receiver Reports
// for trunk compatibility. Most SIP trunks expect periodic RTCP traffic
// and may tear down calls if none is received.
package rtcp

import (
	"encoding/binary"
	"time"
)

const (
	ptSR      = 200 // Sender Report (RFC 3550 §6.4.1)
	ptRR      = 201 // Receiver Report (RFC 3550 §6.4.2)
	ptPSFB    = 206 // Payload-specific feedback (RFC 4585)
	rtcpVer   = 2   // RTCP version (matches RTP)
	reportLen = 24  // bytes per report block

	// NTPEpochOffset is seconds between 1900-01-01 and 1970-01-01.
	ntpEpochOffset = 2_208_988_800

	// IntervalSecs is the minimum RTCP send interval (RFC 3550 §6.2).
	IntervalSecs = 5

	// PLI FMT (RFC 4585 §6.3.1).
	fmtPLI = 1
	// FIR FMT (RFC 5104 §4.3.1.1).
	fmtFIR = 4
)

// Stats tracks RTP statistics for RTCP report generation.
type Stats struct {
	// Outbound (for SR sender info).
	PacketsSent      uint32
	OctetsSent       uint32
	LastRTPTimestamp uint32

	// Inbound (for RR report block).
	PacketsReceived uint32
	RemoteSSRC      uint32

	// Sequence tracking for loss calculation.
	baseSeq        uint16
	maxSeq         uint16
	cycles         uint32
	seqInitialized bool

	// Jitter calculation (RFC 3550 A.8).
	jitter            float64
	prevTransit       int64
	jitterInitialized bool
	baseline          time.Time // monotonic baseline for RTP clock conversion

	// Loss fraction between RR intervals.
	expectedPrior uint32
	receivedPrior uint32

	// Round-trip time: middle 32 bits of last received SR NTP timestamp.
	lastSRNTPMiddle uint32
	lastSRRecvTime  time.Time
	lastSRReceived  bool
}

// NewStats creates a new stats tracker.
func NewStats() *Stats {
	return &Stats{baseline: time.Now()}
}

// RecordRTPSent records an outbound RTP packet for SR sender info.
func (s *Stats) RecordRTPSent(payloadLen int, rtpTimestamp uint32) {
	s.PacketsSent++
	s.OctetsSent += uint32(payloadLen)
	s.LastRTPTimestamp = rtpTimestamp
}

// RecordRTPReceived records an inbound RTP packet for RR report block.
func (s *Stats) RecordRTPReceived(seq uint16, timestamp uint32, ssrc uint32, clockRate uint32) {
	s.PacketsReceived++
	s.RemoteSSRC = ssrc

	if !s.seqInitialized {
		s.baseSeq = seq
		s.maxSeq = seq
		s.seqInitialized = true
	} else {
		udelta := seq - s.maxSeq
		if udelta < 0x8000 {
			if seq < s.maxSeq {
				s.cycles++
			}
			s.maxSeq = seq
		}
	}

	// Jitter calculation per RFC 3550 A.8.
	if clockRate > 0 {
		elapsed := time.Since(s.baseline)
		arrival := int64(elapsed.Seconds() * float64(clockRate))
		transit := arrival - int64(timestamp)
		if s.jitterInitialized {
			d := transit - s.prevTransit
			if d < 0 {
				d = -d
			}
			s.jitter += (float64(d) - s.jitter) / 16.0
		}
		s.prevTransit = transit
		s.jitterInitialized = true
	}
}

// ProcessIncomingSR records a received SR's NTP timestamp for RTT calculation.
func (s *Stats) ProcessIncomingSR(ntpSec, ntpFrac uint32) {
	s.lastSRNTPMiddle = ((ntpSec & 0xFFFF) << 16) | ((ntpFrac >> 16) & 0xFFFF)
	s.lastSRRecvTime = time.Now()
	s.lastSRReceived = true
}

func (s *Stats) extendedMaxSeq() uint32 {
	return (s.cycles << 16) | uint32(s.maxSeq)
}

func (s *Stats) expected() uint32 {
	if !s.seqInitialized {
		return 0
	}
	return s.extendedMaxSeq() - uint32(s.baseSeq) + 1
}

func (s *Stats) cumulativeLost() uint32 {
	exp := s.expected()
	if s.PacketsReceived >= exp {
		return 0
	}
	return exp - s.PacketsReceived
}

func (s *Stats) fractionLost() uint8 {
	exp := s.expected()
	expectedInterval := exp - s.expectedPrior
	receivedInterval := s.PacketsReceived - s.receivedPrior
	s.expectedPrior = exp
	s.receivedPrior = s.PacketsReceived

	if expectedInterval == 0 || receivedInterval >= expectedInterval {
		return 0
	}
	lostInterval := expectedInterval - receivedInterval
	frac := (lostInterval * 256) / expectedInterval
	if frac > 255 {
		return 255
	}
	return uint8(frac)
}

func (s *Stats) delaySinceLastSR() uint32 {
	if !s.lastSRReceived {
		return 0
	}
	elapsed := time.Since(s.lastSRRecvTime)
	secs := uint32(elapsed.Seconds())
	frac := uint32((uint64(elapsed.Nanoseconds()%1e9) * 65536) / 1_000_000_000)
	return (secs << 16) | (frac & 0xFFFF)
}

// NTPNow returns the current time as an NTP timestamp.
func NTPNow() (sec, frac uint32) {
	now := time.Now()
	unix := now.Unix()
	sec = uint32(uint64(unix) + ntpEpochOffset)
	frac = uint32((uint64(now.Nanosecond()) * (1 << 32)) / 1_000_000_000)
	return
}

// BuildSR builds an RTCP Sender Report (RFC 3550 §6.4.1).
// Includes one RR report block if we've received at least one RTP packet.
func BuildSR(ssrc uint32, stats *Stats) []byte {
	hasReport := stats.seqInitialized
	rc := byte(0)
	if hasReport {
		rc = 1
	}

	ntpSec, ntpFrac := NTPNow()

	lengthWords := uint16(6) // 28/4 - 1
	if hasReport {
		lengthWords = 12 // (28+24)/4 - 1
	}
	totalLen := int(lengthWords+1) * 4

	buf := make([]byte, 0, totalLen)

	// Header: V=2, P=0, RC, PT=200.
	buf = append(buf, (rtcpVer<<6)|rc)
	buf = append(buf, ptSR)
	buf = binary.BigEndian.AppendUint16(buf, lengthWords)

	// SSRC.
	buf = binary.BigEndian.AppendUint32(buf, ssrc)

	// NTP timestamp.
	buf = binary.BigEndian.AppendUint32(buf, ntpSec)
	buf = binary.BigEndian.AppendUint32(buf, ntpFrac)

	// RTP timestamp.
	buf = binary.BigEndian.AppendUint32(buf, stats.LastRTPTimestamp)

	// Sender's packet count & octet count.
	buf = binary.BigEndian.AppendUint32(buf, stats.PacketsSent)
	buf = binary.BigEndian.AppendUint32(buf, stats.OctetsSent)

	if hasReport {
		buf = appendReportBlock(buf, stats)
	}

	return buf
}

// BuildRR builds an RTCP Receiver Report (RFC 3550 §6.4.2).
func BuildRR(ssrc uint32, stats *Stats) []byte {
	hasReport := stats.seqInitialized
	rc := byte(0)
	if hasReport {
		rc = 1
	}

	lengthWords := uint16(1) // 8/4 - 1
	if hasReport {
		lengthWords = 7 // (8+24)/4 - 1
	}
	totalLen := int(lengthWords+1) * 4

	buf := make([]byte, 0, totalLen)

	// Header: V=2, P=0, RC, PT=201.
	buf = append(buf, (rtcpVer<<6)|rc)
	buf = append(buf, ptRR)
	buf = binary.BigEndian.AppendUint16(buf, lengthWords)

	// SSRC.
	buf = binary.BigEndian.AppendUint32(buf, ssrc)

	if hasReport {
		buf = appendReportBlock(buf, stats)
	}

	return buf
}

func appendReportBlock(buf []byte, stats *Stats) []byte {
	// SSRC of source being reported.
	buf = binary.BigEndian.AppendUint32(buf, stats.RemoteSSRC)

	fractionLost := stats.fractionLost()
	cumLost := stats.cumulativeLost()
	if cumLost > 0x7FFFFF {
		cumLost = 0x7FFFFF
	}

	// Fraction lost (8 bits) + cumulative lost (24 bits).
	buf = append(buf, fractionLost)
	buf = append(buf, byte((cumLost>>16)&0xFF))
	buf = append(buf, byte((cumLost>>8)&0xFF))
	buf = append(buf, byte(cumLost&0xFF))

	// Extended highest sequence number.
	buf = binary.BigEndian.AppendUint32(buf, stats.extendedMaxSeq())

	// Interarrival jitter.
	buf = binary.BigEndian.AppendUint32(buf, uint32(stats.jitter))

	// Last SR (LSR).
	buf = binary.BigEndian.AppendUint32(buf, stats.lastSRNTPMiddle)

	// Delay since last SR (DLSR).
	buf = binary.BigEndian.AppendUint32(buf, stats.delaySinceLastSR())

	return buf
}

// ReportBlock is a parsed RTCP report block.
type ReportBlock struct {
	SSRC         uint32
	FractionLost uint8
	CumLost      uint32
	HighestSeq   uint32
	Jitter       uint32
	LastSR       uint32
	DelaySinceSR uint32
}

// Packet is a parsed RTCP packet.
type Packet struct {
	Type         uint8 // ptSR or ptRR
	SSRC         uint32
	NTPSec       uint32 // SR only
	NTPFrac      uint32 // SR only
	RTPTimestamp uint32 // SR only
	PacketCount  uint32 // SR only
	OctetCount   uint32 // SR only
	Reports      []ReportBlock
}

// IsSenderReport returns true if the packet is an SR.
func (p *Packet) IsSenderReport() bool { return p.Type == ptSR }

// Parse parses an RTCP packet from raw bytes. Returns nil for unknown types or truncated data.
func Parse(data []byte) *Packet {
	if len(data) < 8 {
		return nil
	}
	version := (data[0] >> 6) & 0x03
	if version != rtcpVer {
		return nil
	}
	rc := data[0] & 0x1F
	pt := data[1]
	ssrc := binary.BigEndian.Uint32(data[4:8])

	switch pt {
	case ptSR:
		if len(data) < 28 {
			return nil
		}
		p := &Packet{
			Type:         ptSR,
			SSRC:         ssrc,
			NTPSec:       binary.BigEndian.Uint32(data[8:12]),
			NTPFrac:      binary.BigEndian.Uint32(data[12:16]),
			RTPTimestamp: binary.BigEndian.Uint32(data[16:20]),
			PacketCount:  binary.BigEndian.Uint32(data[20:24]),
			OctetCount:   binary.BigEndian.Uint32(data[24:28]),
			Reports:      parseReportBlocks(data[28:], rc),
		}
		return p

	case ptRR:
		return &Packet{
			Type:    ptRR,
			SSRC:    ssrc,
			Reports: parseReportBlocks(data[8:], rc),
		}

	default:
		return nil
	}
}

// BuildPLI builds an RTCP Picture Loss Indication (RFC 4585 §6.3.1).
// senderSSRC is our SSRC; mediaSSRC is the remote video SSRC.
func BuildPLI(senderSSRC, mediaSSRC uint32) []byte {
	// PLI: V=2, P=0, FMT=1, PT=206 (PSFB), length=2 (12 bytes total).
	buf := make([]byte, 12)
	buf[0] = (rtcpVer << 6) | fmtPLI
	buf[1] = ptPSFB
	binary.BigEndian.PutUint16(buf[2:4], 2) // length in 32-bit words minus one
	binary.BigEndian.PutUint32(buf[4:8], senderSSRC)
	binary.BigEndian.PutUint32(buf[8:12], mediaSSRC)
	return buf
}

// BuildFIR builds an RTCP Full Intra Request (RFC 5104 §4.3.1.1).
// senderSSRC is our SSRC; mediaSSRC is the remote video SSRC; seqNr is the FIR sequence number.
func BuildFIR(senderSSRC, mediaSSRC uint32, seqNr uint8) []byte {
	// FIR: V=2, P=0, FMT=4, PT=206 (PSFB), length=4 (20 bytes total).
	buf := make([]byte, 20)
	buf[0] = (rtcpVer << 6) | fmtFIR
	buf[1] = ptPSFB
	binary.BigEndian.PutUint16(buf[2:4], 4) // length
	binary.BigEndian.PutUint32(buf[4:8], senderSSRC)
	binary.BigEndian.PutUint32(buf[8:12], 0) // media SSRC field is 0 for FIR
	// FCI: SSRC + seq + reserved
	binary.BigEndian.PutUint32(buf[12:16], mediaSSRC)
	buf[16] = seqNr
	// buf[17:20] reserved (zero)
	return buf
}

func parseReportBlocks(data []byte, count uint8) []ReportBlock {
	blocks := make([]ReportBlock, 0, int(count))
	for i := 0; i < int(count); i++ {
		off := i * reportLen
		if off+reportLen > len(data) {
			break
		}
		b := data[off : off+reportLen]
		blocks = append(blocks, ReportBlock{
			SSRC:         binary.BigEndian.Uint32(b[0:4]),
			FractionLost: b[4],
			CumLost:      uint32(b[5])<<16 | uint32(b[6])<<8 | uint32(b[7]),
			HighestSeq:   binary.BigEndian.Uint32(b[8:12]),
			Jitter:       binary.BigEndian.Uint32(b[12:16]),
			LastSR:       binary.BigEndian.Uint32(b[16:20]),
			DelaySinceSR: binary.BigEndian.Uint32(b[20:24]),
		})
	}
	return blocks
}
