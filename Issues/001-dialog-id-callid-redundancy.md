# DialogID() and CallID() return the same value

## Problem

Both methods on `call` delegate to `c.dlg.CallID()` and return identical values:

```go
func (c *call) DialogID() string { return c.dlg.CallID() }
func (c *call) CallID() string   { return c.dlg.CallID() }
```

Both are exposed on the `Call` interface. In SIP, a dialog identifier and a Call-ID header are distinct concepts (especially in forked-dialog scenarios). Having two methods with different names return the same value is misleading.

## Proposed fix

Either:
1. Differentiate them when real SIP dialog tracking is implemented (dialog ID = Call-ID + local tag + remote tag)
2. Remove `DialogID()` from the interface and keep only `CallID()` if they are not intended to diverge
