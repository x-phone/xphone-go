package xphone

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransport_UDPCreatesSuccessfully(t *testing.T) {
	tr, err := newTransport(TransportConfig{
		Protocol: "udp",
		Host:     "127.0.0.1",
		Port:     0,
	})

	require.NoError(t, err)
	assert.NotNil(t, tr)
	tr.Close()
}

func TestTransport_TCPCreatesSuccessfully(t *testing.T) {
	tr, err := newTransport(TransportConfig{
		Protocol: "tcp",
		Host:     "127.0.0.1",
		Port:     0,
	})

	require.NoError(t, err)
	assert.NotNil(t, tr)
	tr.Close()
}

func TestTransport_TLSRequiresTLSConfig(t *testing.T) {
	_, err := newTransport(TransportConfig{
		Protocol:  "tls",
		Host:      "127.0.0.1",
		Port:      5061,
		TLSConfig: nil,
	})

	assert.ErrorIs(t, err, ErrTLSConfigRequired)
}

func TestTransport_UnknownProtocolErrors(t *testing.T) {
	_, err := newTransport(TransportConfig{
		Protocol: "sctp",
	})
	assert.Error(t, err)
}
