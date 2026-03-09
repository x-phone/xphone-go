package srtp

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeTestRTP builds a minimal RTP packet (V=2, PT=0, no CSRC, no extension).
func makeTestRTP(seq uint16, ts uint32, ssrc uint32, payload []byte) []byte {
	pkt := make([]byte, 12+len(payload))
	pkt[0] = 0x80 // V=2, P=0, X=0, CC=0
	pkt[1] = 0    // PT=0
	binary.BigEndian.PutUint16(pkt[2:4], seq)
	binary.BigEndian.PutUint32(pkt[4:8], ts)
	binary.BigEndian.PutUint32(pkt[8:12], ssrc)
	copy(pkt[12:], payload)
	return pkt
}

func testContext() (*Context, *Context) {
	var key [16]byte
	var salt [14]byte
	rand.Read(key[:])
	rand.Read(salt[:])
	return New(key, salt), New(key, salt)
}

func TestProtectUnprotectRoundTrip(t *testing.T) {
	sender, receiver := testContext()

	payload := []byte("Hello, SRTP!")
	rtp := makeTestRTP(1, 160, 0x12345678, payload)

	srtp, err := sender.Protect(rtp)
	require.NoError(t, err)
	assert.Equal(t, len(rtp)+authTagLen, len(srtp))

	// Encrypted payload should differ from original.
	assert.NotEqual(t, payload, srtp[12:12+len(payload)])

	// Decrypt.
	decrypted, err := receiver.Unprotect(srtp)
	require.NoError(t, err)
	assert.Equal(t, rtp[:12], decrypted[:12]) // header preserved
	assert.Equal(t, payload, decrypted[12:])  // payload restored
}

func TestMultiplePackets(t *testing.T) {
	sender, receiver := testContext()

	for seq := uint16(0); seq < 100; seq++ {
		payload := make([]byte, 160)
		for i := range payload {
			payload[i] = byte(seq)
		}
		rtp := makeTestRTP(seq, uint32(seq)*160, 0xAABBCCDD, payload)
		original := make([]byte, len(rtp))
		copy(original, rtp)

		srtp, err := sender.Protect(rtp)
		require.NoError(t, err)

		decrypted, err := receiver.Unprotect(srtp)
		require.NoError(t, err)
		assert.Equal(t, original, decrypted, "mismatch at seq=%d", seq)
	}
}

