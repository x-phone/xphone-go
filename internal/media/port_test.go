package media

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPortAllocator_AllocatesEvenPorts(t *testing.T) {
	pa := NewPortAllocator(10000, 10010)

	for i := 0; i < 5; i++ {
		port, err := pa.Allocate()
		require.NoError(t, err)
		assert.Equal(t, 0, port%2, "port %d is not even", port)
	}
}

func TestPortAllocator_RoundRobin(t *testing.T) {
	// Range 10000-10004 has even ports: 10000, 10002, 10004 → 3 slots.
	pa := NewPortAllocator(10000, 10004)

	p1, err := pa.Allocate()
	require.NoError(t, err)
	assert.Equal(t, 10000, p1)

	p2, err := pa.Allocate()
	require.NoError(t, err)
	assert.Equal(t, 10002, p2)

	// Release first port — it re-enters pool but cursor doesn't rewind.
	pa.Release(p1)

	// Next allocation must continue forward to 10004, not jump back to 10000.
	p3, err := pa.Allocate()
	require.NoError(t, err)
	assert.Equal(t, 10004, p3, "round-robin must continue forward, not restart from min")

	// Now wrap around — only 10000 is available (released earlier).
	p4, err := pa.Allocate()
	require.NoError(t, err)
	assert.Equal(t, 10000, p4, "round-robin must wrap around to re-use released port")

	// Pool exhausted again.
	_, err = pa.Allocate()
	assert.ErrorIs(t, err, ErrNoPortAvailable)
}

func TestPortAllocator_Exhaustion(t *testing.T) {
	// Range 10000-10002 has even ports: 10000, 10002 → 2 slots.
	pa := NewPortAllocator(10000, 10002)

	_, err := pa.Allocate()
	require.NoError(t, err)

	_, err = pa.Allocate()
	require.NoError(t, err)

	// Pool exhausted.
	_, err = pa.Allocate()
	assert.ErrorIs(t, err, ErrNoPortAvailable)
}

func TestPortAllocator_ReleaseReuse(t *testing.T) {
	// Only one even port in range: 10000.
	pa := NewPortAllocator(10000, 10000)

	port, err := pa.Allocate()
	require.NoError(t, err)
	assert.Equal(t, 10000, port)

	// Exhausted.
	_, err = pa.Allocate()
	require.Error(t, err)

	// Release and re-allocate.
	pa.Release(port)
	port2, err := pa.Allocate()
	require.NoError(t, err)
	assert.Equal(t, 10000, port2)
}

func TestPortAllocator_ConcurrentSafety(t *testing.T) {
	// 50 even ports: 10000, 10002, ..., 10098.
	pa := NewPortAllocator(10000, 10099)

	var wg sync.WaitGroup
	results := make(chan int, 50)
	errs := make(chan error, 50)

	// 50 goroutines each allocate one port.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			port, err := pa.Allocate()
			if err != nil {
				errs <- err
				return
			}
			results <- port
		}()
	}

	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("unexpected error: %v", err)
	}

	// All ports must be unique and even.
	seen := make(map[int]bool)
	for port := range results {
		assert.Equal(t, 0, port%2, "port %d is not even", port)
		assert.False(t, seen[port], "duplicate port %d", port)
		seen[port] = true
	}
	assert.Equal(t, 50, len(seen))
}
