// Package turn implements a TURN client (RFC 5766) for NAT relay allocation.
//
// When STUN alone fails (symmetric NAT, enterprise firewalls), TURN allocates
// a relay address on the server that forwards media on the client's behalf.
//
// Supports:
//   - Allocate with long-term credentials (401 retry)
//   - CreatePermission for peer addresses
//   - ChannelBind for efficient 4-byte framed relay
//   - Background refresh loop (allocation + permissions + channels)
//   - ChannelData framing (RFC 5766 section 11)
package turn

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/x-phone/xphone-go/internal/stun"
)

// channelBinding maps a peer address to its bound channel number.
type channelBinding struct {
	peer    *net.UDPAddr
	channel uint16
}

// Client manages a TURN relay allocation on a TURN server.
type Client struct {
	mu sync.Mutex

	conn       net.PacketConn
	serverAddr *net.UDPAddr
	username   string
	password   string

	realm string
	nonce string
	key   []byte // cached LongTermKey(username, realm, password)

	relayAddr *net.UDPAddr
	lifetime  uint32

	channelBindings []channelBinding
	permissions     []*net.UDPAddr
	nextChannel     uint16

	cancel context.CancelFunc
	done   chan struct{}
	logger *slog.Logger
}

// NewClient creates a TURN client bound to conn, targeting serverAddr.
func NewClient(conn net.PacketConn, serverAddr *net.UDPAddr, username, password string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		conn:        conn,
		serverAddr:  serverAddr,
		username:    username,
		password:    password,
		nextChannel: 0x4000,
		logger:      logger,
	}
}

// Allocate sends an Allocate request and returns the relay address.
// Handles 401 (Unauthorized) by extracting realm/nonce and retrying.
// Starts the background refresh loop on success.
func (c *Client) Allocate() (*net.UDPAddr, error) {
	txnID := stun.GenerateTxnID()
	msg := stun.BuildMessage(stun.AllocateRequest, txnID, []stun.Attr{{
		Type:  stun.AttrRequestedTransport,
		Value: []byte{17, 0, 0, 0}, // UDP = protocol 17
	}})

	c.logger.Info("TURN Allocate", "server", c.serverAddr)
	resp, err := c.sendRecv(msg)
	if err != nil {
		return nil, err
	}

	msgType, ok := stun.MsgType(resp)
	if !ok {
		return nil, fmt.Errorf("turn: response too short")
	}

	if msgType == stun.AllocateError {
		if err := c.extractRealmNonce(resp); err != nil {
			return nil, err
		}
		c.logger.Debug("TURN got 401, retrying with credentials")
		return c.allocateAuthenticated()
	}

	if msgType == stun.AllocateResponse {
		addr, err := c.parseAllocateSuccess(resp)
		if err != nil {
			return nil, err
		}
		c.startRefreshLoop()
		return addr, nil
	}

	return nil, fmt.Errorf("turn: unexpected response type: 0x%04x", msgType)
}

func (c *Client) allocateAuthenticated() (*net.UDPAddr, error) {
	msg := c.buildAuthMsg(stun.AllocateRequest, []stun.Attr{{
		Type:  stun.AttrRequestedTransport,
		Value: []byte{17, 0, 0, 0},
	}})

	c.logger.Info("TURN Allocate (authenticated)", "server", c.serverAddr)
	resp, err := c.sendRecv(msg)
	if err != nil {
		return nil, err
	}

	msgType, ok := stun.MsgType(resp)
	if !ok {
		return nil, fmt.Errorf("turn: response too short")
	}

	if msgType == stun.AllocateError {
		attrs := stun.ParseAttrs(resp[stun.HeaderSize:])
		code, reason := stun.ParseErrorCode(attrs)
		return nil, fmt.Errorf("turn: Allocate rejected: %d %s", code, reason)
	}

	if msgType == stun.AllocateResponse {
		addr, err := c.parseAllocateSuccess(resp)
		if err != nil {
			return nil, err
		}
		c.startRefreshLoop()
		return addr, nil
	}

	return nil, fmt.Errorf("turn: unexpected response: 0x%04x", msgType)
}

