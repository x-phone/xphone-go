# localIPFor() called multiple times per call instead of being cached

## Problem

`localIPFor(cfg.Host)` is called:
1. Once in `newSipUA()` (at Connect time)
2. Once in `sipUA.dial()` (per outbound call)
3. Once in `xphone.go` `Dial()` (per outbound call — `c.localIP`)
4. Once in `ensureRTPPort()` (per inbound accept)

Each call opens a UDP socket (syscall). The result is deterministic for a given host and won't change during a phone session.

## Proposed fix

Cache the result on the `sipUA` struct at construction time and reuse it in `dial()`. For the phone level, `localIPFor` is already cached on the call via `c.localIP` after the first call to `ensureRTPPort`, but the redundant call in `xphone.go:245` could use the cached value from `sipUA`.
