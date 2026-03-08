# TODO — xphone-go

## Optimization

- [ ] **Goroutine-per-callback (~8-16/call)** — `call.go:554-593`
  Each callback fires via `go fn()`. Consider a single dispatch goroutine
  per call to reduce GC pressure under hundreds of concurrent calls.

## Other

- [ ] SIP INFO DTMF
- [ ] Attended transfer
