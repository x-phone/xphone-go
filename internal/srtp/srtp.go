// Package srtp implements SRTP (RFC 3711) with AES_CM_128_HMAC_SHA1_80.
//
// It provides encrypt (Protect) and decrypt (Unprotect) for RTP packets,
// SDES key exchange (RFC 4568), and keying material generation.
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

	// Key derivation labels (RFC 3711 §4.3.1).
	labelCipherKey = 0x00
	labelAuthKey   = 0x01
	labelSalt      = 0x02

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

// Context holds the derived session keys for one direction of SRTP.
type Context struct {
	sessionSalt [14]byte // session salt
	block       cipher.Block
	mac         hash.Hash

	roc            uint32 // rollover counter
	lastSeq        uint16
	seqInitialized bool

	replay replayWindow // replay protection (used by Unprotect only)
}

// New derives session keys from master key (16 bytes) and master salt (14 bytes).
func New(masterKey [16]byte, masterSalt [14]byte) *Context {
	c := &Context{}

	var cipherKey [16]byte
	derived := deriveSessionKey(masterKey, masterSalt, labelCipherKey, 16)
	copy(cipherKey[:], derived[:16])

	var authKey [20]byte
	derived = deriveSessionKey(masterKey, masterSalt, labelAuthKey, 20)
	copy(authKey[:], derived[:])

	derived = deriveSessionKey(masterKey, masterSalt, labelSalt, 14)
	copy(c.sessionSalt[:], derived[:14])

	block, err := aes.NewCipher(cipherKey[:])
	if err != nil {
		panic("srtp: AES cipher creation failed: " + err.Error())
	}
	c.block = block
	c.mac = hmac.New(sha1.New, authKey[:])
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
		return nil, fmt.Errorf("srtp: inline key must be %d bytes, got %d", masterKeyLen+masterSaltLen, len(data))
	}
	var key [16]byte
	var salt [14]byte
	copy(key[:], data[:16])
	copy(salt[:], data[16:])
	return New(key, salt), nil
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

// generateKeystream generates AES-CM keystream for encrypting/decrypting payload.
func (c *Context) generateKeystream(ssrc uint32, index uint64, length int) []byte {
	// IV = (session_salt XOR (ssrc || index)) padded to 16 bytes
	// Layout: [0..3]=0, [4..7]=SSRC, [8..13]=index(48-bit), [14..15]=counter
	var iv [16]byte
	binary.BigEndian.PutUint32(iv[4:8], ssrc)
	binary.BigEndian.PutUint16(iv[8:10], uint16(index>>32))
	binary.BigEndian.PutUint32(iv[10:14], uint32(index))

	// XOR with session salt (14 bytes).
	for i := 0; i < 14; i++ {
		iv[i] ^= c.sessionSalt[i]
	}

	keystream := make([]byte, length)
	counter := uint16(0)
	for off := 0; off < length; off += 16 {
		var ctrBlock [16]byte
		copy(ctrBlock[:], iv[:])
		binary.BigEndian.PutUint16(ctrBlock[14:16], counter)

		var out [16]byte
		c.block.Encrypt(out[:], ctrBlock[:])
		copy(keystream[off:], out[:])
		counter++
	}
	return keystream
}

// computeAuthTag computes HMAC-SHA1 over (packet || ROC), truncated to 80 bits.
func (c *Context) computeAuthTag(packet []byte, roc uint32) [authTagLen]byte {
	c.mac.Reset()
	c.mac.Write(packet)
	var rocBuf [4]byte
	binary.BigEndian.PutUint32(rocBuf[:], roc)
	c.mac.Write(rocBuf[:])
	sum := c.mac.Sum(nil)
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
