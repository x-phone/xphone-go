package xphone

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithAuth_SetsCredentials(t *testing.T) {
	opts := applyDialOptions([]DialOption{
		WithAuth("trunk-user", "trunk-pass"),
	})
	assert.Equal(t, "trunk-user", opts.AuthUsername)
	assert.Equal(t, "trunk-pass", opts.AuthPassword)
}

func TestWithAuth_DefaultsEmpty(t *testing.T) {
	opts := applyDialOptions(nil)
	assert.Empty(t, opts.AuthUsername)
	assert.Empty(t, opts.AuthPassword)
}

func TestResolveAuthCredentials_PerCallOverridesAll(t *testing.T) {
	cfg := Config{
		Username:         "reg-user",
		Password:         "reg-pass",
		OutboundUsername: "outbound-user",
		OutboundPassword: "outbound-pass",
	}
	opts := DialOptions{
		AuthUsername: "call-user",
		AuthPassword: "call-pass",
	}
	user, pass := resolveAuthCredentials(opts, cfg)
	assert.Equal(t, "call-user", user)
	assert.Equal(t, "call-pass", pass)
}

func TestResolveAuthCredentials_OutboundOverridesRegistration(t *testing.T) {
	cfg := Config{
		Username:         "reg-user",
		Password:         "reg-pass",
		OutboundUsername: "outbound-user",
		OutboundPassword: "outbound-pass",
	}
	opts := DialOptions{}
	user, pass := resolveAuthCredentials(opts, cfg)
	assert.Equal(t, "outbound-user", user)
	assert.Equal(t, "outbound-pass", pass)
}

func TestResolveAuthCredentials_FallsBackToRegistration(t *testing.T) {
	cfg := Config{
		Username: "reg-user",
		Password: "reg-pass",
	}
	opts := DialOptions{}
	user, pass := resolveAuthCredentials(opts, cfg)
	assert.Equal(t, "reg-user", user)
	assert.Equal(t, "reg-pass", pass)
}

func TestResolveAuthCredentials_EmptyConfigNoAuth(t *testing.T) {
	cfg := Config{}
	opts := DialOptions{}
	user, pass := resolveAuthCredentials(opts, cfg)
	assert.Empty(t, user)
	assert.Empty(t, pass)
}

func TestResolveAuthCredentials_PerCallOnlyNoConfig(t *testing.T) {
	cfg := Config{}
	opts := DialOptions{
		AuthUsername: "call-user",
		AuthPassword: "call-pass",
	}
	user, pass := resolveAuthCredentials(opts, cfg)
	assert.Equal(t, "call-user", user)
	assert.Equal(t, "call-pass", pass)
}