func (c *Client) parseAllocateSuccess(resp []byte) (*net.UDPAddr, error) {
	if len(resp) < stun.HeaderSize {
		return nil, fmt.Errorf("turn: response too short")
	}
	attrs := stun.ParseAttrs(resp[stun.HeaderSize:])

	var relay *net.UDPAddr
	for _, a := range attrs {
		switch a.Type {
		case stun.AttrXORRelayedAddress:
			addr, err := stun.ParseXORAddr(a.Value)
			if err != nil {
				return nil, err
			}
			relay = addr
		case stun.AttrLifetime:
			if len(a.Value) >= 4 {
				lt := binary.BigEndian.Uint32(a.Value)
				c.mu.Lock()
				c.lifetime = lt
				c.mu.Unlock()
				c.logger.Debug("TURN server lifetime", "seconds", lt)
			}
		}
	}

	if relay == nil {
		return nil, fmt.Errorf("turn: no XOR-RELAYED-ADDRESS in response")
	}

	c.mu.Lock()
	c.relayAddr = relay
	c.mu.Unlock()

	c.logger.Info("TURN allocation succeeded", "relay", relay)
	return relay, nil
}

// CreatePermission creates a permission for the given peer address.
func (c *Client) CreatePermission(peer *net.UDPAddr) error {
	msg := c.buildAuthMsg(stun.CreatePermissionRequest, []stun.Attr{{
		Type:  stun.AttrXORPeerAddress,
		Value: stun.EncodeXORAddr(peer),
	}})

	c.logger.Debug("TURN CreatePermission", "peer", peer)
	resp, err := c.sendRecv(msg)
	if err != nil {
		return err
	}

	msgType, _ := stun.MsgType(resp)
	if msgType == stun.CreatePermissionResponse {
		c.mu.Lock()
		if !c.hasPermission(peer) {
			c.permissions = append(c.permissions, peer)
		}
		c.mu.Unlock()
		c.logger.Debug("TURN permission created", "peer", peer)
		return nil
	}

	return fmt.Errorf("turn: CreatePermission failed: 0x%04x", msgType)
}

// hasPermission returns true if peer is already in the permissions list.
// Must be called with c.mu held.
func (c *Client) hasPermission(peer *net.UDPAddr) bool {
	for _, p := range c.permissions {
		if p.IP.Equal(peer.IP) && p.Port == peer.Port {
			return true
		}
	}
	return false
}

// ChannelBind binds a channel to a peer address for efficient ChannelData relay.
// Returns the channel number assigned.
func (c *Client) ChannelBind(peer *net.UDPAddr) (uint16, error) {
	c.mu.Lock()
	channel := c.nextChannel
	if channel > 0x7FFE {
		c.mu.Unlock()
		return 0, fmt.Errorf("turn: channel numbers exhausted")
	}
	c.nextChannel++
	c.mu.Unlock()

	channelVal := make([]byte, 4)
	binary.BigEndian.PutUint16(channelVal[0:2], channel)

	msg := c.buildAuthMsg(stun.ChannelBindRequest, []stun.Attr{
		{Type: stun.AttrChannelNumber, Value: channelVal},
		{Type: stun.AttrXORPeerAddress, Value: stun.EncodeXORAddr(peer)},
	})

	c.logger.Debug("TURN ChannelBind", "peer", peer, "channel", channel)
	resp, err := c.sendRecv(msg)
	if err != nil {
		return 0, err
	}

	msgType, _ := stun.MsgType(resp)
	if msgType == stun.ChannelBindResponse {
		c.mu.Lock()
		c.channelBindings = append(c.channelBindings, channelBinding{peer: peer, channel: channel})
		c.mu.Unlock()
		c.logger.Debug("TURN channel bound", "peer", peer, "channel", channel)
		return channel, nil
	}

	return 0, fmt.Errorf("turn: ChannelBind failed: 0x%04x", msgType)
}

