# Deduplicate codecNames map in test helpers

## Problem

The payload-type-to-name mapping `{0: "PCMU/8000", 8: "PCMA/8000", 9: "G722/8000", 111: "opus/48000/2"}` appears in three places:

- `internal/sdp/sdp.go` (production, `codecNames` package var)
- `internal/sdp/sdp_test.go` (`sampleSDP` local var)
- `call_test.go` (`testSDP` local var)

## Proposed fix

Both `sampleSDP` and `testSDP` are functionally identical SDP-building helpers. Options:

1. Export `codecNames` from the `sdp` package and reference it in tests
2. Have test helpers call `sdp.BuildOffer` directly instead of reimplementing the same logic
3. Move the shared test helper into `testutil/`
