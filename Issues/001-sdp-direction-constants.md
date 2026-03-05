# Add SDP direction constants

## Problem

Direction strings `"sendrecv"`, `"sendonly"`, `"recvonly"`, and `"inactive"` are used as raw string literals throughout the codebase. A typo at any call site silently produces malformed SDP.

## Locations

- `call.go`: `simulateReInvite` comparisons, `BuildOffer` calls in `Accept`, `Hold`, `Resume`, `startSessionTimer`
- `internal/sdp/sdp.go`: `Parse` switch cases, `MediaDesc.Direction` field comment

## Proposed fix

Add exported constants in the `sdp` package:

```go
const (
    DirSendRecv = "sendrecv"
    DirSendOnly = "sendonly"
    DirRecvOnly = "recvonly"
    DirInactive = "inactive"
)
```

Replace all raw string literals with these constants.
