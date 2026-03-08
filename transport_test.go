package xphone

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSipUA_UDPCreatesSuccessfully(t *testing.T) {
	cfg := testConfig()
	cfg.Transport = "udp"

	tr, err := newSipUA(cfg, "127.0.0.1")
	require.NoError(t, err)
	assert.NotNil(t, tr)
	tr.Close()
}

func TestSipUA_TCPCreatesSuccessfully(t *testing.T) {
	cfg := testConfig()
	cfg.Transport = "tcp"

	tr, err := newSipUA(cfg, "127.0.0.1")
	require.NoError(t, err)
	assert.NotNil(t, tr)
	tr.Close()
}

func TestSipUA_TLSRequiresTLSConfig(t *testing.T) {
	cfg := testConfig()
	cfg.Transport = "tls"
	cfg.TLSConfig = nil

	_, err := newSipUA(cfg, "127.0.0.1")
	assert.ErrorIs(t, err, ErrTLSConfigRequired)
}

func TestSipUA_UnknownProtocolErrors(t *testing.T) {
	cfg := testConfig()
	cfg.Transport = "sctp"

	_, err := newSipUA(cfg, "127.0.0.1")
	assert.Error(t, err)
}
