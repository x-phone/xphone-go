# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

xphone is a Go library (`github.com/x-phone/xphone-go`) that wraps a SIP user agent and exposes an event-driven API for managing concurrent VoIP calls. It handles SIP signaling, registration, RTP media, codec encode/decode, and call control. The full specification is at `docs/xphone-spec.md`.

## Commands

```bash
go build ./...                    # Build all packages
go test ./... -count=1            # Run all tests
go test ./... -v -count=1         # Verbose test output
go test -race ./...               # Race detector (always run before committing)
go test -run TestCall_Hold ./...  # Run a single test by name
go vet ./...                      # Static analysis
make test                         # Shortcut for go test ./...
make test-race                    # Shortcut for go test -race ./...
make lint                         # golangci-lint run ./...
```

## Architecture

### Layer Overview

```
Phone (xphone.go)           — SIP registration, incoming/outgoing call orchestration
  └─ Call (call.go)         — Per-call state machine, SIP dialog control
       ├─ Media (media.go)  — RTP pipeline goroutine: jitter buffer, codec, DTMF dispatch
       └─ SDP (internal/sdp) — SDP offer/answer generation and parsing
```

### Key Abstractions

**`dialog` interface** (`call.go`) — Abstracts SIP dialog operations (send BYE, send re-INVITE, etc.). Production impl is `phoneDialog` in `xphone.go`; tests use `testutil.MockDialog`. All call logic programs against this interface, never against SIP directly.

**`sipTransport` interface** (`transport.go`) — Abstracts SIP transport (UDP/TCP/TLS). Tests use `testutil.MockTransport`.

**`media.CodecProcessor` interface** (`internal/media/codec.go`) — Encode/Decode for audio codecs. Implementations: `pcmuProcessor`, `pcmaProcessor`, `g722Processor`. Factory: `NewCodecProcessor(payloadType, pcmRate)`.

### Call State Machine

States flow: `Idle → Ringing/Dialing → [RemoteRinging/EarlyMedia] → Active ⇄ OnHold → Ended`. All transitions happen under `call.mu` lock. Callbacks are snapshot-copied under the lock, then fired via `go fn()` outside the lock to avoid deadlocks.

### Media Pipeline (`media.go`)

`startMedia()` launches a single goroutine that multiplexes:
- Inbound RTP → RTPRawReader (pre-jitter) → jitter buffer → RTPReader + PCMReader (post-jitter)
- DTMF packets (PT=101) intercepted before jitter buffer, dispatched via `onDTMFFn`
- Outbound: PCMWriter → codec encode → sentRTP; or RTPWriter → sentRTP (mutually exclusive, RTPWriter wins)
- Media timeout timer (suspended during OnHold)

Channel overflow: inbound taps drop oldest; PCMWriter drops newest. All buffers are 256 entries.

### Concurrency Patterns

- `call.mu` protects all call state. Lock ordering: always `call.mu` before `dialog.mu`.
- Callbacks are never called under lock — copy the function pointer, unlock, then `go fn()`.
- `SendDTMF` deliberately uses manual unlock (not defer) to release the mutex before channel I/O.
- `sentRTP` channel is a test hook for capturing outbound packets; it must never be closed while the call is active.

### Test Infrastructure

Tests use mock objects from `testutil/`:
- `MockDialog` — records SIP operations (BYE sent, re-INVITE SDP, etc.)
- `MockTransport` — queues SIP responses for registration and dial flows
- `NewMockDialogWithSessionExpires(seconds)` — creates a dialog with Session-Expires header

Common test helpers in `*_test.go`:
- `activeCall()` — creates an accepted inbound call with media pipeline running and `sentRTP` wired
- `testSDP(ip, port, dir, codecs...)` — generates SDP fixtures for re-INVITE tests
- `drainPackets(ch)` — non-blocking drain of an RTP channel

## Development Approach

- **TDD**: write tests first, then implement. Phase test suites are committed separately before implementation.
- **No test modifications** when implementing: production code must make existing failing tests pass.
- **Verification gate**: `go build ./...`, `go test ./... -count=1`, `go test -race ./...` must all pass before committing.
- **Code review after every task**: run two reviews in parallel before committing:
  1. `/simplify` — inline review (code reuse, quality, efficiency)
  2. A separate Codex review for a second pair of eyes
  Fix any actionable findings before committing. File non-actionable items as issues in `Issues/`.

## Remaining Work

See `docs/remaining-work.md` for the full breakdown of what's left to implement (Phases 5–8), cleanup issues, and what's out of scope.

## Git Conventions

- No `Co-Authored-By` trailer in commits.
- Commit messages: `"Phase N implementation: <summary>"` for phase work.
