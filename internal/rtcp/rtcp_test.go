package rtcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNTPTimestampReasonable(t *testing.T) {
	sec, _ := NTPNow()
	// NTP timestamp for 2024-01-01 is ~3,913,056,000.
	assert.Greater(t, sec, uint32(3_900_000_000), "NTP sec too low")
}

func TestBuildSRNoReportBlock(t *testing.T) {
	stats := NewStats()
	stats.PacketsSent = 100
	stats.OctetsSent = 16000
	stats.LastRTPTimestamp = 320000

	sr := BuildSR(0xDEADBEEF, stats)
	assert.Equal(t, 28, len(sr))

	// Version=2, RC=0, PT=200.
	assert.Equal(t, byte(2), (sr[0]>>6)&0x03)
	assert.Equal(t, byte(0), sr[0]&0x1F)
	assert.Equal(t, byte(200), sr[1])

	// SSRC.
	assert.Equal(t, uint32(0xDEADBEEF), be32(sr[4:8]))

	// Packet count.
	assert.Equal(t, uint32(100), be32(sr[20:24]))
	// Octet count.
	assert.Equal(t, uint32(16000), be32(sr[24:28]))
}

func TestBuildSRWithReportBlock(t *testing.T) {
	stats := NewStats()
	stats.PacketsSent = 50
	stats.OctetsSent = 8000
	stats.LastRTPTimestamp = 160000

	stats.RecordRTPReceived(42, 6720, 0xCAFEBABE, 8000)

	sr := BuildSR(0x12345678, stats)
	assert.Equal(t, 52, len(sr)) // 28 + 24

	// RC=1.
	assert.Equal(t, byte(1), sr[0]&0x1F)

	// Report block SSRC.
	assert.Equal(t, uint32(0xCAFEBABE), be32(sr[28:32]))
}

func TestBuildRRFormat(t *testing.T) {
	stats := NewStats()
	rr := BuildRR(0xABCD1234, stats)
	assert.Equal(t, 8, len(rr))

	assert.Equal(t, byte(2), (rr[0]>>6)&0x03)
	assert.Equal(t, byte(0), rr[0]&0x1F)
	assert.Equal(t, byte(201), rr[1])
	assert.Equal(t, uint32(0xABCD1234), be32(rr[4:8]))
}

func TestBuildRRWithReportBlock(t *testing.T) {
	stats := NewStats()
	stats.RecordRTPReceived(10, 1600, 0x11111111, 8000)

	rr := BuildRR(0x22222222, stats)
	assert.Equal(t, 32, len(rr)) // 8 + 24
	assert.Equal(t, byte(1), rr[0]&0x1F)
}

func TestParseSR(t *testing.T) {
	stats := NewStats()
	stats.PacketsSent = 200
	stats.OctetsSent = 32000
	stats.LastRTPTimestamp = 640000

	sr := BuildSR(0xAAAAAAAA, stats)
	parsed := Parse(sr)
	require.NotNil(t, parsed)

	assert.True(t, parsed.IsSenderReport())
	assert.Equal(t, uint32(0xAAAAAAAA), parsed.SSRC)
	assert.Equal(t, uint32(200), parsed.PacketCount)
	assert.Equal(t, uint32(32000), parsed.OctetCount)
	assert.Equal(t, uint32(640000), parsed.RTPTimestamp)
	assert.Empty(t, parsed.Reports)
}

func TestParseRR(t *testing.T) {
	stats := NewStats()
	rr := BuildRR(0xBBBBBBBB, stats)
	parsed := Parse(rr)
	require.NotNil(t, parsed)

	assert.False(t, parsed.IsSenderReport())
	assert.Equal(t, uint32(0xBBBBBBBB), parsed.SSRC)
	assert.Empty(t, parsed.Reports)
}

func TestParseTooShort(t *testing.T) {
	assert.Nil(t, Parse(nil))
	assert.Nil(t, Parse([]byte{0x80, 200, 0, 0})) // only 4 bytes
}

func TestParseUnknownPT(t *testing.T) {
	data := []byte{0x80, 202, 0, 1, 0, 0, 0, 0} // PT=202 (SDES)
	assert.Nil(t, Parse(data))
}

func TestParseBadVersion(t *testing.T) {
	data := make([]byte, 28)
	data[0] = 0x40 // Version=1
	data[1] = 200
	assert.Nil(t, Parse(data))
}

