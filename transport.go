package xphone

import "context"

// sipTransport is the internal interface for SIP transport.
// Production: implemented by sipUA (backed by sipgo).
// Tests: implemented by testutil.MockTransport.
type sipTransport interface {
	// SendRequest sends a SIP request and waits for a final response.
	// Returns (response code, response header value, error).
	// The headers map is included in the outgoing message (e.g. Authorization).
	SendRequest(ctx context.Context, method string, headers map[string]string) (int, string, error)

	// ReadResponse reads the next response for the current dialog.
	// Used after SendRequest("INVITE") to consume provisional (1xx) and final (2xx) responses.
	// Returns (response code, response header/reason, error).
	ReadResponse(ctx context.Context) (int, string, error)

	// SendSubscribe sends a SIP SUBSCRIBE to the given URI with the specified headers.
	// Used for MWI (RFC 3842). Returns (response code, reason, error).
	SendSubscribe(ctx context.Context, uri string, headers map[string]string) (int, string, error)

	// SendMessage sends a SIP MESSAGE to the given URI with optional headers.
	// Returns (response code, reason, error).
	SendMessage(ctx context.Context, uri string, contentType string, body string, headers map[string]string) (int, string, error)

	// OnMessage registers a callback for incoming SIP MESSAGE requests.
	// Parameters: from, to, contentType, body.
	OnMessage(fn func(from, to, contentType, body string))

	// OnMWINotify registers a callback for incoming MWI NOTIFY bodies.
	// The callback receives the raw application/simple-message-summary body.
	OnMWINotify(fn func(body string))

	// SendKeepalive sends a NAT keepalive packet.
	SendKeepalive() error

	// Respond sends a SIP response to an incoming request (e.g. 100 Trying, 180 Ringing).
	Respond(code int, reason string)

	// OnDrop registers a callback that fires when the transport connection drops.
	OnDrop(fn func())

	// OnIncoming registers a callback for incoming SIP requests (e.g. INVITE).
	OnIncoming(fn func(from, to string))

	Close() error
}
