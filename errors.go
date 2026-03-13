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
	// ErrNoVideo is returned when a video operation is called on an audio-only call.
	ErrNoVideo = errors.New("xphone: no video stream")
	// ErrAlreadyConnected is returned when Connect is called on a phone that is already connected.
	ErrAlreadyConnected = errors.New("xphone: already connected")
	// ErrNotConnected is returned when Disconnect is called on a phone that is not connected.
	ErrNotConnected = errors.New("xphone: not connected")
	// ErrHostRequired is returned when Connect is called without a Host configured.
	ErrHostRequired = errors.New("xphone: Host is required")
	// ErrSubscriptionRejected is returned when the server permanently rejects a SUBSCRIBE.
	ErrSubscriptionRejected = errors.New("xphone: subscription rejected")
	// ErrSubscriptionNotFound is returned when UnsubscribeEvent is called with an unknown ID.
	ErrSubscriptionNotFound = errors.New("xphone: subscription not found")
	// ErrVideoAlreadyActive is returned when AddVideo is called on a call that already has video.
	ErrVideoAlreadyActive = errors.New("xphone: video already active")
	// ErrAlreadyListening is returned when Listen is called on a server that is already listening.
	ErrAlreadyListening = errors.New("xphone: server already listening")
	// ErrNotListening is returned when an operation requires the server to be listening.
	ErrNotListening = errors.New("xphone: server not listening")
	// ErrPeerNotFound is returned when a dial target references an unknown peer name.
	ErrPeerNotFound = errors.New("xphone: peer not found")
	// ErrPeerRejected is returned when an incoming request fails peer authentication.
	ErrPeerRejected = errors.New("xphone: peer authentication failed")
)
