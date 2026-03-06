package xphone

import "errors"

// Sentinel errors returned by Phone and Call methods.
var (
	// ErrNotRegistered is returned when an operation requires an active SIP registration.
	ErrNotRegistered = errors.New("xphone: not registered")
	// ErrCallNotFound is returned when a call ID does not match any active call.
	ErrCallNotFound = errors.New("xphone: call not found")
	// ErrInvalidState is returned when a call operation is invalid for the current state.
	ErrInvalidState = errors.New("xphone: invalid state for operation")
	// ErrMediaTimeout is returned when no RTP packets are received within the configured timeout.
	ErrMediaTimeout = errors.New("xphone: RTP media timeout")
	// ErrDialTimeout is returned when an outbound call is not answered before the dial timeout.
	ErrDialTimeout = errors.New("xphone: dial timeout exceeded before answer")
	// ErrNoRTPPortAvailable is returned when the RTP port range is exhausted.
	ErrNoRTPPortAvailable = errors.New("xphone: RTP port range exhausted")
	// ErrRegistrationFailed is returned when SIP registration is rejected by the server.
	ErrRegistrationFailed = errors.New("xphone: registration failed")
	// ErrTransferFailed is returned when a blind transfer (REFER) is rejected.
	ErrTransferFailed = errors.New("xphone: transfer failed")
	// ErrTLSConfigRequired is returned when TLS transport is selected without providing a TLS config.
	ErrTLSConfigRequired = errors.New("xphone: TLS transport requires TLSConfig")
	// ErrInvalidDTMFDigit is returned when SendDTMF receives a digit outside 0-9, *, #, A-D.
	ErrInvalidDTMFDigit = errors.New("xphone: invalid DTMF digit")
	// ErrAlreadyMuted is returned when Mute is called on an already-muted call.
	ErrAlreadyMuted = errors.New("xphone: already muted")
	// ErrNotMuted is returned when Unmute is called on a call that is not muted.
	ErrNotMuted = errors.New("xphone: not muted")
	// ErrAlreadyConnected is returned when Connect is called on a phone that is already connected.
	ErrAlreadyConnected = errors.New("xphone: already connected")
	// ErrNotConnected is returned when Disconnect is called on a phone that is not connected.
	ErrNotConnected = errors.New("xphone: not connected")
)
