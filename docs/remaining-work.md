# Remaining Work

## Completed

| Phase | Summary |
|-------|---------|
| Phase 1-4 | SIP core, RTP engine, codecs, SDP, DTMF, re-INVITE, session timers |
| Phase 5 | Mute/Unmute, RemoteURI/IP/Port, OnState callback |
| Phase 6 | Connect/Disconnect, functional options, callback buffering |
| Phase 7 | MockCall, MockPhone, Call interface cleanup |
| Cleanup | SDP direction constants, BuildOffer helper, test SDP dedup, defaultCodecPrefs var |
| Logger | `Config.Logger` wired into registry, phone, call state transitions, media pipeline |
| SIP Transport | sipgo-based UAC/UAS, real SIP signaling over UDP |
| Media Pipeline | Production RTP send/receive, port range allocation, codec negotiation |
| Docker/Asterisk | Integration tests E1-E3, E5-E6 passing; dynamic subnet detection for NAT |
| FakePBX Tests | 8 in-process SIP tests (registration, dial, BYE, hold, DTMF, RTP, busy, provisionals) |
| Issue Cleanup | Resolved issues 002-007 (dialog dedup, constant, IP cache, remoteAddr, port allocator, Docker NAT) |

## Open Issues (filed in `Issues/`)

| Issue | File | Status |
|-------|------|--------|
| DialogID()/CallID() redundancy | `Issues/001-dialog-id-callid-redundancy.md` | Deferred — will diverge when real SIP dialog tracking is implemented |

## Integration Test Gaps

| Test | Status | Notes |
|------|--------|-------|
| E4 (Hold/Resume) | Skipped | Asterisk sends BYE after bridge setup — likely re-INVITE collision (491). Needs investigation. |

## Not in Scope (v1)

Per spec: video, SRTP, WebRTC, multi-PBX failover, conference mixing, call recording, SIP INFO DTMF, attended transfer.

## Deferred to v2

| Item | Notes |
|------|-------|
| Opus encoder/decoder | `NewCodecProcessor(111, ...)` currently returns nil. Use `pion/opus` (pure Go, no CGo). |
| Sinc resampler | Required for Opus 48kHz → PCMRate. G.722 also uses naive 2:1 decimation. |
| Wire PCMRate config | `Config.PCMRate` exists in `options.go` but pipeline hardcodes `defaultPCMRate = 8000`. |