func TestRecordRTPSent(t *testing.T) {
	stats := NewStats()
	stats.RecordRTPSent(160, 0)
	stats.RecordRTPSent(160, 160)
	stats.RecordRTPSent(160, 320)

	assert.Equal(t, uint32(3), stats.PacketsSent)
	assert.Equal(t, uint32(480), stats.OctetsSent)
	assert.Equal(t, uint32(320), stats.LastRTPTimestamp)
}

func TestRecordRTPReceivedSeqTracking(t *testing.T) {
	stats := NewStats()
	for seq := uint16(0); seq < 5; seq++ {
		stats.RecordRTPReceived(seq, uint32(seq)*160, 1234, 8000)
	}

	assert.Equal(t, uint32(5), stats.PacketsReceived)
	assert.Equal(t, uint16(4), stats.maxSeq)
	assert.Equal(t, uint16(0), stats.baseSeq)
	assert.Equal(t, uint32(0), stats.cycles)
	assert.Equal(t, uint32(4), stats.extendedMaxSeq())
}

func TestSeqWraparound(t *testing.T) {
	stats := NewStats()
	for _, seq := range []uint16{65534, 65535, 0, 1, 2} {
		stats.RecordRTPReceived(seq, 0, 1234, 0)
	}

	assert.Equal(t, uint16(2), stats.maxSeq)
	assert.Equal(t, uint32(1), stats.cycles)
	// Extended: (1 << 16) | 2 = 65538.
	assert.Equal(t, uint32(65538), stats.extendedMaxSeq())
}

func TestLossFractionCalculation(t *testing.T) {
	stats := NewStats()
	// Receive packets 0, 1, 2, 4, 5 (skip 3).
	for _, seq := range []uint16{0, 1, 2, 4, 5} {
		stats.RecordRTPReceived(seq, 0, 1234, 0)
	}

	assert.Equal(t, uint32(1), stats.cumulativeLost())
	assert.Equal(t, uint32(6), stats.expected()) // 0..=5

	// fraction_lost should be ~42 (1/6 * 256 = 42.67).
	frac := stats.fractionLost()
	assert.Equal(t, uint8(42), frac)
}

func TestSRRoundTripBuildParse(t *testing.T) {
	stats := NewStats()
	stats.PacketsSent = 1000
	stats.OctetsSent = 160000
	stats.LastRTPTimestamp = 160000

	for seq := uint16(0); seq < 10; seq++ {
		stats.RecordRTPReceived(seq, uint32(seq)*160, 0xFEEDFACE, 8000)
	}

	sr := BuildSR(0x99887766, stats)
	parsed := Parse(sr)
	require.NotNil(t, parsed)

	assert.True(t, parsed.IsSenderReport())
	assert.Equal(t, uint32(0x99887766), parsed.SSRC)
	assert.Equal(t, uint32(1000), parsed.PacketCount)
	assert.Equal(t, uint32(160000), parsed.OctetCount)
	assert.Equal(t, uint32(160000), parsed.RTPTimestamp)
	require.Len(t, parsed.Reports, 1)
	assert.Equal(t, uint32(0xFEEDFACE), parsed.Reports[0].SSRC)
	assert.Equal(t, uint32(9), parsed.Reports[0].HighestSeq)
}

func TestProcessIncomingSRStoresNTP(t *testing.T) {
	stats := NewStats()
	stats.ProcessIncomingSR(0xAABBCCDD, 0x11223344)

	// Middle 32 bits: low 16 of sec (0xCCDD) << 16 | high 16 of frac (0x1122).
	assert.Equal(t, uint32(0xCCDD1122), stats.lastSRNTPMiddle)
	assert.True(t, stats.lastSRReceived)
}

func TestDelaySinceLastSRZeroWhenNoSR(t *testing.T) {
	stats := NewStats()
	assert.Equal(t, uint32(0), stats.delaySinceLastSR())
}

func TestParseSRWithReportBlock(t *testing.T) {
	stats := NewStats()
	stats.PacketsSent = 50
	stats.OctetsSent = 8000

	stats.RecordRTPReceived(100, 16000, 0x55555555, 8000)

	sr := BuildSR(0x66666666, stats)
	parsed := Parse(sr)
	require.NotNil(t, parsed)

	require.Len(t, parsed.Reports, 1)
	assert.Equal(t, uint32(0x55555555), parsed.Reports[0].SSRC)
	assert.Equal(t, uint32(100), parsed.Reports[0].HighestSeq)
	assert.Equal(t, uint32(0), parsed.Reports[0].CumLost)
}

// be32 is a test helper to read a big-endian uint32.
func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
