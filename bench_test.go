package xphone

import (
	"io"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/x-phone/xphone-go/testutil"
)

var silentLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func silentCall() *call {
	c := newInboundCall(testutil.NewMockDialog())
	c.logger = silentLogger
	return c
}

// BenchmarkCallLifecycle measures allocations for a full call lifecycle:
// create → accept → hold → resume → end, with all callbacks registered.
func BenchmarkCallLifecycle(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c := silentCall()

		// Register all callback types.
		c.OnState(func(CallState) {})
		c.OnEnded(func(EndReason) {})
		c.OnHold(func() {})
		c.OnResume(func() {})
		c.OnMute(func() {})
		c.OnUnmute(func() {})

		c.Accept()
		c.Hold()
		c.Resume()
		c.Mute()
		c.Unmute()
		c.End()
	}
}

// BenchmarkCallLifecycle_Parallel measures throughput under concurrent load.
func BenchmarkCallLifecycle_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c := silentCall()
			c.OnState(func(CallState) {})
			c.OnEnded(func(EndReason) {})
			c.Accept()
			c.Hold()
			c.Resume()
			c.End()
		}
	})
}

// TestGoroutineLeak_ManyCallLifecycles creates many calls through their full
// lifecycle and verifies that goroutine count returns to baseline.
func TestGoroutineLeak_ManyCallLifecycles(t *testing.T) {
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const numCalls = 500
	var wg sync.WaitGroup
	wg.Add(numCalls)

	for i := 0; i < numCalls; i++ {
		go func() {
			defer wg.Done()
			c := silentCall()
			c.OnState(func(CallState) {})
			c.OnEnded(func(EndReason) {})
			c.Accept()
			c.Hold()
			c.Resume()
			c.End()
		}()
	}
	wg.Wait()

	// Allow dispatch goroutines to drain and exit.
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	after := runtime.NumGoroutine()
	// Allow a small margin for runtime goroutines.
	leaked := after - baseline
	assert.LessOrEqual(t, leaked, 5,
		"goroutine leak: baseline=%d after=%d leaked=%d", baseline, after, leaked)
	t.Logf("goroutines: baseline=%d after=%d delta=%d", baseline, after, leaked)
}

// TestGoroutineLeak_AbandonedCalls verifies that calls ended via Reject
// (short lifecycle) don't leak goroutines.
func TestGoroutineLeak_AbandonedCalls(t *testing.T) {
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const numCalls = 500
	for i := 0; i < numCalls; i++ {
		c := silentCall()
		c.Reject(486, "Busy Here")
	}

	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	after := runtime.NumGoroutine()
	leaked := after - baseline
	assert.LessOrEqual(t, leaked, 5,
		"goroutine leak: baseline=%d after=%d leaked=%d", baseline, after, leaked)
	t.Logf("goroutines: baseline=%d after=%d delta=%d", baseline, after, leaked)
}
