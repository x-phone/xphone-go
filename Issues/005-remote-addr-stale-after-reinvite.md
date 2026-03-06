# remoteAddr not updated after re-INVITE changes remote SDP

## Problem

`call.remoteAddr` is set in `Accept()` and `Dial()` but not updated in `simulateReInvite()` (call.go), even though `remoteSDP` is updated there. If a re-INVITE changes the remote RTP endpoint (e.g., call transfer or address change), the media goroutine continues sending to the stale `remoteAddr` because it captures the value once at startup.

Additionally, `RemoteIP()`/`RemotePort()` re-parse `remoteSDP` on every call while the media pipeline uses the cached `remoteAddr` — these two sources of truth can diverge.

## Proposed fix

Update `remoteAddr` in `simulateReInvite()` when `remoteSDP` changes. Consider caching remote IP/port fields instead of re-parsing SDP in the accessor methods.
