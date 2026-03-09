// Package srtp implements SRTP/SRTCP (RFC 3711) with AES_CM_128_HMAC_SHA1_80.
//
// It provides encrypt/decrypt for both RTP (Protect/Unprotect) and
// RTCP (ProtectRTCP/UnprotectRTCP) packets, SDES key exchange (RFC 4568),
// keying material generation, and key zeroization.
package srtp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash"
)

const (
	masterKeyLen  = 16 // AES-128
	masterSaltLen = 14
	authTagLen    = 10 // HMAC-SHA1-80 (truncated to 80 bits)

	// SRTP key derivation labels (RFC 3711 §4.3.1).
	labelCipherKey = 0x00
	labelAuthKey   = 0x01
	labelSalt      = 0x02

	// SRTCP key derivation labels (RFC 3711 §4.3.1).
	labelSRTCPCipherKey = 0x03
	labelSRTCPAuthKey   = 0x04
	labelSRTCPSalt      = 0x05

	// rtcpHeaderMin is the minimum RTCP packet size (V, PT, length, SSRC).
	rtcpHeaderMin = 8
	// srtcpIndexLen is the size of the SRTCP index word.
	srtcpIndexLen = 4

	// CryptoSuite is the only suite we support.
	CryptoSuite = "AES_CM_128_HMAC_SHA1_80"
)

// replayWindowSize is the number of packets tracked by the sliding window.
// Covers ~2.5 seconds of audio at 50 packets/second.
// Must not exceed 128 (bitmap is uint128).
const replayWindowSize = 128

// Compile-time assert: replayWindowSize <= 128.
const _ = uint(128 - replayWindowSize)

// replayWindow implements sliding-window replay protection per RFC 3711 §3.3.2.
// Tracks the highest accepted packet index and a bitmask of which of the
// previous replayWindowSize packets have been received.
type replayWindow struct {
	top         uint64  // highest accepted 48-bit packet index
	bitmap      uint128 // bit 0 = top, bit 1 = top-1, etc.
	initialized bool
}

// uint128 represents a 128-bit unsigned integer as two 64-bit halves.
type uint128 struct {
	hi, lo uint64
}

func (u uint128) bit(n uint64) uint64 {
	if n < 64 {
		return (u.lo >> n) & 1
	}
	return (u.hi >> (n - 64)) & 1
}

func (u uint128) setBit(n uint64) uint128 {
	if n < 64 {
		return uint128{u.hi, u.lo | (1 << n)}
	}
	return uint128{u.hi | (1 << (n - 64)), u.lo}
}

func (u uint128) shl(n uint64) uint128 {
	if n >= 128 {
		return uint128{}
	}
	if n >= 64 {
		return uint128{u.lo << (n - 64), 0}
	}
	return uint128{(u.hi << n) | (u.lo >> (64 - n)), u.lo << n}
}

// isReplay returns true if the packet should be rejected (duplicate or too old).
func (w *replayWindow) isReplay(index uint64) bool {
	if !w.initialized {
		return false
	}
	if index > w.top {
		return false
	}
	delta := w.top - index
	if delta >= replayWindowSize {
		return true
	}
	return w.bitmap.bit(delta) == 1
}

// accept marks a packet index as received. Call only after successful authentication.
func (w *replayWindow) accept(index uint64) {
	if !w.initialized {
		w.top = index
		w.bitmap = uint128{0, 1}
		w.initialized = true
		return
	}
	if index > w.top {
		shift := index - w.top
		if shift >= replayWindowSize {
			w.bitmap = uint128{0, 1}
		} else {
			w.bitmap = w.bitmap.shl(shift).setBit(0)
		}
		w.top = index
	} else {
		delta := w.top - index
		if delta < replayWindowSize {
			w.bitmap = w.bitmap.setBit(delta)
		}
	}
}

// Context holds the derived session keys for one direction of SRTP/SRTCP.
type Context struct {
	// SRTP fields.
	sessionSalt [14]byte
	authKey     [20]byte // retained for zeroization
	block       cipher.Block
	mac         hash.Hash

	roc            uint32
	lastSeq        uint16
	seqInitialized bool
	replay         replayWindow

	// SRTCP fields (RFC 3711 §3.4).
	srtcpSessionSalt [14]byte
	srtcpAuthKey     [20]byte
	srtcpBlock       cipher.Block
	srtcpMac         hash.Hash
	srtcpIndex       uint32       // monotonically increasing 31-bit sender index
	srtcpReplay      replayWindow // receiver-side replay protection
}

