# TODO — xphone-go

## v0.2.0 Features (parity with Rust roadmap)

- [ ] **302 Redirect Following** — fixes real call failures with trunk providers
  - Parse 3xx responses in INVITE flow
  - Extract Contact header, re-INVITE to new target
  - Max redirect hops (default 3) to prevent loops
  - Branch: `feat/302-redirect`

- [ ] **SRTP Replay Protection** — RFC 3711 §3.3.2
  - Sliding window replay check
  - Reject packets with duplicate or old sequence numbers
  - Branch: `feat/srtp-replay`

- [ ] **RTCP Basic** — sender/receiver reports for trunk compatibility
  - SR/RR packets (RFC 3550)
  - Packet loss, jitter, round-trip stats
  - Periodic RTCP send from media goroutine
  - Branch: `feat/rtcp`

- [ ] **Opus Codec** — modern trunks/WebRTC need it
  - Blocked: pion/opus is decode-only, no Go encoder without CGO
  - 48kHz resampling bridge
  - Branch: `feat/opus`

## Optimization

- [ ] **Goroutine-per-callback (~8-16/call)** — `call.go:554-593`
  Each callback fires via `go fn()`. Consider a single dispatch goroutine
  per call to reduce GC pressure under hundreds of concurrent calls.

## Correctness

- [ ] **`startRTPReader` captures stale `srtpIn` after re-INVITE** — `media.go:74`
  SRTP context is captured once at startup. If a re-INVITE changes keys,
  the RTP reader continues using the old context.

- [ ] **Media timeout fully suspended during hold** — `media.go:286`
  If the network drops while a call is on hold, the call stays in OnHold
  forever. Consider extending (not suspending) the timer during hold
  (e.g., 5 minutes).

## Other

- [ ] SIP INFO DTMF
- [ ] Attended transfer