// Stop stops the refresh loop and sends a best-effort deallocate.
func (c *Client) Stop() {
	c.mu.Lock()
	cancel := c.cancel
	done := c.done
	c.cancel = nil
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}

	c.deallocate()
}

// RelayAddr returns the allocated relay address, or nil if not allocated.
func (c *Client) RelayAddr() *net.UDPAddr {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.relayAddr
}

// ChannelForPeer returns the channel number bound to peer, if any.
func (c *Client) ChannelForPeer(peer *net.UDPAddr) (uint16, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cb := range c.channelBindings {
		if cb.peer.IP.Equal(peer.IP) && cb.peer.Port == peer.Port {
			return cb.channel, true
		}
	}
	return 0, false
}

// ServerAddr returns the TURN server address.
func (c *Client) ServerAddr() *net.UDPAddr {
	return c.serverAddr
}

// --- Internal ---

// buildAuthMsg builds an authenticated STUN message with credentials and integrity.
func (c *Client) buildAuthMsg(msgType uint16, attrs []stun.Attr) []byte {
	txnID := stun.GenerateTxnID()
	attrs = append(attrs, c.credentialAttrs()...)
	msg := stun.BuildMessage(msgType, txnID, attrs)
	return stun.AppendIntegrity(msg, c.longTermKey())
}

func (c *Client) longTermKey() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.key
}

func (c *Client) credentialAttrs() []stun.Attr {
	c.mu.Lock()
	realm := c.realm
	nonce := c.nonce
	c.mu.Unlock()
	return []stun.Attr{
		{Type: stun.AttrUsername, Value: []byte(c.username)},
		{Type: stun.AttrRealm, Value: []byte(realm)},
		{Type: stun.AttrNonce, Value: []byte(nonce)},
	}
}

func (c *Client) sendRecv(msg []byte) ([]byte, error) {
	if _, err := c.conn.WriteTo(msg, c.serverAddr); err != nil {
		return nil, fmt.Errorf("turn: send: %w", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("turn: set deadline: %w", err)
	}
	defer c.conn.SetReadDeadline(time.Time{})

	buf := make([]byte, 2048)
	for {
		n, from, err := c.conn.ReadFrom(buf)
		if err != nil {
			return nil, fmt.Errorf("turn: recv: %w", err)
		}

		fromUDP, ok := from.(*net.UDPAddr)
		if !ok || !fromUDP.IP.Equal(c.serverAddr.IP) {
			continue
		}

		result := make([]byte, n)
		copy(result, buf[:n])
		return result, nil
	}
}

func (c *Client) extractRealmNonce(resp []byte) error {
	if len(resp) < stun.HeaderSize {
		return fmt.Errorf("turn: error response too short")
	}
	attrs := stun.ParseAttrs(resp[stun.HeaderSize:])

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, a := range attrs {
		switch a.Type {
		case stun.AttrRealm:
			c.realm = string(a.Value)
		case stun.AttrNonce:
			c.nonce = string(a.Value)
		}
	}
	if c.realm == "" {
		return fmt.Errorf("turn: no REALM in 401 response")
	}
	c.key = stun.LongTermKey(c.username, c.realm, c.password)
	return nil
}

func (c *Client) deallocate() {
	msg := c.buildAuthMsg(stun.RefreshRequest, []stun.Attr{{
		Type:  stun.AttrLifetime,
		Value: []byte{0, 0, 0, 0},
	}})

	c.logger.Info("TURN deallocate (LIFETIME=0)")
	// Best-effort: don't fail if the server doesn't respond.
	c.conn.WriteTo(msg, c.serverAddr)

	c.mu.Lock()
	c.relayAddr = nil
	c.permissions = nil
	c.channelBindings = nil
	c.mu.Unlock()
}

func (c *Client) startRefreshLoop() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	c.mu.Lock()
	oldCancel := c.cancel
	oldDone := c.done
	c.cancel = cancel
	c.done = done
	lifetime := c.lifetime
	c.mu.Unlock()

	// Stop any existing refresh loop before starting a new one.
	if oldCancel != nil {
		oldCancel()
	}
	if oldDone != nil {
		<-oldDone
	}

	go func() {
		defer close(done)

		// Refresh at half the lifetime, minimum 30s.
		refreshSec := lifetime / 2
		if refreshSec < 30 {
			refreshSec = 30
		}
		refreshInterval := time.Duration(refreshSec) * time.Second
		permInterval := 4 * time.Minute // RFC 5766: permissions expire at 5 min

		refreshTicker := time.NewTicker(refreshInterval)
		permTicker := time.NewTicker(permInterval)
		defer refreshTicker.Stop()
		defer permTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-refreshTicker.C:
				c.sendRefresh()
			case <-permTicker.C:
				c.refreshPermissions()
				c.refreshChannels()
			}
		}
	}()
}