// New derives SRTP and SRTCP session keys from master key (16 bytes) and master salt (14 bytes).
func New(masterKey [16]byte, masterSalt [14]byte) *Context {
	c := &Context{}

	// Derive SRTP session keys (labels 0x00-0x02).
	var cipherKey [16]byte
	derived := deriveSessionKey(masterKey, masterSalt, labelCipherKey, 16)
	copy(cipherKey[:], derived[:16])
	zeroize20(&derived)

	derived = deriveSessionKey(masterKey, masterSalt, labelAuthKey, 20)
	copy(c.authKey[:], derived[:])
	zeroize20(&derived)

	derived = deriveSessionKey(masterKey, masterSalt, labelSalt, 14)
	copy(c.sessionSalt[:], derived[:14])
	zeroize20(&derived)

	block, err := aes.NewCipher(cipherKey[:])
	if err != nil {
		panic("srtp: AES cipher creation failed: " + err.Error())
	}
	zeroize16(&cipherKey)
	c.block = block
	c.mac = hmac.New(sha1.New, c.authKey[:])

	// Derive SRTCP session keys (labels 0x03-0x05).
	var srtcpCipherKey [16]byte
	derived = deriveSessionKey(masterKey, masterSalt, labelSRTCPCipherKey, 16)
	copy(srtcpCipherKey[:], derived[:16])
	zeroize20(&derived)

	derived = deriveSessionKey(masterKey, masterSalt, labelSRTCPAuthKey, 20)
	copy(c.srtcpAuthKey[:], derived[:])
	zeroize20(&derived)

	derived = deriveSessionKey(masterKey, masterSalt, labelSRTCPSalt, 14)
	copy(c.srtcpSessionSalt[:], derived[:14])
	zeroize20(&derived)

	srtcpBlock, err := aes.NewCipher(srtcpCipherKey[:])
	if err != nil {
		panic("srtp: AES cipher creation failed: " + err.Error())
	}
	zeroize16(&srtcpCipherKey)
	c.srtcpBlock = srtcpBlock
	c.srtcpMac = hmac.New(sha1.New, c.srtcpAuthKey[:])

	return c
}

// FromSDESInline creates an SRTP context from a base64-encoded inline key
// (as found in SDP a=crypto: attributes). The inline key is 30 bytes:
// 16 bytes master key + 14 bytes master salt.
func FromSDESInline(inline string) (*Context, error) {
	data, err := base64.StdEncoding.DecodeString(inline)
	if err != nil {
		return nil, fmt.Errorf("srtp: decode inline key: %w", err)
	}
	if len(data) != masterKeyLen+masterSaltLen {
		zeroizeSlice(data)
		return nil, fmt.Errorf("srtp: inline key must be %d bytes, got %d", masterKeyLen+masterSaltLen, len(data))
	}
	var key [16]byte
	var salt [14]byte
	copy(key[:], data[:16])
	copy(salt[:], data[16:])
	zeroizeSlice(data)
	ctx := New(key, salt)
	zeroize16(&key)
	zeroize14(&salt)
	return ctx, nil
}

// Protect encrypts an RTP packet in-place and appends a 10-byte auth tag.
// Returns the SRTP packet (header + encrypted payload + auth tag).
func (c *Context) Protect(rtpPacket []byte) ([]byte, error) {
	headerLen := rtpHeaderLen(rtpPacket)
	if headerLen < 0 || headerLen > len(rtpPacket) {
		return nil, fmt.Errorf("srtp: invalid RTP packet")
	}

	seq := binary.BigEndian.Uint16(rtpPacket[2:4])
	ssrc := binary.BigEndian.Uint32(rtpPacket[8:12])

	c.updateROC(seq)
	index := (uint64(c.roc) << 16) | uint64(seq)

	// Encrypt payload in-place.
	payload := rtpPacket[headerLen:]
	keystream := c.generateKeystream(ssrc, index, len(payload))
	for i := range payload {
		payload[i] ^= keystream[i]
	}

	// Compute auth tag over (header + encrypted payload + ROC).
	tag := c.computeAuthTag(rtpPacket, c.roc)

	// Append auth tag.
	out := make([]byte, len(rtpPacket)+authTagLen)
	copy(out, rtpPacket)
	copy(out[len(rtpPacket):], tag[:])
	return out, nil
}

