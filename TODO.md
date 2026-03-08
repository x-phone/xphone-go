# TODO — xphone-go

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
