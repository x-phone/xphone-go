package xphone

import (
	"context"
	"fmt"
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
type sipgoDialogUAC struct {
	mu       sync.Mutex
	sess     clientSession
	invite   *sip.Request    // the original INVITE request
	response *sip.Response   // the final INVITE response (200 OK)
	cancelFn context.CancelFunc // cancels the WaitAnswer context
	onNotify func(int)
}

func (d *sipgoDialogUAC) Respond(code int, reason string, body []byte) error {
	return fmt.Errorf("xphone: UAC cannot send responses")
}

func (d *sipgoDialogUAC) SendBye() error {
	return fmt.Errorf("xphone: SendBye not yet implemented")
}

func (d *sipgoDialogUAC) SendCancel() error {
	return fmt.Errorf("xphone: SendCancel not yet implemented")
}

func (d *sipgoDialogUAC) SendReInvite(sdp []byte) error {
	return fmt.Errorf("xphone: SendReInvite not yet implemented")
}

func (d *sipgoDialogUAC) SendRefer(target string) error {
	return fmt.Errorf("xphone: SendRefer not yet implemented")
}

func (d *sipgoDialogUAC) OnNotify(fn func(code int)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onNotify = fn
}

func (d *sipgoDialogUAC) CallID() string {
	return ""
}

func (d *sipgoDialogUAC) Header(name string) []string {
	return nil
}

func (d *sipgoDialogUAC) Headers() map[string][]string {
	return nil
}

// Ensure sipgoDialogUAC satisfies the dialog interface at compile time.
var _ dialog = (*sipgoDialogUAC)(nil)
