// Package ice implements ICE-Lite (RFC 8445 §2.2) for SIP media NAT traversal.
//
// ICE-Lite only responds to incoming STUN connectivity checks — it never
// initiates checks. This is sufficient for SIP telephony where the remote
// peer (PBX, trunk, or WebRTC gateway) is typically the controlling agent.
//
// Supports:
//   - Candidate gathering (host, server-reflexive, relay)
//   - ICE credential generation (ufrag + pwd)
//   - STUN Binding Request validation and response
//   - USE-CANDIDATE nomination handling
package ice

import (
	"crypto/rand"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/x-phone/xphone-go/internal/stun"
)

// CandidateType represents an ICE candidate type per RFC 8445 §4.1.1.
type CandidateType int

const (
	CandidateHost            CandidateType = iota // Local address on a network interface.
	CandidateServerReflexive                      // NAT-mapped address discovered via STUN.
	CandidateRelay                                // Address on a TURN relay server.
)

// typePreference returns the type preference for priority calculation (RFC 8445 §5.1.2.1).
func (t CandidateType) typePreference() uint32 {
	switch t {
	case CandidateHost:
		return 126
	case CandidateServerReflexive:
		return 100
	case CandidateRelay:
		return 0
	default:
		return 0
	}
}

func (t CandidateType) String() string {
	switch t {
	case CandidateHost:
		return "host"
	case CandidateServerReflexive:
		return "srflx"
	case CandidateRelay:
		return "relay"
	default:
		return "unknown"
	}
}

// Candidate represents a single ICE candidate.
type Candidate struct {
	Foundation string
	Component  uint32
	Transport  string
	Priority   uint32
	Addr       *net.UDPAddr
	Type       CandidateType
	RelAddr    *net.UDPAddr // base for srflx, srflx for relay; nil for host
}

// SDPValue formats the candidate as an SDP a=candidate: value (without the prefix).
func (c *Candidate) SDPValue() string {
	s := fmt.Sprintf("%s %d %s %d %s %d typ %s",
		c.Foundation, c.Component, c.Transport, c.Priority,
		c.Addr.IP, c.Addr.Port, c.Type)
	if c.RelAddr != nil {
		s += fmt.Sprintf(" raddr %s rport %d", c.RelAddr.IP, c.RelAddr.Port)
	}
	return s
}

// Credentials holds ICE authentication credentials (ice-ufrag and ice-pwd).
type Credentials struct {
	Ufrag string // Username fragment (4+ characters).
	Pwd   string // Password (22+ characters / 128+ bits).
}

// GenerateCredentials generates random ICE credentials.
func GenerateCredentials() Credentials {
	return Credentials{
		Ufrag: randomICEString(8),
		Pwd:   randomICEString(24),
	}
}

// Agent is an ICE-Lite agent that stores local credentials, candidates,
// and responds to incoming STUN Binding Requests.
// Fields are immutable after construction; only nomination state changes.
type Agent struct {
	localCreds  Credentials
	candidates  []Candidate
	ufragPrefix string // "localufrag:" for fast matching

	mu            sync.Mutex
	remoteCreds   *Credentials // reserved for future use (ICE full agent role reversal)
	nominatedAddr *net.UDPAddr
}

// NewAgent creates a new ICE-Lite agent with the given credentials and candidates.
func NewAgent(creds Credentials, candidates []Candidate) *Agent {
	return &Agent{
		localCreds:  creds,
		candidates:  candidates,
		ufragPrefix: creds.Ufrag + ":",
	}
}

// LocalCreds returns the agent's local ICE credentials.
func (a *Agent) LocalCreds() Credentials { return a.localCreds }

// Candidates returns the agent's gathered ICE candidates.
func (a *Agent) Candidates() []Candidate { return a.candidates }

// SetRemoteCredentials sets the remote ICE credentials (parsed from remote SDP).
func (a *Agent) SetRemoteCredentials(creds Credentials) {
	a.mu.Lock()
	a.remoteCreds = &creds
	a.mu.Unlock()
}

// NominatedAddr returns the nominated peer address, or nil if not yet nominated.
func (a *Agent) NominatedAddr() *net.UDPAddr {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.nominatedAddr
}

