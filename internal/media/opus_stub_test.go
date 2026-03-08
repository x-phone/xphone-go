//go:build !opus

package media

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpusStub_ReturnsNil(t *testing.T) {
	assert.Nil(t, NewCodecProcessor(111, 8000), "PT=111 should return nil without opus build tag")
}
