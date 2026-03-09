package ice

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x-phone/xphone-go/internal/stun"
)

func TestPriority_HostHighest(t *testing.T) {
	host := ComputePriority(CandidateHost, 1, 65535)
	srflx := ComputePriority(CandidateServerReflexive, 1, 65535)
	relay := ComputePriority(CandidateRelay, 1, 65535)
	assert.Greater(t, host, srflx)
	assert.Greater(t, srflx, relay)
}

func TestPriority_Component1HigherThan2(t *testing.T) {
	p1 := ComputePriority(CandidateHost, 1, 65535)
	p2 := ComputePriority(CandidateHost, 2, 65535)
	assert.Greater(t, p1, p2)
}

func TestCredentials_Generation(t *testing.T) {
	c1 := GenerateCredentials()
	c2 := GenerateCredentials()
	assert.Len(t, c1.Ufrag, 8)
	assert.Len(t, c1.Pwd, 24)
	assert.NotEqual(t, c1.Ufrag, c2.Ufrag)
	assert.NotEqual(t, c1.Pwd, c2.Pwd)
}

func TestCandidate_SDPValue_Host(t *testing.T) {
	c := Candidate{
		Foundation: "1",
		Component:  1,
		Transport:  "UDP",
		Priority:   2130706431,
		Addr:       &net.UDPAddr{IP: net.IPv4(192, 168, 1, 100), Port: 5004},
		Type:       CandidateHost,
	}
	sdp := c.SDPValue()
	assert.Contains(t, sdp, "192.168.1.100")
	assert.Contains(t, sdp, "5004")
	assert.Contains(t, sdp, "typ host")
	assert.NotContains(t, sdp, "raddr")
}

func TestCandidate_SDPValue_SRflx(t *testing.T) {
	c := Candidate{
		Foundation: "2",
		Component:  1,
		Transport:  "UDP",
		Priority:   1694498815,
		Addr:       &net.UDPAddr{IP: net.IPv4(203, 0, 113, 42), Port: 12345},
		Type:       CandidateServerReflexive,
		RelAddr:    &net.UDPAddr{IP: net.IPv4(192, 168, 1, 100), Port: 5004},
	}
	sdp := c.SDPValue()
	assert.Contains(t, sdp, "typ srflx")
	assert.Contains(t, sdp, "raddr 192.168.1.100 rport 5004")
}

func TestCandidate_SDPValue_Relay(t *testing.T) {
	c := Candidate{
		Foundation: "3",
		Component:  1,
		Transport:  "UDP",
		Priority:   16777215,
		Addr:       &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 50000},
		Type:       CandidateRelay,
		RelAddr:    &net.UDPAddr{IP: net.IPv4(203, 0, 113, 42), Port: 12345},
	}
	sdp := c.SDPValue()
	assert.Contains(t, sdp, "typ relay")
	assert.Contains(t, sdp, "raddr 203.0.113.42")
}

func TestParseCandidate_Host(t *testing.T) {
	c, err := ParseCandidate("1 1 UDP 2130706431 192.168.1.100 5004 typ host")
	require.NoError(t, err)
	assert.Equal(t, "1", c.Foundation)
	assert.Equal(t, uint32(1), c.Component)
	assert.Equal(t, uint32(2130706431), c.Priority)
	assert.Equal(t, CandidateHost, c.Type)
	assert.Nil(t, c.RelAddr)
}

func TestParseCandidate_SRflxWithRaddr(t *testing.T) {
	c, err := ParseCandidate("2 1 UDP 1694498815 203.0.113.42 12345 typ srflx raddr 192.168.1.100 rport 5004")
	require.NoError(t, err)
	assert.Equal(t, CandidateServerReflexive, c.Type)
	require.NotNil(t, c.RelAddr)
	assert.Equal(t, 5004, c.RelAddr.Port)
}

func TestParseCandidate_RoundTrip(t *testing.T) {
	original := Candidate{
		Foundation: "2",
		Component:  1,
		Transport:  "UDP",
		Priority:   1694498815,
		Addr:       &net.UDPAddr{IP: net.IPv4(203, 0, 113, 42), Port: 12345},
		Type:       CandidateServerReflexive,
		RelAddr:    &net.UDPAddr{IP: net.IPv4(192, 168, 1, 100), Port: 5004},
	}
	parsed, err := ParseCandidate(original.SDPValue())
	require.NoError(t, err)
	assert.Equal(t, original.Foundation, parsed.Foundation)
	assert.Equal(t, original.Component, parsed.Component)
	assert.Equal(t, original.Priority, parsed.Priority)
	assert.Equal(t, original.Type, parsed.Type)
	assert.True(t, original.Addr.IP.Equal(parsed.Addr.IP))
	assert.Equal(t, original.Addr.Port, parsed.Addr.Port)
}

func TestParseCandidate_Invalid(t *testing.T) {
	_, err := ParseCandidate("")
	assert.Error(t, err)
	_, err = ParseCandidate("too short")
	assert.Error(t, err)
	_, err = ParseCandidate("1 1 UDP 100 1.2.3.4 5 nottyp host")
	assert.Error(t, err)
}

