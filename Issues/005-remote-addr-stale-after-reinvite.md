# remoteAddr not updated after re-INVITE changes remote SDP

## Status: RESOLVED

## Problem

`call.remoteAddr` was set in `Accept()` and `Dial()` but not updated in `simulateReInvite()`, even though `remoteSDP` was updated there. The media goroutine captured `remoteAddr` once at startup and continued sending to the stale address.

Additionally, `RemoteIP()`/`RemotePort()` re-parsed `remoteSDP` on every call while the media pipeline used the cached `remoteAddr` — two sources of truth that could diverge.

## Fix

- `simulateReInvite()` now calls `setRemoteEndpoint()` which updates `remoteAddr`, `remoteIP`, and `remotePort` atomically
- Media goroutine reads `c.remoteAddr` under lock on each send instead of capturing once at startup
- `RemoteIP()`/`RemotePort()` use cached fields with SDP-parse fallback for test compatibility
