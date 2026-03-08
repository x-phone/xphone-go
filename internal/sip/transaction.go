package sip

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

var (
	ErrTransactionTimeout = errors.New("sip: transaction timeout")
	ErrTransactionStopped = errors.New("sip: transaction manager stopped")
)

// pendingTx represents a pending client transaction waiting for a response.
type pendingTx struct {
	respCh chan *Message
}

// TransactionManager manages SIP client transactions.
// It dispatches incoming responses to the correct pending transaction by Via branch,
// and incoming requests to the OnRequest callback.
type TransactionManager struct {
	conn *Conn
	mu   sync.Mutex
	// pending maps Via branch to the transaction's response channel.
	pending   map[string]*pendingTx
	onRequest func(*Message, net.Addr) // callback for incoming requests
	stopped   bool
	done      chan struct{}
}

// NewTransactionManager creates a new TransactionManager and starts its read loop.
func NewTransactionManager(conn *Conn) *TransactionManager {
	tm := &TransactionManager{
		conn:    conn,
		pending: make(map[string]*pendingTx),
		done:    make(chan struct{}),
	}
	go tm.readLoop()
	return tm
}

// Stop shuts down the read loop and cancels all pending transactions.
// Blocked Send() and ReadResponse() calls unblock via the done channel.
func (tm *TransactionManager) Stop() {
	tm.mu.Lock()
	if tm.stopped {
		tm.mu.Unlock()
		return
	}
	tm.stopped = true
	tm.pending = make(map[string]*pendingTx)
	tm.mu.Unlock()
	close(tm.done)
}

// Send sends a SIP request and waits for the first response.
// It auto-generates a Via header with a unique branch if none is set.
// Returns the first response (provisional or final).
func (tm *TransactionManager) Send(req *Message, dst *net.UDPAddr, timeout time.Duration) (*Message, error) {
	tm.mu.Lock()
	if tm.stopped {
		tm.mu.Unlock()
		return nil, ErrTransactionStopped
	}

	// Generate branch and set Via if not present.
	branch := req.ViaBranch()
	if branch == "" {
		branch = generateBranch()
		localAddr := tm.conn.LocalAddr().(*net.UDPAddr)
		via := fmt.Sprintf("SIP/2.0/UDP %s:%d;branch=%s",
			localAddr.IP.String(), localAddr.Port, branch)
		req.SetHeader("Via", via)
	}

	// Register this transaction.
	tx := &pendingTx{
		respCh: make(chan *Message, 8), // buffer for provisional responses
	}
	tm.pending[branch] = tx
	tm.mu.Unlock()

	// Send the request.
	if err := tm.conn.Send(req.Bytes(), dst); err != nil {
		tm.removeTx(branch)
		return nil, err
	}

	// Wait for first response, timeout, or stop.
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case resp := <-tx.respCh:
		return resp, nil
	case <-timer.C:
		tm.removeTx(branch)
		return nil, ErrTransactionTimeout
	case <-tm.done:
		return nil, ErrTransactionStopped
	}
}

// ReadResponse reads the next response for a transaction identified by its Via branch.
// Used after Send() to consume subsequent provisional/final responses (e.g., for INVITE flows).
func (tm *TransactionManager) ReadResponse(branch string, timeout time.Duration) (*Message, error) {
	tm.mu.Lock()
	tx, ok := tm.pending[branch]
	tm.mu.Unlock()
	if !ok {
		return nil, errors.New("sip: no pending transaction for branch")
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case resp := <-tx.respCh:
		return resp, nil
	case <-timer.C:
		return nil, ErrTransactionTimeout
	case <-tm.done:
		return nil, ErrTransactionStopped
	}
}

// OnRequest registers a callback for incoming SIP requests (INVITE, BYE, etc.).
func (tm *TransactionManager) OnRequest(fn func(*Message, net.Addr)) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.onRequest = fn
}

// readLoop continuously reads from the connection and dispatches responses.
func (tm *TransactionManager) readLoop() {
	for {
		select {
		case <-tm.done:
			return
		default:
		}

		data, addr, err := tm.conn.Receive(500 * time.Millisecond)
		if err != nil {
			// Timeout — just loop again (check done).
			continue
		}

		msg, err := Parse(data)
		if err != nil {
			continue
		}

		// Dispatch incoming requests (INVITE, BYE, etc.) to the callback.
		if !msg.IsResponse() {
			tm.mu.Lock()
			fn := tm.onRequest
			tm.mu.Unlock()
			if fn != nil {
				fn(msg, addr)
			}
			continue
		}

		branch := msg.ViaBranch()
		if branch == "" {
			continue
		}

		tm.mu.Lock()
		tx, ok := tm.pending[branch]
		if ok {
			select {
			case tx.respCh <- msg:
			default:
				// Channel full — drop newest.
			}
		}
		tm.mu.Unlock()
	}
}

// RemoveTx removes a completed transaction from the pending map.
// Callers should call this after they are done reading responses for a transaction.
func (tm *TransactionManager) RemoveTx(branch string) {
	tm.removeTx(branch)
}

func (tm *TransactionManager) removeTx(branch string) {
	tm.mu.Lock()
	delete(tm.pending, branch)
	tm.mu.Unlock()
}

// generateBranch creates a unique Via branch per RFC 3261 §8.1.1.7.
// Branches must start with "z9hG4bK" (magic cookie).
func generateBranch() string {
	b := make([]byte, 12)
	rand.Read(b)
	return fmt.Sprintf("z9hG4bK%x", b)
}
