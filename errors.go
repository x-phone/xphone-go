package xphone

import "errors"

var (
	ErrNotRegistered      = errors.New("xphone: not registered")
	ErrCallNotFound       = errors.New("xphone: call not found")
	ErrInvalidState       = errors.New("xphone: invalid state for operation")
	ErrMediaTimeout       = errors.New("xphone: RTP media timeout")
	ErrDialTimeout        = errors.New("xphone: dial timeout exceeded before answer")
	ErrNoRTPPortAvailable = errors.New("xphone: RTP port range exhausted")
	ErrRegistrationFailed = errors.New("xphone: registration failed")
	ErrTransferFailed     = errors.New("xphone: transfer failed")
	ErrTLSConfigRequired  = errors.New("xphone: TLS transport requires TLSConfig")
	ErrInvalidDTMFDigit   = errors.New("xphone: invalid DTMF digit")
	ErrAlreadyMuted       = errors.New("xphone: already muted")
	ErrNotMuted           = errors.New("xphone: not muted")
	ErrAlreadyConnected   = errors.New("xphone: already connected")
	ErrNotConnected       = errors.New("xphone: not connected")
)
