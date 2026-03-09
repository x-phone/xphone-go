package xphone

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/emiago/sipgo/sip"
)

const contentTypeSDP = "application/sdp"
const contentTypeDTMFRelay = "application/dtmf-relay"
const contentTypeMWI = "application/simple-message-summary"
const contentTypeTextPlain = "text/plain"
const contentTypeDialogInfo = "application/dialog-info+xml"
const eventMWI = "message-summary"
const eventDialog = "dialog"

// sipRequestTimeout is the deadline for SIP dialog operations (BYE, re-INVITE, REFER).
const sipRequestTimeout = 5 * time.Second

// dialogSession is the minimal session interface shared by both UAC and UAS.
type dialogSession interface {
	Bye(ctx context.Context) error
	Do(ctx context.Context, req *sip.Request) (*sip.Response, error)
	WriteRequest(req *sip.Request) error
}

// dialogBase holds fields and methods shared by sipgoDialogUAC and sipgoDialogUAS.
type dialogBase struct {
	mu       sync.Mutex
	sess     dialogSession
	invite   *sip.Request  // immutable after construction
	response *sip.Response // immutable after construction
	onNotify func(int)
}

func (d *dialogBase) SendBye() error {
	ctx, cancel := context.WithTimeout(context.Background(), sipRequestTimeout)
	defer cancel()
	return d.sess.Bye(ctx)
}

func (d *dialogBase) SendReInvite(sdpBody []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), sipRequestTimeout)
	defer cancel()
	req := sip.NewRequest(sip.INVITE, d.invite.Recipient)
	if len(sdpBody) > 0 {
		req.AppendHeader(sip.NewHeader("Content-Type", contentTypeSDP))
	}
	req.SetBody(sdpBody)
	_, err := d.sess.Do(ctx, req)
	return err
}

func (d *dialogBase) SendRefer(target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), sipRequestTimeout)
	defer cancel()
	req := sip.NewRequest(sip.REFER, d.invite.Recipient)
	req.AppendHeader(sip.NewHeader("Refer-To", target))
	_, err := d.sess.Do(ctx, req)
	return err
}

func (d *dialogBase) SendInfoDTMF(digit string, duration int) error {
	body, err := EncodeInfoDTMF(digit, duration)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), sipRequestTimeout)
	defer cancel()
	req := sip.NewRequest(sip.INFO, d.invite.Recipient)
	req.AppendHeader(sip.NewHeader("Content-Type", contentTypeDTMFRelay))
	req.SetBody([]byte(body))
	_, err = d.sess.Do(ctx, req)
	return err
}

func (d *dialogBase) OnNotify(fn func(code int)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onNotify = fn
}

func (d *dialogBase) FireNotify(code int) {
	d.mu.Lock()
	fn := d.onNotify
	d.mu.Unlock()
	if fn != nil {
		fn(code)
	}
}

func (d *dialogBase) CallID() string {
	if h := d.invite.CallID(); h != nil {
		return h.Value()
	}
	return ""
}

func (d *dialogBase) Header(name string) []string {
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

func (d *dialogBase) Headers() map[string][]string {
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

// clientSession extends dialogSession with cancel support for outbound calls.
type clientSession interface {
	dialogSession
}

// sipgoDialogUAC implements the dialog interface for outbound (UAC) calls
// backed by a sipgo DialogClientSession.
type sipgoDialogUAC struct {
	dialogBase
	cancelFn     context.CancelFunc // cancels the WaitAnswer context
	rtpConn      net.PacketConn     // bound RTP socket from dial; ownership transferred to call
	srtpLocalKey string             // base64 SRTP inline key generated for this dialog
}

func (d *sipgoDialogUAC) Respond(code int, reason string, body []byte) error {
	return ErrInvalidState
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

// Ensure sipgoDialogUAC satisfies the dialog interface at compile time.
var _ dialog = (*sipgoDialogUAC)(nil)

// serverSession extends dialogSession with Respond and Close for inbound calls.
type serverSession interface {
	dialogSession
	Respond(statusCode int, reason string, body []byte, headers ...sip.Header) error
	Close() error
}

// sipgoDialogUAS implements the dialog interface for inbound (UAS) calls
// backed by a sipgo DialogServerSession.
type sipgoDialogUAS struct {
	dialogBase
}

func (d *sipgoDialogUAS) Respond(code int, reason string, body []byte) error {
	var headers []sip.Header
	if len(body) > 0 {
		headers = append(headers, sip.NewHeader("Content-Type", contentTypeSDP))
	}
	return d.sess.(serverSession).Respond(code, reason, body, headers...)
}

func (d *sipgoDialogUAS) SendCancel() error {
	return ErrInvalidState
}

// Ensure sipgoDialogUAS satisfies the dialog interface at compile time.
var _ dialog = (*sipgoDialogUAS)(nil)
