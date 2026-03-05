# Remaining Work

Status after cleanup — all phases complete, cleanup issues resolved.

## Completed

| Phase | Summary |
|-------|---------|
| Phase 1–4 | SIP core, RTP engine, codecs, SDP, DTMF, re-INVITE, session timers |
| Phase 5 | Mute/Unmute, RemoteURI/IP/Port, OnState callback |
| Phase 6 | Connect/Disconnect, functional options, callback buffering |
| Phase 7 | MockCall, MockPhone, Call interface cleanup |
| Cleanup | SDP direction constants, BuildOffer helper, test SDP dedup, defaultCodecPrefs var |
| Logger | `Config.Logger` wired into registry, phone, call state transitions, media pipeline |

## Open Issues (filed in `Issues/`)

| Issue | File | Status |
|-------|------|--------|
| `DialogID()`/`CallID()` redundancy | `Issues/001-dialog-id-callid-redundancy.md` | Deferred — will diverge when real SIP dialog tracking is implemented |

## Remaining

### Docker/Integration Tests

No Docker/Asterisk CI setup. Spec describes `docker-compose` with extensions 1001-1003. Blocked on real SIP transport implementation.

## Not in Scope (v1)

Per spec: video, SRTP, WebRTC, multi-PBX failover, conference mixing, call recording, SIP INFO DTMF, attended transfer.

## Deferred to v2

| Item | Notes |
|------|-------|
| Opus encoder/decoder | `NewCodecProcessor(111, ...)` currently returns nil. Use `pion/opus` (pure Go, no CGo). |
| Sinc resampler | Required for Opus 48kHz → PCMRate. G.722 also uses naive 2:1 decimation. |
| Wire PCMRate config | `Config.PCMRate` exists in `options.go` but pipeline hardcodes `defaultPCMRate = 8000`. |
