# "application/sdp" repeated as magic string in 4 production locations

## Problem

The string `"application/sdp"` appears as a raw literal in:
- `sipgo_dialog.go` — UAC `SendReInvite`, UAS `Respond`, UAS `SendReInvite`
- `sipua.go` — `dial()` INVITE

A typo in any one would silently cause SDP body mishandling.

## Proposed fix

Define a package-level constant: `const contentTypeSDP = "application/sdp"` and use it in all four locations.
