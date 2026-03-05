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
	mu        sync.Mutex
	ports     []int       // all even ports in [min,max]
	available map[int]bool // ports currently in pool
	cursor    int          // round-robin index into ports
}

// NewPortAllocator creates a PortAllocator for even ports in [min, max].
func NewPortAllocator(min, max int) *PortAllocator {
	var ports []int
	for p := min; p <= max; p++ {
		if p%2 == 0 {
			ports = append(ports, p)
		}
	}
	available := make(map[int]bool, len(ports))
	for _, p := range ports {
		available[p] = true
	}
	return &PortAllocator{
		ports:     ports,
		available: available,
		cursor:    0,
	}
}

// Allocate returns the next available even port, or ErrNoPortAvailable.
func (p *PortAllocator) Allocate() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	n := len(p.ports)
	for i := 0; i < n; i++ {
		idx := (p.cursor + i) % n
		port := p.ports[idx]
		if p.available[port] {
			delete(p.available, port)
			p.cursor = (idx + 1) % n
			return port, nil
		}
	}
	return 0, ErrNoPortAvailable
}

// Release returns a port to the pool for future allocation.
func (p *PortAllocator) Release(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.available[port] = true
}