// Unprotect verifies and decrypts an SRTP packet.
// Returns the original RTP packet (without auth tag).
func (c *Context) Unprotect(srtpPacket []byte) ([]byte, error) {
	if len(srtpPacket) < authTagLen {
		return nil, fmt.Errorf("srtp: packet too short")
	}

	// Split off the auth tag.
	authOffset := len(srtpPacket) - authTagLen
	authPortion := srtpPacket[:authOffset]
	receivedTag := srtpPacket[authOffset:]

	headerLen := rtpHeaderLen(authPortion)
	if headerLen < 0 || headerLen > len(authPortion) {
		return nil, fmt.Errorf("srtp: invalid SRTP packet")
	}

	seq := binary.BigEndian.Uint16(authPortion[2:4])
	ssrc := binary.BigEndian.Uint32(authPortion[8:12])

	// Estimate ROC for authentication check.
	estimatedROC := c.estimateROC(seq)
	index := (uint64(estimatedROC) << 16) | uint64(seq)

	// Replay check (RFC 3711 §3.3.2) — cheap, before expensive HMAC.
	if c.replay.isReplay(index) {
		return nil, fmt.Errorf("srtp: replay detected")
	}

	// Verify auth tag.
	expectedTag := c.computeAuthTag(authPortion, estimatedROC)
	if subtle.ConstantTimeCompare(receivedTag, expectedTag[:]) != 1 {
		return nil, fmt.Errorf("srtp: authentication failed")
	}

	// Auth verified — commit replay window and ROC/seq state.
	// Only advance ROC/lastSeq when this packet's index exceeds the current
	// highest (RFC 3711 §3.3.1: s_l tracks the highest received index).
	c.replay.accept(index)
	currentHighest := (uint64(c.roc) << 16) | uint64(c.lastSeq)
	if !c.seqInitialized || index > currentHighest {
		c.roc = estimatedROC
		c.lastSeq = seq
		c.seqInitialized = true
	}

	// Decrypt payload.
	rtp := make([]byte, authOffset)
	copy(rtp, authPortion)
	payload := rtp[headerLen:]
	keystream := c.generateKeystream(ssrc, index, len(payload))
	for i := range payload {
		payload[i] ^= keystream[i]
	}

	return rtp, nil
}

// ProtectRTCP encrypts an RTCP packet and appends SRTCP index + auth tag.
// Layout: [header(8)][encrypted_payload][SRTCP_index(4)][auth_tag(10)]
// First 8 bytes stay cleartext (authenticated but not encrypted).
func (c *Context) ProtectRTCP(rtcpPacket []byte) ([]byte, error) {
	if len(rtcpPacket) < rtcpHeaderMin {
		return nil, fmt.Errorf("srtcp: packet too short")
	}

	ssrc := binary.BigEndian.Uint32(rtcpPacket[4:8])
	index := c.srtcpIndex
	c.srtcpIndex++

	out := make([]byte, len(rtcpPacket))
	copy(out, rtcpPacket)

	// Encrypt bytes 8..end (header stays cleartext).
	if len(out) > rtcpHeaderMin {
		payload := out[rtcpHeaderMin:]
		keystream := c.generateSRTCPKeystream(ssrc, uint64(index), len(payload))
		for i := range payload {
			payload[i] ^= keystream[i]
		}
	}

	// Append SRTCP index with E-bit set (bit 31 = 1 → encrypted).
	var indexWord [4]byte
	binary.BigEndian.PutUint32(indexWord[:], 0x80000000|index)
	out = append(out, indexWord[:]...)

	// Auth tag covers: encrypted RTCP + SRTCP index.
	tag := c.computeSRTCPAuthTag(out)
	out = append(out, tag[:]...)
	return out, nil
}

