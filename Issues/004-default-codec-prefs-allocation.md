# Consider making defaultCodecPrefs a package-level var

## Problem

`defaultCodecPrefs()` allocates a new `[]int{8, 0, 9, 111}` slice on every call. It is called in `Accept`, `Hold`, `Resume`, `simulateReInvite`, `negotiateCodec`, and the session timer callback. No callers mutate the returned slice.

## Severity

Low. All call sites are on the signalling path (not per-packet), so the allocation cost is negligible in practice.

## Proposed fix

Replace the function with a package-level variable:

```go
var defaultCodecPrefs = []int{8, 0, 9, 111}
```

Alternatively, long-term this should come from `Config.CodecPrefs` (already defined in `options.go`) once `Config` is threaded into the `call` struct.