// HandleBindingRequest validates an incoming STUN Binding Request and returns
// the response bytes to send back. Returns nil if the request is invalid.
func (a *Agent) HandleBindingRequest(data []byte, from *net.UDPAddr) []byte {
	if !stun.IsMessage(data) {
		return nil
	}

	msgType, ok := stun.MsgType(data)
	if !ok || msgType != stun.BindingRequest {
		return nil
	}

	txnID, ok := stun.TxnID(data)
	if !ok {
		return nil
	}

	attrs := stun.ParseAttrs(data[stun.HeaderSize:])

	// Validate USERNAME: must be "local_ufrag:remote_ufrag".
	usernameVal := stun.FindAttr(attrs, stun.AttrUsername)
	if usernameVal == nil {
		return nil
	}
	username := string(usernameVal)
	if !strings.HasPrefix(username, a.ufragPrefix) {
		return nil
	}

	// Verify MESSAGE-INTEGRITY using local password as key.
	miOffset := stun.FindAttrOffset(data, stun.AttrMessageIntegrity)
	if miOffset < 0 {
		return nil
	}
	if !stun.VerifyIntegrity(data, miOffset, []byte(a.localCreds.Pwd)) {
		return nil
	}

	// Check for USE-CANDIDATE (nomination).
	if stun.FindAttr(attrs, stun.AttrUseCandidate) != nil {
		a.mu.Lock()
		a.nominatedAddr = from
		a.mu.Unlock()
	}

	// Build Binding Response with XOR-MAPPED-ADDRESS + MESSAGE-INTEGRITY.
	return stun.BuildBindingResponse(txnID, from, []byte(a.localCreds.Pwd))
}

// ComputePriority calculates ICE candidate priority per RFC 8445 §5.1.2.
func ComputePriority(candType CandidateType, component, localPref uint32) uint32 {
	return (1<<24)*candType.typePreference() + (1<<8)*localPref + (256 - component)
}

// GatherCandidates gathers ICE candidates from the given addresses.
// localAddr is the host address. srflxAddr is the STUN-mapped address (may be nil).
// relayAddr is the TURN relay address (may be nil). component is 1 for RTP.
func GatherCandidates(localAddr *net.UDPAddr, srflxAddr, relayAddr *net.UDPAddr, component uint32) []Candidate {
	candidates := make([]Candidate, 0, 3)

	// Host candidate.
	candidates = append(candidates, Candidate{
		Foundation: "1",
		Component:  component,
		Transport:  "UDP",
		Priority:   ComputePriority(CandidateHost, component, 65535),
		Addr:       localAddr,
		Type:       CandidateHost,
	})

	// Server-reflexive candidate.
	if srflxAddr != nil {
		candidates = append(candidates, Candidate{
			Foundation: "2",
			Component:  component,
			Transport:  "UDP",
			Priority:   ComputePriority(CandidateServerReflexive, component, 65535),
			Addr:       srflxAddr,
			Type:       CandidateServerReflexive,
			RelAddr:    localAddr,
		})
	}

	// Relay candidate.
	if relayAddr != nil {
		relBase := localAddr
		if srflxAddr != nil {
			relBase = srflxAddr
		}
		candidates = append(candidates, Candidate{
			Foundation: "3",
			Component:  component,
			Transport:  "UDP",
			Priority:   ComputePriority(CandidateRelay, component, 65535),
			Addr:       relayAddr,
			Type:       CandidateRelay,
			RelAddr:    relBase,
		})
	}

	return candidates
}

// ParseCandidate parses an SDP a=candidate: value into a Candidate.
// Format: foundation component transport priority addr port typ type [raddr X rport Y]
func ParseCandidate(line string) (*Candidate, error) {
	parts := strings.Fields(line)
	if len(parts) < 8 {
		return nil, fmt.Errorf("ice: candidate too short")
	}

	component, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("ice: bad component: %w", err)
	}
	priority, err := strconv.ParseUint(parts[3], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("ice: bad priority: %w", err)
	}
	port, err := strconv.Atoi(parts[5])
	if err != nil {
		return nil, fmt.Errorf("ice: bad port: %w", err)
	}
	if parts[6] != "typ" {
		return nil, fmt.Errorf("ice: missing 'typ' keyword")
	}

	var candType CandidateType
	switch parts[7] {
	case "host":
		candType = CandidateHost
	case "srflx":
		candType = CandidateServerReflexive
	case "relay":
		candType = CandidateRelay
	default:
		return nil, fmt.Errorf("ice: unknown candidate type: %s", parts[7])
	}

	c := &Candidate{
		Foundation: parts[0],
		Component:  uint32(component),
		Transport:  parts[2],
		Priority:   uint32(priority),
		Addr:       &net.UDPAddr{IP: net.ParseIP(parts[4]), Port: port},
		Type:       candType,
	}

	// Optional raddr/rport.
	if len(parts) >= 12 && parts[8] == "raddr" && parts[10] == "rport" {
		rport, err := strconv.Atoi(parts[11])
		if err == nil {
			c.RelAddr = &net.UDPAddr{IP: net.ParseIP(parts[9]), Port: rport}
		}
	}

	return c, nil
}

// randomICEString generates a random string of given length using alphanumeric chars.
func randomICEString(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("ice: crypto/rand failed: " + err.Error())
	}
	for i := range b {
		b[i] = chars[b[i]%byte(len(chars))]
	}
	return string(b)
}