// UnprotectRTCP verifies auth tag, strips SRTCP index, and decrypts the packet.
// Input: SRTCP bytes [header][encrypted_payload][SRTCP_index(4)][auth_tag(10)]
// Output: raw RTCP bytes.
func (c *Context) UnprotectRTCP(srtcpPacket []byte) ([]byte, error) {
	// Minimum: 8 (header) + 4 (SRTCP index) + 10 (auth tag) = 22.
	minLen := rtcpHeaderMin + srtcpIndexLen + authTagLen
	if len(srtcpPacket) < minLen {
		return nil, fmt.Errorf("srtcp: packet too short")
	}

	tagStart := len(srtcpPacket) - authTagLen
	indexStart := tagStart - srtcpIndexLen
	receivedTag := srtcpPacket[tagStart:]
	authenticatedPortion := srtcpPacket[:tagStart] // everything before auth tag

	// Extract SRTCP index word (E-bit + 31-bit index).
	indexWord := binary.BigEndian.Uint32(srtcpPacket[indexStart : indexStart+4])
	encrypted := (indexWord & 0x80000000) != 0
	index := indexWord & 0x7FFFFFFF

	// Replay check.
	if c.srtcpReplay.isReplay(uint64(index)) {
		return nil, fmt.Errorf("srtcp: replay detected")
	}

	// Verify auth tag.
	expectedTag := c.computeSRTCPAuthTag(authenticatedPortion)
	if subtle.ConstantTimeCompare(receivedTag, expectedTag[:]) != 1 {
		return nil, fmt.Errorf("srtcp: authentication failed")
	}

	// Auth passed — update replay window.
	c.srtcpReplay.accept(uint64(index))

	// RTCP data is everything before the SRTCP index.
	out := make([]byte, indexStart)
	copy(out, srtcpPacket[:indexStart])

	// Decrypt if E-bit is set.
	if encrypted && len(out) > rtcpHeaderMin {
		ssrc := binary.BigEndian.Uint32(out[4:8])
		payload := out[rtcpHeaderMin:]
		keystream := c.generateSRTCPKeystream(ssrc, uint64(index), len(payload))
		for i := range payload {
			payload[i] ^= keystream[i]
		}
	}

	return out, nil
}

// Zeroize clears all sensitive key material from the context.
// Call this when the SRTP session is no longer needed.
func (c *Context) Zeroize() {
	zeroize20(&c.authKey)
	zeroize14(&c.sessionSalt)
	zeroize20(&c.srtcpAuthKey)
	zeroize14(&c.srtcpSessionSalt)
	c.block = nil
	c.mac = nil
	c.srtcpBlock = nil
	c.srtcpMac = nil
}

// generateSRTCPKeystream generates AES-CM keystream using SRTCP keys.
func (c *Context) generateSRTCPKeystream(ssrc uint32, index uint64, length int) []byte {
	return aesCMKeystream(c.srtcpBlock, &c.srtcpSessionSalt, ssrc, index, length)
}

// computeSRTCPAuthTag computes HMAC-SHA1 over the SRTCP authenticated portion.
// Unlike SRTP, SRTCP does not append ROC (RFC 3711 §3.4).
func (c *Context) computeSRTCPAuthTag(packet []byte) [authTagLen]byte {
	c.srtcpMac.Reset()
	c.srtcpMac.Write(packet)
	return hmacTag(c.srtcpMac, nil)
}

// --- zeroization helpers ---

