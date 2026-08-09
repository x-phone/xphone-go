package xphone

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Connect() must not hold p.mu across the STUN network round-trip (#106):
// concurrent State() calls should return promptly while STUN is in flight.
func TestConnect_DoesNotHoldMutexAcrossStun(t *testing.T) {
	orig := stunMappedAddr
	defer func() { stunMappedAddr = orig }()

	stunEntered := make(chan struct{})
	releaseStun := make(chan struct{})
	stunMappedAddr = func(server string, timeout time.Duration) (string, int, error) {
		close(stunEntered)
		<-releaseStun // hold "in the network" until the test lets go
		return "203.0.113.9", 3478, nil
	}

	p := &phone{
		cfg:     Config{StunServer: "stun.example.com:3478"},
		state:   PhoneStateDisconnected,
		localIP: "10.0.0.1",
		logger:  silentLogger,
	}

	connectReturned := make(chan struct{})
	go func() {
		// Connect will fail later (no real transport), but it must reach and
		// block inside STUN first — which is all this test cares about.
		_ = p.Connect(context.Background())
		close(connectReturned)
	}()

	<-stunEntered // Connect is now parked inside STUN

	// State() must not block behind the STUN round-trip.
	got := make(chan PhoneState, 1)
	go func() { got <- p.State() }()
	select {
	case st := <-got:
		assert.Equal(t, PhoneStateRegistering, st)
	case <-time.After(2 * time.Second):
		t.Fatal("State() blocked on p.mu while STUN was in flight — lock held across network I/O")
	}

	close(releaseStun)
	<-connectReturned
}
