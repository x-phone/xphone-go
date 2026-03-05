# Extract BuildOffer placeholder IP/port into a helper

## Problem

Every call to `sdp.BuildOffer` from `call.go` passes the same placeholder values `"0.0.0.0"` and `0` for IP and port. These are meaningful RFC 4566 values (all-interfaces address, disabled stream) but are used here as "not yet allocated" placeholders. When real RTP port allocation is wired in, all four call sites must be hunted down and updated.

## Locations

- `call.go`: `Accept`, `Hold`, `Resume`, `startSessionTimer`

## Proposed fix

Add a helper method on `call` that encapsulates address/port resolution:

```go
func (c *call) buildLocalSDP(direction string) string {
    return sdp.BuildOffer("0.0.0.0", 0, defaultCodecPrefs(), direction)
}
```

When real port allocation lands, only this method needs to change.
