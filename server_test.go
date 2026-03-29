package xphone

import (
	"errors"
	"strings"
	"testing"
)

func TestNewServer_Defaults(t *testing.T) {
	srv := NewServer(ServerConfig{})
	s := srv.(*server)
	if s.cfg.Listen != "0.0.0.0:5060" {
		t.Errorf("default listen = %q, want 0.0.0.0:5060", s.cfg.Listen)
	}
	if s.cfg.MediaTimeout != 30e9 {
		t.Errorf("default media timeout = %v, want 30s", s.cfg.MediaTimeout)
	}
	if s.cfg.PCMRate != 8000 {
		t.Errorf("default PCM rate = %d, want 8000", s.cfg.PCMRate)
	}
	if s.State() != ServerStateStopped {
		t.Errorf("initial state = %v, want ServerStateStopped", s.State())
	}
}

func TestNewServer_PeerDefaults(t *testing.T) {
	srv := NewServer(ServerConfig{
		Peers: []PeerConfig{
			{Name: "test"},
		},
	})
	s := srv.(*server)
	if s.cfg.Peers[0].Port != 5060 {
		t.Errorf("default peer port = %d, want 5060", s.cfg.Peers[0].Port)
	}
}

func TestNewServer_PeerHostPortSplit(t *testing.T) {
	srv := NewServer(ServerConfig{
		Peers: []PeerConfig{
			{Name: "with-port", Host: "10.0.0.1:5080"},
			{Name: "without-port", Host: "10.0.0.2"},
		},
	})
	s := srv.(*server)
	if s.cfg.Peers[0].Host != "10.0.0.1" {
		t.Errorf("peer host = %q, want 10.0.0.1", s.cfg.Peers[0].Host)
	}
	if s.cfg.Peers[0].Port != 5080 {
		t.Errorf("peer port = %d, want 5080", s.cfg.Peers[0].Port)
	}
	if s.cfg.Peers[1].Host != "10.0.0.2" {
		t.Errorf("peer host = %q, want 10.0.0.2", s.cfg.Peers[1].Host)
	}
	if s.cfg.Peers[1].Port != 5060 {
		t.Errorf("peer port = %d, want 5060 (default)", s.cfg.Peers[1].Port)
	}
}

func TestNewServer_WithConfig(t *testing.T) {
	srv := NewServer(ServerConfig{
		Listen:     "0.0.0.0:5080",
		RTPPortMin: 10200,
		RTPPortMax: 10300,
		RTPAddress: "203.0.113.1",
		CodecPrefs: []Codec{CodecPCMU, CodecPCMA},
		Peers: []PeerConfig{
			{Name: "office-pbx", Host: "192.168.1.10"},
			{Name: "twilio", Hosts: []string{"54.172.60.0/30", "54.244.51.0/30"}},
		},
	})
	s := srv.(*server)

	if s.cfg.Listen != "0.0.0.0:5080" {
		t.Errorf("listen = %q, want 0.0.0.0:5080", s.cfg.Listen)
	}
	if s.cfg.RTPPortMin != 10200 || s.cfg.RTPPortMax != 10300 {
		t.Errorf("RTP range = %d-%d, want 10200-10300", s.cfg.RTPPortMin, s.cfg.RTPPortMax)
	}
	if s.cfg.RTPAddress != "203.0.113.1" {
		t.Errorf("RTP address = %q, want 203.0.113.1", s.cfg.RTPAddress)
	}
	if len(s.codecPrefs) != 2 {
		t.Errorf("codec prefs len = %d, want 2", len(s.codecPrefs))
	}
	if len(s.cfg.Peers) != 2 {
		t.Errorf("peers len = %d, want 2", len(s.cfg.Peers))
	}
}

func TestServer_FindPeer(t *testing.T) {
	srv := NewServer(ServerConfig{
		Peers: []PeerConfig{
			{Name: "alpha", Host: "10.0.0.1"},
			{Name: "beta", Host: "10.0.0.2"},
		},
	})
	s := srv.(*server)

	if p := s.findPeer("alpha"); p == nil || p.Name != "alpha" {
		t.Error("expected to find peer alpha")
	}
	if p := s.findPeer("gamma"); p != nil {
		t.Error("expected nil for unknown peer")
	}
}

