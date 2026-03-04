package xphone

// CallState represents the current state of a call.
type CallState int

const (
	StateIdle          CallState = iota
	StateRinging                 // inbound: INVITE received, not yet accepted
	StateDialing                 // outbound: INVITE sent, no response yet
	StateRemoteRinging           // outbound: 180 received
	StateEarlyMedia              // outbound: 183 received + WithEarlyMedia set
	StateActive                  // call established, RTP flowing
	StateOnHold                  // re-INVITE with a=sendonly/inactive
	StateEnded                   // terminal
)

// PhoneState represents the registration state of the phone.
type PhoneState int

const (
	PhoneStateDisconnected    PhoneState = iota
	PhoneStateRegistering
	PhoneStateRegistered
	PhoneStateUnregistering
	PhoneStateRegistrationFailed
)

// EndReason describes why a call ended.
type EndReason int

const (
	EndedByLocal     EndReason = iota // End() while Active/OnHold
	EndedByRemote                     // BYE received
	EndedByTimeout                    // MediaTimeout exceeded
	EndedByError                      // internal or transport error
	EndedByTransfer                   // REFER completed
	EndedByRejected                   // Reject() called
	EndedByCancelled                  // End() before 200 OK (outbound)
)

// Direction indicates whether a call is inbound or outbound.
type Direction int

const (
	DirectionInbound  Direction = iota
	DirectionOutbound
)
