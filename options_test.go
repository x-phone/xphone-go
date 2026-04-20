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

func TestWithAuthUsername_SetsConfig(t *testing.T) {
	cfg := Config{}
	WithAuthUsername("auth-id")(&cfg)
	assert.Equal(t, "auth-id", cfg.AuthUsername)
}

func TestResolveAuthCredentials_AuthUsernameFallbackForInvite(t *testing.T) {
	cfg := Config{
		Username:     "1001",
		Password:     "secret",
		AuthUsername: "auth-id",
	}
	opts := DialOptions{}
	user, pass := resolveAuthCredentials(opts, cfg)
	assert.Equal(t, "auth-id", user, "INVITE digest should use AuthUsername when OutboundUsername is unset")
	assert.Equal(t, "secret", pass)
}

func TestResolveAuthCredentials_OutboundUsernameBeatsAuthUsername(t *testing.T) {
	cfg := Config{
		Username:         "1001",
		Password:         "secret",
		AuthUsername:     "auth-id",
		OutboundUsername: "outbound-user",
		OutboundPassword: "outbound-pass",
	}
	opts := DialOptions{}
	user, pass := resolveAuthCredentials(opts, cfg)
	assert.Equal(t, "outbound-user", user, "OutboundUsername must take precedence over AuthUsername")
	assert.Equal(t, "outbound-pass", pass)
}

func TestResolveRegisterAuthUsername_UsesAuthUsername(t *testing.T) {
	cfg := Config{Username: "1001", AuthUsername: "auth-id"}
	assert.Equal(t, "auth-id", resolveRegisterAuthUsername(cfg))
}

func TestResolveRegisterAuthUsername_FallsBackToUsername(t *testing.T) {
	cfg := Config{Username: "1001"}
	assert.Equal(t, "1001", resolveRegisterAuthUsername(cfg))
}

func TestResolveRegisterAuthUsername_BothEmpty(t *testing.T) {
	cfg := Config{}
	assert.Empty(t, resolveRegisterAuthUsername(cfg))
}