func (c *Client) sendRefresh() {
	c.mu.Lock()
	lifetime := c.lifetime
	c.mu.Unlock()

	lt := make([]byte, 4)
	binary.BigEndian.PutUint32(lt, lifetime)

	msg := c.buildAuthMsg(stun.RefreshRequest, []stun.Attr{
		{Type: stun.AttrLifetime, Value: lt},
	})

	if _, err := c.conn.WriteTo(msg, c.serverAddr); err != nil {
		c.logger.Warn("TURN refresh send failed", "err", err)
	}
}

func (c *Client) refreshPermissions() {
	c.mu.Lock()
	peers := make([]*net.UDPAddr, len(c.permissions))
	copy(peers, c.permissions)
	c.mu.Unlock()

	for _, peer := range peers {
		msg := c.buildAuthMsg(stun.CreatePermissionRequest, []stun.Attr{{
			Type:  stun.AttrXORPeerAddress,
			Value: stun.EncodeXORAddr(peer),
		}})
		c.conn.WriteTo(msg, c.serverAddr)
	}
}

func (c *Client) refreshChannels() {
	c.mu.Lock()
	bindings := make([]channelBinding, len(c.channelBindings))
	copy(bindings, c.channelBindings)
	c.mu.Unlock()

	for _, cb := range bindings {
		channelVal := make([]byte, 4)
		binary.BigEndian.PutUint16(channelVal[0:2], cb.channel)
		msg := c.buildAuthMsg(stun.ChannelBindRequest, []stun.Attr{
			{Type: stun.AttrChannelNumber, Value: channelVal},
			{Type: stun.AttrXORPeerAddress, Value: stun.EncodeXORAddr(cb.peer)},
		})
		c.conn.WriteTo(msg, c.serverAddr)
	}
}

// --- ChannelData framing (RFC 5766 section 11) ---

// WrapChannelData wraps data in a ChannelData frame (4-byte header).
func WrapChannelData(channel uint16, data []byte) []byte {
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint16(buf[0:2], channel)
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(data)))
	copy(buf[4:], data)
	return buf
}

// ParseChannelData parses a ChannelData frame. Returns (channel, payload, ok).
func ParseChannelData(data []byte) (uint16, []byte, bool) {
	if len(data) < 4 {
		return 0, nil, false
	}
	channel := binary.BigEndian.Uint16(data[0:2])
	length := int(binary.BigEndian.Uint16(data[2:4]))
	if len(data) < 4+length {
		return 0, nil, false
	}
	return channel, data[4 : 4+length], true
}

// IsChannelData returns true if the first byte indicates a ChannelData message
// (0x40..0x7F per RFC 5764 section 5.1.2).
func IsChannelData(data []byte) bool {
	return len(data) > 0 && data[0] >= 0x40 && data[0] <= 0x7F
}
