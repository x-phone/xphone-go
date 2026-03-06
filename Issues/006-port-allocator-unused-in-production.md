# internal/media PortAllocator is unused in production code

## Status: RESOLVED

## Fix

Removed `internal/media/portalloc.go` and `internal/media/port_test.go`. Production code uses `listenRTPPort()` in `call.go` which does a linear even-port scan and actually binds the socket, making a separate allocator unnecessary.