func zeroize14(b *[14]byte) { *b = [14]byte{} }
func zeroize16(b *[16]byte) { *b = [16]byte{} }
func zeroize20(b *[20]byte) { *b = [20]byte{} }
func zeroizeSlice(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// GenerateKeyingMaterial generates 30 random bytes (16 key + 14 salt)
// and returns the base64-encoded inline key for SDP a=crypto: attributes.
func GenerateKeyingMaterial() (string, error) {
	buf := make([]byte, masterKeyLen+masterSaltLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("srtp: generate keying material: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// --- internal helpers ---

// deriveSessionKey implements RFC 3711 §4.3.1 key derivation.
// It uses AES-CM with the master key to derive session keys.
func deriveSessionKey(masterKey [16]byte, masterSalt [14]byte, label byte, outLen int) [20]byte {
	// x = label || r (r=0 for index 0, which covers all our needs)
	// key_id = label << (n_s * 8) where n_s = 14 - 7 = 7 bytes (48-bit index)
	// But for SRTP key derivation with KDR=0, the derivation rate is 0,
	// meaning r=0 always, so key_id = label * 2^48.
	var x [14]byte
	x[7-1] = label // label at byte position 6 (48-bit left-shift of label)

	// IV = (master_salt XOR x) padded to 16 bytes (2 zero bytes appended)
	var iv [16]byte
	for i := 0; i < 14; i++ {
		iv[i] = masterSalt[i] ^ x[i]
	}

	block, err := aes.NewCipher(masterKey[:])
	if err != nil {
		panic("srtp: AES cipher creation failed: " + err.Error())
	}

	// Generate output using AES-CM (counter mode).
	var result [20]byte
	generated := 0
	counter := uint16(0)
	for generated < outLen {
		// Set counter in last 2 bytes of IV.
		var ctrBlock [16]byte
		copy(ctrBlock[:], iv[:])
		binary.BigEndian.PutUint16(ctrBlock[14:16], counter)

		var out [16]byte
		block.Encrypt(out[:], ctrBlock[:])
		n := copy(result[generated:outLen], out[:])
		generated += n
		counter++
	}
	return result
}

// generateKeystream generates AES-CM keystream for encrypting/decrypting RTP payload.
func (c *Context) generateKeystream(ssrc uint32, index uint64, length int) []byte {
	return aesCMKeystream(c.block, &c.sessionSalt, ssrc, index, length)
}

// computeAuthTag computes HMAC-SHA1 over (packet || ROC), truncated to 80 bits.
func (c *Context) computeAuthTag(packet []byte, roc uint32) [authTagLen]byte {
	var rocBuf [4]byte
	binary.BigEndian.PutUint32(rocBuf[:], roc)
	// SRTP auth covers packet + ROC (RFC 3711 §4.2).
	c.mac.Reset()
	c.mac.Write(packet)
	c.mac.Write(rocBuf[:])
	return hmacTag(c.mac, nil)
}

// --- shared crypto helpers ---

// aesCMKeystream generates AES-CM keystream (RFC 3711 §4.1.1).
// Used by both SRTP and SRTCP with their respective block ciphers and salts.
func aesCMKeystream(block cipher.Block, salt *[14]byte, ssrc uint32, index uint64, length int) []byte {
	// IV = (salt XOR (ssrc || index)) padded to 16 bytes.
	// Layout: [0..3]=0, [4..7]=SSRC, [8..13]=index(48-bit), [14..15]=counter
	var iv [16]byte
	binary.BigEndian.PutUint32(iv[4:8], ssrc)
	binary.BigEndian.PutUint16(iv[8:10], uint16(index>>32))
	binary.BigEndian.PutUint32(iv[10:14], uint32(index))
	for i := 0; i < 14; i++ {
		iv[i] ^= salt[i]
	}

	keystream := make([]byte, length)
	counter := uint16(0)
	for off := 0; off < length; off += 16 {
		var ctrBlock [16]byte
		copy(ctrBlock[:], iv[:])
		binary.BigEndian.PutUint16(ctrBlock[14:16], counter)

		var out [16]byte
		block.Encrypt(out[:], ctrBlock[:])
		copy(keystream[off:], out[:])
		counter++
	}
	return keystream
}

// hmacTag computes the HMAC-SHA1 tag truncated to authTagLen bytes.
// If mac has already been written to, pass nil for data.
func hmacTag(mac hash.Hash, data []byte) [authTagLen]byte {
	if data != nil {
		mac.Reset()
		mac.Write(data)
	}
	sum := mac.Sum(nil)
	var tag [authTagLen]byte
	copy(tag[:], sum[:authTagLen])
	return tag
}

// updateROC updates the rollover counter based on the outgoing sequence number.
func (c *Context) updateROC(seq uint16) {
	if !c.seqInitialized {
		c.lastSeq = seq
		c.seqInitialized = true
		return
	}
	if seq < c.lastSeq && (c.lastSeq-seq) > 0x8000 {
		c.roc++
	}
	c.lastSeq = seq
}

// estimateROC estimates the ROC for an incoming packet (used before auth check).
func (c *Context) estimateROC(seq uint16) uint32 {
	if !c.seqInitialized {
		return 0
	}
	if seq < c.lastSeq && (c.lastSeq-seq) > 0x8000 {
		return c.roc + 1
	}
	if seq > c.lastSeq && (seq-c.lastSeq) > 0x8000 {
		if c.roc > 0 {
			return c.roc - 1
		}
	}
	return c.roc
}

// rtpHeaderLen calculates the length of an RTP header including CSRC and extensions.
func rtpHeaderLen(pkt []byte) int {
	if len(pkt) < 12 {
		return -1
	}
	version := (pkt[0] >> 6) & 0x03
	if version != 2 {
		return -1
	}
	cc := int(pkt[0] & 0x0F)
	headerLen := 12 + cc*4
	if headerLen > len(pkt) {
		return -1
	}

	// Check extension bit.
	if pkt[0]&0x10 != 0 {
		if headerLen+4 > len(pkt) {
			return -1
		}
		extLen := int(binary.BigEndian.Uint16(pkt[headerLen+2:headerLen+4])) * 4
		headerLen += 4 + extLen
	}
	if headerLen > len(pkt) {
		return -1
	}
	return headerLen
}
