# Changelog

## Unreleased

### Features
- `ReplaceAudioWriter(newSrc <-chan []int16)` atomically swaps the audio input channel on an active call — enables in-app music on hold without audio bleed (#58)
- Outbound proxy support: `WithOutboundProxy("sip:proxy.example.com:5060")` routes INVITEs through a proxy
- Separate outbound credentials: `WithOutboundCredentials("trunk-user", "trunk-pass")` for INVITE auth
- `WithHeader()` / `DialOptions.CustomHeaders` now applied to outbound INVITEs (previously ignored)

## v0.4.5

### Bug fixes
- `BlindTransfer` and `AttendedTransfer` now surface REFER failures via `EndedByTransferFailed` end reason — previously failure NOTIFYs (>= 300) were silently ignored (#54)

## v0.4.4

### Bug fixes
- Fix in-dialog request routing (REFER, re-INVITE, INFO) for inbound UAS calls — use remote party's Contact address per RFC 3261 §12.2.1.1 instead of the original INVITE Request-URI, which looped back to the server itself (#51)

## v0.4.3

### Bug fixes
- Server mode: SRTP now covers video streams (previously only audio contexts were initialized)
- `hasURIParam` is now case-insensitive per RFC 3261 §19.1.1

### Internal
- Deduplicate ~120 lines of shared logic across phone/server and audio/video paths
- Extract shared helpers: `mediaStream.reset()`, `startRTCPReader()`, `startRTPReaderFor()`, `wireCallCallbacks()`, `extractHostPort()`, `call.setupSRTP()`

## v0.4.2

### Bug fixes
- Server mode: include listening port in SIP Contact header — fixes ACK routing to non-default ports (e.g., 5080)

## v0.4.1

### Bug fixes
- Force IPv4 (`udp4`) for all RTP/RTCP sockets and address resolution — fixes zero outbound RTP on Linux dual-stack hosts
- Log `WriteTo` errors on RTP, RTCP (first failure per stream), and DTMF sends for diagnosability
- Log total RTP/RTCP send error counts when media pipeline exits
- Log STUN binding response `WriteTo` errors
- `Connect()` now returns an error when SIP registration fails (previously always returned nil)
- "phone connected" log no longer emits on registration failure
- `WithCredentials` now accepts `host:port` format (e.g. `"10.0.0.7:5060"`) — previously caused malformed SIP URIs

## v0.4.0

### Server mode
- `Server` connection mode: accept and place calls directly with trusted SIP peers without registration
- Peer authentication: IP/CIDR matching + SIP digest auth (RFC 2617)
- Per-peer RTP address and codec overrides
- Stale call reaper (30s setup TTL, 4h active TTL)
- FakePBX integration tests for inbound/outbound calls, RTP, DTMF, peer rejection

### Docs
- Add Connection Modes section to README (Phone vs Server)
- Add Server mode to features table
- Add PacedPCMWriter documentation to README
- Add RTP port range documentation to README

## v0.3.2

### Media
- `PacedPCMWriter()` channel for TTS burst audio — auto-frames and paces arbitrary-length PCM at 20ms intervals

### Bug fixes
- Fix concurrent inbound calls getting `m=audio 0` in SDP by eagerly allocating RTP port

### CI
- Add changelog check: PRs now require a CHANGELOG.md entry

### Tests
- FakePBX integration tests for multiple simultaneous calls and PacedPCMWriter

## v0.3.1

### Internal
- Reduce cyclomatic complexity in hotspot functions

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
