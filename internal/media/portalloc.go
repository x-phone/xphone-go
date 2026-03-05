package media

import (
	"errors"
	"sync"
)

// ErrNoPortAvailable is returned when the RTP port range is exhausted.
var ErrNoPortAvailable = errors.New("media: RTP port range exhausted")

// PortAllocator allocates even-numbered RTP ports from a configured range
// using round-robin allocation.
type PortAllocator struct {
	mu   sync.Mutex
	min  int
	max  int
	pool []int
	next int
}

// NewPortAllocator creates a PortAllocator for even ports in [min, max].
func NewPortAllocator(min, max int) *PortAllocator {
	return &PortAllocator{
		min: min,
		max: max,
	}
}

// Allocate returns the next available even port, or ErrNoPortAvailable.
func (p *PortAllocator) Allocate() (int, error) {
	return 0, ErrNoPortAvailable
}

// Release returns a port to the pool for future allocation.
func (p *PortAllocator) Release(port int) {
}
