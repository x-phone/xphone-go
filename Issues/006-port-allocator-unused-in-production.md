# internal/media PortAllocator is unused in production code

## Problem

`internal/media/portalloc.go` contains a `PortAllocator` with round-robin and release semantics. Meanwhile, `call.go` has `listenRTPPort()` which does its own linear even-port scan. The two serve overlapping purposes but neither uses the other.

`PortAllocator` is only tested, never called from production code.

## Proposed fix

Either:
1. Wire `PortAllocator` into `listenRTPPort` (use it to pick ports, then bind)
2. Remove `PortAllocator` if the simpler `listenRTPPort` approach is sufficient
