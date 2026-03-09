//go:build !g729

package media

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestG729Stub_ReturnsNil(t *testing.T) {
	assert.Nil(t, NewCodecProcessor(18, 8000), "PT=18 should return nil without g729 build tag")
}