func TestAuthenticationFailure(t *testing.T) {
	sender, _ := testContext()

	// Create a different receiver with different keys.
	var key2 [16]byte
	var salt2 [14]byte
	rand.Read(key2[:])
	rand.Read(salt2[:])
	wrongReceiver := New(key2, salt2)

	rtp := makeTestRTP(1, 160, 0x12345678, []byte("secret"))
	srtp, err := sender.Protect(rtp)
	require.NoError(t, err)

	_, err = wrongReceiver.Unprotect(srtp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestTamperedPacket(t *testing.T) {
	sender, receiver := testContext()

	rtp := makeTestRTP(1, 160, 0x12345678, []byte("secret data"))
	srtp, err := sender.Protect(rtp)
	require.NoError(t, err)

	// Tamper with encrypted payload.
	srtp[15] ^= 0xFF

	_, err = receiver.Unprotect(srtp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestPacketTooShort(t *testing.T) {
	ctx := &Context{}
	_, err := ctx.Unprotect(make([]byte, 5))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestFromSDESInline(t *testing.T) {
	// Generate a valid inline key.
	buf := make([]byte, 30)
	rand.Read(buf)
	inline := base64.StdEncoding.EncodeToString(buf)

	ctx, err := FromSDESInline(inline)
	require.NoError(t, err)
	assert.NotNil(t, ctx)

	// Round-trip test.
	rtp := makeTestRTP(42, 1000, 0xDEADBEEF, []byte("audio data here!"))
	original := make([]byte, len(rtp))
	copy(original, rtp)

	ctx2, err := FromSDESInline(inline)
	require.NoError(t, err)

	srtp, err := ctx.Protect(rtp)
	require.NoError(t, err)

	decrypted, err := ctx2.Unprotect(srtp)
	require.NoError(t, err)
	assert.Equal(t, original, decrypted)
}

func TestFromSDESInlineInvalid(t *testing.T) {
	_, err := FromSDESInline("not-valid-base64!!!")
	assert.Error(t, err)

	// Wrong length (20 bytes instead of 30).
	short := base64.StdEncoding.EncodeToString(make([]byte, 20))
	_, err = FromSDESInline(short)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "30 bytes")
}

func TestGenerateKeyingMaterial(t *testing.T) {
	key1, err := GenerateKeyingMaterial()
	require.NoError(t, err)

	// Should be valid base64, decoding to 30 bytes.
	data, err := base64.StdEncoding.DecodeString(key1)
	require.NoError(t, err)
	assert.Equal(t, 30, len(data))

	// Two calls should produce different keys.
	key2, err := GenerateKeyingMaterial()
	require.NoError(t, err)
	assert.NotEqual(t, key1, key2)
}

func TestROCRollover(t *testing.T) {
	sender, receiver := testContext()

	// Send packets near the seq boundary to trigger ROC rollover.
	for seq := uint16(0xFFF0); seq != 0x0010; seq++ {
		payload := []byte{byte(seq), byte(seq >> 8)}
		rtp := makeTestRTP(seq, uint32(seq)*160, 0x11223344, payload)
		original := make([]byte, len(rtp))
		copy(original, rtp)

		srtp, err := sender.Protect(rtp)
		require.NoError(t, err)

		decrypted, err := receiver.Unprotect(srtp)
		require.NoError(t, err, "failed at seq=%d", seq)
		assert.Equal(t, original, decrypted, "mismatch at seq=%d", seq)
	}
}

func TestRTPHeaderLenWithCSRC(t *testing.T) {
	// V=2, CC=2 → header = 12 + 2*4 = 20
	pkt := make([]byte, 28)
	pkt[0] = 0x82 // V=2, CC=2
	assert.Equal(t, 20, rtpHeaderLen(pkt))
}

func TestRTPHeaderLenWithExtension(t *testing.T) {
	// V=2, X=1, CC=0, extension length = 1 word (4 bytes)
	pkt := make([]byte, 20)
	pkt[0] = 0x90 // V=2, X=1
	// Extension header at offset 12: profile (2) + length (2)
	binary.BigEndian.PutUint16(pkt[14:16], 1) // 1 word = 4 bytes
	assert.Equal(t, 12+4+4, rtpHeaderLen(pkt))
}

func TestRTPHeaderLenTooShort(t *testing.T) {
	assert.Equal(t, -1, rtpHeaderLen(make([]byte, 5)))
}

func TestRTPHeaderLenBadVersion(t *testing.T) {
	pkt := make([]byte, 12)
	pkt[0] = 0x00 // V=0
	assert.Equal(t, -1, rtpHeaderLen(pkt))
}

func TestProtectPreservesHeader(t *testing.T) {
	sender, _ := testContext()

	rtp := makeTestRTP(100, 8000, 0xABCD1234, []byte("payload"))
	headerCopy := make([]byte, 12)
	copy(headerCopy, rtp[:12])

	srtp, err := sender.Protect(rtp)
	require.NoError(t, err)

	// SRTP header should match original RTP header.
	assert.Equal(t, headerCopy, srtp[:12])
}

func TestEmptyPayload(t *testing.T) {
	sender, receiver := testContext()

	rtp := makeTestRTP(1, 160, 0x12345678, nil)
	original := make([]byte, len(rtp))
	copy(original, rtp)

	srtp, err := sender.Protect(rtp)
	require.NoError(t, err)
	assert.Equal(t, len(rtp)+authTagLen, len(srtp))

	decrypted, err := receiver.Unprotect(srtp)
	require.NoError(t, err)
	assert.Equal(t, original, decrypted)
}

// --- Replay window unit tests ---

func TestReplayWindowRejectsDuplicate(t *testing.T) {
	var w replayWindow
	assert.False(t, w.isReplay(100))
	w.accept(100)
	assert.True(t, w.isReplay(100)) // exact duplicate
}

func TestReplayWindowAcceptsNewPackets(t *testing.T) {
	var w replayWindow
	for i := uint64(0); i < 200; i++ {
		assert.False(t, w.isReplay(i))
		w.accept(i)
	}
}

func TestReplayWindowRejectsOldPackets(t *testing.T) {
	var w replayWindow
	w.accept(200)
	assert.True(t, w.isReplay(200-replayWindowSize)) // just outside window
	assert.True(t, w.isReplay(0))                    // way too old
}

func TestReplayWindowAcceptsOutOfOrderWithinWindow(t *testing.T) {
	var w replayWindow
	for i := uint64(0); i <= 50; i++ {
		w.accept(i)
	}
	// Jump ahead.
	w.accept(100)
	// Packets 51..99 are within window and not yet seen.
	for i := uint64(51); i < 100; i++ {
		assert.False(t, w.isReplay(i), "packet %d should not be a replay", i)
		w.accept(i)
	}
	// All should now be replays.
	for i := uint64(0); i <= 100; i++ {
		assert.True(t, w.isReplay(i), "packet %d should be a replay", i)
	}
}

func TestReplayWindowBoundary(t *testing.T) {
	var w replayWindow
	w.accept(replayWindowSize)     // top = 128
	assert.False(t, w.isReplay(1)) // delta=127, within window
	assert.True(t, w.isReplay(0))  // delta=128, too old
}

func TestReplayWindowLargeJump(t *testing.T) {
	var w replayWindow
	w.accept(0)
	w.accept(1000)
	assert.True(t, w.isReplay(1000))  // duplicate
	assert.True(t, w.isReplay(0))     // way behind window
	assert.False(t, w.isReplay(999))  // within window, not yet seen
	assert.False(t, w.isReplay(1001)) // ahead of top
}

// --- SRTP replay integration tests ---

func TestSRTPReplayDetected(t *testing.T) {
	sender, receiver := testContext()

	rtp := makeTestRTP(1, 160, 0x12345678, []byte("audio"))
	protected, err := sender.Protect(rtp)
	require.NoError(t, err)

	// First unprotect succeeds.
	_, err = receiver.Unprotect(protected)
	require.NoError(t, err)

	// Replay of the same packet is rejected.
	_, err = receiver.Unprotect(protected)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replay")
}

func TestSRTPOutOfOrderWithinWindowOK(t *testing.T) {
	sender, receiver := testContext()

	// Protect packets 1..=5.
	protected := make([][]byte, 5)
	for i := 0; i < 5; i++ {
		seq := uint16(i + 1)
		rtp := makeTestRTP(seq, uint32(seq)*160, 0xAABBCCDD, []byte{byte(seq)})
		p, err := sender.Protect(rtp)
		require.NoError(t, err)
		protected[i] = p
	}

	// Receive out of order: 5, 3, 1, 2, 4.
	for _, idx := range []int{4, 2, 0, 1, 3} {
		_, err := receiver.Unprotect(protected[idx])
		assert.NoError(t, err, "seq %d should succeed", idx+1)
	}

	// All should now be replays.
	for i, pkt := range protected {
		_, err := receiver.Unprotect(pkt)
		assert.Error(t, err, "seq %d should be rejected as replay", i+1)
	}
}

func TestSRTPOldPacketRejected(t *testing.T) {
	sender, receiver := testContext()

	// Protect and save packet with seq=1.
	oldRTP := makeTestRTP(1, 160, 0x12345678, []byte("old"))
	oldProtected, err := sender.Protect(oldRTP)
	require.NoError(t, err)

	// Send 200 more packets to push seq=1 out of the window.
	for seq := uint16(2); seq < 202; seq++ {
		rtp := makeTestRTP(seq, uint32(seq)*160, 0x12345678, []byte{byte(seq)})
		p, err := sender.Protect(rtp)
		require.NoError(t, err)
		_, err = receiver.Unprotect(p)
		require.NoError(t, err)
	}

	// Old packet (seq=1) is now behind the window.
	_, err = receiver.Unprotect(oldProtected)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replay")
}

// --- SRTCP tests ---

// makeTestRTCP builds a minimal RTCP Sender Report (V=2, PT=200, SSRC).
func makeTestRTCP(ssrc uint32) []byte {
	pkt := make([]byte, 28)                 // 8 header + 20 sender info
	pkt[0] = 0x80                           // V=2, RC=0
	pkt[1] = 200                            // PT=SR
	binary.BigEndian.PutUint16(pkt[2:4], 6) // length in 32-bit words minus one
	binary.BigEndian.PutUint32(pkt[4:8], ssrc)
	// Fill sender info with non-zero data.
	for i := 8; i < 28; i++ {
		pkt[i] = byte(i)
	}
	return pkt
}

func TestSRTCPProtectUnprotectRoundTrip(t *testing.T) {
	sender, receiver := testContext()

	rtcp := makeTestRTCP(0xDEADBEEF)
	protected, err := sender.ProtectRTCP(rtcp)
	require.NoError(t, err)

	decrypted, err := receiver.UnprotectRTCP(protected)
	require.NoError(t, err)
	assert.Equal(t, rtcp, decrypted)
}

func TestSRTCPHeaderStaysCleartext(t *testing.T) {
	sender, _ := testContext()

	rtcp := makeTestRTCP(0xCAFEBABE)
	protected, err := sender.ProtectRTCP(rtcp)
	require.NoError(t, err)

	// First 8 bytes (V, PT, length, SSRC) must be identical.
	assert.Equal(t, rtcp[:8], protected[:8])
}

func TestSRTCPEBitSet(t *testing.T) {
	sender, _ := testContext()

	rtcp := makeTestRTCP(0x11111111)
	protected, err := sender.ProtectRTCP(rtcp)
	require.NoError(t, err)

	// SRTCP index is at [len - 14 .. len - 10] (before the 10-byte auth tag).
	idxStart := len(protected) - authTagLen - srtcpIndexLen
	indexWord := binary.BigEndian.Uint32(protected[idxStart : idxStart+4])
	assert.NotEqual(t, uint32(0), indexWord&0x80000000, "E-bit must be set")
	assert.Equal(t, uint32(0), indexWord&0x7FFFFFFF, "first packet index should be 0")
}

func TestSRTCPIndexIncrements(t *testing.T) {
	sender, _ := testContext()

	rtcp := makeTestRTCP(0x22222222)
	for expected := uint32(0); expected < 5; expected++ {
		protected, err := sender.ProtectRTCP(rtcp)
		require.NoError(t, err)

		idxStart := len(protected) - authTagLen - srtcpIndexLen
		indexWord := binary.BigEndian.Uint32(protected[idxStart : idxStart+4])
		assert.Equal(t, expected, indexWord&0x7FFFFFFF)
	}
}

func TestSRTCPAuthFailure(t *testing.T) {
	sender, receiver := testContext()

	rtcp := makeTestRTCP(0x33333333)
	protected, err := sender.ProtectRTCP(rtcp)
	require.NoError(t, err)

	// Flip a byte in the encrypted payload.
	protected[10] ^= 0xFF

	_, err = receiver.UnprotectRTCP(protected)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestSRTCPReplayDetected(t *testing.T) {
	sender, receiver := testContext()

	rtcp := makeTestRTCP(0x44444444)
	protected, err := sender.ProtectRTCP(rtcp)
	require.NoError(t, err)

	_, err = receiver.UnprotectRTCP(protected)
	require.NoError(t, err)

	// Replay of the same packet is rejected.
	_, err = receiver.UnprotectRTCP(protected)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replay")
}

func TestSRTCPWrongKeyFails(t *testing.T) {
	var keyA, keyB [16]byte
	var salt [14]byte
	rand.Read(keyA[:])
	rand.Read(keyB[:])
	rand.Read(salt[:])

	sender := New(keyA, salt)
	receiver := New(keyB, salt)

	rtcp := makeTestRTCP(0x55555555)
	protected, err := sender.ProtectRTCP(rtcp)
	require.NoError(t, err)

	_, err = receiver.UnprotectRTCP(protected)
	assert.Error(t, err)
}

func TestSRTCPProtectTooShort(t *testing.T) {
	sender, _ := testContext()
	_, err := sender.ProtectRTCP(make([]byte, 4))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestSRTCPUnprotectTooShort(t *testing.T) {
	_, receiver := testContext()
	_, err := receiver.UnprotectRTCP(make([]byte, 21)) // needs at least 22
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestSRTCPKeyDerivationDiffersFromSRTP(t *testing.T) {
	var key [16]byte
	var salt [14]byte
	for i := range key {
		key[i] = byte(i + 0x23)
	}
	for i := range salt {
		salt[i] = byte(i + 0x24)
	}

	srtpCK := deriveSessionKey(key, salt, labelCipherKey, 16)
	srtcpCK := deriveSessionKey(key, salt, labelSRTCPCipherKey, 16)
	assert.NotEqual(t, srtpCK, srtcpCK, "SRTP and SRTCP cipher keys must differ")

	srtpAK := deriveSessionKey(key, salt, labelAuthKey, 20)
	srtcpAK := deriveSessionKey(key, salt, labelSRTCPAuthKey, 20)
	assert.NotEqual(t, srtpAK, srtcpAK, "SRTP and SRTCP auth keys must differ")

	srtpSalt := deriveSessionKey(key, salt, labelSalt, 14)
	srtcpSalt := deriveSessionKey(key, salt, labelSRTCPSalt, 14)
	assert.NotEqual(t, srtpSalt, srtcpSalt, "SRTP and SRTCP salts must differ")
}

func TestSRTCPMultiplePacketsRoundTrip(t *testing.T) {
	sender, receiver := testContext()

	for i := uint32(0); i < 20; i++ {
		rtcp := makeTestRTCP(0xAA000000 | i)
		protected, err := sender.ProtectRTCP(rtcp)
		require.NoError(t, err)

		decrypted, err := receiver.UnprotectRTCP(protected)
		require.NoError(t, err)
		assert.Equal(t, rtcp, decrypted, "mismatch at SRTCP index %d", i)
	}
}

func TestSRTCPOutOfOrderWithinWindow(t *testing.T) {
	sender, receiver := testContext()

	rtcp := makeTestRTCP(0xBBBBBBBB)
	protected := make([][]byte, 5)
	for i := 0; i < 5; i++ {
		p, err := sender.ProtectRTCP(rtcp)
		require.NoError(t, err)
		protected[i] = p
	}

	// Receive in reverse order — all should succeed within the replay window.
	for i := len(protected) - 1; i >= 0; i-- {
		_, err := receiver.UnprotectRTCP(protected[i])
		assert.NoError(t, err, "reverse packet %d should succeed", i)
	}
}

// --- Key zeroization tests ---

func TestZeroize(t *testing.T) {
	var key [16]byte
	var salt [14]byte
	for i := range key {
		key[i] = 0x11
	}
	for i := range salt {
		salt[i] = 0x22
	}

	ctx := New(key, salt)

	// Verify keys are non-zero before zeroization.
	assert.NotEqual(t, [20]byte{}, ctx.authKey)
	assert.NotEqual(t, [14]byte{}, ctx.sessionSalt)
	assert.NotEqual(t, [20]byte{}, ctx.srtcpAuthKey)
	assert.NotEqual(t, [14]byte{}, ctx.srtcpSessionSalt)

	ctx.Zeroize()

	assert.Equal(t, [20]byte{}, ctx.authKey, "authKey should be zeroed")
	assert.Equal(t, [14]byte{}, ctx.sessionSalt, "sessionSalt should be zeroed")
	assert.Equal(t, [20]byte{}, ctx.srtcpAuthKey, "srtcpAuthKey should be zeroed")
	assert.Equal(t, [14]byte{}, ctx.srtcpSessionSalt, "srtcpSessionSalt should be zeroed")
}

func TestDeriveSessionKeyDeterministic(t *testing.T) {
	var key [16]byte
	var salt [14]byte
	for i := range key {
		key[i] = byte(i)
	}
	for i := range salt {
		salt[i] = byte(i + 16)
	}

	k1 := deriveSessionKey(key, salt, labelCipherKey, 16)
	k2 := deriveSessionKey(key, salt, labelCipherKey, 16)
	assert.Equal(t, k1, k2)

	// Different label should produce different key.
	k3 := deriveSessionKey(key, salt, labelAuthKey, 16)
	assert.NotEqual(t, k1[:16], k3[:16])
}
