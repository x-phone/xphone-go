package xphone

import (
	"context"
	"sync"

	"github.com/emiago/sipgo/sip"
)

// clientSession is a thin interface wrapping the methods xphone needs from
// sipgo's DialogClientSession. This exists to enable unit testing without
// a real SIP stack.
type clientSession interface {
	Bye(ctx context.Context) error
	Do(ctx context.Context, req *sip.Request) (*sip.Response, error)
	WriteRequest(req *sip.Request) error
}

// sipgoDialogUAC implements the dialog interface for outbound (UAC) calls
// backed by a sipgo DialogClientSession.
//
// sess, invite, and response are immutable after construction.
// mu protects only cancelFn and onNotify.
type sipgoDialogUAC struct {
	mu       sync.Mutex
	sess     clientSession
	invite   *sip.Request       // immutable after construction
	response *sip.Response      // immutable after construction
	cancelFn context.CancelFunc // cancels the WaitAnswer context
	onNotify func(int)
}

func (d *sipgoDialogUAC) Respond(code int, reason string, body []byte) error {
	return ErrInvalidState
}

func (d *sipgoDialogUAC) SendBye() error {
	return d.sess.Bye(context.Background())
}

func (d *sipgoDialogUAC) SendCancel() error {
	d.mu.Lock()
	fn := d.cancelFn
	d.mu.Unlock()
	if fn == nil {
		return ErrInvalidState
	}
	fn()
	return nil
}

func (d *sipgoDialogUAC) SendReInvite(sdpBody []byte) error {
	req := sip.NewRequest(sip.INVITE, d.invite.Recipient)
	req.SetBody(sdpBody)
	_, err := d.sess.Do(context.Background(), req)
	return err
}

func (d *sipgoDialogUAC) SendRefer(target string) error {
	req := sip.NewRequest(sip.REFER, d.invite.Recipient)
	req.AppendHeader(sip.NewHeader("Refer-To", target))
	_, err := d.sess.Do(context.Background(), req)
	return err
}

func (d *sipgoDialogUAC) OnNotify(fn func(code int)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onNotify = fn
}

func (d *sipgoDialogUAC) CallID() string {
	if h := d.invite.CallID(); h != nil {
		return h.Value()
	}
	return ""
}

func (d *sipgoDialogUAC) Header(name string) []string {
	var vals []string
	// Check response first (Session-Expires, updated To tag, etc.)
	if d.response != nil {
		for _, h := range d.response.GetHeaders(name) {
			vals = append(vals, h.Value())
		}
	}
	if len(vals) > 0 {
		return vals
	}
	// Fall back to request headers
	if d.invite != nil {
		for _, h := range d.invite.GetHeaders(name) {
			vals = append(vals, h.Value())
		}
	}
	return vals
}

func (d *sipgoDialogUAC) Headers() map[string][]string {
	result := make(map[string][]string)
	// Collect from response first (has updated To tag, Session-Expires, etc.)
	if d.response != nil {
		for _, h := range d.response.Headers() {
			name := h.Name()
			result[name] = append(result[name], h.Value())
		}
	}
	// Add request-only headers not already present from response
	if d.invite != nil {
		for _, h := range d.invite.Headers() {
			name := h.Name()
			if _, exists := result[name]; !exists {
				result[name] = append(result[name], h.Value())
			}
		}
	}
	return result
}

// Ensure sipgoDialogUAC satisfies the dialog interface at compile time.
var _ dialog = (*sipgoDialogUAC)(nil)

// serverSession is a thin interface wrapping the methods xphone needs from
// sipgo's DialogServerSession. This exists to enable unit testing without
// a real SIP stack.
type serverSession interface {
	Respond(statusCode int, reason string, body []byte, headers ...sip.Header) error
	Bye(ctx context.Context) error
	Do(ctx context.Context, req *sip.Request) (*sip.Response, error)
	WriteRequest(req *sip.Request) error
	Close() error
}

// sipgoDialogUAS implements the dialog interface for inbound (UAS) calls
// backed by a sipgo DialogServerSession.
//
// sess, invite, and response are immutable after construction.
// mu protects only onNotify.
type sipgoDialogUAS struct {
	mu       sync.Mutex
	sess     serverSession
	invite   *sip.Request  // immutable after construction
	response *sip.Response // immutable after construction
	onNotify func(int)
}

func (d *sipgoDialogUAS) Respond(code int, reason string, body []byte) error {
	return d.sess.Respond(code, reason, body)
}

func (d *sipgoDialogUAS) SendBye() error {
	return d.sess.Bye(context.Background())
}

func (d *sipgoDialogUAS) SendCancel() error {
	return ErrInvalidState
}

func (d *sipgoDialogUAS) SendReInvite(sdpBody []byte) error {
	req := sip.NewRequest(sip.INVITE, d.invite.Recipient)
	req.SetBody(sdpBody)
	_, err := d.sess.Do(context.Background(), req)
	return err
}

func (d *sipgoDialogUAS) SendRefer(target string) error {
	req := sip.NewRequest(sip.REFER, d.invite.Recipient)
	req.AppendHeader(sip.NewHeader("Refer-To", target))
	_, err := d.sess.Do(context.Background(), req)
	return err
}

func (d *sipgoDialogUAS) OnNotify(fn func(code int)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onNotify = fn
}

func (d *sipgoDialogUAS) CallID() string {
	if h := d.invite.CallID(); h != nil {
		return h.Value()
	}
	return ""
}

func (d *sipgoDialogUAS) Header(name string) []string {
	var vals []string
	if d.response != nil {
		for _, h := range d.response.GetHeaders(name) {
			vals = append(vals, h.Value())
		}
	}
	if len(vals) > 0 {
		return vals
	}
	if d.invite != nil {
		for _, h := range d.invite.GetHeaders(name) {
			vals = append(vals, h.Value())
		}
	}
	return vals
}

func (d *sipgoDialogUAS) Headers() map[string][]string {
	result := make(map[string][]string)
	if d.response != nil {
		for _, h := range d.response.Headers() {
			name := h.Name()
			result[name] = append(result[name], h.Value())
		}
	}
	if d.invite != nil {
		for _, h := range d.invite.Headers() {
			name := h.Name()
			if _, exists := result[name]; !exists {
				result[name] = append(result[name], h.Value())
			}
		}
	}
	return result
}

// Ensure sipgoDialogUAS satisfies the dialog interface at compile time.
var _ dialog = (*sipgoDialogUAS)(nil)
