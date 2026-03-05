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

## Open Issues (filed in `Issues/`)

| Issue | File | Status |
|-------|------|--------|
| `DialogID()`/`CallID()` redundancy | `Issues/005-dialog-id-callid-redundancy.md` | Deferred — will diverge when real SIP dialog tracking is implemented |

## Remaining

### Logger Wiring

`Config.Logger *slog.Logger` is declared but never read or used anywhere. Wire into registry, transport, media pipeline, and call state transitions.

### Docker/Integration Tests

No Docker/Asterisk CI setup. Spec describes `docker-compose` with extensions 1001-1003. Blocked on real SIP transport implementation.

## Not in Scope (v1)

Per spec: video, SRTP, WebRTC, multi-PBX failover, conference mixing, call recording, SIP INFO DTMF, attended transfer.

## Deferred to v2

| Item | Notes |
|------|-------|
| Opus encoder/decoder | `NewCodecProcessor(111, ...)` currently returns nil. Needs CGo or pure-Go Opus binding. |
| Sinc resampler | Required for Opus 48kHz → PCMRate. G.722 also uses naive 2:1 decimation. |
| Wire PCMRate config | `Config.PCMRate` exists in `options.go` but pipeline hardcodes `defaultPCMRate = 8000`. |
