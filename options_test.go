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
