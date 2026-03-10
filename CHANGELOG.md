# Changelog

## v0.3.0

### Video
- H.264 (RFC 6184) and VP8 (RFC 7741) packetizers/depacketizers
- Video RTP pipeline with VideoReader / VideoWriter / VideoRTPReader / VideoRTPWriter
- Mid-call video upgrade and downgrade via re-INVITE
- RTCP PLI/FIR for keyframe requests
- Video SRTP (separate contexts for audio and video)
- OnVideo callback fires for all video activation paths

### Audio codecs
- G.729 codec support via bcg729 (build tag `g729`)

### Call control
- Attended transfer (RFC 3891 Replaces)
- SIP INFO DTMF (RFC 2976) with configurable DtmfMode
- Call waiting with `Phone.Calls()` API

### Messaging & presence
- SIP MESSAGE instant messaging (RFC 3428)
- SIP SUBSCRIBE/NOTIFY (RFC 6665)
- MWI / voicemail notification (RFC 3842)
- BLF / Busy Lamp Field monitoring (RFC 4235)
- SIP Presence (RFC 3856)

### Network
- TURN relay for symmetric NAT (RFC 5766)
- ICE-Lite (RFC 8445 §2.2)

### Security
- SRTCP encryption (RFC 3711 §3.4)
- Key material zeroization

### Internal
- Extract media pipeline into per-stream mediaStream abstraction
- Video SDP support with multi-media m= lines (RFC 3264)
- Single dispatch goroutine per call (replaces per-callback goroutines)
- Hold media timeout enforcement (5 minutes)
- Fix stale SRTP context after re-INVITE

## v0.2.0

- 302 redirect following for INVITE
- SRTP replay protection (RFC 3711 §3.3.2)
- RTCP Sender/Receiver Reports (RFC 3550)
- Opus codec support (build tag `opus`)

## v0.1.0

- SIP registration with auth, keepalive, auto-reconnect
- Inbound and outbound calls
- Hold / Resume via re-INVITE
- Blind transfer (REFER)
- Mute / Unmute
- Early media (183 Session Progress)
- Session timers (RFC 4028)
- G.711 (PCMU, PCMA) and G.722 codecs
- PCM audio frames and raw RTP access
- Jitter buffer
- SRTP (AES_CM_128_HMAC_SHA1_80) with SDES key exchange
- STUN NAT traversal (RFC 5389)
- TCP and TLS SIP transport
- MockPhone and MockCall for unit testing
- FakePBX integration tests
- sipcli interactive terminal client