func TestServer_ResolvePeerAddr(t *testing.T) {
	s := &server{}

	tests := []struct {
		name string
		peer PeerConfig
		want string
	}{
		{
			name: "host with default port",
			peer: PeerConfig{Host: "10.0.0.1", Port: 5060},
			want: "10.0.0.1:5060",
		},
		{
			name: "host with custom port",
			peer: PeerConfig{Host: "10.0.0.1", Port: 5080},
			want: "10.0.0.1:5080",
		},
		{
			name: "hosts list fallback",
			peer: PeerConfig{Hosts: []string{"10.0.0.0/24", "10.0.0.5"}, Port: 5060},
			want: "10.0.0.5:5060",
		},
		{
			name: "no host available",
			peer: PeerConfig{Hosts: []string{"10.0.0.0/24"}, Port: 5060},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.resolvePeerAddr(&tt.peer)
			if got != tt.want {
				t.Errorf("resolvePeerAddr = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServer_ResolveLocalIP(t *testing.T) {
	tests := []struct {
		name string
		cfg  ServerConfig
		want string
	}{
		{
			name: "explicit RTPAddress",
			cfg:  ServerConfig{Listen: "0.0.0.0:5080", RTPAddress: "203.0.113.1"},
			want: "203.0.113.1",
		},
		{
			name: "specific listen address",
			cfg:  ServerConfig{Listen: "192.168.1.100:5080"},
			want: "192.168.1.100",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &server{cfg: tt.cfg}
			got := s.resolveLocalIP()
			if got != tt.want {
				t.Errorf("resolveLocalIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServer_Callbacks(t *testing.T) {
	srv := NewServer(ServerConfig{})

	var incomingCalled, stateCalled, endedCalled, dtmfCalled, errorCalled bool
	srv.OnIncoming(func(Call) { incomingCalled = true })
	srv.OnCallState(func(Call, CallState) { stateCalled = true })
	srv.OnCallEnded(func(Call, EndReason) { endedCalled = true })
	srv.OnCallDTMF(func(Call, string) { dtmfCalled = true })
	srv.OnError(func(error) { errorCalled = true })

	s := srv.(*server)
	if len(s.incomingFns) == 0 {
		t.Error("OnIncoming not set")
	}
	if len(s.onCallStateFns) == 0 {
		t.Error("OnCallState not set")
	}
	if len(s.onCallEndedFns) == 0 {
		t.Error("OnCallEnded not set")
	}
	if len(s.onCallDTMFFns) == 0 {
		t.Error("OnCallDTMF not set")
	}
	if len(s.onErrorFns) == 0 {
		t.Error("OnError not set")
	}

	// Suppress linter warnings about unused variables.
	_ = incomingCalled
	_ = stateCalled
	_ = endedCalled
	_ = dtmfCalled
	_ = errorCalled
}

func TestServer_FindCall_Empty(t *testing.T) {
	srv := NewServer(ServerConfig{})
	if c := srv.FindCall("nonexistent"); c != nil {
		t.Error("expected nil for nonexistent call")
	}
}

func TestServer_Calls_Empty(t *testing.T) {
	srv := NewServer(ServerConfig{})
	calls := srv.Calls()
	if len(calls) != 0 {
		t.Errorf("expected 0 calls, got %d", len(calls))
	}
}

func TestServer_DialNotListening(t *testing.T) {
	srv := NewServer(ServerConfig{
		Peers: []PeerConfig{{Name: "test", Host: "10.0.0.1"}},
	})
	_, err := srv.Dial(nil, "test", "+15551234567", "+15559876543")
	if err == nil {
		t.Fatal("expected error when not listening")
	}
}

func TestServer_DialURINotListening(t *testing.T) {
	srv := NewServer(ServerConfig{})
	_, err := srv.DialURI(nil, "sip:1003@10.0.0.1:5060", "+15559876543")
	if !errors.Is(err, ErrNotListening) {
		t.Fatalf("expected ErrNotListening, got %v", err)
	}
}

func TestServer_DialURIInvalidURI(t *testing.T) {
	srv := NewServer(ServerConfig{})
	_, err := srv.DialURI(nil, "http://example.com", "+15559876543")
	if err == nil || !strings.Contains(err.Error(), "invalid SIP URI") {
		t.Fatalf("expected invalid SIP URI error, got %v", err)
	}
}

func TestServer_DialURINoUserPart(t *testing.T) {
	srv := NewServer(ServerConfig{})
	_, err := srv.DialURI(nil, "sip:10.0.0.1:5060", "+15559876543")
	if err == nil || !strings.Contains(err.Error(), "no user part") {
		t.Fatalf("expected no user part error, got %v", err)
	}
}

func TestServer_ResolveRTPAddressForPeer(t *testing.T) {
	s := &server{localIP: "203.0.113.1"}

	// Peer with override.
	peer := &PeerConfig{Name: "custom", RTPAddress: "10.0.0.99"}
	if got := s.resolveRTPAddressForPeer(peer); got != "10.0.0.99" {
		t.Errorf("expected peer RTP address, got %q", got)
	}

	// Peer without override — falls back to server IP.
	peer2 := &PeerConfig{Name: "default"}
	if got := s.resolveRTPAddressForPeer(peer2); got != "203.0.113.1" {
		t.Errorf("expected server RTP address, got %q", got)
	}

	// Nil peer — falls back to server IP.
	if got := s.resolveRTPAddressForPeer(nil); got != "203.0.113.1" {
		t.Errorf("expected server RTP address for nil peer, got %q", got)
	}
}

func TestServer_PeerCodecsConfig(t *testing.T) {
	srv := NewServer(ServerConfig{
		CodecPrefs: []Codec{CodecPCMU, CodecPCMA, CodecG722},
		Peers: []PeerConfig{
			{Name: "restricted", Host: "10.0.0.1", Codecs: []Codec{CodecPCMU}},
			{Name: "unrestricted", Host: "10.0.0.2"},
		},
	})
	s := srv.(*server)

	// Restricted peer should have codecs set.
	p1 := s.findPeer("restricted")
	if len(p1.Codecs) != 1 || p1.Codecs[0] != CodecPCMU {
		t.Errorf("expected restricted peer to have PCMU only, got %v", p1.Codecs)
	}

	// Unrestricted peer should have empty codecs (use server defaults).
	p2 := s.findPeer("unrestricted")
	if len(p2.Codecs) != 0 {
		t.Errorf("expected unrestricted peer to have no codec override, got %v", p2.Codecs)
	}
}
