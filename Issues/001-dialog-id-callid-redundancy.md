# DialogID() and CallID() return the same value

## Status: RESOLVED

## Problem

`DialogID()` and `CallID()` on the `Call` interface both returned `dlg.CallID()` (the SIP Call-ID header). Two methods with different names returning the same value was misleading.

## Fix

- Removed `DialogID()` from the `Call` interface and all implementations
- Changed `ID()` to use Twilio-style `CA` prefix (`CA` + 32 hex chars) so it's visually distinct from a SIP Call-ID
- `ID()` — xphone's internal call identifier (e.g. `CA8f3a1b...`), use in app logic
- `CallID()` — the SIP Call-ID header, use for PBX log/CDR correlation