func TestGatherCandidates_HostOnly(t *testing.T) {
	local := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 100), Port: 5004}
	cands := GatherCandidates(local, nil, nil, 1)
	assert.Len(t, cands, 1)
	assert.Equal(t, CandidateHost, cands[0].Type)
	assert.Equal(t, local, cands[0].Addr)
}

func TestGatherCandidates_AllThree(t *testing.T) {
	local := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 100), Port: 5004}
	srflx := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 42), Port: 12345}
	relay := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 50000}
	cands := GatherCandidates(local, srflx, relay, 1)
	assert.Len(t, cands, 3)
	assert.Equal(t, CandidateHost, cands[0].Type)
	assert.Equal(t, CandidateServerReflexive, cands[1].Type)
	assert.Equal(t, CandidateRelay, cands[2].Type)
	assert.Greater(t, cands[0].Priority, cands[1].Priority)
	assert.Greater(t, cands[1].Priority, cands[2].Priority)
}

func TestAgent_BindingRequestResponse(t *testing.T) {
	localCreds := Credentials{Ufrag: "localufrag", Pwd: "localpassword12345678901"}
	agent := NewAgent(localCreds, nil)

	// Build a STUN Binding Request with USERNAME and MESSAGE-INTEGRITY.
	txnID := stun.GenerateTxnID()
	username := localCreds.Ufrag + ":remoteufrag"
	msg := stun.BuildMessage(stun.BindingRequest, txnID, []stun.Attr{
		{Type: stun.AttrUsername, Value: []byte(username)},
		{Type: stun.AttrPriority, Value: []byte{0, 0, 0, 100}},
	})
	msg = stun.AppendIntegrity(msg, []byte(localCreds.Pwd))

	from := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5000}
	resp := agent.HandleBindingRequest(msg, from)
	require.NotNil(t, resp)

	// Verify response is a valid STUN Binding Response.
	assert.True(t, stun.IsMessage(resp))
	mt, ok := stun.MsgType(resp)
	require.True(t, ok)
	assert.Equal(t, stun.BindingResponse, mt)
	id, ok := stun.TxnID(resp)
	require.True(t, ok)
	assert.Equal(t, txnID, id)
}

func TestAgent_RejectsWrongUsername(t *testing.T) {
	localCreds := Credentials{Ufrag: "localufrag", Pwd: "localpassword12345678901"}
	agent := NewAgent(localCreds, nil)

	txnID := stun.GenerateTxnID()
	msg := stun.BuildMessage(stun.BindingRequest, txnID, []stun.Attr{
		{Type: stun.AttrUsername, Value: []byte("wrongufrag:remote")},
	})
	msg = stun.AppendIntegrity(msg, []byte(localCreds.Pwd))

	from := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5000}
	assert.Nil(t, agent.HandleBindingRequest(msg, from))
}

func TestAgent_RejectsBadIntegrity(t *testing.T) {
	localCreds := Credentials{Ufrag: "localufrag", Pwd: "localpassword12345678901"}
	agent := NewAgent(localCreds, nil)

	txnID := stun.GenerateTxnID()
	username := localCreds.Ufrag + ":remote"
	msg := stun.BuildMessage(stun.BindingRequest, txnID, []stun.Attr{
		{Type: stun.AttrUsername, Value: []byte(username)},
	})
	msg = stun.AppendIntegrity(msg, []byte("wrong-password"))

	from := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5000}
	assert.Nil(t, agent.HandleBindingRequest(msg, from))
}

func TestAgent_Nomination(t *testing.T) {
	localCreds := Credentials{Ufrag: "localufrag", Pwd: "localpassword12345678901"}
	agent := NewAgent(localCreds, nil)

	txnID := stun.GenerateTxnID()
	username := localCreds.Ufrag + ":remote"
	msg := stun.BuildMessage(stun.BindingRequest, txnID, []stun.Attr{
		{Type: stun.AttrUsername, Value: []byte(username)},
		{Type: stun.AttrUseCandidate, Value: nil},
	})
	msg = stun.AppendIntegrity(msg, []byte(localCreds.Pwd))

	from := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5000}
	resp := agent.HandleBindingRequest(msg, from)
	require.NotNil(t, resp)
	assert.True(t, from.IP.Equal(agent.NominatedAddr().IP))
	assert.Equal(t, from.Port, agent.NominatedAddr().Port)
}

func TestCandidateType_String(t *testing.T) {
	assert.Equal(t, "host", CandidateHost.String())
	assert.Equal(t, "srflx", CandidateServerReflexive.String())
	assert.Equal(t, "relay", CandidateRelay.String())
}

func TestFindAttrOffset(t *testing.T) {
	txnID := [12]byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA}
	msg := stun.BuildMessage(stun.BindingRequest, txnID, []stun.Attr{
		{Type: stun.AttrUsername, Value: []byte("test")},
		{Type: stun.AttrPriority, Value: []byte{0, 0, 0, 100}},
	})
	msg = stun.AppendIntegrity(msg, []byte("key"))

	offset := stun.FindAttrOffset(msg, stun.AttrMessageIntegrity)
	assert.Greater(t, offset, 0)

	offset = stun.FindAttrOffset(msg, 0x9999)
	assert.Equal(t, -1, offset)
}
