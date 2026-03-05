package sip

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ClientConfig holds the configuration for a SIP client.
type ClientConfig struct {
	LocalAddr  string       // local bind address (e.g. "0.0.0.0:0")
	ServerAddr *net.UDPAddr // SIP server address
	Username   string
	Password   string
	Domain     string // SIP domain (e.g. "pbx.local")
}

// Client is a SIP UA client that can send REGISTER and other requests,
// and receive incoming requests (INVITE, BYE, etc.).
type Client struct {
	conn   *Conn
	tm     *TransactionManager
	cfg    ClientConfig
	cseq   atomic.Int64
	callID string // persistent Call-ID for REGISTER

	mu         sync.Mutex
	onIncoming func(*Message)
	branch     string // last transaction branch (for ReadResponse)
	closed     bool
}

// NewClient creates a new SIP client, binding to LocalAddr.
func NewClient(cfg ClientConfig) (*Client, error) {
	conn, err := Listen("udp", cfg.LocalAddr)
	if err != nil {
		return nil, err
	}
	tm := NewTransactionManager(conn)
	c := &Client{
		conn:   conn,
		tm:     tm,
		cfg:    cfg,
		callID: generateBranch(), // reuse random generator for a unique Call-ID
	}
	c.cseq.Store(0)
	return c, nil
}

// LocalAddr returns the local address the client is bound to.
func (c *Client) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

// Close shuts down the client.
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.tm.Stop()
	return c.conn.Close()
}

// OnIncoming registers a callback for incoming SIP requests.
// The callback receives the parsed request message.
func (c *Client) OnIncoming(fn func(*Message)) {
	c.mu.Lock()
	c.onIncoming = fn
	c.mu.Unlock()
	c.tm.OnRequest(func(msg *Message, addr net.Addr) {
		c.mu.Lock()
		cb := c.onIncoming
		c.mu.Unlock()
		if cb != nil {
			cb(msg)
		}
	})
}

// SendRegister sends a REGISTER request.
// Handles 401 auth challenges automatically.
// Returns (response code, WWW-Authenticate or reason, error).
func (c *Client) SendRegister(ctx context.Context) (int, string, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, "", ErrTransactionStopped
	}
	c.mu.Unlock()

	req := c.buildRequest("REGISTER", "sip:"+c.cfg.Domain, nil)

	timeout := contextTimeout(ctx, 5*time.Second)
	resp, err := c.tm.Send(req, c.cfg.ServerAddr, timeout)
	if err != nil {
		return 0, "", err
	}
	branch := req.ViaBranch()
	defer c.tm.RemoveTx(branch)

	// Handle 401 auth challenge.
	if resp.StatusCode == 401 {
		authHdr := resp.Header("WWW-Authenticate")
		ch, err := ParseChallenge(authHdr)
		if err != nil {
			return 401, authHdr, nil
		}
		creds := &Credentials{
			Username: c.cfg.Username,
			Password: c.cfg.Password,
		}
		authVal := BuildAuthorization(ch, creds, "REGISTER", "sip:"+c.cfg.Domain)

		// Retry with Authorization.
		retry := c.buildRequest("REGISTER", "sip:"+c.cfg.Domain, map[string]string{
			"Authorization": authVal,
		})
		resp, err = c.tm.Send(retry, c.cfg.ServerAddr, timeout)
		if err != nil {
			return 0, "", err
		}
		retryBranch := retry.ViaBranch()
		defer c.tm.RemoveTx(retryBranch)
	}

	return resp.StatusCode, resp.Reason, nil
}

// SendKeepalive sends a CRLF NAT keepalive packet to the server.
func (c *Client) SendKeepalive() error {
	return c.conn.Send([]byte("\r\n\r\n"), c.cfg.ServerAddr)
}

// buildRequest creates a SIP request with standard headers.
func (c *Client) buildRequest(method, requestURI string, extraHeaders map[string]string) *Message {
	seq := c.cseq.Add(1)
	localAddr := c.conn.LocalAddr().(*net.UDPAddr)

	msg := &Message{
		Method:     method,
		RequestURI: requestURI,
	}

	fromTag := generateBranch()[:8]
	msg.SetHeader("From", fmt.Sprintf("<sip:%s@%s>;tag=%s",
		c.cfg.Username, c.cfg.Domain, fromTag))
	msg.SetHeader("To", fmt.Sprintf("<sip:%s@%s>",
		c.cfg.Username, c.cfg.Domain))
	msg.SetHeader("Call-ID", c.callID)
	msg.SetHeader("CSeq", fmt.Sprintf("%d %s", seq, method))
	msg.SetHeader("Contact", fmt.Sprintf("<sip:%s@%s:%d>",
		c.cfg.Username, localAddr.IP.String(), localAddr.Port))
	msg.SetHeader("Max-Forwards", "70")
	msg.SetHeader("User-Agent", "xphone")

	for k, v := range extraHeaders {
		msg.SetHeader(k, v)
	}

	return msg
}

// contextTimeout extracts the remaining time from a context deadline,
// capped at maxTimeout. Returns maxTimeout if the context has no deadline.
func contextTimeout(ctx context.Context, maxTimeout time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < maxTimeout {
			return remaining
		}
	}
	return maxTimeout
}
