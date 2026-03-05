# Remaining Work

Status after Phase 4 — 155 tests passing, no races.

## Phase 5 — Mute/Unmute & Missing Call Accessors

| Item | File(s) | Notes |
|------|---------|-------|
| `Mute()` / `Unmute()` | `call.go` | Stubs returning nil. Need to suppress outbound RTP in media pipeline. |
| `OnMute` / `OnUnmute` callbacks | `call.go` | Accept callback but silently discard — no field stored, never fired. |
| `RemoteURI()` | `call.go` | Returns `""`. Parse from SIP From/Contact header or dialog. |
| `RemoteIP()` | `call.go` | Returns `""`. Parse from remote SDP `c=` line (already parsed by `sdp.Parse`). |
| `RemotePort()` | `call.go` | Returns `0`. Parse from remote SDP `m=` line. |
| `OnState` callback | `call.go` | Stored in `onStateFn` but never fired on any state transition. Add `fireOnState()` calls at each transition point. |

## Phase 6 — sipgo Integration (Connect/Disconnect)

| Item | File(s) | Notes |
|------|---------|-------|
| `Phone.Connect()` | `xphone.go` | Returns `errors.New("not implemented")`. Wire real `emiago/sipgo` UA, SIP transport, and registration. |
| `Phone.Disconnect()` | `xphone.go` | Returns `errors.New("not implemented")`. Send un-REGISTER, stop keepalives, transition to `PhoneStateUnregistering` → `PhoneStateDisconnected`. |
| `PhoneStateUnregistering` | `xphone.go`, `events.go` | Defined but unreachable — needs `Disconnect()` implementation. |
| Phone-level functional options | `options.go`, `xphone.go` | No `WithCredentials`, `WithTransport`, `WithRTPPorts`, `WithCodecs`, etc. Only raw `Config` struct today. Add `New(opts ...PhoneOption)` constructor. |
| `OnRegistered`/`OnUnregistered`/`OnError` before Connect | `xphone.go` | Currently silently dropped if `p.reg` is nil. Buffer callbacks and apply after `Connect()`. |

## Phase 7 — Test Infrastructure

| Item | File(s) | Notes |
|------|---------|-------|
| `testutil/mock_phone.go` | `testutil/` | Does not exist. Spec calls for `testutil.NewMockPhone()`. |
| `testutil/mock_call.go` | `testutil/` | Exists but empty struct — doesn't implement `Call` interface. |
| Integration tests | `testutil/docker/` | No Docker/Asterisk CI setup. Spec describes `docker-compose` with extensions 1001-1003. |

## Cleanup Issues (filed in `Issues/`)

| Issue | File |
|-------|------|
| Add SDP direction constants (`DirSendRecv`, etc.) | `Issues/001-sdp-direction-constants.md` |
| Extract BuildOffer placeholder IP/port into helper | `Issues/002-buildoffer-placeholder-values.md` |
| Deduplicate `codecNames` map in test helpers | `Issues/003-codec-names-test-duplication.md` |
| `defaultCodecPrefs()` → package-level var | `Issues/004-default-codec-prefs-allocation.md` |
| `DialogID()`/`CallID()` redundancy | `Issues/005-dialog-id-callid-redundancy.md` |

## Not in Scope (v1)

Per spec: video, SRTP, WebRTC, multi-PBX failover, conference mixing, call recording, SIP INFO DTMF, attended transfer.

## Deferred to v2

| Item | Notes |
|------|-------|
| Opus encoder/decoder | `NewCodecProcessor(111, ...)` currently returns nil. Needs CGo or pure-Go Opus binding. |
| Sinc resampler | Required for Opus 48kHz → PCMRate. G.722 also uses naive 2:1 decimation. |
| Wire PCMRate config | `Config.PCMRate` exists in `options.go` but pipeline hardcodes `defaultPCMRate = 8000`. |

## Logger

`Config.Logger *slog.Logger` is declared but never read or used anywhere. Wire into registry, transport, media pipeline, and call state transitions.
